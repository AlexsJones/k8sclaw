package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
)

const personaPackFinalizer = "sympozium.ai/personapack-finalizer"

// PersonaPackReconciler reconciles PersonaPack objects.
// It stamps out SympoziumInstances, SympoziumSchedules, and memory
// ConfigMaps for each persona defined in the pack.
type PersonaPackReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=personapacks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=personapacks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=personapacks/finalizers,verbs=update

// Reconcile handles PersonaPack create/update/delete events.
func (r *PersonaPackReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("personapack", req.NamespacedName)

	pack := &sympoziumv1alpha1.PersonaPack{}
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
	if !controllerutil.ContainsFinalizer(pack, personaPackFinalizer) {
		controllerutil.AddFinalizer(pack, personaPackFinalizer)
		if err := r.Update(ctx, pack); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconcile each persona → instance + schedule + memory
	var installed []sympoziumv1alpha1.InstalledPersona
	var installErr error
	for _, persona := range pack.Spec.Personas {
		ip, err := r.reconcilePersona(ctx, log, pack, &persona)
		if err != nil {
			log.Error(err, "Failed to reconcile persona", "persona", persona.Name)
			installErr = err
			continue
		}
		installed = append(installed, ip)
	}

	// Update status
	pack.Status.PersonaCount = len(pack.Spec.Personas)
	pack.Status.InstalledCount = len(installed)
	pack.Status.InstalledPersonas = installed
	if installErr != nil {
		pack.Status.Phase = "Error"
	} else {
		pack.Status.Phase = "Ready"
	}
	if err := r.Status().Update(ctx, pack); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, installErr
}

// reconcilePersona ensures the SympoziumInstance and optional
// SympoziumSchedule exist for one persona.
func (r *PersonaPackReconciler) reconcilePersona(
	ctx context.Context,
	log logr.Logger,
	pack *sympoziumv1alpha1.PersonaPack,
	persona *sympoziumv1alpha1.PersonaSpec,
) (sympoziumv1alpha1.InstalledPersona, error) {
	instanceName := pack.Name + "-" + persona.Name
	ip := sympoziumv1alpha1.InstalledPersona{
		Name:         persona.Name,
		InstanceName: instanceName,
	}

	// --- SympoziumInstance ---
	existingInst := &sympoziumv1alpha1.SympoziumInstance{}
	err := r.Get(ctx, client.ObjectKey{Name: instanceName, Namespace: pack.Namespace}, existingInst)
	if errors.IsNotFound(err) {
		inst := r.buildInstance(pack, persona, instanceName)
		if err := ctrl.SetControllerReference(pack, inst, r.Scheme); err != nil {
			return ip, fmt.Errorf("set owner ref on instance: %w", err)
		}
		log.Info("Creating SympoziumInstance for persona", "instance", instanceName, "persona", persona.Name)
		if err := r.Create(ctx, inst); err != nil {
			return ip, fmt.Errorf("create instance %s: %w", instanceName, err)
		}
	} else if err != nil {
		return ip, fmt.Errorf("get instance %s: %w", instanceName, err)
	}
	// Don't update existing instances — users own them after creation.

	// --- Memory seeds ---
	if persona.Memory != nil && len(persona.Memory.Seeds) > 0 {
		if err := r.reconcileMemorySeeds(ctx, log, pack, persona, instanceName); err != nil {
			log.Error(err, "Failed to seed memory", "instance", instanceName)
			// Non-fatal: continue
		}
	}

	// --- SympoziumSchedule ---
	if persona.Schedule != nil {
		schedName := instanceName + "-schedule"
		ip.ScheduleName = schedName

		existingSched := &sympoziumv1alpha1.SympoziumSchedule{}
		err := r.Get(ctx, client.ObjectKey{Name: schedName, Namespace: pack.Namespace}, existingSched)
		if errors.IsNotFound(err) {
			sched := r.buildSchedule(pack, persona, instanceName, schedName)
			if err := ctrl.SetControllerReference(pack, sched, r.Scheme); err != nil {
				return ip, fmt.Errorf("set owner ref on schedule: %w", err)
			}
			log.Info("Creating SympoziumSchedule for persona", "schedule", schedName, "persona", persona.Name)
			if err := r.Create(ctx, sched); err != nil {
				return ip, fmt.Errorf("create schedule %s: %w", schedName, err)
			}
		} else if err != nil {
			return ip, fmt.Errorf("get schedule %s: %w", schedName, err)
		}
	}

	return ip, nil
}

// buildInstance creates a SympoziumInstance spec from a persona definition.
func (r *PersonaPackReconciler) buildInstance(
	pack *sympoziumv1alpha1.PersonaPack,
	persona *sympoziumv1alpha1.PersonaSpec,
	instanceName string,
) *sympoziumv1alpha1.SympoziumInstance {
	model := persona.Model
	if model == "" {
		model = "gpt-4o" // sensible default; overridden by onboarding
	}

	inst := &sympoziumv1alpha1.SympoziumInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName,
			Namespace: pack.Namespace,
			Labels: map[string]string{
				"sympozium.ai/persona-pack": pack.Name,
				"sympozium.ai/persona":      persona.Name,
			},
		},
		Spec: sympoziumv1alpha1.SympoziumInstanceSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: model,
				},
			},
			// Copy auth refs from the pack (set during install via TUI).
			AuthRefs: pack.Spec.AuthRefs,
			Memory: &sympoziumv1alpha1.MemorySpec{
				Enabled:      true,
				MaxSizeKB:    256,
				SystemPrompt: persona.SystemPrompt,
			},
		},
	}

	// Skills
	for _, s := range persona.Skills {
		inst.Spec.Skills = append(inst.Spec.Skills, sympoziumv1alpha1.SkillRef{
			SkillPackRef: s,
		})
	}

	// Channels
	for _, ch := range persona.Channels {
		inst.Spec.Channels = append(inst.Spec.Channels, sympoziumv1alpha1.ChannelSpec{
			Type: ch,
		})
	}

	// Policy — use the pack's policy ref if set.
	inst.Spec.PolicyRef = pack.Spec.PolicyRef

	return inst
}

// buildSchedule creates a SympoziumSchedule from a persona's schedule config.
func (r *PersonaPackReconciler) buildSchedule(
	pack *sympoziumv1alpha1.PersonaPack,
	persona *sympoziumv1alpha1.PersonaSpec,
	instanceName, schedName string,
) *sympoziumv1alpha1.SympoziumSchedule {
	cron := persona.Schedule.Cron
	if cron == "" {
		cron = intervalToCron(persona.Schedule.Interval)
	}

	return &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      schedName,
			Namespace: pack.Namespace,
			Labels: map[string]string{
				"sympozium.ai/persona-pack": pack.Name,
				"sympozium.ai/persona":      persona.Name,
			},
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			InstanceRef:       instanceName,
			Schedule:          cron,
			Task:              persona.Schedule.Task,
			Type:              persona.Schedule.Type,
			ConcurrencyPolicy: "Forbid",
			IncludeMemory:     true,
		},
	}
}

// reconcileMemorySeeds creates or patches the memory ConfigMap with seed data.
func (r *PersonaPackReconciler) reconcileMemorySeeds(
	ctx context.Context,
	log logr.Logger,
	pack *sympoziumv1alpha1.PersonaPack,
	persona *sympoziumv1alpha1.PersonaSpec,
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
					"sympozium.ai/persona-pack": pack.Name,
					"sympozium.ai/persona":      persona.Name,
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
func intervalToCron(interval string) string {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "1m", "1min":
		return "* * * * *"
	case "5m", "5min":
		return "*/5 * * * *"
	case "10m", "10min":
		return "*/10 * * * *"
	case "15m", "15min":
		return "*/15 * * * *"
	case "30m", "30min":
		return "*/30 * * * *"
	case "1h", "60m":
		return "0 * * * *"
	case "2h":
		return "0 */2 * * *"
	case "4h":
		return "0 */4 * * *"
	case "6h":
		return "0 */6 * * *"
	case "12h":
		return "0 */12 * * *"
	case "24h", "1d":
		return "0 0 * * *"
	default:
		// If it already looks like a cron expression, return as-is
		if strings.Contains(interval, " ") {
			return interval
		}
		return "0 * * * *" // default: hourly
	}
}

// reconcileDelete cleans up resources owned by the PersonaPack.
func (r *PersonaPackReconciler) reconcileDelete(
	ctx context.Context,
	log logr.Logger,
	pack *sympoziumv1alpha1.PersonaPack,
) (ctrl.Result, error) {
	log.Info("Reconciling PersonaPack deletion")

	// Owner references handle cascade deletion of instances and schedules,
	// but we clean up memory ConfigMaps explicitly since they may not
	// have owner references.
	for _, persona := range pack.Spec.Personas {
		cmName := pack.Name + "-" + persona.Name + "-memory"
		var cm corev1.ConfigMap
		if err := r.Get(ctx, client.ObjectKey{Name: cmName, Namespace: pack.Namespace}, &cm); err == nil {
			log.Info("Deleting memory ConfigMap", "configmap", cmName)
			if err := r.Delete(ctx, &cm); err != nil && !errors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
	}

	controllerutil.RemoveFinalizer(pack, personaPackFinalizer)
	if err := r.Update(ctx, pack); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller.
func (r *PersonaPackReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.PersonaPack{}).
		Owns(&sympoziumv1alpha1.SympoziumInstance{}).
		Owns(&sympoziumv1alpha1.SympoziumSchedule{}).
		Complete(r)
}
