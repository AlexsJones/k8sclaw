package controller

import (
	"context"
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// newTestInstance builds a minimal Agent for testing.
func newTestInstance() *sympoziumv1alpha1.Agent {
	return &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Channels: []sympoziumv1alpha1.ChannelSpec{
				{
					Type:      "telegram",
					ConfigRef: sympoziumv1alpha1.SecretRef{Secret: "telegram-secret"},
				},
			},
		},
	}
}

// ── Channel deployment label tests ───────────────────────────────────────────
// These tests verify that channel pods carry the labels required by the
// NetworkPolicy selectors so that egress (DNS, NATS, HTTPS) is allowed.

func TestBuildChannelDeployment_HasComponentLabel(t *testing.T) {
	r := &AgentReconciler{}
	instance := newTestInstance()
	ch := instance.Spec.Channels[0]
	deploy := r.buildChannelDeployment(instance, ch, "test-instance-channel-telegram")

	// Pod template labels are what NetworkPolicy podSelector matches against.
	podLabels := deploy.Spec.Template.Labels
	if podLabels["sympozium.ai/component"] != "channel" {
		t.Errorf("pod component label = %q, want %q", podLabels["sympozium.ai/component"], "channel")
	}
}

func TestBuildChannelDeployment_LabelsMatchSelector(t *testing.T) {
	r := &AgentReconciler{}
	instance := newTestInstance()
	ch := instance.Spec.Channels[0]
	deploy := r.buildChannelDeployment(instance, ch, "test-instance-channel-telegram")

	podLabels := deploy.Spec.Template.Labels
	selectorLabels := deploy.Spec.Selector.MatchLabels

	for k, v := range selectorLabels {
		if podLabels[k] != v {
			t.Errorf("selector label %q=%q not matched in pod labels (got %q)", k, v, podLabels[k])
		}
	}
}

func TestBuildChannelDeployment_ChannelTypeLabel(t *testing.T) {
	r := &AgentReconciler{}
	instance := newTestInstance()
	ch := instance.Spec.Channels[0]
	deploy := r.buildChannelDeployment(instance, ch, "test-instance-channel-telegram")

	podLabels := deploy.Spec.Template.Labels
	if podLabels["sympozium.ai/channel"] != "telegram" {
		t.Errorf("channel label = %q, want telegram", podLabels["sympozium.ai/channel"])
	}
}

func TestBuildChannelDeployment_InstanceLabel(t *testing.T) {
	r := &AgentReconciler{}
	instance := newTestInstance()
	ch := instance.Spec.Channels[0]
	deploy := r.buildChannelDeployment(instance, ch, "test-instance-channel-telegram")

	podLabels := deploy.Spec.Template.Labels
	if podLabels["sympozium.ai/instance"] != "test-instance" {
		t.Errorf("instance label = %q, want test-instance", podLabels["sympozium.ai/instance"])
	}
}

func TestBuildChannelDeployment_AllChannelTypes(t *testing.T) {
	// Verify every supported channel type gets the correct "channel" component
	// label so that network policies apply uniformly.
	channelTypes := []string{"telegram", "discord", "slack", "whatsapp"}

	for _, chType := range channelTypes {
		t.Run(chType, func(t *testing.T) {
			r := &AgentReconciler{}
			instance := newTestInstance()
			ch := sympoziumv1alpha1.ChannelSpec{
				Type:      chType,
				ConfigRef: sympoziumv1alpha1.SecretRef{Secret: chType + "-secret"},
			}
			deploy := r.buildChannelDeployment(instance, ch, "test-instance-channel-"+chType)

			podLabels := deploy.Spec.Template.Labels
			if podLabels["sympozium.ai/component"] != "channel" {
				t.Errorf("component label = %q, want channel", podLabels["sympozium.ai/component"])
			}
			if podLabels["sympozium.ai/channel"] != chType {
				t.Errorf("channel label = %q, want %q", podLabels["sympozium.ai/channel"], chType)
			}
		})
	}
}

func TestBuildChannelDeployment_EventBusEnvVar(t *testing.T) {
	// Channel pods must be able to reach the NATS event bus to deliver messages.
	r := &AgentReconciler{}
	instance := newTestInstance()
	ch := instance.Spec.Channels[0]
	deploy := r.buildChannelDeployment(instance, ch, "test-instance-channel-telegram")

	container := deploy.Spec.Template.Spec.Containers[0]
	var found bool
	for _, env := range container.Env {
		if env.Name == "EVENT_BUS_URL" {
			found = true
			if !strings.Contains(env.Value, "nats://") {
				t.Errorf("EVENT_BUS_URL = %q, expected nats:// prefix", env.Value)
			}
			break
		}
	}
	if !found {
		t.Error("EVENT_BUS_URL env var not found on channel container")
	}
}

func TestBuildChannelDeployment_ImageRegistry(t *testing.T) {
	r := &AgentReconciler{ImageTag: "v0.1.5"}
	instance := newTestInstance()
	ch := instance.Spec.Channels[0]

	// Clear env to test default
	os.Unsetenv("SYMPOZIUM_IMAGE_REGISTRY")
	deploy := r.buildChannelDeployment(instance, ch, "test-instance-channel-telegram")

	container := deploy.Spec.Template.Spec.Containers[0]
	if !strings.Contains(container.Image, "channel-telegram:v0.1.5") {
		t.Errorf("image = %q, expected channel-telegram:v0.1.5", container.Image)
	}
}

// ── Agent-run labels remain unchanged ────────────────────────────────────────
// Guard against regressions: agent-run pods must keep their labels so the
// existing agent network policies continue to match.

func TestBuildJob_AgentRunComponentLabel(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	job, _ := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)

	labels := job.Spec.Template.Labels
	if labels["sympozium.ai/component"] != "agent-run" {
		t.Errorf("agent-run component label = %q, want agent-run", labels["sympozium.ai/component"])
	}
}

func TestBuildJob_AgentRunInstanceLabel(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	job, _ := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)

	labels := job.Spec.Template.Labels
	if labels["sympozium.ai/instance"] != "my-instance" {
		t.Errorf("agent-run instance label = %q, want my-instance", labels["sympozium.ai/instance"])
	}
}

// ── Label consistency: channel vs agent-run ──────────────────────────────────
// Channel pods and agent-run pods must use different component labels so that
// network policies can target them independently.

func TestComponentLabels_ChannelAndAgentRunAreDifferent(t *testing.T) {
	// Build a channel deployment
	ir := &AgentReconciler{}
	instance := newTestInstance()
	ch := instance.Spec.Channels[0]
	deploy := ir.buildChannelDeployment(instance, ch, "test-instance-channel-telegram")
	channelComponent := deploy.Spec.Template.Labels["sympozium.ai/component"]

	// Build an agent-run job
	ar := &AgentRunReconciler{}
	run := newTestRun()
	job, _ := ar.buildJob(context.Background(), run, false, nil, nil, nil, nil)
	agentComponent := job.Spec.Template.Labels["sympozium.ai/component"]

	if channelComponent == agentComponent {
		t.Errorf("channel and agent-run pods share component label %q — network policies cannot distinguish them", channelComponent)
	}
	if channelComponent != "channel" {
		t.Errorf("channel component = %q, want channel", channelComponent)
	}
	if agentComponent != "agent-run" {
		t.Errorf("agent-run component = %q, want agent-run", agentComponent)
	}
}
