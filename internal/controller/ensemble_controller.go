package controller

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

const ensembleFinalizer = "sympozium.ai/ensemble-finalizer"

// EnsembleReconciler reconciles Ensemble objects.
// It stamps out Agents, SympoziumSchedules, and memory
// ConfigMaps for each persona defined in the pack.
type EnsembleReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Log          logr.Logger
	EventBus     eventbus.EventBus
	DensityCache *DensityCache // optional: set when llmfit DaemonSet is enabled
}

// defaultObservabilitySpec builds an ObservabilitySpec from env vars injected
// by the Helm chart / kustomize, falling back to sensible defaults matching the
// built-in OTel collector's service address.
func defaultObservabilitySpec() *sympoziumv1alpha1.ObservabilitySpec {
	enabled := strings.EqualFold(os.Getenv("SYMPOZIUM_DEFAULT_OTEL_ENABLED"), "true")
	endpoint := os.Getenv("SYMPOZIUM_DEFAULT_OTEL_ENDPOINT")
	if endpoint == "" {
		endpoint = "sympozium-otel-collector.sympozium-system.svc:4317"
	}
	protocol := os.Getenv("SYMPOZIUM_DEFAULT_OTEL_PROTOCOL")
	if protocol == "" {
		protocol = "grpc"
	}
	serviceName := os.Getenv("SYMPOZIUM_DEFAULT_OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "sympozium"
	}
	return &sympoziumv1alpha1.ObservabilitySpec{
		Enabled:      enabled,
		OTLPEndpoint: endpoint,
		OTLPProtocol: protocol,
		ServiceName:  serviceName,
		ResourceAttributes: map[string]string{
			"deployment.environment": "cluster",
			"k8s.cluster.name":       "unknown",
		},
	}
}

func isManagedEnsembleAuthSecret(ensembleName, secretName string, labels map[string]string) bool {
	if strings.TrimSpace(secretName) == "" {
		return false
	}
	if labels != nil && labels["sympozium.ai/ensemble"] == ensembleName {
		return true
	}
	if secretName == ensembleName+"-credentials" {
		return true
	}
	// TUI-created naming convention: <pack>-<provider>-key
	if strings.HasPrefix(secretName, ensembleName+"-") && strings.HasSuffix(secretName, "-key") {
		return true
	}
	return false
}

func (r *EnsembleReconciler) deleteManagedAuthSecrets(ctx context.Context, pack *sympoziumv1alpha1.Ensemble) (int, error) {
	if pack == nil {
		return 0, nil
	}
	seen := make(map[string]struct{}, len(pack.Spec.AuthRefs))
	deleted := 0
	for _, ref := range pack.Spec.AuthRefs {
		name := strings.TrimSpace(ref.Secret)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}

		sec := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: pack.Namespace}, sec); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return deleted, err
		}
		if !isManagedEnsembleAuthSecret(pack.Name, name, sec.Labels) {
			continue
		}
		if err := r.Delete(ctx, sec); err != nil && !errors.IsNotFound(err) {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=ensembles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=ensembles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=ensembles/finalizers,verbs=update

// Reconcile handles Ensemble create/update/delete events.
func (r *EnsembleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("ensemble", req.NamespacedName)

	pack := &sympoziumv1alpha1.Ensemble{}
	if err := r.Get(ctx, req.NamespacedName, pack); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !pack.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, log, pack)
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(pack, ensembleFinalizer) {
		controllerutil.AddFinalizer(pack, ensembleFinalizer)
		if err := r.Update(ctx, pack); err != nil {
			return ctrl.Result{}, err
		}
	}

	// If the pack is not enabled, clean up any previously created
	// resources and mark the pack as Inactive (catalog-only).
	if !pack.Spec.Enabled {
		log.Info("Ensemble is not enabled, cleaning up any existing resources")
		for _, persona := range pack.Spec.AgentConfigs {
			if err := r.cleanupPersona(ctx, log, pack, &persona); err != nil {
				log.Error(err, "Failed to clean up persona for disabled pack", "persona", persona.Name)
			}
		}

		// Wait for stamped resources to actually disappear before deleting auth secrets.
		var instList sympoziumv1alpha1.AgentList
		if err := r.List(ctx, &instList, client.InNamespace(pack.Namespace), client.MatchingLabels{"sympozium.ai/ensemble": pack.Name}); err != nil {
			return ctrl.Result{}, err
		}
		var schedList sympoziumv1alpha1.SympoziumScheduleList
		if err := r.List(ctx, &schedList, client.InNamespace(pack.Namespace), client.MatchingLabels{"sympozium.ai/ensemble": pack.Name}); err != nil {
			return ctrl.Result{}, err
		}
		if len(instList.Items) > 0 || len(schedList.Items) > 0 {
			log.Info("Waiting for persona resources to terminate before auth secret cleanup",
				"instancesRemaining", len(instList.Items),
				"schedulesRemaining", len(schedList.Items))
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		if len(pack.Spec.AuthRefs) > 0 {
			deleted, err := r.deleteManagedAuthSecrets(ctx, pack)
			if err != nil {
				return ctrl.Result{}, err
			}
			if deleted > 0 {
				log.Info("Deleted managed Ensemble auth secrets", "count", deleted)
			}
		}

		// Clean up shared memory infrastructure when pack is disabled.
		if err := r.cleanupSharedMemory(ctx, log, pack); err != nil {
			log.Error(err, "Failed to clean up shared memory for disabled pack")
		}

		pack.Status.Phase = "Inactive"
		pack.Status.AgentConfigCount = len(pack.Spec.AgentConfigs)
		pack.Status.InstalledCount = 0
		pack.Status.InstalledAgentConfigs = nil
		pack.Status.SharedMemoryReady = false
		pack.Status.AllAgentsServing = false
		pack.Status.StimulusDelivered = false
		if err := r.Status().Update(ctx, pack); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Resolve modelRef once for the whole ensemble.
	var modelEndpoint string
	if pack.Spec.ModelRef != "" {
		model, err := ResolveModelRef(ctx, r.Client, pack.Spec.ModelRef, pack.Namespace)
		if err != nil {
			log.Info("Model not found for modelRef, waiting", "modelRef", pack.Spec.ModelRef)
			return ctrl.Result{RequeueAfter: 10_000_000_000}, nil // 10s
		}
		if model.Status.Phase != sympoziumv1alpha1.ModelPhaseReady {
			log.Info("Model not ready, waiting", "modelRef", pack.Spec.ModelRef, "phase", model.Status.Phase)
			return ctrl.Result{RequeueAfter: 10_000_000_000}, nil
		}
		modelEndpoint = model.Status.Endpoint
	}

	// Validate the relationship graph for cycles and stimulus constraints before proceeding.
	if err := validateRelationshipGraph(pack.Spec.AgentConfigs, pack.Spec.Relationships, pack.Spec.Stimulus, pack.Spec.WorkflowType); err != nil {
		log.Error(err, "Invalid relationship graph")
		pack.Status.Phase = "Error"
		if statusErr := r.Status().Update(ctx, pack); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, nil
	}

	// Reconcile each persona → instance + schedule + memory
	var installed []sympoziumv1alpha1.InstalledAgentConfig
	var installErr error
	for i, persona := range pack.Spec.AgentConfigs {
		// Skip personas that have been excluded (disabled via TUI).
		if isExcluded(persona.Name, pack.Spec.ExcludeAgentConfigs) {
			if err := r.cleanupPersona(ctx, log, pack, &persona); err != nil {
				log.Error(err, "Failed to clean up excluded persona", "persona", persona.Name)
			}
			continue
		}
		ip, err := r.reconcileAgentConfig(ctx, log, pack, &persona, i, modelEndpoint)
		if err != nil {
			log.Error(err, "Failed to reconcile persona", "persona", persona.Name)
			installErr = err
			continue
		}
		installed = append(installed, ip)
	}

	// Reconcile shared memory infrastructure for the pack.
	if err := r.reconcileSharedMemory(ctx, log, pack); err != nil {
		log.Error(err, "Failed to reconcile shared memory")
		installErr = err
	}

	// Update status
	pack.Status.AgentConfigCount = len(pack.Spec.AgentConfigs)
	pack.Status.InstalledCount = len(installed)
	pack.Status.InstalledAgentConfigs = installed
	if installErr != nil {
		pack.Status.Phase = "Error"
	} else {
		pack.Status.Phase = "Ready"
	}

	// Check if all agents are ready for stimulus delivery.
	if pack.Spec.Stimulus != nil && installErr == nil {
		if err := r.reconcileStimulus(ctx, log, pack); err != nil {
			log.Error(err, "Failed to reconcile stimulus")
		}
	}

	if err := r.Status().Update(ctx, pack); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, installErr
}

// reconcileAgentConfig ensures the Agent and optional
// SympoziumSchedule exist for one persona.
func (r *EnsembleReconciler) reconcileAgentConfig(
	ctx context.Context,
	log logr.Logger,
	pack *sympoziumv1alpha1.Ensemble,
	persona *sympoziumv1alpha1.AgentConfigSpec,
	personaIndex int,
	modelEndpoint string,
) (sympoziumv1alpha1.InstalledAgentConfig, error) {
	instanceName := pack.Name + "-" + persona.Name
	ip := sympoziumv1alpha1.InstalledAgentConfig{
		Name:         persona.Name,
		InstanceName: instanceName,
	}

	// --- Agent ---
	existingInst := &sympoziumv1alpha1.Agent{}
	err := r.Get(ctx, client.ObjectKey{Name: instanceName, Namespace: pack.Namespace}, existingInst)
	if errors.IsNotFound(err) {
		inst := r.buildAgent(pack, persona, instanceName, modelEndpoint)
		if err := ctrl.SetControllerReference(pack, inst, r.Scheme); err != nil {
			return ip, fmt.Errorf("set owner ref on instance: %w", err)
		}
		log.Info("Creating Agent for persona", "instance", instanceName, "persona", persona.Name)
		if err := r.Create(ctx, inst); err != nil {
			return ip, fmt.Errorf("create instance %s: %w", instanceName, err)
		}
	} else if err != nil {
		return ip, fmt.Errorf("get instance %s: %w", instanceName, err)
	} else {
		// Update pack-level settings on existing instances — authRefs, model,
		// and channels are owned by the pack, not per-instance configuration.
		needsUpdate := false

		// Propagate provider label.
		wantProvider := persona.Provider
		if existingInst.Labels["sympozium.ai/provider"] != wantProvider {
			if wantProvider != "" {
				existingInst.Labels["sympozium.ai/provider"] = wantProvider
			} else {
				delete(existingInst.Labels, "sympozium.ai/provider")
			}
			needsUpdate = true
		}

		// Propagate authRefs changes (filtered by persona provider).
		wantAuthRefs := resolveAuthRefs(pack, persona, modelEndpoint)
		if !authRefsEqual(existingInst.Spec.AuthRefs, wantAuthRefs) {
			existingInst.Spec.AuthRefs = wantAuthRefs
			needsUpdate = true
		}

		// Propagate model changes (with same defaults as buildAgent).
		wantModel := resolveModel(pack, persona, modelEndpoint)
		if existingInst.Spec.Agents.Default.Model != wantModel {
			existingInst.Spec.Agents.Default.Model = wantModel
			needsUpdate = true
		}

		// Propagate baseURL changes (e.g. switching to/from a local provider).
		wantBaseURL := resolveBaseURL(pack, persona, modelEndpoint)
		if existingInst.Spec.Agents.Default.BaseURL != wantBaseURL {
			existingInst.Spec.Agents.Default.BaseURL = wantBaseURL
			needsUpdate = true
		}

		// Propagate persona systemPrompt changes so edits to the pack
		// actually reach the running agents (otherwise a pack author
		// can't tune agent behaviour without re-stamping instances).
		if existingInst.Spec.Memory == nil {
			existingInst.Spec.Memory = &sympoziumv1alpha1.MemorySpec{
				Enabled:   true,
				MaxSizeKB: 256,
			}
			needsUpdate = true
		}
		if existingInst.Spec.Memory.SystemPrompt != persona.SystemPrompt {
			existingInst.Spec.Memory.SystemPrompt = persona.SystemPrompt
			needsUpdate = true
		}

		// Propagate channel list changes from persona definition.
		wantChannels := make(map[string]bool)
		for _, ch := range persona.Channels {
			wantChannels[ch] = true
		}
		haveChannels := make(map[string]bool)
		for _, ch := range existingInst.Spec.Channels {
			haveChannels[ch.Type] = true
		}
		if len(persona.Channels) > 0 && !channelSetsEqual(wantChannels, haveChannels) {
			var channelSpecs []sympoziumv1alpha1.ChannelSpec
			for _, ch := range persona.Channels {
				channelSpecs = append(channelSpecs, sympoziumv1alpha1.ChannelSpec{Type: ch})
			}
			existingInst.Spec.Channels = channelSpecs
			needsUpdate = true
		}

		// Always reconcile per-channel fields (ConfigRef, AccessControl,
		// Triggers, Volumes, VolumeMounts) so edits to ensemble/persona
		// channel configuration propagate without requiring agent recreation.
		for i := range existingInst.Spec.Channels {
			ch := &existingInst.Spec.Channels[i]
			desired := buildChannelSpec(pack, persona, ch.Type)
			if !reflect.DeepEqual(ch.ConfigRef, desired.ConfigRef) {
				ch.ConfigRef = desired.ConfigRef
				needsUpdate = true
			}
			if !reflect.DeepEqual(ch.AccessControl, desired.AccessControl) {
				ch.AccessControl = desired.AccessControl
				needsUpdate = true
			}
			if !reflect.DeepEqual(ch.Triggers, desired.Triggers) {
				ch.Triggers = desired.Triggers
				needsUpdate = true
			}
			if !reflect.DeepEqual(ch.Slack, desired.Slack) {
				ch.Slack = desired.Slack
				needsUpdate = true
			}
			if !reflect.DeepEqual(ch.Volumes, desired.Volumes) {
				ch.Volumes = desired.Volumes
				needsUpdate = true
			}
			if !reflect.DeepEqual(ch.VolumeMounts, desired.VolumeMounts) {
				ch.VolumeMounts = desired.VolumeMounts
				needsUpdate = true
			}
		}

		// Propagate provider headers changes.
		wantProviderHeaders := mergeProviderHeaders(pack.Spec.ProviderHeaders, persona.ProviderHeaders)
		if !reflect.DeepEqual(existingInst.Spec.Agents.Default.ProviderHeaders, wantProviderHeaders) {
			existingInst.Spec.Agents.Default.ProviderHeaders = wantProviderHeaders
			needsUpdate = true
		}
		wantHeadersSecretRef := resolveProviderHeadersSecretRef(pack, persona)
		if existingInst.Spec.Agents.Default.ProviderHeadersSecretRef != wantHeadersSecretRef {
			existingInst.Spec.Agents.Default.ProviderHeadersSecretRef = wantHeadersSecretRef
			needsUpdate = true
		}

		if existingInst.Spec.Agents.Default.RunTimeout != persona.RunTimeout {
			existingInst.Spec.Agents.Default.RunTimeout = persona.RunTimeout
			needsUpdate = true
		}

		// Propagate env changes from persona definition.
		if !reflect.DeepEqual(existingInst.Spec.Agents.Default.Env, persona.Env) {
			existingInst.Spec.Agents.Default.Env = persona.Env
			needsUpdate = true
		}

		// Propagate skills changes from persona definition.
		wantSkills := buildDesiredSkills(pack, persona)
		if !skillRefsEqual(existingInst.Spec.Skills, wantSkills) {
			existingInst.Spec.Skills = wantSkills
			needsUpdate = true
		}

		// Propagate MCP server changes from persona definition.
		if !mcpServerRefsEqual(existingInst.Spec.MCPServers, persona.MCPServers) {
			existingInst.Spec.MCPServers = persona.MCPServers
			needsUpdate = true
		}

		// Propagate subagent limits from persona definition so changes to
		// maxDepth, maxConcurrent, or maxChildrenPerAgent reach existing Agents.
		if !reflect.DeepEqual(existingInst.Spec.Agents.Default.Subagents, persona.Subagents) {
			existingInst.Spec.Agents.Default.Subagents = persona.Subagents
			needsUpdate = true
		}

		// Propagate volumes from ensemble.
		if !reflect.DeepEqual(existingInst.Spec.Volumes, pack.Spec.Volumes) {
			existingInst.Spec.Volumes = pack.Spec.Volumes
			needsUpdate = true
		}

		// Propagate volume mounts from ensemble.
		if !reflect.DeepEqual(existingInst.Spec.VolumeMounts, pack.Spec.VolumeMounts) {
			existingInst.Spec.VolumeMounts = pack.Spec.VolumeMounts
			needsUpdate = true
		}

		// Propagate sandbox config from ensemble.
		if !reflect.DeepEqual(existingInst.Spec.Agents.Default.AgentSandbox, pack.Spec.AgentSandbox) {
			existingInst.Spec.Agents.Default.AgentSandbox = pack.Spec.AgentSandbox
			needsUpdate = true
		}

		// Propagate lifecycle from persona.
		if !reflect.DeepEqual(existingInst.Spec.Agents.Default.Lifecycle, persona.Lifecycle) {
			existingInst.Spec.Agents.Default.Lifecycle = persona.Lifecycle
			needsUpdate = true
		}

		// Propagate policy ref from ensemble.
		if existingInst.Spec.PolicyRef != pack.Spec.PolicyRef {
			existingInst.Spec.PolicyRef = pack.Spec.PolicyRef
			needsUpdate = true
		}

		if needsUpdate {
			log.Info("Updating pack-level settings on existing instance", "instance", instanceName)
			if err := r.Update(ctx, existingInst); err != nil {
				return ip, fmt.Errorf("update instance %s: %w", instanceName, err)
			}
		}
	}
	// Instance is now up to date — users own other fields after creation.

	// --- Memory seeds ---
	if persona.Memory != nil && len(persona.Memory.Seeds) > 0 {
		if err := r.reconcileMemorySeeds(ctx, log, pack, persona, instanceName); err != nil {
			log.Error(err, "Failed to seed memory", "instance", instanceName)
			// Non-fatal: continue
		}
	}

	// --- SympoziumSchedule ---
	// In a "pipeline" ensemble, a persona that is the target of a sequential
	// edge runs only when its predecessor completes (see
	// triggerSequentialSuccessors in agentrun_controller.go). Giving it an
	// independent schedule would let it fire on its own clock — and because a
	// freshly created schedule backdates its first tick and runs immediately,
	// the successor starts in parallel with the pipeline head, bypassing the
	// ordering. Suppress the schedule for such successors so the predecessor's
	// completion is their only trigger.
	schedName := instanceName + "-schedule"
	wantSchedule := persona.Schedule != nil && !isSequentialSuccessor(pack, persona.Name)
	if wantSchedule {
		ip.ScheduleName = schedName

		desired := r.buildSchedule(pack, persona, instanceName, schedName, personaIndex)
		existingSched := &sympoziumv1alpha1.SympoziumSchedule{}
		err := r.Get(ctx, client.ObjectKey{Name: schedName, Namespace: pack.Namespace}, existingSched)
		if errors.IsNotFound(err) {
			if err := ctrl.SetControllerReference(pack, desired, r.Scheme); err != nil {
				return ip, fmt.Errorf("set owner ref on schedule: %w", err)
			}
			log.Info("Creating SympoziumSchedule for persona", "schedule", schedName, "persona", persona.Name)
			if err := r.Create(ctx, desired); err != nil {
				return ip, fmt.Errorf("create schedule %s: %w", schedName, err)
			}
		} else if err != nil {
			return ip, fmt.Errorf("get schedule %s: %w", schedName, err)
		} else {
			// Suspend is not expressible on the ensemble, so it can only have
			// been set out-of-band — by an operator or by an agent's
			// schedule_task tool. Carry it over: overwriting the whole spec
			// would silently resume a schedule someone deliberately paused.
			desired.Spec.Suspend = existingSched.Spec.Suspend

			needsUpdate := false
			if !reflect.DeepEqual(existingSched.Spec, desired.Spec) {
				existingSched.Spec = desired.Spec
				needsUpdate = true
			}
			if existingSched.Labels == nil {
				existingSched.Labels = map[string]string{}
			}
			for k, v := range desired.Labels {
				if existingSched.Labels[k] != v {
					existingSched.Labels[k] = v
					needsUpdate = true
				}
			}
			if needsUpdate {
				log.Info("Updating SympoziumSchedule for persona", "schedule", schedName, "persona", persona.Name)
				if err := r.Update(ctx, existingSched); err != nil {
					return ip, fmt.Errorf("update schedule %s: %w", schedName, err)
				}
			}
		}
	} else {
		// Either the persona has no schedule, or it's a pipeline successor whose
		// schedule is suppressed — remove any stale SympoziumSchedule so an
		// ensemble that previously ran with independent schedules is cleaned up.
		if persona.Schedule != nil && !wantSchedule {
			log.Info("Suppressing schedule for sequential successor — triggered by predecessor completion",
				"persona", persona.Name)
		}
		existingSched := &sympoziumv1alpha1.SympoziumSchedule{}
		err := r.Get(ctx, client.ObjectKey{Name: schedName, Namespace: pack.Namespace}, existingSched)
		if err == nil {
			log.Info("Deleting stale SympoziumSchedule for persona", "schedule", schedName, "persona", persona.Name)
			if err := r.Delete(ctx, existingSched); err != nil && !errors.IsNotFound(err) {
				return ip, fmt.Errorf("delete stale schedule %s: %w", schedName, err)
			}
		} else if !errors.IsNotFound(err) {
			return ip, fmt.Errorf("get stale schedule %s: %w", schedName, err)
		}
	}

	return ip, nil
}

// personaOverridesEndpoint reports whether a persona pins its own model provider
// and therefore opts out of the ensemble's cluster-local model endpoint. Setting
// a provider is the signal for a "mixed ensemble": the ensemble may default to a
// local model while this persona talks to a cloud provider directly, with its own
// model, base URL, and matching auth key.
func personaOverridesEndpoint(persona *sympoziumv1alpha1.AgentConfigSpec) bool {
	return persona.Provider != ""
}

// resolveAuthRefs selects the auth refs an agent should receive. A cluster-local
// endpoint needs none; a persona pinned to its own provider takes that provider's
// matching key even under a local endpoint (mixed ensembles).
func resolveAuthRefs(pack *sympoziumv1alpha1.Ensemble, persona *sympoziumv1alpha1.AgentConfigSpec, modelEndpoint string) []sympoziumv1alpha1.SecretRef {
	authRefs := pack.Spec.AuthRefs
	// Cluster-internal inference needs no external auth.
	if modelEndpoint != "" {
		authRefs = nil
	}
	// A provider-pinned persona uses that provider's key, overriding the
	// local-endpoint nil above so mixed ensembles authenticate correctly.
	if persona.Provider != "" {
		var matched []sympoziumv1alpha1.SecretRef
		for _, ref := range pack.Spec.AuthRefs {
			if ref.Provider == persona.Provider {
				matched = append(matched, ref)
			}
		}
		if len(matched) > 0 {
			authRefs = matched
		}
	}
	return authRefs
}

// resolveModel computes the desired model name from persona, ensemble defaults,
// and local model endpoint override.
// Precondition: when modelEndpoint is non-empty, pack.Spec.ModelRef must also be
// non-empty (the Reconcile method enforces this before calling resolveModel).
func resolveModel(pack *sympoziumv1alpha1.Ensemble, persona *sympoziumv1alpha1.AgentConfigSpec, modelEndpoint string) string {
	model := persona.Model
	if model == "" {
		model = "gpt-4o"
	}
	// The cluster-local model applies only to personas that haven't pinned their
	// own provider; a mixed-ensemble persona keeps its own model.
	if modelEndpoint != "" && pack.Spec.ModelRef != "" && !personaOverridesEndpoint(persona) {
		model = pack.Spec.ModelRef
	}
	return model
}

// resolveBaseURL computes the agent's model base URL. The ensemble's local
// endpoint applies unless the persona pins its own provider; an explicit
// persona.BaseURL always wins.
func resolveBaseURL(pack *sympoziumv1alpha1.Ensemble, persona *sympoziumv1alpha1.AgentConfigSpec, modelEndpoint string) string {
	baseURL := pack.Spec.BaseURL
	if modelEndpoint != "" && !personaOverridesEndpoint(persona) {
		baseURL = modelEndpoint
	}
	if persona.BaseURL != "" {
		baseURL = persona.BaseURL
	}
	return baseURL
}

// resolveProviderHeadersSecretRef selects the provider-headers secret, letting a
// persona-level ref override the ensemble-level one.
func resolveProviderHeadersSecretRef(pack *sympoziumv1alpha1.Ensemble, persona *sympoziumv1alpha1.AgentConfigSpec) string {
	ref := pack.Spec.ProviderHeadersSecretRef
	if persona.ProviderHeadersSecretRef != "" {
		ref = persona.ProviderHeadersSecretRef
	}
	return ref
}

// buildAgent creates a Agent spec from a persona definition.
func (r *EnsembleReconciler) buildAgent(
	pack *sympoziumv1alpha1.Ensemble,
	persona *sympoziumv1alpha1.AgentConfigSpec,
	instanceName string,
	modelEndpoint string,
) *sympoziumv1alpha1.Agent {
	model := resolveModel(pack, persona, modelEndpoint)
	authRefs := resolveAuthRefs(pack, persona, modelEndpoint)
	baseURL := resolveBaseURL(pack, persona, modelEndpoint)

	// Merge provider headers: ensemble-level base, persona-level overrides.
	providerHeaders := mergeProviderHeaders(pack.Spec.ProviderHeaders, persona.ProviderHeaders)
	providerHeadersSecretRef := resolveProviderHeadersSecretRef(pack, persona)

	labels := map[string]string{
		"sympozium.ai/ensemble":     pack.Name,
		"sympozium.ai/agent-config": persona.Name,
	}
	if persona.Provider != "" {
		labels["sympozium.ai/provider"] = persona.Provider
	}

	inst := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName,
			Namespace: pack.Namespace,
			Labels:    labels,
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			DisplayName: persona.DisplayName,
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model:                    model,
					BaseURL:                  baseURL,
					ProviderHeaders:          providerHeaders,
					ProviderHeadersSecretRef: providerHeadersSecretRef,
					AgentSandbox:             pack.Spec.AgentSandbox,
					Lifecycle:                persona.Lifecycle,
					Subagents:                persona.Subagents,
					Env:                      persona.Env,
					RunTimeout:               persona.RunTimeout,
				},
			},
			AuthRefs: authRefs,
			Memory: &sympoziumv1alpha1.MemorySpec{
				Enabled:      true,
				MaxSizeKB:    256,
				SystemPrompt: persona.SystemPrompt,
			},
			Observability: defaultObservabilitySpec(),
			Volumes:       pack.Spec.Volumes,
			VolumeMounts:  pack.Spec.VolumeMounts,
		},
	}

	// Skills — skip "mcp-bridge" which is a sidecar marker, not a SkillPack.
	for _, s := range persona.Skills {
		if s == "mcp-bridge" {
			continue
		}
		ref := sympoziumv1alpha1.SkillRef{
			SkillPackRef: s,
		}
		// Apply pack-level skill params if configured (e.g. repo for github-gitops).
		if pack.Spec.SkillParams != nil {
			if params, ok := pack.Spec.SkillParams[s]; ok && len(params) > 0 {
				ref.Params = params
			}
		}
		inst.Spec.Skills = append(inst.Spec.Skills, ref)
	}

	// Ensure memory skill is always attached.
	hasMemory := false
	for _, s := range inst.Spec.Skills {
		if s.SkillPackRef == "memory" {
			hasMemory = true
			break
		}
	}
	if !hasMemory {
		inst.Spec.Skills = append(inst.Spec.Skills, sympoziumv1alpha1.SkillRef{
			SkillPackRef: "memory",
		})
	}

	// Channels
	for _, ch := range persona.Channels {
		cs := buildChannelSpec(pack, persona, ch)
		inst.Spec.Channels = append(inst.Spec.Channels, cs)
	}

	// MCP servers — persona-level configuration, mirrors how Skills works.
	inst.Spec.MCPServers = persona.MCPServers

	// Policy — use the pack's policy ref if set.
	inst.Spec.PolicyRef = pack.Spec.PolicyRef

	// Web endpoint — add the web-endpoint skill instead of the legacy field.
	if persona.WebEndpoint != nil && persona.WebEndpoint.Enabled {
		params := map[string]string{}
		if persona.WebEndpoint.Hostname != "" {
			params["hostname"] = persona.WebEndpoint.Hostname
		}
		inst.Spec.Skills = append(inst.Spec.Skills, sympoziumv1alpha1.SkillRef{
			SkillPackRef: "web-endpoint",
			Params:       params,
		})
	}

	return inst
}

// isSequentialSuccessor reports whether a persona's execution is driven by an
// upstream persona completing rather than by its own schedule. Any persona that
// is the target of a sequential edge is triggered by triggerSequentialSuccessors
// when its predecessor succeeds, so it must not also get an independent
// SympoziumSchedule — the two would race, and the scheduled run would fire at
// t=0 with no predecessor output to act on.
//
// This holds for every workflow type, not just "pipeline": a sequential edge
// means the same thing wherever it is declared.
func isSequentialSuccessor(pack *sympoziumv1alpha1.Ensemble, personaName string) bool {
	for _, rel := range pack.Spec.Relationships {
		if rel.Type == "sequential" && rel.Target == personaName {
			return true
		}
	}
	return false
}

// buildSchedule creates a SympoziumSchedule from a persona's schedule config.
// personaIndex is used to stagger interval-based schedules so that personas in
// the same pack don't fire simultaneously and contend for a shared LLM.
func (r *EnsembleReconciler) buildSchedule(
	pack *sympoziumv1alpha1.Ensemble,
	persona *sympoziumv1alpha1.AgentConfigSpec,
	instanceName, schedName string,
	personaIndex int,
) *sympoziumv1alpha1.SympoziumSchedule {
	cron := persona.Schedule.Cron
	if cron == "" {
		// Stagger each persona by 2 minutes to avoid GPU contention on local LLMs.
		// For a 5-min interval with 7 personas this gives offsets 0,2,4,1,3,0,2 —
		// at most 2 agents overlap instead of all 7 firing simultaneously.
		staggerMin := personaIndex * 2
		cron = intervalToCron(persona.Schedule.Interval, staggerMin)
	}

	return &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      schedName,
			Namespace: pack.Namespace,
			Labels: map[string]string{
				"sympozium.ai/ensemble":     pack.Name,
				"sympozium.ai/agent-config": persona.Name,
			},
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			AgentRef:          instanceName,
			Schedule:          cron,
			Task:              r.buildScheduleTask(pack, persona),
			Type:              persona.Schedule.Type,
			ConcurrencyPolicy: "Forbid",
			IncludeMemory:     true,
			FirstTick:         persona.Schedule.FirstTick,
		},
	}
}

// buildScheduleTask constructs the task string for a persona's schedule.
// If the pack has a TaskOverride, it prepends the team-level directive.
func (r *EnsembleReconciler) buildScheduleTask(
	pack *sympoziumv1alpha1.Ensemble,
	persona *sympoziumv1alpha1.AgentConfigSpec,
) string {
	base := persona.Schedule.Task
	if pack.Spec.TaskOverride != "" {
		return fmt.Sprintf("TEAM OBJECTIVE: %s\n\nYOUR ROLE TASK: %s", pack.Spec.TaskOverride, base)
	}
	return base
}

// reconcileMemorySeeds creates or patches the memory ConfigMap with seed data.
func (r *EnsembleReconciler) reconcileMemorySeeds(
	ctx context.Context,
	log logr.Logger,
	pack *sympoziumv1alpha1.Ensemble,
	persona *sympoziumv1alpha1.AgentConfigSpec,
	instanceName string,
) error {
	cmName := instanceName + "-memory"

	var cm corev1.ConfigMap
	err := r.Get(ctx, client.ObjectKey{Name: cmName, Namespace: pack.Namespace}, &cm)
	if errors.IsNotFound(err) {
		// Create with seeds
		var sb strings.Builder
		sb.WriteString("# Memory\n\n")
		for _, seed := range persona.Memory.Seeds {
			sb.WriteString("- " + seed + "\n")
		}
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: pack.Namespace,
				Labels: map[string]string{
					"sympozium.ai/ensemble":     pack.Name,
					"sympozium.ai/agent-config": persona.Name,
					"sympozium.ai/memory":       "true",
				},
			},
			Data: map[string]string{
				"MEMORY.md": sb.String(),
			},
		}
		log.Info("Creating memory ConfigMap with seeds", "configmap", cmName)
		return r.Create(ctx, &cm)
	} else if err != nil {
		return err
	}

	// ConfigMap already exists — don't overwrite user memory
	return nil
}

// intervalToCron converts a human-readable interval to a cron expression.
// offsetMin staggers the schedule by shifting the minute field, so that
// personas in the same pack don't all fire simultaneously and contend for
// a shared LLM (especially important with local models like LM Studio).
func intervalToCron(interval string, offsetMin int) string {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "1m", "1min":
		return "* * * * *" // can't stagger 1-minute intervals
	case "5m", "5min":
		return fmt.Sprintf("%d/5 * * * *", offsetMin%5)
	case "10m", "10min":
		return fmt.Sprintf("%d/10 * * * *", offsetMin%10)
	case "15m", "15min":
		return fmt.Sprintf("%d/15 * * * *", offsetMin%15)
	case "30m", "30min":
		return fmt.Sprintf("%d/30 * * * *", offsetMin%30)
	case "1h", "60m":
		return fmt.Sprintf("%d * * * *", offsetMin%60)
	case "2h":
		return fmt.Sprintf("%d */2 * * *", offsetMin%60)
	case "3h":
		return fmt.Sprintf("%d */3 * * *", offsetMin%60)
	case "4h":
		return fmt.Sprintf("%d */4 * * *", offsetMin%60)
	case "6h":
		return fmt.Sprintf("%d */6 * * *", offsetMin%60)
	case "12h":
		return fmt.Sprintf("%d */12 * * *", offsetMin%60)
	case "24h", "1d":
		return fmt.Sprintf("%d 0 * * *", offsetMin%60)
	default:
		// If it already looks like a cron expression, return as-is
		if strings.Contains(interval, " ") {
			return interval
		}
		return fmt.Sprintf("%d * * * *", offsetMin%60) // default: hourly
	}
}

// isExcluded checks whether a persona name appears in the exclusion list.
func isExcluded(name string, excludes []string) bool {
	for _, e := range excludes {
		if e == name {
			return true
		}
	}
	return false
}

// cleanupPersona deletes the Instance, Schedule, and memory ConfigMap
// for a persona that has been excluded from the pack.
func (r *EnsembleReconciler) cleanupPersona(
	ctx context.Context,
	log logr.Logger,
	pack *sympoziumv1alpha1.Ensemble,
	persona *sympoziumv1alpha1.AgentConfigSpec,
) error {
	instanceName := pack.Name + "-" + persona.Name

	// Cancel active AgentRuns and delete all runs for this persona.
	var runList sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &runList, client.InNamespace(pack.Namespace), client.MatchingLabels{"sympozium.ai/instance": instanceName}); err == nil {
		for i := range runList.Items {
			run := &runList.Items[i]
			switch run.Status.Phase {
			case sympoziumv1alpha1.AgentRunPhaseRunning,
				sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate,
				sympoziumv1alpha1.AgentRunPhasePending,
				sympoziumv1alpha1.AgentRunPhaseServing:
				log.Info("Cancelling running AgentRun for disabled persona", "agentrun", run.Name)
				if run.Status.PodName != "" {
					pod := &corev1.Pod{}
					if err := r.Get(ctx, client.ObjectKey{Name: run.Status.PodName, Namespace: pack.Namespace}, pod); err == nil {
						if err := r.Delete(ctx, pod); err != nil && !errors.IsNotFound(err) {
							log.Error(err, "Failed to delete pod for cancelled AgentRun", "pod", run.Status.PodName)
						}
					}
				}
				run.Status.Phase = sympoziumv1alpha1.AgentRunPhaseFailed
				if err := r.Status().Update(ctx, run); err != nil && !errors.IsNotFound(err) {
					log.Error(err, "Failed to mark AgentRun as failed", "agentrun", run.Name)
				}
			}
			// Delete all runs (active or terminal) so the ensemble starts clean on re-enable.
			if err := r.Delete(ctx, run); err != nil && !errors.IsNotFound(err) {
				log.Error(err, "Failed to delete AgentRun for disabled persona", "agentrun", run.Name)
			}
		}
	}

	// Delete Agent
	inst := &sympoziumv1alpha1.Agent{}
	if err := r.Get(ctx, client.ObjectKey{Name: instanceName, Namespace: pack.Namespace}, inst); err == nil {
		log.Info("Deleting excluded persona instance", "instance", instanceName)
		if err := r.Delete(ctx, inst); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete instance %s: %w", instanceName, err)
		}
	}

	// Delete SympoziumSchedule
	schedName := instanceName + "-schedule"
	sched := &sympoziumv1alpha1.SympoziumSchedule{}
	if err := r.Get(ctx, client.ObjectKey{Name: schedName, Namespace: pack.Namespace}, sched); err == nil {
		log.Info("Deleting excluded persona schedule", "schedule", schedName)
		if err := r.Delete(ctx, sched); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete schedule %s: %w", schedName, err)
		}
	}

	// Delete memory ConfigMap
	cmName := instanceName + "-memory"
	var cm corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKey{Name: cmName, Namespace: pack.Namespace}, &cm); err == nil {
		log.Info("Deleting excluded persona memory", "configmap", cmName)
		if err := r.Delete(ctx, &cm); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete configmap %s: %w", cmName, err)
		}
	}

	return nil
}

// reconcileDelete cleans up resources owned by the Ensemble.
func (r *EnsembleReconciler) reconcileDelete(
	ctx context.Context,
	log logr.Logger,
	pack *sympoziumv1alpha1.Ensemble,
) (ctrl.Result, error) {
	log.Info("Reconciling Ensemble deletion")

	// Clean up shared memory infrastructure.
	if err := r.cleanupSharedMemory(ctx, log, pack); err != nil {
		log.Error(err, "Failed to clean up shared memory during deletion")
	}

	// Owner references handle cascade deletion of instances and schedules,
	// but we clean up memory ConfigMaps explicitly since they may not
	// have owner references.
	for _, persona := range pack.Spec.AgentConfigs {
		cmName := pack.Name + "-" + persona.Name + "-memory"
		var cm corev1.ConfigMap
		if err := r.Get(ctx, client.ObjectKey{Name: cmName, Namespace: pack.Namespace}, &cm); err == nil {
			log.Info("Deleting memory ConfigMap", "configmap", cmName)
			if err := r.Delete(ctx, &cm); err != nil && !errors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
	}

	controllerutil.RemoveFinalizer(pack, ensembleFinalizer)
	if err := r.Update(ctx, pack); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// authRefsEqual returns true if two SecretRef slices are equivalent.
func authRefsEqual(a, b []sympoziumv1alpha1.SecretRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Provider != b[i].Provider || a[i].Secret != b[i].Secret {
			return false
		}
	}
	return true
}

// mergeProviderHeaders merges ensemble-level and persona-level provider headers.
// Persona keys take precedence on collision. Returns nil if both inputs are empty.
func mergeProviderHeaders(ensembleHeaders, personaHeaders map[string]string) map[string]string {
	if len(ensembleHeaders) == 0 && len(personaHeaders) == 0 {
		return nil
	}
	merged := make(map[string]string)
	for k, v := range ensembleHeaders {
		merged[k] = v
	}
	for k, v := range personaHeaders {
		merged[k] = v
	}
	return merged
}

// channelSetsEqual returns true if two channel sets contain the same types.
func channelSetsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// buildChannelSpec computes the desired ChannelSpec for a given channel type
// from pack and persona configuration. Persona-level overrides take priority
// over ensemble-level defaults for AccessControl and Triggers.
func buildChannelSpec(pack *sympoziumv1alpha1.Ensemble, persona *sympoziumv1alpha1.AgentConfigSpec, ch string) sympoziumv1alpha1.ChannelSpec {
	cs := sympoziumv1alpha1.ChannelSpec{Type: ch}
	if pack.Spec.ChannelConfigs != nil {
		if secretName, ok := pack.Spec.ChannelConfigs[ch]; ok && secretName != "" {
			cs.ConfigRef = sympoziumv1alpha1.SecretRef{Secret: secretName}
		}
	}
	if persona.ChannelAccessControl != nil {
		if ac, ok := persona.ChannelAccessControl[ch]; ok {
			cs.AccessControl = ac
		}
	}
	if cs.AccessControl == nil && pack.Spec.ChannelAccessControl != nil {
		if ac, ok := pack.Spec.ChannelAccessControl[ch]; ok {
			cs.AccessControl = ac
		}
	}
	if persona.ChannelTriggers != nil {
		if tr, ok := persona.ChannelTriggers[ch]; ok {
			cs.Triggers = tr
		}
	}
	if cs.Triggers == nil && pack.Spec.ChannelTriggers != nil {
		if tr, ok := pack.Spec.ChannelTriggers[ch]; ok {
			cs.Triggers = tr
		}
	}
	// Slack-specific options: persona-level overrides take priority
	// over ensemble-level. Only applied to the slack channel type.
	if ch == "slack" {
		if persona.SlackOptions != nil {
			cs.Slack = persona.SlackOptions
		} else if pack.Spec.SlackOptions != nil {
			cs.Slack = pack.Spec.SlackOptions
		}
	}
	if v, ok := pack.Spec.ChannelVolumes[ch]; ok {
		cs.Volumes = v
	}
	if vm, ok := pack.Spec.ChannelVolumeMounts[ch]; ok {
		cs.VolumeMounts = vm
	}
	return cs
}

// buildDesiredSkills computes the desired skills list for a persona, matching
// the logic in buildAgent. This is used to reconcile skills on existing Agents.
func buildDesiredSkills(pack *sympoziumv1alpha1.Ensemble, persona *sympoziumv1alpha1.AgentConfigSpec) []sympoziumv1alpha1.SkillRef {
	var skills []sympoziumv1alpha1.SkillRef
	for _, s := range persona.Skills {
		if s == "mcp-bridge" {
			continue
		}
		ref := sympoziumv1alpha1.SkillRef{
			SkillPackRef: s,
		}
		if pack.Spec.SkillParams != nil {
			if params, ok := pack.Spec.SkillParams[s]; ok && len(params) > 0 {
				ref.Params = params
			}
		}
		skills = append(skills, ref)
	}

	// Ensure memory skill is always attached.
	hasMemory := false
	for _, s := range skills {
		if s.SkillPackRef == "memory" {
			hasMemory = true
			break
		}
	}
	if !hasMemory {
		skills = append(skills, sympoziumv1alpha1.SkillRef{
			SkillPackRef: "memory",
		})
	}

	// Web endpoint skill.
	if persona.WebEndpoint != nil && persona.WebEndpoint.Enabled {
		params := map[string]string{}
		if persona.WebEndpoint.Hostname != "" {
			params["hostname"] = persona.WebEndpoint.Hostname
		}
		skills = append(skills, sympoziumv1alpha1.SkillRef{
			SkillPackRef: "web-endpoint",
			Params:       params,
		})
	}

	return skills
}

// skillRefsEqual compares two SkillRef slices for equality.
func skillRefsEqual(a, b []sympoziumv1alpha1.SkillRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].SkillPackRef != b[i].SkillPackRef || a[i].ConfigMapRef != b[i].ConfigMapRef {
			return false
		}
		if len(a[i].Params) != len(b[i].Params) {
			return false
		}
		for k, v := range a[i].Params {
			if b[i].Params[k] != v {
				return false
			}
		}
	}
	return true
}

// mcpServerRefsEqual compares two MCPServerRef slices for equality.
func mcpServerRefsEqual(a, b []sympoziumv1alpha1.MCPServerRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// reconcileSharedMemory ensures PVC, Deployment, and Service exist for the
// pack-level shared memory server when SharedMemory is enabled.
func (r *EnsembleReconciler) reconcileSharedMemory(ctx context.Context, log logr.Logger, pack *sympoziumv1alpha1.Ensemble) error {
	if pack.Spec.SharedMemory == nil || !pack.Spec.SharedMemory.Enabled {
		// Shared memory not requested — clean up any existing resources.
		if pack.Status.SharedMemoryReady {
			return r.cleanupSharedMemory(ctx, log, pack)
		}
		return nil
	}

	ensembleName := pack.Name
	ns := pack.Namespace

	pvcName := ensembleName + "-shared-memory-db"
	deployName := ensembleName + "-shared-memory"

	storageSize := pack.Spec.SharedMemory.StorageSize
	if storageSize == "" {
		storageSize = "1Gi"
	}

	tag := os.Getenv("SYMPOZIUM_IMAGE_TAG")
	if tag == "" {
		tag = "latest"
	}
	registry := os.Getenv("SYMPOZIUM_IMAGE_REGISTRY")
	if registry == "" {
		registry = "ghcr.io/sympozium-ai/sympozium"
	}
	image := fmt.Sprintf("%s/skill-memory:%s", registry, tag)

	sharedLabels := map[string]string{
		"sympozium.ai/component": "shared-memory",
		"sympozium.ai/ensemble":  ensembleName,
	}

	// --- PVC ---
	var existingPVC corev1.PersistentVolumeClaim
	if err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: ns}, &existingPVC); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		pvc := corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: ns,
				Labels:    sharedLabels,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(storageSize),
					},
				},
			},
		}
		if err := controllerutil.SetControllerReference(pack, &pvc, r.Scheme); err != nil {
			return err
		}
		log.Info("Creating shared memory PVC", "name", pvcName)
		if err := r.Create(ctx, &pvc); err != nil {
			return err
		}
	}

	// --- Deployment ---
	var existingDeploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: deployName, Namespace: ns}, &existingDeploy); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}

		replicas := int32(1)
		fsGroup := int64(1000)
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deployName,
				Namespace: ns,
				Labels:    sharedLabels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Strategy: appsv1.DeploymentStrategy{
					Type: appsv1.RecreateDeploymentStrategyType,
				},
				Selector: &metav1.LabelSelector{
					MatchLabels: sharedLabels,
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: sharedLabels,
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:            "memory-server",
								Image:           image,
								ImagePullPolicy: corev1.PullIfNotPresent,
								Ports: []corev1.ContainerPort{
									{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
								},
								Env: []corev1.EnvVar{
									{Name: "MEMORY_DB_PATH", Value: "/data/memory.db"},
									{Name: "MEMORY_PORT", Value: "8080"},
								},
								VolumeMounts: []corev1.VolumeMount{
									{Name: "memory-db", MountPath: "/data"},
								},
								StartupProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/health",
											Port: intstr.FromInt32(8080),
										},
									},
									PeriodSeconds:    2,
									FailureThreshold: 30,
								},
								ReadinessProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/health",
											Port: intstr.FromInt32(8080),
										},
									},
									PeriodSeconds: 10,
								},
								LivenessProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/health",
											Port: intstr.FromInt32(8080),
										},
									},
									InitialDelaySeconds: 5,
									PeriodSeconds:       30,
								},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("50m"),
										corev1.ResourceMemory: resource.MustParse("64Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("200m"),
										corev1.ResourceMemory: resource.MustParse("128Mi"),
									},
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "memory-db",
								VolumeSource: corev1.VolumeSource{
									PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
										ClaimName: pvcName,
									},
								},
							},
						},
						SecurityContext: &corev1.PodSecurityContext{
							FSGroup: &fsGroup,
						},
					},
				},
			},
		}

		// Gate the admin-only DELETE /delete endpoint on a Secret-backed bearer
		// token, injected into the shared-memory pod only (never into agents), so
		// workflow_memory_* entries are equally deletable and token-guarded.
		deploy.Spec.Template.Spec.Containers[0].Env = append(deploy.Spec.Template.Spec.Containers[0].Env, memoryAdminTokenEnv()...)

		if err := controllerutil.SetControllerReference(pack, deploy, r.Scheme); err != nil {
			return err
		}
		log.Info("Creating shared memory Deployment", "name", deployName)
		if err := r.Create(ctx, deploy); err != nil {
			return err
		}
	} else {
		// Already exists. The rest of the spec is deliberately left alone, but the
		// admin-token env is reconciled so enabling adminDelete (or pointing it at a
		// different Secret) takes effect without deleting the Deployment.
		if err := syncMemoryAdminTokenEnv(ctx, r.Client, log, &existingDeploy); err != nil {
			return err
		}
	}

	// --- Service ---
	var existingSvc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Name: deployName, Namespace: ns}, &existingSvc); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deployName,
				Namespace: ns,
				Labels:    sharedLabels,
			},
			Spec: corev1.ServiceSpec{
				Selector: sharedLabels,
				Ports: []corev1.ServicePort{
					{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP},
				},
			},
		}
		if err := controllerutil.SetControllerReference(pack, svc, r.Scheme); err != nil {
			return err
		}
		log.Info("Creating shared memory Service", "name", deployName)
		if err := r.Create(ctx, svc); err != nil {
			return err
		}
	}

	pack.Status.SharedMemoryReady = true
	return nil
}

// cleanupSharedMemory deletes the PVC, Deployment, and Service for shared memory.
func (r *EnsembleReconciler) cleanupSharedMemory(ctx context.Context, log logr.Logger, pack *sympoziumv1alpha1.Ensemble) error {
	ensembleName := pack.Name
	ns := pack.Namespace
	deployName := ensembleName + "-shared-memory"
	pvcName := ensembleName + "-shared-memory-db"

	// Delete Service
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: ns}}
	if err := r.Delete(ctx, svc); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete shared memory service: %w", err)
	}

	// Delete Deployment
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: ns}}
	if err := r.Delete(ctx, deploy); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete shared memory deployment: %w", err)
	}

	// Delete PVC
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: ns}}
	if err := r.Delete(ctx, pvc); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete shared memory pvc: %w", err)
	}

	log.Info("Cleaned up shared memory resources", "pack", ensembleName)
	pack.Status.SharedMemoryReady = false
	return nil
}

// reconcileStimulus checks whether all agents in the ensemble are ready
// (Running or Serving phase). On the transition edge (not-all-ready →
// all-ready), it creates an AgentRun targeting the stimulus relationship
// target. We accept "Running" (not just "Serving") because the stimulus
// typically creates the *first* AgentRun for the target agent.
func (r *EnsembleReconciler) reconcileStimulus(ctx context.Context, log logr.Logger, pack *sympoziumv1alpha1.Ensemble) error {
	// Count agents whose infrastructure is ready (Running or Serving).
	// "Running" means the Agent CRD is reconciled and memory deployments
	// are up; "Serving" means it additionally has active AgentRun pods.
	// The stimulus creates the *first* AgentRun, so we cannot require
	// Serving — that would be a deadlock.
	readyCount := 0
	for _, ip := range pack.Status.InstalledAgentConfigs {
		var agent sympoziumv1alpha1.Agent
		if err := r.Get(ctx, types.NamespacedName{Name: ip.InstanceName, Namespace: pack.Namespace}, &agent); err != nil {
			continue
		}
		if agent.Status.Phase == "Running" || agent.Status.Phase == "Serving" {
			readyCount++
		}
	}

	allReady := readyCount > 0 && readyCount == len(pack.Status.InstalledAgentConfigs)
	prevAllReady := pack.Status.AllAgentsServing
	pack.Status.AllAgentsServing = allReady

	// A manual stimulus never fires on readiness — the ensemble goes Ready and
	// waits for the trigger API. AllAgentsServing is still tracked above so the
	// edge is available to anything else that watches it.
	if !pack.Spec.Stimulus.FiresOnReady() {
		if !prevAllReady && allReady {
			log.Info("Ensemble ready; stimulus is manual and awaits an explicit trigger",
				"ensemble", pack.Name, "stimulus", pack.Spec.Stimulus.Name)
		}
		return nil
	}

	// Detect the edge: not-all-ready → all-ready.
	if !prevAllReady && allReady && !pack.Status.StimulusDelivered {
		if err := r.deliverStimulus(ctx, log, pack, StimulusTriggerSourceReadiness); err != nil {
			return err
		}
	}

	return nil
}

// deliverStimulus creates an AgentRun for the stimulus target agent.
func (r *EnsembleReconciler) deliverStimulus(ctx context.Context, log logr.Logger, pack *sympoziumv1alpha1.Ensemble, triggerSource string) error {
	// Resolve stimulus relationship target.
	targetPersona, err := ResolveStimulusTarget(pack)
	if err != nil {
		return err
	}

	targetAgentName := pack.Name + "-" + targetPersona

	// Look up the target agent instance.
	var targetInst sympoziumv1alpha1.Agent
	if err := r.Get(ctx, types.NamespacedName{Name: targetAgentName, Namespace: pack.Namespace}, &targetInst); err != nil {
		return fmt.Errorf("stimulus target agent %q not found: %w", targetAgentName, err)
	}

	agentRun := BuildStimulusRun(ctx, r.Client, pack, &targetInst, targetPersona, triggerSource, time.Now())
	runName := agentRun.Name

	if err := r.Create(ctx, agentRun); err != nil {
		return fmt.Errorf("failed to create stimulus AgentRun: %w", err)
	}

	log.Info("Delivered stimulus",
		"run", runName,
		"target", targetPersona,
		"trigger", triggerSource,
		"generation", pack.Status.StimulusGeneration+1)

	pack.Status.StimulusDelivered = true
	pack.Status.StimulusGeneration++

	// Publish event to the bus if available.
	if r.EventBus != nil {
		r.EventBus.Publish(ctx, eventbus.TopicStimulusDelivered, &eventbus.Event{
			Topic:     eventbus.TopicStimulusDelivered,
			Timestamp: time.Now(),
			Metadata: map[string]string{
				"ensemble":      pack.Name,
				"target":        targetPersona,
				"triggerSource": triggerSource,
				"runName":       runName,
			},
		})
	}

	return nil
}

// validateRelationshipGraph checks that all relationship source/target names
// reference existing personas and that the sequential edges form a DAG (no
// cycles). Delegation and supervision edges are not checked for cycles because
// delegation is on-demand and supervision has no runtime effect.
// It also validates stimulus relationship constraints and that the declared
// workflowType is backed by the relationship edges that drive it.
func validateRelationshipGraph(personas []sympoziumv1alpha1.AgentConfigSpec, relationships []sympoziumv1alpha1.AgentConfigRelationship, stimulus *sympoziumv1alpha1.StimulusSpec, workflowType string) error {
	// A declared workflowType must be backed by the relationship edges that
	// drive it; otherwise the orchestration pattern silently does nothing. Only
	// "pipeline" (sequential edges) and "delegation" (delegation edges) have
	// runtime behaviour — "autonomous" is the implicit default and needs none.
	switch workflowType {
	case "pipeline":
		if !hasRelationshipType(relationships, "sequential") {
			return fmt.Errorf("workflowType %q requires at least one sequential relationship edge", workflowType)
		}
	case "delegation":
		if !hasRelationshipType(relationships, "delegation") {
			return fmt.Errorf("workflowType %q requires at least one delegation relationship edge", workflowType)
		}
	}

	if len(relationships) == 0 && stimulus == nil {
		return nil
	}

	// Build the set of valid persona names.
	names := make(map[string]bool, len(personas))
	for _, p := range personas {
		names[p.Name] = true
	}

	// Validate stimulus relationships.
	stimulusRelCount := 0
	for _, rel := range relationships {
		if rel.Type == "stimulus" {
			stimulusRelCount++
		}
	}
	if stimulusRelCount > 1 {
		return fmt.Errorf("at most one stimulus relationship is allowed per ensemble, found %d", stimulusRelCount)
	}
	if stimulusRelCount == 1 && stimulus == nil {
		return fmt.Errorf("stimulus relationship defined but no stimulus spec configured")
	}
	if stimulus != nil && stimulusRelCount == 0 {
		return fmt.Errorf("stimulus spec configured but no stimulus relationship defined")
	}
	if stimulus != nil {
		if strings.TrimSpace(stimulus.Prompt) == "" {
			return fmt.Errorf("stimulus prompt must not be empty")
		}
		for _, rel := range relationships {
			if rel.Type == "stimulus" {
				if rel.Source != stimulus.Name {
					return fmt.Errorf("stimulus relationship source %q must match stimulus name %q", rel.Source, stimulus.Name)
				}
				if !names[rel.Target] {
					return fmt.Errorf("stimulus relationship references unknown persona %q (target)", rel.Target)
				}
				break
			}
		}
	}

	// Validate references and build the adjacency list for sequential edges.
	adj := make(map[string][]string)
	for _, rel := range relationships {
		if rel.Type == "stimulus" {
			continue // stimulus source is not a persona, skip persona name check
		}
		if !names[rel.Source] {
			return fmt.Errorf("relationship references unknown persona %q (source)", rel.Source)
		}
		if !names[rel.Target] {
			return fmt.Errorf("relationship references unknown persona %q (target)", rel.Target)
		}
		// Sequential and delegation edges both drive one persona to invoke
		// another; a cycle in either is a config-time error (a delegation cycle
		// is a fork-bomb topology bounded only at runtime by maxDelegationDepth).
		if rel.Type == "sequential" || rel.Type == "delegation" {
			adj[rel.Source] = append(adj[rel.Source], rel.Target)
		}
	}

	// DFS cycle detection using coloring: 0=white, 1=gray, 2=black.
	color := make(map[string]int, len(names))
	var path []string

	var dfs func(node string) error
	dfs = func(node string) error {
		color[node] = 1 // gray — currently visiting
		path = append(path, node)
		for _, next := range adj[node] {
			if color[next] == 1 {
				// Found a cycle — build the cycle path for the error message.
				cycleStart := 0
				for i, n := range path {
					if n == next {
						cycleStart = i
						break
					}
				}
				cycle := append(path[cycleStart:], next)
				return fmt.Errorf("cycle detected in relationship graph: %s", strings.Join(cycle, " -> "))
			}
			if color[next] == 0 {
				if err := dfs(next); err != nil {
					return err
				}
			}
		}
		path = path[:len(path)-1]
		color[node] = 2 // black — done
		return nil
	}

	for name := range names {
		if color[name] == 0 {
			if err := dfs(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// hasRelationshipType reports whether any relationship edge has the given type.
func hasRelationshipType(relationships []sympoziumv1alpha1.AgentConfigRelationship, relType string) bool {
	for _, rel := range relationships {
		if rel.Type == relType {
			return true
		}
	}
	return false
}

// SetupWithManager registers the controller.
func (r *EnsembleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.Ensemble{}).
		Owns(&sympoziumv1alpha1.Agent{}).
		Owns(&sympoziumv1alpha1.SympoziumSchedule{}).
		Complete(r)
}
