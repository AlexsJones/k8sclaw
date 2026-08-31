package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

// subscribableBus hands out a real channel per topic so subscribeToInbound can
// be driven end to end. testEventBus returns nil from Subscribe, which is
// enough for the publish-only paths but not for an inbound one.
type subscribableBus struct {
	mu       sync.Mutex
	channels map[string]chan *eventbus.Event
}

func newSubscribableBus() *subscribableBus {
	return &subscribableBus{channels: map[string]chan *eventbus.Event{}}
}

func (b *subscribableBus) Publish(_ context.Context, topic string, event *eventbus.Event) error {
	b.mu.Lock()
	ch, ok := b.channels[topic]
	b.mu.Unlock()
	if !ok {
		return nil
	}
	ch <- event
	return nil
}

func (b *subscribableBus) Subscribe(_ context.Context, topic string) (<-chan *eventbus.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.channels[topic]; !ok {
		b.channels[topic] = make(chan *eventbus.Event, 1)
	}
	return b.channels[topic], nil
}

func (b *subscribableBus) Close() error { return nil }

// TestSubscribeToInbound_WritesGateVerdict proves the controller's verdict
// reaches the parked agent-runner as the file it polls for. Without this hop
// the pod parks until its own timeout and the chain silently stops.
func TestSubscribeToInbound_WritesGateVerdict(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, DirGate), 0o750); err != nil {
		t.Fatalf("mkdir gate: %v", err)
	}
	bus := newSubscribableBus()
	bridge := NewBridge(base, "run-7", "agent-alpha", bus, logr.Discard(), "default")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bridge.subscribeToInbound(ctx)

	topic := fmt.Sprintf("%s.%s", eventbus.TopicGateVerdict, "run-7")
	event, err := eventbus.NewEvent(topic, nil, &GateVerdict{
		Attempt: 2,
		Action:  GateVerdictActionContinue,
		Reason:  "tests still fail",
		Output:  "FAIL: 1 case",
	})
	if err != nil {
		t.Fatalf("new event: %v", err)
	}
	// Subscribe happens inside the goroutine; retry the publish until the
	// bridge's channel exists.
	publishUntilDelivered(t, bus, topic, event)

	path := filepath.Join(base, DirGate, "verdict-2.json")
	data := readWhenPresent(t, path, 2*time.Second)

	var got GateVerdict
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("verdict file is not valid JSON: %v", err)
	}
	if got.Attempt != 2 || got.Action != GateVerdictActionContinue {
		t.Errorf("got %+v, want a retry verdict for attempt 2", got)
	}
	if got.Reason != "tests still fail" || got.Output != "FAIL: 1 case" {
		t.Errorf("the gate's feedback was lost in transit: %+v", got)
	}
}

// A verdict with no usable attempt number would land in a file the runner can
// never match, so it is dropped rather than written.
func TestSubscribeToInbound_DropsAVerdictWithNoAttempt(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, DirGate), 0o750); err != nil {
		t.Fatalf("mkdir gate: %v", err)
	}
	bus := newSubscribableBus()
	bridge := NewBridge(base, "run-8", "agent-alpha", bus, logr.Discard(), "default")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bridge.subscribeToInbound(ctx)

	topic := fmt.Sprintf("%s.%s", eventbus.TopicGateVerdict, "run-8")
	event, err := eventbus.NewEvent(topic, nil, map[string]string{"action": "continue"})
	if err != nil {
		t.Fatalf("new event: %v", err)
	}
	publishUntilDelivered(t, bus, topic, event)

	time.Sleep(200 * time.Millisecond)
	entries, err := os.ReadDir(filepath.Join(base, DirGate))
	if err != nil {
		t.Fatalf("read gate dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("wrote %d file(s) for a verdict with no attempt number", len(entries))
	}
}

// A parked attempt's result must not tear the bridge down: the runner is still
// alive holding its conversation, and an exiting bridge would take the pod's
// only channel to the control plane with it.
func TestHandleOutputFile_AttemptResultDoesNotEndTheRun(t *testing.T) {
	base := t.TempDir()
	outputDir := filepath.Join(base, DirOutput)
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	bus := &testEventBus{}
	bridge := NewBridge(base, "run-9", "agent-alpha", bus, logr.Discard(), "default")

	path := filepath.Join(outputDir, "result-1.json")
	body, _ := json.Marshal(AttemptResult{Attempt: 1, Status: "success", Response: "draft"})
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write attempt result: %v", err)
	}

	bridge.handleOutputFile(context.Background(), FileEvent{Path: path})

	select {
	case <-bridge.agentDone:
		t.Fatal("an attempt result signalled agent completion; the bridge would exit mid-chain")
	default:
	}
	if len(bus.published) != 0 {
		t.Errorf("published %d event(s) for a parked attempt, want 0", len(bus.published))
	}
}

func publishUntilDelivered(t *testing.T, bus *subscribableBus, topic string, event *eventbus.Event) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bus.mu.Lock()
		_, ok := bus.channels[topic]
		bus.mu.Unlock()
		if ok {
			if err := bus.Publish(context.Background(), topic, event); err != nil {
				t.Fatalf("publish: %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("bridge never subscribed to %s", topic)
}

func readWhenPresent(t *testing.T, path string, budget time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return nil
}
