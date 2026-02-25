// Package controller contains the schedule router which handles agent-initiated
// schedule requests. When an agent calls the schedule_task tool, the IPC bridge
// publishes a schedule.upsert event to NATS. This router subscribes to those
// events and creates, updates, suspends, resumes, or deletes ClawSchedule CRDs.
package controller

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeclawv1alpha1 "github.com/kubeclaw/kubeclaw/api/v1alpha1"
	"github.com/kubeclaw/kubeclaw/internal/eventbus"
)

// ScheduleRouter subscribes to schedule.upsert events from the IPC bridge
// and creates/modifies ClawSchedule CRDs so agents can set their own heartbeats.
type ScheduleRouter struct {
	Client   client.Client
	EventBus eventbus.EventBus
	Log      logr.Logger
}

// scheduleRequest is the JSON payload written by the schedule_task tool.
type scheduleRequest struct {
	Name     string `json:"name"`
	Action   string `json:"action"` // create, update, suspend, resume, delete
	Schedule string `json:"schedule,omitempty"`
	Task     string `json:"task,omitempty"`
}

// Start begins listening for schedule upsert events. It blocks until ctx is cancelled.
func (sr *ScheduleRouter) Start(ctx context.Context) error {
	sr.Log.Info("Starting schedule router")

	ch, err := sr.EventBus.Subscribe(ctx, eventbus.TopicScheduleUpsert)
	if err != nil {
		return fmt.Errorf("subscribing to %s: %w", eventbus.TopicScheduleUpsert, err)
	}

	for {
		select {
		case <-ctx.Done():
			sr.Log.Info("Schedule router shutting down")
			return nil
		case event := <-ch:
			sr.handleScheduleEvent(ctx, event)
		}
	}
}

// handleScheduleEvent processes a single schedule request from an agent.
func (sr *ScheduleRouter) handleScheduleEvent(ctx context.Context, event *eventbus.Event) {
	instanceName := event.Metadata["instanceName"]

	var req scheduleRequest
	if err := json.Unmarshal(event.Data, &req); err != nil {
		sr.Log.Error(err, "failed to unmarshal schedule request")
		return
	}

	if req.Name == "" || req.Action == "" {
		sr.Log.Info("Ignoring schedule request with missing name or action")
		return
	}

	// Resolve namespace from the instance.
	namespace := "default"
	if instanceName != "" {
		var instances kubeclawv1alpha1.ClawInstanceList
		if err := sr.Client.List(ctx, &instances); err == nil {
			for i := range instances.Items {
				if instances.Items[i].Name == instanceName {
					namespace = instances.Items[i].Namespace
					break
				}
			}
		}
	}

	// Prefix the schedule name with instance name for uniqueness.
	scheduleName := fmt.Sprintf("%s-%s", instanceName, req.Name)

	sr.Log.Info("Processing schedule request",
		"instance", instanceName,
		"name", req.Name,
		"action", req.Action,
		"schedule", req.Schedule,
	)

	switch req.Action {
	case "create":
		sr.createSchedule(ctx, namespace, scheduleName, instanceName, req)
	case "update":
		sr.updateSchedule(ctx, namespace, scheduleName, req)
	case "suspend":
		sr.suspendSchedule(ctx, namespace, scheduleName, true)
	case "resume":
		sr.suspendSchedule(ctx, namespace, scheduleName, false)
	case "delete":
		sr.deleteSchedule(ctx, namespace, scheduleName)
	default:
		sr.Log.Info("Unknown schedule action", "action", req.Action)
	}
}

// createSchedule creates a new ClawSchedule CR.
func (sr *ScheduleRouter) createSchedule(ctx context.Context, namespace, name, instanceName string, req scheduleRequest) {
	schedule := &kubeclawv1alpha1.ClawSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"kubeclaw.io/instance": instanceName,
				"kubeclaw.io/source":   "agent",
			},
		},
		Spec: kubeclawv1alpha1.ClawScheduleSpec{
			InstanceRef:       instanceName,
			Schedule:          req.Schedule,
			Task:              req.Task,
			Type:              "heartbeat",
			ConcurrencyPolicy: "Forbid",
			IncludeMemory:     true,
		},
	}

	if err := sr.Client.Create(ctx, schedule); err != nil {
		if errors.IsAlreadyExists(err) {
			sr.Log.Info("Schedule already exists, updating instead", "name", name)
			sr.updateSchedule(ctx, namespace, name, req)
			return
		}
		sr.Log.Error(err, "failed to create ClawSchedule", "name", name)
		return
	}

	sr.Log.Info("Created ClawSchedule from agent request",
		"name", name,
		"schedule", req.Schedule,
		"instance", instanceName,
	)
}

// updateSchedule patches an existing ClawSchedule with new schedule/task.
func (sr *ScheduleRouter) updateSchedule(ctx context.Context, namespace, name string, req scheduleRequest) {
	existing := &kubeclawv1alpha1.ClawSchedule{}
	if err := sr.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existing); err != nil {
		if errors.IsNotFound(err) {
			sr.Log.Info("Schedule not found for update", "name", name)
		} else {
			sr.Log.Error(err, "failed to get ClawSchedule for update", "name", name)
		}
		return
	}

	if req.Schedule != "" {
		existing.Spec.Schedule = req.Schedule
	}
	if req.Task != "" {
		existing.Spec.Task = req.Task
	}
	// Ensure it's not suspended when updating.
	existing.Spec.Suspend = false

	if err := sr.Client.Update(ctx, existing); err != nil {
		sr.Log.Error(err, "failed to update ClawSchedule", "name", name)
		return
	}

	sr.Log.Info("Updated ClawSchedule from agent request", "name", name)
}

// suspendSchedule sets or clears the Suspend flag on a ClawSchedule.
func (sr *ScheduleRouter) suspendSchedule(ctx context.Context, namespace, name string, suspend bool) {
	existing := &kubeclawv1alpha1.ClawSchedule{}
	if err := sr.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existing); err != nil {
		if errors.IsNotFound(err) {
			sr.Log.Info("Schedule not found for suspend/resume", "name", name)
		} else {
			sr.Log.Error(err, "failed to get ClawSchedule", "name", name)
		}
		return
	}

	existing.Spec.Suspend = suspend
	if err := sr.Client.Update(ctx, existing); err != nil {
		sr.Log.Error(err, "failed to suspend/resume ClawSchedule", "name", name, "suspend", suspend)
		return
	}

	action := "resumed"
	if suspend {
		action = "suspended"
	}
	sr.Log.Info("ClawSchedule "+action, "name", name)
}

// deleteSchedule removes a ClawSchedule CR.
func (sr *ScheduleRouter) deleteSchedule(ctx context.Context, namespace, name string) {
	existing := &kubeclawv1alpha1.ClawSchedule{}
	if err := sr.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existing); err != nil {
		if errors.IsNotFound(err) {
			sr.Log.Info("Schedule not found for deletion", "name", name)
		} else {
			sr.Log.Error(err, "failed to get ClawSchedule for deletion", "name", name)
		}
		return
	}

	if err := sr.Client.Delete(ctx, existing); err != nil {
		sr.Log.Error(err, "failed to delete ClawSchedule", "name", name)
		return
	}

	sr.Log.Info("Deleted ClawSchedule", "name", name)
}
