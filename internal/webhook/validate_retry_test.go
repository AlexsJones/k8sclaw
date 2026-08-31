package webhook

import (
	"testing"

	"github.com/go-logr/logr"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func runRequestingRetry(maxAttempts int) *sympoziumv1alpha1.AgentRun {
	return &sympoziumv1alpha1.AgentRun{
		Spec: sympoziumv1alpha1.AgentRunSpec{
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				Retry: &sympoziumv1alpha1.RetrySpec{MaxAttempts: maxAttempts},
			},
		},
	}
}

func policyWithRetryCeiling(maxAttempts int) *sympoziumv1alpha1.SympoziumPolicy {
	return &sympoziumv1alpha1.SympoziumPolicy{
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{
			LifecyclePolicy: &sympoziumv1alpha1.LifecyclePolicySpec{
				MaxRetryAttempts: maxAttempts,
			},
		},
	}
}

// A gate that always returns "retry" burns tokens silently, so the operator's
// ceiling has to bind at admission rather than be trusted to the run's spec.
func TestValidateRetryPolicy(t *testing.T) {
	pe := &PolicyEnforcer{Log: logr.Discard()}

	cases := []struct {
		name    string
		run     *sympoziumv1alpha1.AgentRun
		policy  *sympoziumv1alpha1.SympoziumPolicy
		wantErr bool
	}{
		{"within ceiling", runRequestingRetry(2), policyWithRetryCeiling(3), false},
		{"exactly at ceiling is allowed", runRequestingRetry(3), policyWithRetryCeiling(3), false},
		{"above ceiling is denied", runRequestingRetry(5), policyWithRetryCeiling(3), true},
		{"no ceiling configured allows anything", runRequestingRetry(50), policyWithRetryCeiling(0), false},
		{"no lifecycle policy allows anything", runRequestingRetry(50), &sympoziumv1alpha1.SympoziumPolicy{}, false},
		{"run without retry is unaffected", &sympoziumv1alpha1.AgentRun{}, policyWithRetryCeiling(1), false},
		{
			"run with lifecycle but no retry is unaffected",
			&sympoziumv1alpha1.AgentRun{Spec: sympoziumv1alpha1.AgentRunSpec{
				Lifecycle: &sympoziumv1alpha1.LifecycleHooks{GateDefault: "block"},
			}},
			policyWithRetryCeiling(1),
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := pe.validateRetryPolicy(tc.run, tc.policy)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}
