package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/robfig/cron/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/sessionkey"
	"github.com/sympozium-ai/sympozium/internal/toolpolicy"
)

// maxScheduleRunCreateRetries bounds the collision-retry loop when the
// scheduler's chosen run suffix clashes with an existing AgentRun (e.g. from
// a prior incarnation of this schedule before it was deleted and recreated).
const maxScheduleRunCreateRetries = 100

const sympoziumScheduleFinalizer = "sympozium.ai/schedule-finalizer"

// SympoziumScheduleReconciler reconciles SympoziumSchedule objects.
type SympoziumScheduleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumschedules/finalizers,verbs=update

// Reconcile handles SympoziumSchedule create/update/delete events.
func (r *SympoziumScheduleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("sympoziumschedule", req.NamespacedName)

	schedule := &sympoziumv1alpha1.SympoziumSchedule{}
	if err := r.Get(ctx, req.NamespacedName, schedule); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion.
	if !schedule.DeletionTimestamp.IsZero() {
		controllerutil.RemoveFinalizer(schedule, sympoziumScheduleFinalizer)
		return ctrl.Result{}, r.Update(ctx, schedule)
	}

	// Add finalizer.
	if !controllerutil.ContainsFinalizer(schedule, sympoziumScheduleFinalizer) {
		controllerutil.AddFinalizer(schedule, sympoziumScheduleFinalizer)
		if err := r.Update(ctx, schedule); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Handle suspended schedules.
	if schedule.Spec.Suspend {
		if schedule.Status.Phase != "Suspended" {
			schedule.Status.Phase = "Suspended"
			_ = r.Status().Update(ctx, schedule)
		}
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	// Parse the cron schedule.
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(schedule.Spec.Schedule)
	if err != nil {
		log.Error(err, "invalid cron expression", "schedule", schedule.Spec.Schedule)
		schedule.Status.Phase = "Error"
		_ = r.Status().Update(ctx, schedule)
		return ctrl.Result{}, nil
	}

	now := time.Now()

	// Compute next run time from last run.  When a schedule has never fired
	// (LastRunTime is nil) we backdate by one full cron interval so that
	// sched.Next() finds exactly one past tick — triggering one immediate
	// run without the duplicates caused by a 24h backdate.
	var lastRun time.Time
	switch {
	case schedule.Status.LastRunTime != nil:
		lastRun = schedule.Status.LastRunTime.Time
	case schedule.Spec.WaitsForFirstInterval():
		// Defer the first run by a full interval. Anchor to the creation
		// timestamp rather than "now": now moves forward on every reconcile, so
		// the next tick would recede ahead of it and the schedule would never
		// fire at all.
		lastRun = schedule.CreationTimestamp.Time
		if lastRun.IsZero() {
			lastRun = now
		}
	default:
		// First run — compute the interval between two consecutive ticks
		// and backdate by that amount so the first tick lands before now.
		firstTick := sched.Next(now)
		if firstTick.IsZero() {
			log.Info("cron expression has no reachable next time; parking schedule", "schedule", schedule.Spec.Schedule)
			schedule.Status.Phase = "Active"
			schedule.Status.NextRunTime = nil
			_ = r.Status().Update(ctx, schedule)
			return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
		}
		secondTick := sched.Next(firstTick)
		interval := secondTick.Sub(firstTick)
		lastRun = now.Add(-interval)
	}
	nextRun := sched.Next(lastRun)

	// Guard: cron expressions with unreachable dates (e.g. "0 0 31 2 *")
	// cause sched.Next() to return a zero time after exhausting its search
	// window. Without this check the controller treats the zero time as
	// "already past due" and fires on every reconcile.
	if nextRun.IsZero() {
		log.Info("cron expression has no reachable next time; parking schedule", "schedule", schedule.Spec.Schedule)
		schedule.Status.Phase = "Active"
		schedule.Status.NextRunTime = nil
		_ = r.Status().Update(ctx, schedule)
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	// Update status with next run time.
	nextRunMeta := metav1.NewTime(nextRun)
	schedule.Status.NextRunTime = &nextRunMeta
	schedule.Status.Phase = "Active"

	// Check if it's time to fire.
	if now.Before(nextRun) {
		delay := nextRun.Sub(now)
		if delay > 60*time.Second {
			delay = 60 * time.Second
		}
		_ = r.Status().Update(ctx, schedule)
		return ctrl.Result{RequeueAfter: delay}, nil
	}

	// Pipeline ordering: don't start a new pipeline pass while the previous one
	// is still in flight. ConcurrencyPolicy below only guards this schedule's own
	// previous run; a pipeline head can finish and hand off to a sequential
	// successor that is still running, so we must also block re-triggering while
	// any run in the ensemble is active.
	if ensembleName := schedule.Labels["sympozium.ai/ensemble"]; ensembleName != "" {
		ensemble := &sympoziumv1alpha1.Ensemble{}
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: schedule.Namespace,
			Name:      ensembleName,
		}, ensemble); err == nil && ensemble.Spec.WorkflowType == "pipeline" {
			if activeRun, inFlight, err := r.pipelineInFlight(ctx, schedule.Namespace, ensembleName); err != nil {
				log.Error(err, "failed to check pipeline in-flight state")
			} else if inFlight {
				log.Info("Skipping trigger — pipeline still in flight",
					"ensemble", ensembleName, "activeRun", activeRun)
				_ = r.Status().Update(ctx, schedule)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
		}
	}

	// Check concurrency policy by listing live runs (not cached status) to
	// avoid the TOCTOU race where two reconcile loops both read the same
	// stale LastRunName, both pass the check, and both create a new run.
	if schedule.Spec.ConcurrencyPolicy == "Forbid" {
		var scheduleRuns sympoziumv1alpha1.AgentRunList
		if err := r.List(ctx, &scheduleRuns,
			client.InNamespace(schedule.Namespace),
			client.MatchingLabels{"sympozium.ai/schedule": schedule.Name},
		); err == nil {
			for i := range scheduleRuns.Items {
				phase := scheduleRuns.Items[i].Status.Phase
				if isAgentRunActive(phase) {
					log.Info("Skipping trigger — active run exists (Forbid policy)",
						"activeRun", scheduleRuns.Items[i].Name, "phase", phase)
					_ = r.Status().Update(ctx, schedule)
					return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
				}
			}
		}
	}

	// Skip if there's already a serving AgentRun for this instance.
	// A serving run means a web-proxy Deployment is active — no need to
	// spawn additional heartbeat runs.
	//
	// Also skip while a stimulus run for this instance is still active. Both
	// triggers land on the target agent the moment an ensemble is enabled —
	// the stimulus on the readiness edge, the schedule via its backdated first
	// tick — and the ConcurrencyPolicy check above cannot see it because it
	// only considers this schedule's own runs. Without this the agent gets the
	// same work twice and does it twice, in parallel.
	var allRuns sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &allRuns,
		client.InNamespace(schedule.Namespace),
		client.MatchingLabels{"sympozium.ai/instance": schedule.Spec.AgentRef},
	); err == nil {
		for _, run := range allRuns.Items {
			if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseServing {
				log.Info("Skipping trigger — instance has a serving AgentRun",
					"servingRun", run.Name)
				_ = r.Status().Update(ctx, schedule)
				return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
			}
			if run.Labels["sympozium.ai/stimulus"] == "true" && isAgentRunActive(run.Status.Phase) {
				log.Info("Skipping trigger — instance has an active stimulus run",
					"stimulusRun", run.Name, "phase", run.Status.Phase)
				_ = r.Status().Update(ctx, schedule)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
		}
	}

	// Build the task, optionally including memory context.
	task := schedule.Spec.Task
	if schedule.Spec.IncludeMemory {
		memoryContent := r.readMemoryConfigMap(ctx, schedule.Namespace, schedule.Spec.AgentRef)
		if memoryContent != "" {
			task = fmt.Sprintf("## Memory Context\n%s\n\n## Task\n%s", memoryContent, task)
		}
	}

	// Look up instance to get model config.
	instance := &sympoziumv1alpha1.Agent{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: schedule.Namespace,
		Name:      schedule.Spec.AgentRef,
	}, instance); err != nil {
		log.Error(err, "instance not found", "instance", schedule.Spec.AgentRef)
		schedule.Status.Phase = "Error"
		_ = r.Status().Update(ctx, schedule)
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	// Pick the next available run number. The naive `TotalRuns+1` choice
	// collides when this schedule has been deleted and recreated (e.g.
	// Ensemble disabled then re-enabled) because the counter resets
	// but the old AgentRun resources persist. List existing runs that
	// belong to this schedule, find the highest numeric suffix, and
	// start from there.
	nextNum, err := r.nextScheduledRunNumber(ctx, schedule)
	if err != nil {
		log.Error(err, "failed to list existing scheduled runs; falling back to TotalRuns counter")
		nextNum = int(schedule.Status.TotalRuns) + 1
	}

	// Create the AgentRun.
	runName := fmt.Sprintf("%s-%d", schedule.Name, nextNum)
	agentRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: schedule.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance": schedule.Spec.AgentRef,
				"sympozium.ai/schedule": schedule.Name,
				"sympozium.ai/type":     schedule.Spec.Type,
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef:   schedule.Spec.AgentRef,
			Task:       sympoziumv1alpha1.NewStringTask(task),
			AgentID:    fmt.Sprintf("schedule-%s", schedule.Name),
			SessionKey: sessionkey.ForSchedule(schedule.Name),
			Model: sympoziumv1alpha1.ModelSpec{
				Provider: resolveProvider(instance),
				Model:    instance.Spec.Agents.Default.Model,
			},
			ImagePullSecrets: instance.Spec.ImagePullSecrets,
			Lifecycle:        instance.Spec.Agents.Default.Lifecycle,
			SystemPrompt:     memorySystemPrompt(instance),
			Volumes:          instance.Spec.Volumes,
			VolumeMounts:     instance.Spec.VolumeMounts,
			Env:              instance.Spec.Agents.Default.Env,
			Timeout:          instance.Spec.Agents.Default.ParseRunTimeout(),
			ToolPolicy:       toolpolicy.ForAgent(ctx, r.Client, instance),
		},
	}

	// Propagate the ensemble label so pipeline in-flight checks can see this run.
	if ensembleName := schedule.Labels["sympozium.ai/ensemble"]; ensembleName != "" {
		agentRun.Labels["sympozium.ai/ensemble"] = ensembleName
	}

	// Enable canary mode for system canary runs.
	if instance.Labels["sympozium.ai/ensemble"] == "system-canary" {
		agentRun.Spec.CanaryMode = true
	}

	// Copy model config from instance.
	if instance.Spec.Agents.Default.BaseURL != "" {
		agentRun.Spec.Model.BaseURL = instance.Spec.Agents.Default.BaseURL
	}
	if instance.Spec.Agents.Default.Thinking != "" {
		agentRun.Spec.Model.Thinking = instance.Spec.Agents.Default.Thinking
	}
	if len(instance.Spec.Agents.Default.NodeSelector) > 0 {
		agentRun.Spec.Model.NodeSelector = instance.Spec.Agents.Default.NodeSelector
	}
	if len(instance.Spec.Agents.Default.ProviderHeaders) > 0 {
		agentRun.Spec.Model.ProviderHeaders = instance.Spec.Agents.Default.ProviderHeaders
	}
	if instance.Spec.Agents.Default.ProviderHeadersSecretRef != "" {
		agentRun.Spec.Model.ProviderHeadersSecretRef = instance.Spec.Agents.Default.ProviderHeadersSecretRef
	}

	// Resolve auth secret from the instance.
	agentRun.Spec.Model.AuthSecretRef = resolveAuthSecret(instance)

	// Copy skill refs, excluding server-mode skills (e.g. web-endpoint) that
	// should not be spawned as ephemeral schedule runs.
	for _, skill := range instance.Spec.Skills {
		if skill.SkillPackRef == "web-endpoint" || skill.SkillPackRef == "skillpack-web-endpoint" {
			continue
		}
		agentRun.Spec.Skills = append(agentRun.Spec.Skills, skill)
	}

	// Link the AgentRun to its parent Schedule via a controller owner
	// reference. When the Schedule is deleted (e.g. Ensemble is
	// disabled and its owned Schedule is cascade-deleted), Kubernetes
	// garbage collection will remove the AgentRuns too — so disabling
	// a pack no longer leaves orphan Failed runs cluttering the UX
	// with "instance not found" errors from policy validation.
	if err := controllerutil.SetControllerReference(schedule, agentRun, r.Scheme); err != nil {
		log.Error(err, "failed to set controller reference on AgentRun")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Collision-retry loop: if another actor (or a race with our own
	// prior reconcile) created the same name, bump the suffix and try
	// again. Bounded to avoid an unbounded spin.
	for attempt := 0; attempt < maxScheduleRunCreateRetries; attempt++ {
		err := r.Create(ctx, agentRun)
		if err == nil {
			break
		}
		if !errors.IsAlreadyExists(err) {
			log.Error(err, "failed to create AgentRun")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		log.Info("scheduled run name collision; trying next suffix",
			"run", runName, "attempt", attempt+1)
		nextNum++
		runName = fmt.Sprintf("%s-%d", schedule.Name, nextNum)
		agentRun.ObjectMeta.Name = runName
		if attempt == maxScheduleRunCreateRetries-1 {
			log.Error(fmt.Errorf("exceeded collision retries"),
				"could not pick a unique scheduled run name", "lastAttempted", runName)
			return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
		}
	}

	log.Info("Created scheduled AgentRun", "run", runName, "type", schedule.Spec.Type)

	// Update status. Use the actual number that was committed (after any
	// collision retries) so TotalRuns stays in sync with observable state.
	nowMeta := metav1.Now()
	schedule.Status.LastRunTime = &nowMeta
	schedule.Status.LastRunName = runName
	if int64(nextNum) > schedule.Status.TotalRuns {
		schedule.Status.TotalRuns = int64(nextNum)
	}

	// Recompute next run from now.
	next := sched.Next(now)
	nextMeta := metav1.NewTime(next)
	schedule.Status.NextRunTime = &nextMeta

	_ = r.Status().Update(ctx, schedule)

	delay := next.Sub(now)
	if delay > 60*time.Second {
		delay = 60 * time.Second
	}
	return ctrl.Result{RequeueAfter: delay}, nil
}

// nextScheduledRunNumber returns the next numeric suffix to use when naming
// a scheduled AgentRun, picking the higher of:
//
//   - status.TotalRuns + 1 (the counter the scheduler maintains), and
//   - 1 + the maximum observed suffix on existing AgentRuns that belong to
//     this schedule (identified via the sympozium.ai/schedule label).
//
// The second branch handles the case where this schedule was previously
// deleted (e.g. Ensemble disabled) and recreated: TotalRuns resets to 0
// but orphan AgentRuns from the previous incarnation persist with names like
// `<schedule>-1`, `<schedule>-2`, … Picking a suffix that's already in use
// would collide silently and leave the scheduler emitting ghost "created"
// log lines without actually creating runs.
func (r *SympoziumScheduleReconciler) nextScheduledRunNumber(ctx context.Context, schedule *sympoziumv1alpha1.SympoziumSchedule) (int, error) {
	base := int(schedule.Status.TotalRuns) + 1
	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(schedule.Namespace),
		client.MatchingLabels{"sympozium.ai/schedule": schedule.Name},
	); err != nil {
		return base, err
	}
	prefix := schedule.Name + "-"
	maxObserved := 0
	for i := range runs.Items {
		name := runs.Items[i].Name
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, prefix)
		n, err := strconv.Atoi(suffix)
		if err != nil || n <= 0 {
			continue
		}
		if n > maxObserved {
			maxObserved = n
		}
	}
	if maxObserved+1 > base {
		return maxObserved + 1, nil
	}
	return base, nil
}

// isAgentRunActive reports whether a phase means the run has not finished.
// Beyond the obvious Pending/Running/Serving it covers:
//
//   - PostRunning: the agent is done but its postRun lifecycle hooks are not.
//   - AwaitingDelegate: the run is parked while a delegate_to_persona child
//     works. An orchestrator can sit here for tens of minutes.
//   - "": the AgentRun controller has not observed the run yet.
//
// Omitting a phase here makes a Forbid schedule stack a second run on top of
// a live one, so keep this in sync with the AgentRunPhase constants.
func isAgentRunActive(phase sympoziumv1alpha1.AgentRunPhase) bool {
	switch phase {
	case sympoziumv1alpha1.AgentRunPhasePending,
		sympoziumv1alpha1.AgentRunPhaseRunning,
		sympoziumv1alpha1.AgentRunPhaseServing,
		sympoziumv1alpha1.AgentRunPhasePostRunning,
		sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate,
		"":
		return true
	}
	return false
}

// pipelineInFlight reports whether any AgentRun belonging to the given ensemble
// is still active (see isAgentRunActive). It is used to stop a pipeline
// head's schedule from kicking off a second pass while a previous pass is still
// working its way through the sequential successors. Returns the name of the
// first active run found for diagnostics.
func (r *SympoziumScheduleReconciler) pipelineInFlight(ctx context.Context, namespace, ensembleName string) (string, bool, error) {
	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(namespace),
		client.MatchingLabels{"sympozium.ai/ensemble": ensembleName},
	); err != nil {
		return "", false, err
	}
	for i := range runs.Items {
		if isAgentRunActive(runs.Items[i].Status.Phase) {
			return runs.Items[i].Name, true, nil
		}
	}
	return "", false, nil
}

// readMemoryConfigMap reads the MEMORY.md content from the instance's memory
// ConfigMap. Returns empty string if not found.
func (r *SympoziumScheduleReconciler) readMemoryConfigMap(ctx context.Context, namespace, instanceName string) string {
	cmName := fmt.Sprintf("%s-memory", instanceName)
	var configMap corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      cmName,
	}, &configMap); err != nil {
		return ""
	}
	return configMap.Data["MEMORY.md"]
}

// SetupWithManager sets up the controller with the Manager.
func (r *SympoziumScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.SympoziumSchedule{}).
		Owns(&sympoziumv1alpha1.AgentRun{}).
		Complete(r)
}
