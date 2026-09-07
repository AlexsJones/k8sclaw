package cellnreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type runIssuancePayload struct {
	APIVersion string                            `json:"apiVersion"`
	Request    IssuerRequest                     `json:"request"`
	Candidate  cellnauthority.ExecutionCandidate `json:"candidate"`
}

func payloadHash(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(h[:])
}

// IssueForRun durably freezes before the first remote call. Supply seed only for
// initial preparation; nil resumes saved state. Neither retries nor API conflicts
// may regenerate identity. This helper provisions only; it never dispatches or
// writes CellnRequest/CellnActionID/readiness or transitions the AgentRun phase.
func (c *IssuerClient) IssueForRun(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, loader cellnauthority.ModelLoader, seed *IssuerRequest) (*IssuedSelection, error) {
	ctx, cancel := context.WithTimeout(ctx, 110*time.Second)
	defer cancel()
	if writer == nil || reader == nil || key.Namespace == "" || key.Name == "" {
		return nil, fmt.Errorf("status writer, uncached reader and run key required")
	}
	var run api.AgentRun
	if err := reader.Get(ctx, key, &run); err != nil {
		return nil, err
	}
	if err := provisionableRun(&run); err != nil {
		return nil, err
	}
	var wanted *api.CellnIssuanceStatus
	if run.Status.CellnIssuance == nil {
		if seed == nil || seed.APIVersion != "sympozium.ai/celln-issuer-request-v1" {
			return nil, fmt.Errorf("initial frozen issuance seed required")
		}
		if err := sameIssuanceRun(&run, seed.Frozen.Run); err != nil {
			return nil, err
		}
		expected, err := loader.BuildExecution(ctx, seed.Frozen, seed.Approval, seed.Artifacts)
		if err != nil {
			return nil, err
		}
		body, err := json.Marshal(runIssuancePayload{APIVersion: "sympozium.ai/celln-run-issuance-v1", Request: *seed, Candidate: *expected})
		if err != nil || len(body) > 393216 || len(c.endpoint) > 2048 {
			return nil, fmt.Errorf("issuance payload exceeds status bound")
		}
		wanted = &api.CellnIssuanceStatus{Phase: "Prepared", Target: c.endpoint, Payload: string(body), PayloadSHA256: payloadHash(string(body))}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var current api.AgentRun
			if err := reader.Get(ctx, key, &current); err != nil {
				return err
			}
			if err := provisionableRun(&current); err != nil {
				return err
			}
			if err := sameIssuanceRun(&current, seed.Frozen.Run); err != nil {
				return err
			}
			if saved := current.Status.CellnIssuance; saved != nil {
				if saved.Target != wanted.Target || saved.Payload != wanted.Payload || saved.PayloadSHA256 != wanted.PayloadSHA256 {
					return fmt.Errorf("concurrent issuance chose a different frozen payload")
				}
				wanted = saved.DeepCopy()
				return nil
			}
			current.Status.CellnIssuance = wanted.DeepCopy()
			return writer.Status().Update(ctx, &current)
		}); err != nil {
			return nil, err
		}
	} else {
		wanted = run.Status.CellnIssuance.DeepCopy()
	}
	payload, issued, err := decodeRunIssuance(wanted, c.endpoint)
	if err != nil {
		return nil, err
	}
	if err := sameIssuanceRun(&run, payload.Request.Frozen.Run); err != nil {
		return nil, err
	}
	if seed != nil && !reflect.DeepEqual(*seed, payload.Request) {
		return nil, fmt.Errorf("retry seed differs from durable issuance")
	}
	expected, err := loader.BuildExecution(ctx, payload.Request.Frozen, payload.Request.Approval, payload.Request.Artifacts)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(*expected, payload.Candidate) {
		return nil, fmt.Errorf("saved candidate differs from independently derived request")
	}
	if issued != nil {
		return issued, nil
	}
	issued, err = c.Issue(ctx, loader, payload.Request.Frozen, payload.Request.Approval, payload.Request.Artifacts)
	if err != nil {
		return nil, err
	}
	result, err := json.Marshal(issued)
	if err != nil || len(result) > 196608 {
		return nil, fmt.Errorf("issued result exceeds status bound")
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current api.AgentRun
		if err := reader.Get(ctx, key, &current); err != nil {
			return err
		}
		if err := provisionableRun(&current); err != nil {
			return err
		}
		if err := sameIssuanceRun(&current, payload.Request.Frozen.Run); err != nil {
			return err
		}
		saved := current.Status.CellnIssuance
		if saved == nil || saved.Target != wanted.Target || saved.Payload != wanted.Payload || saved.PayloadSHA256 != wanted.PayloadSHA256 {
			return fmt.Errorf("durable issuance changed during remote provisioning")
		}
		if saved.Phase == "Issued" {
			if saved.Result != string(result) {
				return fmt.Errorf("concurrent issuer returned a different outcome")
			}
			return nil
		}
		if saved.Phase != "Prepared" || saved.Result != "" {
			return fmt.Errorf("invalid durable issuance transition")
		}
		saved.Phase, saved.Result = "Issued", string(result)
		return writer.Status().Update(ctx, &current)
	}); err != nil {
		return nil, err
	}
	return issued, nil
}

func provisionableRun(run *api.AgentRun) error {
	if run.Spec.Backend != "celln" || run.DeletionTimestamp != nil || run.Status.CellnRequest != "" || run.Status.CellnActionID != "" || (run.Status.Phase != "" && run.Status.Phase != api.AgentRunPhasePending) {
		return fmt.Errorf("run is not in undispatched Celln provisioning state")
	}
	return nil
}

func sameIssuanceRun(run *api.AgentRun, expected cellnauthority.Subject) error {
	actual, err := cellnauthority.IdentifySubject("AgentRun", run.ObjectMeta, run.Spec)
	if err != nil || actual != expected {
		return fmt.Errorf("AgentRun identity changed during issuance")
	}
	return nil
}

func decodeRunIssuance(saved *api.CellnIssuanceStatus, target string) (runIssuancePayload, *IssuedSelection, error) {
	var payload runIssuancePayload
	if saved.Target != target || len(saved.Payload) > 393216 || saved.PayloadSHA256 != payloadHash(saved.Payload) || len(saved.Result) > 196608 {
		return payload, nil, fmt.Errorf("durable issuer target or payload mismatch")
	}
	d := json.NewDecoder(bytes.NewBufferString(saved.Payload))
	d.DisallowUnknownFields()
	if d.Decode(&payload) != nil || d.Decode(new(any)) != io.EOF || payload.APIVersion != "sympozium.ai/celln-run-issuance-v1" || payload.Request.APIVersion != "sympozium.ai/celln-issuer-request-v1" {
		return payload, nil, fmt.Errorf("invalid durable issuance payload")
	}
	if saved.Phase == "Prepared" && saved.Result == "" {
		return payload, nil, nil
	}
	if saved.Phase != "Issued" || saved.Result == "" {
		return payload, nil, fmt.Errorf("invalid durable issuance state")
	}
	var issued IssuedSelection
	d = json.NewDecoder(bytes.NewBufferString(saved.Result))
	d.DisallowUnknownFields()
	if d.Decode(&issued) != nil || d.Decode(new(any)) != io.EOF {
		return payload, nil, fmt.Errorf("invalid durable issuance outcome")
	}
	if err := validateRemoteIssuance(payload.Candidate, issued); err != nil {
		return payload, nil, err
	}
	return payload, &issued, nil
}
