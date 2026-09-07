package cellnreview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fixture(t *testing.T) (client.Client, Options, *api.CellnToolSubmission) {
	t.Helper()
	hash := "blake3:" + strings.Repeat("a", 64)
	s := &api.CellnToolSubmission{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "tenant", UID: "original", Generation: 1,
			Annotations: map[string]string{"forged": "approval"}},
		Spec: api.CellnToolSpec{Revision: "v1", Description: "review me", SupportOwner: "operator",
			PublisherKey: strings.Repeat("b", 64), Executable: api.CellnImmutableRef{Hash: hash},
			Closure: api.CellnImmutableRef{Hash: hash}, EntryPoint: "/example", InvocationABI: "celln.argv/v1",
			ArgumentsSchema: api.CellnImmutableRef{Hash: hash}, ResultSchema: api.CellnImmutableRef{Hash: hash},
			Platform: "linux/amd64", Lane: "tool", Limits: api.CellnToolLimits{TimeoutMillis: 1000,
				MemoryBytes: 33554432, ArgumentBytes: 1024, OutputBytes: 1024, Workspace: "none", Effects: "none"}},
		Status: api.CellnToolStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}},
	}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s).Build()
	id, err := cellnauthority.Identify(api.CellnTool{ObjectMeta: s.ObjectMeta, Spec: s.Spec})
	if err != nil {
		t.Fatal(err)
	}
	return c, Options{Namespace: s.Namespace, Name: s.Name, SubmissionUID: s.UID, ReviewedSpecSHA256: id.SpecSHA256,
		Binary: "/trusted/celln", PolicyRoot: "/operator/policy", BundleDir: "/operator/bundle"}, s
}

func reports(s *api.CellnToolSubmission) runner {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "--root" {
			return json.Marshal(closureReport{APIVersion: "celln.dev/closure-verification-v1", Scope: "descriptor-and-local-toolfs-bytes",
				Closure: s.Spec.Closure.Hash, Publisher: s.Spec.PublisherKey, EntryPoint: s.Spec.EntryPoint, Executable: s.Spec.Executable.Hash,
				PolicyHash: s.Spec.Closure.Hash, Toolfs: s.Spec.Closure.Hash, LocalToolfsVerified: true, LocalToolfsBytes: 100,
				ArtifactReadiness: "not_checked", Conformance: "not_checked"})
		}
		return json.Marshal(schemaReport{APIVersion: "celln.dev/tool-schema-verification-v1", Profile: "celln.tool-schema/v1",
			Schema: args[len(args)-1], Scope: "schema-and-data-only"})
	}
}

func TestOperatorPublicationBindsReviewAndNeverCopiesAuthority(t *testing.T) {
	c, o, s := fixture(t)
	calls := 0
	tool, err := approve(context.Background(), c, o, func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		calls++
		if binary != o.Binary {
			t.Fatal("untrusted binary selection")
		}
		return reports(s)(ctx, binary, args...)
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 4 || len(tool.Status.Conditions) != 0 || tool.Annotations["forged"] != "" || len(tool.OwnerReferences) != 0 {
		t.Fatalf("authority copied: %+v", tool)
	}
	if tool.Annotations["celln.sympozium.ai/reviewed-submission-uid"] != "original" || tool.Annotations["celln.sympozium.ai/reviewed-spec-sha256"] != o.ReviewedSpecSHA256 {
		t.Fatal("unbound publication")
	}
	if _, err := approve(context.Background(), c, o, reports(s)); err == nil {
		t.Fatal("existing revision overwritten")
	}
}

func TestRefusalsNeverPublish(t *testing.T) {
	for _, scenario := range []string{"uid", "spec", "relative-path", "command", "old-binary", "wrong-schema", "interpreter", "wrong-publisher", "unverified-bytes", "trailing", "huge", "policy-change", "recreated", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			c, o, s := fixture(t)
			switch scenario {
			case "uid":
				o.SubmissionUID = "stale"
			case "spec":
				o.ReviewedSpecSHA256 = "stale"
			case "relative-path":
				o.Binary = "celln"
			}
			calls := 0
			_, err := approve(context.Background(), c, o, func(ctx context.Context, binary string, args ...string) ([]byte, error) {
				calls++
				if scenario == "command" {
					return nil, fmt.Errorf("signature refused")
				}
				data, err := reports(s)(ctx, binary, args...)
				switch scenario {
				case "old-binary":
					data = []byte(`{"apiVersion":"celln.dev/closure-verification-v1","scope":"descriptor-authenticity-only"}`)
				case "interpreter", "wrong-publisher", "unverified-bytes":
					var report map[string]any
					_ = json.Unmarshal(data, &report)
					if scenario == "interpreter" {
						report["interpreter"] = true
					}
					if scenario == "wrong-publisher" {
						report["publisher"] = strings.Repeat("c", 64)
					}
					if scenario == "unverified-bytes" {
						report["localToolfsVerified"] = false
					}
					data, _ = json.Marshal(report)
				case "wrong-schema":
					if calls == 2 {
						data = []byte(`{"schema":"wrong"}`)
					}
				case "trailing":
					data = append(data, []byte(" {}")...)
				case "huge":
					data = []byte(strings.Repeat(" ", (1<<20)+1))
				case "policy-change":
					if calls == 4 {
						var report map[string]any
						_ = json.Unmarshal(data, &report)
						report["policyHash"] = "blake3:" + strings.Repeat("c", 64)
						data, _ = json.Marshal(report)
					}
				case "recreated", "deleted":
					if calls == 4 {
						if err := c.Delete(ctx, s); err != nil {
							t.Fatal(err)
						}
						if scenario == "recreated" {
							replacement := s.DeepCopy()
							replacement.UID = "replacement"
							replacement.ResourceVersion = ""
							if err := c.Create(ctx, replacement); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				return data, err
			})
			if err == nil {
				t.Fatal("expected refusal")
			}
			var tool api.CellnTool
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: o.Namespace, Name: o.Name}, &tool); err == nil {
				t.Fatal("published after refusal")
			}
		})
	}
}

func TestBoundedOutput(t *testing.T) {
	var b boundedOutput
	if _, err := b.Write(make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("x")); err == nil || b.Len() != 1<<20 {
		t.Fatal("unbounded report")
	}
}
