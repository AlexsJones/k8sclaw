package webhook

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return scheme
}

func newTestInstance(name, namespace, policyRef string) *sympoziumv1alpha1.Agent {
	return &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       sympoziumv1alpha1.AgentSpec{PolicyRef: policyRef},
	}
}

func admissionCreateRequest(t *testing.T, run *sympoziumv1alpha1.AgentRun) admission.Request {
	t.Helper()
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshal run: %v", err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

// ── Fix 10: Env var validation ───────────────────────────────────────────────

func TestPolicyEnforcer_DeniesPathEnvVar(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "test-policy")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy", Namespace: "default"},
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Env:      map[string]string{"PATH": "/malicious/bin"},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if resp.Allowed {
		t.Fatal("expected webhook to DENY run with PATH env var override")
	}
}

func TestPolicyEnforcer_DeniesLdPreloadEnvVar(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "test-policy")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy", Namespace: "default"},
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Env:      map[string]string{"LD_PRELOAD": "/malicious/lib.so"},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if resp.Allowed {
		t.Fatal("expected webhook to DENY run with LD_PRELOAD env var")
	}
}

func TestPolicyEnforcer_AllowsSafeEnvVars(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "test-policy")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy", Namespace: "default"},
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Env:      map[string]string{"MY_CUSTOM_VAR": "safe-value", "DEBUG": "true"},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if !resp.Allowed {
		t.Fatalf("expected webhook to ALLOW run with safe env vars; got denied: %s", resp.Result.Message)
	}
}

// ── Fix 3: Image registry allowlist ──────────────────────────────────────────

func TestPolicyEnforcer_DeniesDisallowedLifecycleImage(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "image-policy")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "image-policy", Namespace: "default"},
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{
			ImagePolicy: &sympoziumv1alpha1.ImagePolicySpec{
				AllowedRegistries: []string{"ghcr.io/sympozium-ai/"},
			},
		},
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				PreRun: []sympoziumv1alpha1.LifecycleHookContainer{
					{Name: "evil", Image: "evil.io/malware:latest"},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if resp.Allowed {
		t.Fatal("expected webhook to DENY run with image from disallowed registry")
	}
}

func TestPolicyEnforcer_DeniesHookEnvValueAndValueFrom(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "p")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				PreRun: []sympoziumv1alpha1.LifecycleHookContainer{
					{
						Name:  "fetch",
						Image: "busybox:1.36",
						Env: []sympoziumv1alpha1.EnvVar{
							{
								Name:  "TOKEN",
								Value: "plaintext",
								ValueFrom: &sympoziumv1alpha1.EnvVarSource{
									SecretKeyRef: &sympoziumv1alpha1.SecretKeySelector{Name: "s", Key: "k"},
								},
							},
						},
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if resp.Allowed {
		t.Fatal("expected webhook to DENY hook env that sets both value and valueFrom")
	}
}

func TestPolicyEnforcer_DeniesHookEnvIncompleteSecretKeyRef(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "p")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				PreRun: []sympoziumv1alpha1.LifecycleHookContainer{
					{
						Name:  "fetch",
						Image: "busybox:1.36",
						Env: []sympoziumv1alpha1.EnvVar{
							{
								Name: "TOKEN",
								ValueFrom: &sympoziumv1alpha1.EnvVarSource{
									SecretKeyRef: &sympoziumv1alpha1.SecretKeySelector{Name: "s"},
								},
							},
						},
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if resp.Allowed {
		t.Fatal("expected webhook to DENY secretKeyRef missing key")
	}
}

func TestPolicyEnforcer_AllowsHookEnvSecretKeyRef(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "p")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				PreRun: []sympoziumv1alpha1.LifecycleHookContainer{
					{
						Name:  "fetch",
						Image: "busybox:1.36",
						Env: []sympoziumv1alpha1.EnvVar{
							{
								Name: "TOKEN",
								ValueFrom: &sympoziumv1alpha1.EnvVarSource{
									SecretKeyRef: &sympoziumv1alpha1.SecretKeySelector{Name: "gh-pat", Key: "token"},
								},
							},
						},
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if !resp.Allowed {
		t.Fatalf("expected webhook to ALLOW valid hook secretKeyRef; got denied: %s", resp.Result.Message)
	}
}

func TestPolicyEnforcer_AllowsAllowedLifecycleImage(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "image-policy")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "image-policy", Namespace: "default"},
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{
			ImagePolicy: &sympoziumv1alpha1.ImagePolicySpec{
				AllowedRegistries: []string{"ghcr.io/sympozium-ai/"},
			},
		},
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				PreRun: []sympoziumv1alpha1.LifecycleHookContainer{
					{Name: "good", Image: "ghcr.io/sympozium-ai/my-hook:v1"},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if !resp.Allowed {
		t.Fatalf("expected webhook to ALLOW run with image from allowed registry; got denied: %s", resp.Result.Message)
	}
}

func TestPolicyEnforcer_NoImagePolicy_AllowsAnyImage(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "no-image-policy")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "no-image-policy", Namespace: "default"},
		// No ImagePolicy set
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				PreRun: []sympoziumv1alpha1.LifecycleHookContainer{
					{Name: "any", Image: "any-registry.io/anything:latest"},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if !resp.Allowed {
		t.Fatalf("expected webhook to ALLOW any image when no ImagePolicy is set; got denied: %s", resp.Result.Message)
	}
}

func TestPolicyEnforcer_DeniesDisallowedSandboxImage(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "image-policy")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "image-policy", Namespace: "default"},
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{
			ImagePolicy: &sympoziumv1alpha1.ImagePolicySpec{
				AllowedRegistries: []string{"ghcr.io/sympozium-ai/"},
			},
		},
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Sandbox: &sympoziumv1alpha1.AgentRunSandboxSpec{
				Enabled: true,
				Image:   "evil.io/rootkit:latest",
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if resp.Allowed {
		t.Fatal("expected webhook to DENY run with sandbox image from disallowed registry")
	}
}

// ── Fix 6: Lifecycle RBAC bounds ─────────────────────────────────────────────

func TestPolicyEnforcer_DeniesLifecycleRBAC_DeniedResources(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "lifecycle-policy")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "lifecycle-policy", Namespace: "default"},
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{
			LifecyclePolicy: &sympoziumv1alpha1.LifecyclePolicySpec{
				DeniedResources: []string{"secrets", "clusterroles"},
			},
		},
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				RBAC: []sympoziumv1alpha1.RBACRule{
					{
						APIGroups: []string{""},
						Resources: []string{"secrets"},
						Verbs:     []string{"get", "list"},
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if resp.Allowed {
		t.Fatal("expected webhook to DENY lifecycle RBAC requesting access to denied resource 'secrets'")
	}
}

func TestPolicyEnforcer_AllowsLifecycleRBAC_NonDeniedResources(t *testing.T) {
	scheme := newTestScheme(t)
	instance := newTestInstance("inst", "default", "lifecycle-policy")
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "lifecycle-policy", Namespace: "default"},
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{
			LifecyclePolicy: &sympoziumv1alpha1.LifecyclePolicySpec{
				DeniedResources: []string{"secrets", "clusterroles"},
			},
		},
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				RBAC: []sympoziumv1alpha1.RBACRule{
					{
						APIGroups: []string{""},
						Resources: []string{"configmaps"},
						Verbs:     []string{"get", "list"},
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	resp := pe.Handle(context.Background(), admissionCreateRequest(t, run))
	if !resp.Allowed {
		t.Fatalf("expected webhook to ALLOW lifecycle RBAC for non-denied resource 'configmaps'; got denied: %s", resp.Result.Message)
	}
}

// ── Fix 2: Model validation webhook ─────────────────────────────────────────

func admissionModelRequest(t *testing.T, model *sympoziumv1alpha1.Model) admission.Request {
	t.Helper()
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("marshal model: %v", err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

func TestModelValidator_DeniesEmptySource(t *testing.T) {
	scheme := newTestScheme(t)
	mv := &ModelValidator{Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	model := &sympoziumv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default"},
		Spec: sympoziumv1alpha1.ModelCRDSpec{
			Source: sympoziumv1alpha1.ModelSource{}, // empty
		},
	}

	resp := mv.Handle(context.Background(), admissionModelRequest(t, model))
	if resp.Allowed {
		t.Fatal("expected webhook to DENY model with empty source")
	}
}

func TestModelValidator_DeniesInvalidURLScheme(t *testing.T) {
	scheme := newTestScheme(t)
	mv := &ModelValidator{Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	model := &sympoziumv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default"},
		Spec: sympoziumv1alpha1.ModelCRDSpec{
			Source: sympoziumv1alpha1.ModelSource{
				URL: "ftp://example.com/model.gguf",
			},
		},
	}

	resp := mv.Handle(context.Background(), admissionModelRequest(t, model))
	if resp.Allowed {
		t.Fatal("expected webhook to DENY model with ftp:// URL scheme")
	}
}

func TestModelValidator_AllowsHTTPSURL(t *testing.T) {
	scheme := newTestScheme(t)
	mv := &ModelValidator{Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	model := &sympoziumv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default"},
		Spec: sympoziumv1alpha1.ModelCRDSpec{
			Source: sympoziumv1alpha1.ModelSource{
				URL: "https://huggingface.co/model.gguf",
			},
		},
	}

	resp := mv.Handle(context.Background(), admissionModelRequest(t, model))
	if !resp.Allowed {
		t.Fatalf("expected webhook to ALLOW model with https URL; got denied: %s", resp.Result.Message)
	}
}

func TestModelValidator_AllowsModelID(t *testing.T) {
	scheme := newTestScheme(t)
	mv := &ModelValidator{Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	model := &sympoziumv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default"},
		Spec: sympoziumv1alpha1.ModelCRDSpec{
			Source: sympoziumv1alpha1.ModelSource{
				ModelID: "meta-llama/Llama-3.1-8B-Instruct",
			},
		},
	}

	resp := mv.Handle(context.Background(), admissionModelRequest(t, model))
	if !resp.Allowed {
		t.Fatalf("expected webhook to ALLOW model with modelID; got denied: %s", resp.Result.Message)
	}
}

func TestModelValidator_DeniesInvalidSHA256(t *testing.T) {
	scheme := newTestScheme(t)
	mv := &ModelValidator{Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	model := &sympoziumv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default"},
		Spec: sympoziumv1alpha1.ModelCRDSpec{
			Source: sympoziumv1alpha1.ModelSource{
				URL:    "https://example.com/model.gguf",
				SHA256: "not-a-valid-hash",
			},
		},
	}

	resp := mv.Handle(context.Background(), admissionModelRequest(t, model))
	if resp.Allowed {
		t.Fatal("expected webhook to DENY model with invalid SHA256")
	}
}

func TestModelValidator_AllowsValidSHA256(t *testing.T) {
	scheme := newTestScheme(t)
	mv := &ModelValidator{Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	model := &sympoziumv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default"},
		Spec: sympoziumv1alpha1.ModelCRDSpec{
			Source: sympoziumv1alpha1.ModelSource{
				URL:    "https://example.com/model.gguf",
				SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},
		},
	}

	resp := mv.Handle(context.Background(), admissionModelRequest(t, model))
	if !resp.Allowed {
		t.Fatalf("expected webhook to ALLOW model with valid SHA256; got denied: %s", resp.Result.Message)
	}
}

// ── Helper function unit tests ───────────────────────────────────────────────

func TestIsImageAllowed(t *testing.T) {
	policy := &sympoziumv1alpha1.ImagePolicySpec{
		AllowedRegistries: []string{"ghcr.io/sympozium-ai/", "docker.io/library/"},
	}

	tests := []struct {
		image string
		want  bool
	}{
		{"ghcr.io/sympozium-ai/my-image:v1", true},
		{"docker.io/library/busybox:1.36", true},
		{"evil.io/malware:latest", false},
		{"ghcr.io/other-org/image:v1", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			got := policy.Allows(tt.image)
			if got != tt.want {
				t.Errorf("Allows(%q) = %v, want %v", tt.image, got, tt.want)
			}
		})
	}
}

func TestDeniedEnvVarKeys(t *testing.T) {
	// All these should be denied
	for _, key := range []string{"PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "HOME", "SHELL", "USER", "HOSTNAME"} {
		if !deniedEnvVarKeys[key] {
			t.Errorf("deniedEnvVarKeys should contain %q", key)
		}
	}
	// These should be allowed
	for _, key := range []string{"MY_VAR", "DEBUG", "OPENAI_API_KEY"} {
		if deniedEnvVarKeys[key] {
			t.Errorf("deniedEnvVarKeys should NOT contain %q", key)
		}
	}
}
