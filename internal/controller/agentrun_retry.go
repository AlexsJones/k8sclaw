package controller

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// Gate-driven attempt retry: a response gate returning {"action":"retry"} gets a
// successor AgentRun carrying its rejection back to the agent, rather than
// ending the run. Distinct from the Job's BackoffLimit (hard-disabled at 0),
// which replays an identical pod with no feedback.
//
// The retry decision is not agent-controlled: the gate hook is an
// operator-declared image in lifecycle.postRun, and the agent cannot patch the
// gate-verdict annotation for its own run. That, maxAttempts (capped at
// admission by SympoziumPolicy) and a cumulative token budget bound the loop.

const (
	// retryOfLabel and retryAttemptLabel mirror sympozium.ai/sequential-from, so
	// a chain is queryable with -l sympozium.ai/retry-of=<name>.
	retryOfLabel      = "sympozium.ai/retry-of"
	retryAttemptLabel = "sympozium.ai/attempt"

	// retryNotBeforeAnnotation holds an RFC3339 instant reconcilePending waits
	// for. It expresses lifecycle.retry.backoff without adding a phase.
	retryNotBeforeAnnotation = "sympozium.ai/retry-not-before"

	// retryChainWalkLimit bounds the retryOf walk so a cyclic chain costs a
	// fixed number of Gets.
	retryChainWalkLimit = 32

	// maxRunNameLen is the DNS subdomain limit on object names.
	maxRunNameLen = 253

	// defaultRetryGateOutputMaxChars bounds the gate output fed back to the
	// agent. Override with SYMPOZIUM_RETRY_GATE_OUTPUT_MAX_CHARS: a test-suite
	// dump needs more room than a lint summary.
	defaultRetryGateOutputMaxChars = 4000
	retryGateOutputMaxCharsEnv     = "SYMPOZIUM_RETRY_GATE_OUTPUT_MAX_CHARS"
)

// retrySuffixPattern matches the "-retry-<n>" suffix createRetryRun appends.
var retrySuffixPattern = regexp.MustCompile(`-retry-\d+$`)

// retryGateOutputLimit returns the gate-output bound, from
// SYMPOZIUM_RETRY_GATE_OUTPUT_MAX_CHARS when it parses to a positive int.
func retryGateOutputLimit() int {
	if v := os.Getenv(retryGateOutputMaxCharsEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultRetryGateOutputMaxChars
}

// gateRetrySpec returns the retry config in force, or nil when gate-driven
// retry is off. Empty `on` means ["gate"]; "failure" is schema-only.
func gateRetrySpec(agentRun *sympoziumv1alpha1.AgentRun) *sympoziumv1alpha1.RetrySpec {
	if agentRun.Spec.Lifecycle == nil || agentRun.Spec.Lifecycle.Retry == nil {
		return nil
	}
	spec := agentRun.Spec.Lifecycle.Retry
	if len(spec.On) == 0 {
		return spec
	}
	for _, on := range spec.On {
		if on == "gate" {
			return spec
		}
	}
	return nil
}

// currentAttempt reports the run's 1-based position in its chain.
//
// status.attempt is the record, but it is written after the successor's Create
// and can be lost; the sympozium.ai/attempt label is set atomically as part of
// that Create. Reading through to the label keeps maxAttempts bounding the
// chain even when the status write never landed — without it currentAttempt
// reads 1 forever, the bound never trips, and retryChainName recomputes the
// name the run already has. A first attempt has neither, and reads as 1.
func currentAttempt(agentRun *sympoziumv1alpha1.AgentRun) int {
	if agentRun.Status.Attempt >= 1 {
		return agentRun.Status.Attempt
	}
	if n, err := strconv.Atoi(agentRun.Labels[retryAttemptLabel]); err == nil && n >= 1 {
		return n
	}
	return 1
}

// retryPredecessor names the attempt this run supersedes, falling back to the
// lineage label for the same reason currentAttempt does.
func retryPredecessor(agentRun *sympoziumv1alpha1.AgentRun) string {
	if agentRun.Status.RetryOf != "" {
		return agentRun.Status.RetryOf
	}
	return agentRun.Labels[retryOfLabel]
}

// retryChainName builds the successor's name, stripping the predecessor's own
// retry suffix so attempt 3 is foo-retry-3, not foo-retry-2-retry-3. Being
// deterministic is what makes the IsAlreadyExists check in createRetryRun an
// idempotency guard.
func retryChainName(predecessorName string, attempt int) string {
	base := retrySuffixPattern.ReplaceAllString(predecessorName, "")
	suffix := fmt.Sprintf("-retry-%d", attempt)
	if len(base)+len(suffix) > maxRunNameLen {
		base = base[:maxRunNameLen-len(suffix)]
		base = strings.TrimRight(base, "-")
	}
	return base + suffix
}

// retryChainTokens sums status.tokenUsage.totalTokens across the chain, walking
// retryOf backwards. Attempts pruned by run-history limits contribute nothing.
func (r *AgentRunReconciler) retryChainTokens(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) int64 {
	var total int64
	if agentRun.Status.TokenUsage != nil {
		total += int64(agentRun.Status.TokenUsage.TotalTokens)
	}

	seen := map[string]bool{agentRun.Name: true}
	name := retryPredecessor(agentRun)
	for i := 0; i < retryChainWalkLimit && name != "" && !seen[name]; i++ {
		seen[name] = true
		var prev sympoziumv1alpha1.AgentRun
		if err := r.Get(ctx, client.ObjectKey{Namespace: agentRun.Namespace, Name: name}, &prev); err != nil {
			break
		}
		if prev.Status.TokenUsage != nil {
			total += int64(prev.Status.TokenUsage.TotalTokens)
		}
		name = retryPredecessor(&prev)
	}
	return total
}

// tryCreateRetryRun creates the successor attempt for a "retry" verdict.
//
// An empty name means the current run must resolve terminally; exhausted
// separates "out of attempts or budget" from "retry not configured, or the
// successor could not be created". Callers fall through to reject either way.
func (r *AgentRunReconciler) tryCreateRetryRun(
	ctx context.Context, log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun, verdict *gateVerdict,
) (successorName string, exhausted bool) {
	spec := gateRetrySpec(agentRun)
	if spec == nil {
		log.Info("Gate verdict: retry, but lifecycle.retry is not configured", "reason", verdict.Reason)
		return "", false
	}

	attempt := currentAttempt(agentRun)
	if attempt >= spec.MaxAttempts {
		log.Info("Gate verdict: retry, but attempts are exhausted",
			"attempt", attempt, "maxAttempts", spec.MaxAttempts)
		return "", true
	}

	if spec.MaxChainTokens > 0 {
		used := r.retryChainTokens(ctx, agentRun)
		if used >= spec.MaxChainTokens {
			log.Info("Gate verdict: retry, but the chain token budget is spent",
				"chainTokens", used, "maxChainTokens", spec.MaxChainTokens)
			return "", true
		}
	}

	successor, err := r.createRetryRun(ctx, log, agentRun, verdict, spec, attempt+1)
	if err != nil {
		// Reject is the safe failure: the user gets the gate's rejection rather
		// than an approval it never gave.
		log.Error(err, "Failed to create retry successor; treating verdict as reject")
		return "", false
	}
	return successor, false
}

// createRetryRun clones the run's spec onto a successor carrying the retry card.
//
// It shares no builder with triggerSequentialSuccessors: that assembles a spec
// from the *target* Agent because the successor is a different persona. A retry
// is the same run again, so the predecessor's spec is the source.
func (r *AgentRunReconciler) createRetryRun(
	ctx context.Context, log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun, verdict *gateVerdict,
	spec *sympoziumv1alpha1.RetrySpec, attempt int,
) (string, error) {
	runName := retryChainName(agentRun.Name, attempt)

	annotations := map[string]string{}
	// Keep the chain in one trace, as sequential successors do.
	if tp := agentRun.Annotations["otel.dev/traceparent"]; tp != "" {
		annotations["otel.dev/traceparent"] = tp
	}
	// Channel provenance: ChannelRouter.handleCompleted reads the reply address
	// off the run that produced the result, which is now the successor. Without
	// these a Slack-triggered run that retries goes silent.
	for _, key := range []string{
		"sympozium.ai/reply-channel",
		"sympozium.ai/reply-chat-id",
		"sympozium.ai/reply-thread-id",
		"sympozium.ai/reply-message-ts",
		"sympozium.ai/sender-name",
		"sympozium.ai/sender-id",
		"sympozium.ai/agent-display-name",
	} {
		if v := agentRun.Annotations[key]; v != "" {
			annotations[key] = v
		}
	}
	// sympozium.ai/gate-verdict is not copied: the successor would resolve
	// instantly against its predecessor's verdict, looping within one reconcile.

	if spec.Backoff != nil && spec.Backoff.Duration > 0 {
		annotations[retryNotBeforeAnnotation] = time.Now().Add(spec.Backoff.Duration).UTC().Format(time.RFC3339)
	}

	labels := map[string]string{
		retryOfLabel:      agentRun.Name,
		retryAttemptLabel: strconv.Itoa(attempt),
	}
	for _, key := range []string{
		"sympozium.ai/instance",
		"sympozium.ai/ensemble",
		"sympozium.ai/dry-run",
		"sympozium.ai/source",
		"sympozium.ai/source-channel",
	} {
		if v := agentRun.Labels[key]; v != "" {
			labels[key] = v
		}
	}
	// sympozium.ai/sequential-triggered marks the predecessor, not the run, so
	// it is not carried either.

	successor := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        runName,
			Namespace:   agentRun.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: *agentRun.Spec.DeepCopy(),
	}
	successor.Spec.Task = sympoziumv1alpha1.NewStringTask(
		buildRetryTask(agentRun.Spec.Task.GetPrompt(), agentRun.Status.Result, verdict, attempt, spec.MaxAttempts))

	if err := r.Create(ctx, successor); err != nil {
		if !errors.IsAlreadyExists(err) {
			return "", fmt.Errorf("creating retry successor %s: %w", runName, err)
		}
		// Deterministic naming: a duplicate reconcile lands here instead of
		// creating a second attempt.
		log.Info("Retry successor already exists", "run", runName)
		return runName, nil
	}

	// Status is a subresource, so lineage is written after Create — through the
	// uncached reader, since the informer cache has not seen the object yet.
	if err := r.updateFreshStatusWithRetry(ctx, successor, func(ar *sympoziumv1alpha1.AgentRun) {
		ar.Status.Attempt = attempt
		ar.Status.RetryOf = agentRun.Name
	}); err != nil {
		// Not fatal: the lineage labels carry the same facts and
		// currentAttempt/retryPredecessor read through to them, so the chain
		// stays bounded. Log it — a missing status.attempt is otherwise
		// invisible until someone reads the print columns.
		log.Error(err, "Failed to write retry lineage to successor status",
			"run", runName, "attempt", attempt)
	}

	log.Info("Created retry successor run", "run", runName, "attempt", attempt, "maxAttempts", spec.MaxAttempts)
	return runName, nil
}

// retireForRetry ends a superseded attempt.
//
// Not failRun: that publishes TopicAgentRunFailed, which the channel router
// turns into a failure message. The chain has not failed, so nothing is
// published and status.error stays empty. The reason="retried" metric attribute
// keeps these separable from real failures.
func (r *AgentRunReconciler) retireForRetry(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, successorName string) error {
	now := metav1.Now()
	err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseFailed
		ar.Status.CompletedAt = &now
		ar.Status.GateVerdict = "retried"
		ar.Status.Conditions = append(ar.Status.Conditions, metav1.Condition{
			Type:               "Retried",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "GateRequestedRetry",
			Message:            fmt.Sprintf("Response gate rejected this attempt; superseded by %s", successorName),
		})
	})
	if err != nil {
		return err
	}

	agentRunsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("sympozium.agent.status", "failed"),
		attribute.String("sympozium.instance", agentRun.Spec.AgentRef),
		attribute.String("reason", "retried"),
	))

	slog.InfoContext(ctx, "agent.run.retried",
		"agent_run", agentRun.Name,
		"instance", agentRun.Spec.AgentRef,
		"successor", successorName,
		"attempt", currentAttempt(agentRun),
	)
	return nil
}

// buildRetryTask produces the card the successor receives. It reuses the
// handoff card's shape and bounds so both kinds of injected context truncate
// the same way.
func buildRetryTask(predecessorTask, predecessorResult string, verdict *gateVerdict, attempt, maxAttempts int) string {
	originalTask := extractOriginalTask(predecessorTask)
	if len(originalTask) > handoffTaskMaxChars {
		originalTask = originalTask[:handoffTaskMaxChars] + "..."
	}
	if predecessorResult == "" {
		predecessorResult = "(the previous attempt produced no output)"
	} else if len(predecessorResult) > handoffResultMaxChars {
		predecessorResult = predecessorResult[:handoffResultMaxChars] + fmt.Sprintf(
			"\n\n[truncated: your previous attempt produced %d characters and this card carries the first %d. "+
				"Do not treat the text above as the complete attempt.]",
			len(predecessorResult), handoffResultMaxChars)
	}

	reason := verdict.Reason
	if reason == "" {
		reason = "The response gate rejected the previous attempt without giving a reason."
	}
	gateOutput := verdict.Response
	if limit := retryGateOutputLimit(); len(gateOutput) > limit {
		gateOutput = gateOutput[:limit] + fmt.Sprintf(
			"\n\n[truncated: the gate produced %d characters and this card carries the first %d.]",
			len(verdict.Response), limit)
	}

	card := fmt.Sprintf(
		"## Retry %d of %d\n\n### Original Task\n%s\n\n### Your Previous Attempt\n%s\n\n### Why It Was Rejected\n%s",
		attempt, maxAttempts, originalTask, predecessorResult, reason)
	if gateOutput != "" {
		card += "\n\n### Gate Output\n" + gateOutput
	}
	return card
}
