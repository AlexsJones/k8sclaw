//go:build system

package system_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestRunCreateDelete(t *testing.T) {
	ns := createTestNamespace(t)
	agentName := "sys-run-agent"

	// Create agent via API (lm-studio provider with baseURL = no auth secret needed).
	agentBody := map[string]any{
		"name":     agentName,
		"provider": "lm-studio",
		"model":    "test-model",
		"baseURL":  "http://fake-lmstudio:1234/v1",
	}
	rec := httpDo(t, http.MethodPost, fmt.Sprintf("/api/v1/agents?namespace=%s", ns), agentBody)
	requireStatus(t, rec, http.StatusCreated)

	t.Cleanup(func() {
		httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/agents/%s?namespace=%s", agentName, ns), nil)
	})

	// Dispatch a run and extract name from response.
	type runResp struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	rr, code := httpJSON[runResp](t, http.MethodPost, fmt.Sprintf("/api/v1/runs?namespace=%s", ns), map[string]any{
		"agentRef": agentName,
		"task":     "Say hello",
	})
	if code != http.StatusCreated {
		t.Fatalf("create run status = %d", code)
	}
	runName := rr.Metadata.Name
	if runName == "" {
		t.Fatal("run name is empty in response")
	}

	// Wait for the AgentRun controller to create a Job.
	pollUntil(t, 15*time.Second, 200*time.Millisecond, func() bool {
		var jobs batchv1.JobList
		if err := k8sClient.List(testCtx, &jobs, client.InNamespace(ns)); err != nil {
			return false
		}
		for _, j := range jobs.Items {
			if j.Labels["sympozium.ai/agent-run"] == runName {
				return true
			}
		}
		return false
	})

	// Delete the run via API.
	rec = httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/runs/%s?namespace=%s", runName, ns), nil)
	if rec.Code != 200 && rec.Code != 204 {
		t.Fatalf("delete run status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Verify AgentRun is gone.
	pollUntil(t, 15*time.Second, 200*time.Millisecond, func() bool {
		var run sympoziumv1alpha1.AgentRun
		err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: runName}, &run)
		return err != nil
	})
}

func TestRunRequiresAgent(t *testing.T) {
	ns := createTestNamespace(t)

	body := map[string]any{
		"agentRef": "nonexistent-agent",
		"task":     "Should fail",
	}
	rec := httpDo(t, http.MethodPost, fmt.Sprintf("/api/v1/runs?namespace=%s", ns), body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

func TestRunJobShape(t *testing.T) {
	ns := createTestNamespace(t)
	agentName := "sys-run-shape"

	agentBody := map[string]any{
		"name":     agentName,
		"provider": "lm-studio",
		"model":    "qwen/qwen3.5-9b",
		"baseURL":  "http://fake-lmstudio:1234/v1",
	}
	rec := httpDo(t, http.MethodPost, fmt.Sprintf("/api/v1/agents?namespace=%s", ns), agentBody)
	requireStatus(t, rec, http.StatusCreated)
	t.Cleanup(func() {
		httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/agents/%s?namespace=%s", agentName, ns), nil)
	})

	// Create a run.
	runBody := map[string]any{
		"agentRef": agentName,
		"task":     "Verify job shape",
	}
	type runResp struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	rr, code := httpJSON[runResp](t, http.MethodPost, fmt.Sprintf("/api/v1/runs?namespace=%s", ns), runBody)
	if code != http.StatusCreated {
		t.Fatalf("create run status = %d", code)
	}
	runName := rr.Metadata.Name

	t.Cleanup(func() {
		httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/runs/%s?namespace=%s", runName, ns), nil)
	})

	// Wait for Job and verify its shape.
	var foundJob batchv1.Job
	pollUntil(t, 15*time.Second, 200*time.Millisecond, func() bool {
		var jobs batchv1.JobList
		if err := k8sClient.List(testCtx, &jobs, client.InNamespace(ns)); err != nil {
			return false
		}
		for _, j := range jobs.Items {
			if j.Labels["sympozium.ai/agent-run"] == runName {
				foundJob = j
				return true
			}
		}
		return false
	})

	// Verify the Job has at least one container.
	containers := foundJob.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		t.Fatal("job has no containers")
	}

	// Verify the Job labels reference the correct agent.
	if foundJob.Labels["sympozium.ai/instance"] != agentName {
		t.Errorf("job instance label = %q, want %q", foundJob.Labels["sympozium.ai/instance"], agentName)
	}

	// Verify the input ConfigMap was created with the task text.
	var inputCM corev1.ConfigMap
	assertExists(t, &inputCM, ns, fmt.Sprintf("%s-input", runName))
	if inputCM.Data["task"] != "Verify job shape" {
		t.Errorf("input ConfigMap task = %q, want %q", inputCM.Data["task"], "Verify job shape")
	}
}

// TestRunRetryFieldsRoundTrip proves the retry spec and its status lineage
// survive a real apiserver. Unit tests run against a fake client that never
// applies the CRD schema, so a missing or misspelled field would be silently
// pruned there and only show up in a cluster.
func TestRunRetryFieldsRoundTrip(t *testing.T) {
	ns := createTestNamespace(t)

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "sys-run-retry", Namespace: ns},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "sys-retry-agent",
			Task:     sympoziumv1alpha1.NewStringTask("Verify retry round-trip"),
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				GateDefault: "block",
				Retry: &sympoziumv1alpha1.RetrySpec{
					MaxAttempts:    3,
					Backoff:        &metav1.Duration{Duration: 30 * time.Second},
					MaxChainTokens: 200000,
					On:             []string{"gate"},
				},
				PostRun: []sympoziumv1alpha1.LifecycleHookContainer{
					{Name: "check", Image: "check:latest", Gate: true},
				},
			},
		},
	}
	if err := k8sClient.Create(testCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, run) })

	// k8sClient reads through the manager's informer cache, so a Get straight
	// after Create can miss.
	var got sympoziumv1alpha1.AgentRun
	pollUntil(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return k8sClient.Get(testCtx, client.ObjectKeyFromObject(run), &got) == nil
	})

	if got.Spec.Lifecycle == nil {
		t.Fatal("lifecycle was pruned by the apiserver")
	}
	retry := got.Spec.Lifecycle.Retry
	if retry == nil {
		t.Fatal("lifecycle.retry was pruned by the apiserver")
	}
	if retry.MaxAttempts != 3 {
		t.Errorf("maxAttempts = %d, want 3", retry.MaxAttempts)
	}
	if retry.Backoff == nil || retry.Backoff.Duration != 30*time.Second {
		t.Errorf("backoff = %v, want 30s", retry.Backoff)
	}
	if retry.MaxChainTokens != 200000 {
		t.Errorf("maxChainTokens = %d, want 200000", retry.MaxChainTokens)
	}
	if len(retry.On) != 1 || retry.On[0] != "gate" {
		t.Errorf("on = %v, want [gate]", retry.On)
	}

	// Assert on the object the apiserver returns from the status write rather
	// than on a later Get: the live controller reconciles this run and will
	// overwrite status at any moment, and what is under test here is whether
	// the schema accepts the fields, not who wrote them last. For the same
	// reason the write itself retries — a concurrent reconcile makes it
	// conflict, which is not a schema failure.
	pollUntil(t, 15*time.Second, 100*time.Millisecond, func() bool {
		if err := k8sClient.Get(testCtx, client.ObjectKeyFromObject(run), &got); err != nil {
			return false
		}
		got.Status.Attempt = 2
		got.Status.RetryOf = "sys-run-retry-predecessor"
		got.Status.GateVerdict = "retried"
		return k8sClient.Status().Update(testCtx, &got) == nil
	})
	if got.Status.Attempt != 2 {
		t.Errorf("status.attempt = %d, want 2 (pruned by the apiserver?)", got.Status.Attempt)
	}
	if got.Status.RetryOf != "sys-run-retry-predecessor" {
		t.Errorf("status.retryOf = %q, want %q (pruned by the apiserver?)", got.Status.RetryOf, "sys-run-retry-predecessor")
	}
	if got.Status.GateVerdict != "retried" {
		t.Errorf("status.gateVerdict = %q, want %q", got.Status.GateVerdict, "retried")
	}
}

// TestRetryPrintColumnsAreServed proves the apiserver actually serves the
// retry columns. They are how a chain is discovered from the command line, and
// a typo in a JSONPath marker fails silently — the column just renders empty.
func TestRetryPrintColumnsAreServed(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	key := client.ObjectKey{Name: "agentruns.sympozium.ai"}
	pollUntil(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return k8sClient.Get(testCtx, key, crd) == nil
	})

	served := map[string]string{}
	for _, v := range crd.Spec.Versions {
		if v.Name != "v1alpha1" {
			continue
		}
		for _, col := range v.AdditionalPrinterColumns {
			served[col.Name] = col.JSONPath
		}
	}

	for name, wantPath := range map[string]string{
		"Gate":     ".status.gateVerdict",
		"Attempt":  ".status.attempt",
		"Retry Of": ".status.retryOf",
	} {
		got, ok := served[name]
		if !ok {
			t.Errorf("column %q is not served; got %v", name, served)
			continue
		}
		if got != wantPath {
			t.Errorf("column %q JSONPath = %q, want %q", name, got, wantPath)
		}
	}
}

// TestPolicyRetryCeilingRoundTrips proves the operator-side ceiling survives the
// apiserver too — the webhook that enforces it is not registered in envtest, so
// only the schema is under test here.
func TestPolicyRetryCeilingRoundTrips(t *testing.T) {
	ns := createTestNamespace(t)

	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "sys-retry-policy", Namespace: ns},
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{
			LifecyclePolicy: &sympoziumv1alpha1.LifecyclePolicySpec{MaxRetryAttempts: 3},
		},
	}
	if err := k8sClient.Create(testCtx, policy); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, policy) })

	var got sympoziumv1alpha1.SympoziumPolicy
	pollUntil(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return k8sClient.Get(testCtx, client.ObjectKeyFromObject(policy), &got) == nil
	})
	if got.Spec.LifecyclePolicy == nil || got.Spec.LifecyclePolicy.MaxRetryAttempts != 3 {
		t.Errorf("lifecyclePolicy = %+v, want maxRetryAttempts 3", got.Spec.LifecyclePolicy)
	}
}
