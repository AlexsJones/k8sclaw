package controller

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/nats-io/nats.go"
)

// DensitySubscriber subscribes to raw NATS subjects published by llmfit
// DaemonSet pods and updates the DensityCache.
//
// llmfit publishes raw NATS (not JetStream) to subjects:
//
//	llmfit.system.{hostname}    — hardware specs every 60s
//	llmfit.fit.{hostname}       — model fitness scores
//	llmfit.runtimes.{hostname}  — runtime availability
//	llmfit.installed.{hostname} — installed models
//
// Each message is wrapped in a common envelope:
//
//	{ "timestamp": "...", "hostname": "...", "event_type": "...", "version": "1", "data": {...} }
//
// Implements sigs.k8s.io/controller-runtime manager.Runnable so it can be
// added to the controller manager via mgr.Add().
type DensitySubscriber struct {
	NATSUrl string
	Cache   *DensityCache
	Log     logr.Logger
}

// llmfitEnvelope is the common wrapper for all llmfit NATS events.
type llmfitEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Hostname  string          `json:"hostname"`
	EventType string          `json:"event_type"`
	Version   string          `json:"version"`
	Data      json.RawMessage `json:"data"`
}

// llmfitSystemData mirrors the "data" field of llmfit.system.* events.
type llmfitSystemData = SystemSpecs

// llmfitFitData mirrors the top-level structure of llmfit.fit.* events.
type llmfitFitData struct {
	TotalModels    int            `json:"total_models"`
	ReturnedModels int            `json:"returned_models"`
	Models         []ModelFitInfo `json:"models"`
}

// llmfitRuntimesData mirrors the "data" field of llmfit.runtimes.* events.
type llmfitRuntimesData struct {
	Runtimes []RuntimeStatus `json:"runtimes"`
}

// llmfitInstalledData mirrors the "data" field of llmfit.installed.* events.
type llmfitInstalledData struct {
	Models []InstalledModelInfo `json:"models"`
}

// Start connects to NATS, subscribes to llmfit.> subjects, and populates
// the DensityCache until ctx is cancelled.
func (fs *DensitySubscriber) Start(ctx context.Context) error {
	fs.Log.Info("Connecting to NATS for llmfit density events", "url", fs.NATSUrl)

	opts := []nats.Option{
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1), // Reconnect indefinitely.
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				fs.Log.Info("NATS disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			fs.Log.Info("NATS reconnected")
		}),
	}
	if user, password := os.Getenv("NATS_USERNAME"), os.Getenv("NATS_PASSWORD"); user != "" || password != "" {
		opts = append(opts, nats.UserInfo(user, password))
	}
	nc, err := nats.Connect(fs.NATSUrl, opts...)
	if err != nil {
		return err
	}
	defer nc.Close()

	// Subscribe to all llmfit events using a wildcard.
	sub, err := nc.Subscribe("llmfit.>", func(msg *nats.Msg) {
		fs.handleMessage(msg)
	})
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()

	fs.Log.Info("Subscribed to llmfit.> for density telemetry")

	// Periodic garbage collection of stale entries.
	gcTicker := time.NewTicker(5 * time.Minute)
	defer gcTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			fs.Log.Info("Stopping density subscriber")
			return nil
		case <-gcTicker.C:
			fs.Cache.GarbageCollect()
		}
	}
}

// handleMessage parses an llmfit NATS event and updates the cache.
func (fs *DensitySubscriber) handleMessage(msg *nats.Msg) {
	var env llmfitEnvelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		fs.Log.V(1).Info("Failed to unmarshal llmfit event", "error", err, "subject", msg.Subject)
		return
	}

	if env.Hostname == "" {
		// Extract hostname from subject as fallback: llmfit.{type}.{hostname}
		parts := strings.SplitN(msg.Subject, ".", 3)
		if len(parts) == 3 {
			env.Hostname = parts[2]
		}
	}

	if env.Hostname == "" {
		fs.Log.V(1).Info("Ignoring llmfit event with no hostname", "subject", msg.Subject)
		return
	}

	now := time.Now()

	switch env.EventType {
	case "system":
		var data llmfitSystemData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			fs.Log.V(1).Info("Failed to unmarshal system data", "error", err, "node", env.Hostname)
			return
		}
		fs.Cache.Update(&NodeDensity{
			NodeName: env.Hostname,
			LastSeen: now,
			System:   data,
		})

	case "fit":
		var data llmfitFitData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			fs.Log.V(1).Info("Failed to unmarshal fit data", "error", err, "node", env.Hostname)
			return
		}
		fs.Cache.Update(&NodeDensity{
			NodeName:  env.Hostname,
			LastSeen:  now,
			ModelFits: data.Models,
		})

	case "runtimes":
		var data llmfitRuntimesData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			fs.Log.V(1).Info("Failed to unmarshal runtimes data", "error", err, "node", env.Hostname)
			return
		}
		fs.Cache.Update(&NodeDensity{
			NodeName: env.Hostname,
			LastSeen: now,
			Runtimes: data.Runtimes,
		})

	case "installed":
		var data llmfitInstalledData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			fs.Log.V(1).Info("Failed to unmarshal installed data", "error", err, "node", env.Hostname)
			return
		}
		fs.Cache.Update(&NodeDensity{
			NodeName:        env.Hostname,
			LastSeen:        now,
			InstalledModels: data.Models,
		})

	default:
		// Unknown event type — ignore gracefully. Future llmfit versions
		// may publish additional event types.
		fs.Log.V(2).Info("Ignoring unknown llmfit event type", "type", env.EventType, "node", env.Hostname)
	}
}
