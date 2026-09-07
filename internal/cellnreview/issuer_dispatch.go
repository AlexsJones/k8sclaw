package cellnreview

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FreezeIssuedDispatch commits the exact verified outcome to the existing
// dispatch journal before submission. It makes no host requests, chooses no
// route, and asserts neither readiness nor continuing host authorization.
// Callers must submit these exact bytes/ID to the owning serving process and
// never fall back to forge, regenerate identity, or fail over an ambiguous task.
func (c *IssuerClient) FreezeIssuedDispatch(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, loader cellnauthority.ModelLoader) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if writer == nil || reader == nil || key.Namespace == "" || key.Name == "" {
		return nil, fmt.Errorf("status writer, uncached reader and run key required")
	}
	var result json.RawMessage
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var run api.AgentRun
		if err := reader.Get(ctx, key, &run); err != nil {
			return err
		}
		if run.Spec.Backend != "celln" || run.DeletionTimestamp != nil || (run.Status.Phase != "" && run.Status.Phase != api.AgentRunPhasePending) || run.Status.CellnIssuance == nil {
			return fmt.Errorf("run is not pending catalogue dispatch")
		}
		payload, issued, err := decodeRunIssuance(run.Status.CellnIssuance, c.endpoint)
		if err != nil {
			return err
		}
		if issued == nil {
			return fmt.Errorf("issuance has no committed outcome")
		}
		if err := sameIssuanceRun(&run, payload.Request.Frozen.Run); err != nil {
			return err
		}
		expected, err := loader.BuildExecution(ctx, payload.Request.Frozen, payload.Request.Approval, payload.Request.Artifacts)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(*expected, payload.Candidate) {
			return fmt.Errorf("saved candidate differs from independently derived request")
		}
		// Full request shape and identity have already been checked by
		// decodeRunIssuance against the independently derived candidate.
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(issued.Request, &identity); err != nil || identity.ID == "" {
			return fmt.Errorf("verified request has no execution identity")
		}
		if run.Status.CellnRequest != "" || run.Status.CellnActionID != "" {
			if run.Status.CellnRequest != string(issued.Request) || run.Status.CellnActionID != identity.ID {
				return fmt.Errorf("dispatch journal differs from issued outcome")
			}
			result = append(json.RawMessage(nil), issued.Request...)
			return nil
		}
		run.Status.CellnRequest = string(issued.Request)
		run.Status.CellnActionID = identity.ID
		if err := writer.Status().Update(ctx, &run); err != nil {
			return err
		}
		result = append(json.RawMessage(nil), issued.Request...)
		return nil
	})
	if err != nil {
		// Never return dispatchable bytes after an ambiguous status write.
		return nil, err
	}
	return result, nil
}
