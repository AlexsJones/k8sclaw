package cellnreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRealCellnReview(t *testing.T) {
	binary, bundle := os.Getenv("CELLN_REVIEW_BINARY"), os.Getenv("CELLN_REVIEW_FIXTURE")
	if binary == "" || bundle == "" {
		t.Skip("requires explicit trusted Celln binary and public prepare_review_fixture output")
	}
	data, err := os.ReadFile(filepath.Join(bundle, "submission.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s api.CellnToolSubmission
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	s.Namespace, s.UID, s.Generation = "review-test", "real-cli-fixture", 1
	id, err := cellnauthority.Identify(api.CellnTool{ObjectMeta: s.ObjectMeta, Spec: s.Spec})
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&s).Build()
	o := Options{Namespace: s.Namespace, Name: s.Name, SubmissionUID: s.UID, ReviewedSpecSHA256: id.SpecSHA256, Binary: binary, PolicyRoot: bundle, BundleDir: bundle}
	tool, err := Approve(context.Background(), c, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(tool.Status.Conditions) != 0 {
		t.Fatal("manufactured readiness")
	}
	// Actual mismatched schema bytes are refused by Celln, not by a mock report.
	changed := s.DeepCopy()
	changed.Name = "mismatched-schema"
	changed.Spec.ArgumentsSchema = s.Spec.ResultSchema
	c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(changed).Build()
	id, err = cellnauthority.Identify(api.CellnTool{ObjectMeta: changed.ObjectMeta, Spec: changed.Spec})
	if err != nil {
		t.Fatal(err)
	}
	o.Name, o.ReviewedSpecSHA256 = changed.Name, id.SpecSHA256
	if _, err := Approve(context.Background(), c, o); err == nil {
		t.Fatal("schema mismatch accepted")
	}
}
