package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/toolpolicy"
)

// Trigger sources recorded on the sympozium.ai/trigger-source label.
const (
	// StimulusTriggerSourceReadiness marks a stimulus the Ensemble controller
	// delivered automatically once every agent reported ready.
	StimulusTriggerSourceReadiness = "readiness"
	// StimulusTriggerSourceManual marks a stimulus a human asked for through
	// the trigger API.
	StimulusTriggerSourceManual = "manual"
)

// ResolveStimulusTarget returns the agent config name the ensemble's stimulus
// edge points at. Validation guarantees at most one such edge.
func ResolveStimulusTarget(pack *sympoziumv1alpha1.Ensemble) (string, error) {
	for _, rel := range pack.Spec.Relationships {
		if rel.Type == "stimulus" {
			return rel.Target, nil
		}
	}
	return "", fmt.Errorf("stimulus spec configured but no stimulus relationship found")
}

// BuildStimulusRun constructs the AgentRun that delivers an ensemble's stimulus
// prompt to its target agent.
//
// Both delivery paths — the readiness edge in the Ensemble controller and the
// manual trigger endpoint in the API server — build their run here. They used to
// each assemble one by hand, and had drifted: the API server defaulted the
// provider to "openai" instead of resolving it from the agent, and omitted the
// ToolPolicy entirely, so a manually triggered agent ran with tools its
// agent config had denied.
func BuildStimulusRun(
	ctx context.Context,
	c client.Reader,
	pack *sympoziumv1alpha1.Ensemble,
	targetInst *sympoziumv1alpha1.Agent,
	targetPersona string,
	triggerSource string,
	now time.Time,
) *sympoziumv1alpha1.AgentRun {
	targetAgentName := targetInst.Name
	runName := fmt.Sprintf("%s-stimulus-%d", targetAgentName, now.UnixMilli()%100000)

	return &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: pack.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance":       targetAgentName,
				"sympozium.ai/ensemble":       pack.Name,
				"sympozium.ai/stimulus":       "true",
				"sympozium.ai/trigger-source": triggerSource,
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: targetAgentName,
			Task:     sympoziumv1alpha1.NewStringTask(pack.Spec.Stimulus.Prompt),
			AgentID:  fmt.Sprintf("stimulus-%s", pack.Spec.Stimulus.Name),
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:                 resolveProvider(targetInst),
				Model:                    targetInst.Spec.Agents.Default.Model,
				BaseURL:                  targetInst.Spec.Agents.Default.BaseURL,
				AuthSecretRef:            resolveAuthSecret(targetInst),
				ProviderHeaders:          targetInst.Spec.Agents.Default.ProviderHeaders,
				ProviderHeadersSecretRef: targetInst.Spec.Agents.Default.ProviderHeadersSecretRef,
			},
			Skills:           targetInst.Spec.Skills,
			ImagePullSecrets: targetInst.Spec.ImagePullSecrets,
			Volumes:          targetInst.Spec.Volumes,
			VolumeMounts:     targetInst.Spec.VolumeMounts,
			Tolerations:      targetInst.Spec.Agents.Default.Tolerations,
			Env:              targetInst.Spec.Agents.Default.Env,
			Timeout:          targetInst.Spec.Agents.Default.ParseRunTimeout(),
			ToolPolicy:       toolpolicy.ForAgent(ctx, c, targetInst),
		},
	}
}
