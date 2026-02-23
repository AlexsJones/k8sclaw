// Package orchestrator handles the construction of Kubernetes Jobs/Pods
// for AgentRun executions and manages sub-agent spawning.
package orchestrator

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// PodBuilder constructs pod specs for agent runs.
type PodBuilder struct {
	// DefaultAgentImage is the default agent runner image.
	DefaultAgentImage string

	// DefaultIPCBridgeImage is the default IPC bridge sidecar image.
	DefaultIPCBridgeImage string

	// DefaultSandboxImage is the default sandbox sidecar image.
	DefaultSandboxImage string

	// EventBusURL is the URL of the event bus (NATS).
	EventBusURL string
}

// NewPodBuilder creates a PodBuilder with default settings.
func NewPodBuilder() *PodBuilder {
	return &PodBuilder{
		DefaultAgentImage:     "ghcr.io/alexsjones/k8sclaw/agent-runner:latest",
		DefaultIPCBridgeImage: "ghcr.io/alexsjones/k8sclaw/ipc-bridge:latest",
		DefaultSandboxImage:   "ghcr.io/alexsjones/k8sclaw/sandbox:latest",
		EventBusURL:           "nats://nats.k8sclaw-system.svc:4222",
	}
}

// AgentPodConfig holds configuration for building an agent pod.
type AgentPodConfig struct {
	RunID          string
	InstanceName   string
	AgentID        string
	SessionKey     string
	ModelProvider  string
	ModelName      string
	ThinkingMode   string
	AuthSecretRef  string
	SandboxEnabled bool
	SandboxImage   string
	SpawnDepth     int
	Skills         []SkillMount
}

// SkillMount describes a skill ConfigMap to mount.
type SkillMount struct {
	Name string
	Type string // "skillpack" or "configmap"
}

// BuildAgentContainer creates the main agent container spec.
func (pb *PodBuilder) BuildAgentContainer(config AgentPodConfig) corev1.Container {
	readOnly := true
	noPrivEsc := false

	return corev1.Container{
		Name:            "agent",
		Image:           pb.DefaultAgentImage,
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{
			ReadOnlyRootFilesystem:   &readOnly,
			AllowPrivilegeEscalation: &noPrivEsc,
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
		Env: []corev1.EnvVar{
			{Name: "AGENT_RUN_ID", Value: config.RunID},
			{Name: "AGENT_ID", Value: config.AgentID},
			{Name: "SESSION_KEY", Value: config.SessionKey},
			{Name: "INSTANCE_NAME", Value: config.InstanceName},
			{Name: "MODEL_PROVIDER", Value: config.ModelProvider},
			{Name: "MODEL_NAME", Value: config.ModelName},
			{Name: "THINKING_MODE", Value: config.ThinkingMode},
			{Name: "SPAWN_DEPTH", Value: fmt.Sprintf("%d", config.SpawnDepth)},
		},
		EnvFrom: []corev1.EnvFromSource{
			{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: config.AuthSecretRef,
					},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"},
			{Name: "skills", MountPath: "/skills", ReadOnly: true},
			{Name: "ipc", MountPath: "/ipc"},
			{Name: "tmp", MountPath: "/tmp"},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("250m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		},
	}
}

// BuildIPCBridgeContainer creates the IPC bridge sidecar container spec.
func (pb *PodBuilder) BuildIPCBridgeContainer(config AgentPodConfig) corev1.Container {
	return corev1.Container{
		Name:            "ipc-bridge",
		Image:           pb.DefaultIPCBridgeImage,
		ImagePullPolicy: corev1.PullAlways,
		Env: []corev1.EnvVar{
			{Name: "AGENT_RUN_ID", Value: config.RunID},
			{Name: "INSTANCE_NAME", Value: config.InstanceName},
			{Name: "EVENT_BUS_URL", Value: pb.EventBusURL},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "ipc", MountPath: "/ipc"},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
	}
}

// BuildSandboxContainer creates the sandbox sidecar container spec.
func (pb *PodBuilder) BuildSandboxContainer(config AgentPodConfig) corev1.Container {
	readOnly := true
	image := pb.DefaultSandboxImage
	if config.SandboxImage != "" {
		image = config.SandboxImage
	}

	return corev1.Container{
		Name:            "sandbox",
		Image:           image,
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{
			ReadOnlyRootFilesystem: &readOnly,
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
		Command: []string{"sleep", "infinity"},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"},
			{Name: "tmp", MountPath: "/tmp"},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}
}

// BuildVolumes creates the volume list for an agent pod.
func (pb *PodBuilder) BuildVolumes(config AgentPodConfig) []corev1.Volume {
	workspaceSize := resource.MustParse("1Gi")
	ipcSize := resource.MustParse("64Mi")
	tmpSize := resource.MustParse("256Mi")

	volumes := []corev1.Volume{
		{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &workspaceSize},
			},
		},
		{
			Name: "ipc",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: &ipcSize,
				},
			},
		},
		{
			Name: "tmp",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmpSize},
			},
		},
	}

	// Build projected skills volume
	var projections []corev1.VolumeProjection
	for _, skill := range config.Skills {
		projections = append(projections, corev1.VolumeProjection{
			ConfigMap: &corev1.ConfigMapProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: skill.Name},
			},
		})
	}

	if len(projections) > 0 {
		volumes = append(volumes, corev1.Volume{
			Name: "skills",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{Sources: projections},
			},
		})
	} else {
		volumes = append(volumes, corev1.Volume{
			Name: "skills",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

	return volumes
}
