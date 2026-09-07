package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// Render the actual chart, then check Kubernetes selector semantics. This is
// not a substitute for the M0 cross-namespace test on a policy-enforcing CNI.
func TestCellnNetworkPolicy(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required for chart rendering")
	}
	for _, tc := range []struct {
		name, namespace, settings string
		enabled                   bool
	}{
		{"default", "sympozium-system", "celln.enabled=true", true},
		{"custom-namespace", "custom-control", "celln.enabled=true,namespace=custom-control", true},
		{"ordinary-policies-disabled", "sympozium-system", "celln.enabled=true,networkPolicies.enabled=false", true},
		{"celln-disabled", "sympozium-system", "celln.enabled=false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := exec.Command("helm", "template", "m0", "../../charts/sympozium", "--set", tc.settings).CombinedOutput()
			if err != nil {
				t.Fatalf("helm template: %v\n%s", err, out)
			}
			decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
			var policy *networkingv1.NetworkPolicy
			var controller appsv1.Deployment
			for {
				var raw json.RawMessage
				if err := decoder.Decode(&raw); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if len(bytes.TrimSpace(raw)) == 0 {
					continue // Helm may emit comment-only YAML documents.
				}
				var meta struct {
					Kind     string
					Metadata struct{ Name string }
				}
				if err := json.Unmarshal(raw, &meta); err != nil {
					t.Fatal(err)
				}
				if meta.Kind == "NetworkPolicy" && meta.Metadata.Name == "celln-router-ingress" {
					if policy != nil {
						t.Fatal("duplicate ingress policy")
					}
					policy = &networkingv1.NetworkPolicy{}
					if err := json.Unmarshal(raw, policy); err != nil {
						t.Fatal(err)
					}
				}
				if meta.Kind == "Deployment" && meta.Metadata.Name == "sympozium-controller-manager" {
					if err := json.Unmarshal(raw, &controller); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !tc.enabled {
				if policy != nil {
					t.Fatal("Celln disabled but policy rendered")
				}
				return
			}
			if policy == nil {
				t.Fatal("Celln router is missing its ingress policy")
			}
			if policy.Namespace != "celln-system" || policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"] != "celln-router" {
				t.Fatal("policy does not target router")
			}
			if len(policy.Spec.PolicyTypes) != 1 || policy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
				t.Fatal("ingress isolation missing")
			}
			if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 1 {
				t.Fatal("unexpected additional ingress grants")
			}
			rule := policy.Spec.Ingress[0]
			if len(rule.Ports) != 1 || rule.Ports[0].Port.IntVal != 8788 || rule.Ports[0].Protocol == nil || *rule.Ports[0].Protocol != "TCP" {
				t.Fatal("must grant only router TCP port")
			}
			peer := rule.From[0]
			if peer.NamespaceSelector == nil || peer.PodSelector == nil || peer.IPBlock != nil {
				t.Fatal("namespace and pod selectors must be intersected")
			}
			nsSelector, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
			if err != nil {
				t.Fatal(err)
			}
			podSelector, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
			if err != nil {
				t.Fatal(err)
			}
			allowed := func(ns string, podLabels map[string]string) bool {
				return nsSelector.Matches(labels.Set{"kubernetes.io/metadata.name": ns}) && podSelector.Matches(labels.Set(podLabels))
			}
			if controller.Namespace != tc.namespace || !allowed(controller.Namespace, controller.Spec.Template.Labels) {
				t.Fatal("actual controller pod cannot reach router")
			}
			if allowed("tenant", controller.Spec.Template.Labels) {
				t.Fatal("tenant copying controller labels is allowed")
			}
			if allowed(tc.namespace, map[string]string{"sympozium.ai/role": "agent"}) {
				t.Fatal("ordinary control-plane namespace pod is allowed")
			}
			if allowed("tenant", nil) {
				t.Fatal("unrelated tenant is allowed")
			}
		})
	}
}
