package cellnreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Real API-server authorization and selection proof. Never creates AgentRuns,
// executes a cell, pauses controllers or obtains model credentials.
func TestLiveCatalogueSelectionAndGrantIsolation(t *testing.T) {
	configPath := os.Getenv("CELLN_COMPOSITION_KUBECONFIG")
	fixture := os.Getenv("CELLN_COMPOSITION_FIXTURE")
	if configPath == "" || fixture == "" {
		t.Skip("explicit isolated kubeconfig and composition fixture required")
	}
	if !filepath.IsAbs(configPath) || !filepath.IsAbs(fixture) {
		t.Fatal("absolute paths required")
	}
	config, err := clientcmd.LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.CurrentContext != "kind-celln-deployed" {
		t.Fatal("refusing non-test Kubernetes context")
	}
	cfg, err := clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Timeout = 10 * time.Second
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	create := func(object client.Object) {
		t.Helper()
		if err := c.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	namespace := func(prefix string) string {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: prefix}}
		create(ns)
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			if err := c.Delete(cleanup, ns); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("namespace cleanup: %v", err)
			}
		})
		return ns.Name
	}
	tenant := namespace("celln-selection-tenant-")
	operator := namespace("celln-selection-operator-")
	bytes, err := os.ReadFile(filepath.Join(fixture, "catalogue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalogue struct {
		RuntimeSpec api.AgentRuntimeSpec `json:"runtimeSpec"`
		Tools       []struct {
			Name string            `json:"name"`
			Spec api.CellnToolSpec `json:"spec"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(bytes, &catalogue); err != nil {
		t.Fatal(err)
	}
	rt := &api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Name: "runtime", Namespace: tenant}, Spec: catalogue.RuntimeSpec}
	create(rt)
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: tenant}, Spec: api.AgentSpec{RuntimeRef: rt.Name}}
	agent.Spec.Agents.Default.Model = "deepseek-chat"
	create(agent)
	aid, err := cellnauthority.IdentifySubject("Agent", agent.ObjectMeta, agent.Spec)
	if err != nil {
		t.Fatal(err)
	}
	rid, err := cellnauthority.IdentifySubject("AgentRuntime", rt.ObjectMeta, rt.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var grants []cellnauthority.Grant
	var selected []cellnauthority.Selection
	for _, tool := range catalogue.Tools {
		object := &api.CellnTool{ObjectMeta: metav1.ObjectMeta{Name: tool.Name, Namespace: tenant}, Spec: tool.Spec}
		create(object)
		id, err := cellnauthority.Identify(*object)
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, cellnauthority.Grant{Tool: id, Limits: object.Spec.Limits})
		selected = append(selected, cellnauthority.Selection{Name: object.Name, Revision: object.Spec.Revision})
	}
	for _, layer := range []string{"operator", "runtime", "agent"} {
		bytes, _ := json.Marshal(cellnauthority.GrantDocument{APIVersion: "sympozium.ai/celln-grants-v1", Layer: layer, Agent: aid, Runtime: rid, Grants: grants})
		create(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: layer, Namespace: operator}, Data: map[string]string{"grants.json": string(bytes)}})
	}
	l := cellnauthority.Loader{Reader: c, OperatorSource: types.NamespacedName{Namespace: operator, Name: "operator"}, RuntimeSource: types.NamespacedName{Namespace: operator, Name: "runtime"}, AgentSource: types.NamespacedName{Namespace: operator, Name: "agent"}}
	snapshot, err := l.Resolve(ctx, client.ObjectKeyFromObject(agent), selected)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := cellnauthority.Prepare(*snapshot, 33554432)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Composition.Sources) != 3 || len(plan.BorrowedTools) != 2 {
		t.Fatal("live source selection mismatch")
	}
	// Give the tenant real ConfigMap write permission in its own namespace.
	// It must still be unable to edit the independently configured grant source.
	user := "celln-selection-user-" + tenant
	create(&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "tenant-configmaps", Namespace: tenant}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get", "create", "update"}}}})
	create(&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "tenant-configmaps", Namespace: tenant}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "tenant-configmaps"}, Subjects: []rbacv1.Subject{{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: user}}})
	tenantConfig := rest.CopyConfig(cfg)
	tenantConfig.Impersonate = rest.ImpersonationConfig{UserName: user}
	tenantClient, err := client.New(tenantConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	var original corev1.ConfigMap
	if err := c.Get(ctx, l.OperatorSource, &original); err != nil {
		t.Fatal(err)
	}
	lookalike := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: original.Name, Namespace: tenant}, Data: original.Data}
	if err := tenantClient.Create(ctx, lookalike); err != nil {
		t.Fatal(err)
	}
	original.Data = map[string]string{"grants.json": "{}"}
	if err := tenantClient.Update(ctx, &original); !apierrors.IsForbidden(err) {
		t.Fatalf("tenant grant update must be forbidden: %v", err)
	}
	// Removing the genuine operator grant must not fall back to the lookalike.
	if err := c.Delete(ctx, &original); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Resolve(ctx, client.ObjectKeyFromObject(agent), selected); err == nil {
		t.Fatal("withdrawn source fell back to tenant lookalike")
	}
	var runs api.AgentRunList
	if err := c.List(ctx, &runs, client.InNamespace(tenant)); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 0 {
		t.Fatal("unexpected AgentRun")
	}
	t.Logf("PASS live API selection of two real-artifact catalogue tools; tenant grant edit forbidden; genuine grant withdrawal refuses; namespaces=%s,%s; AgentRuns=0, modelCalls=0, KVM=not-run", tenant, operator)
}
