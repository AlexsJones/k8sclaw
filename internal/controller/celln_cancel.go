package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// Acknowledging cancellation is not teardown. Keep reconciling (and retain
// the Kubernetes finalizer) until Celln reports a terminal execution. This
// also cancels work when another controller marks an AgentRun failed.
func (r *AgentRunReconciler) cancelCelln(ctx context.Context, run *sympoziumv1alpha1.AgentRun) (bool, error) {
	id := run.Status.CellnActionID
	if id == "" {
		return true, nil
	}
	if id != cellnActionID(run) {
		return false, fmt.Errorf("Celln: cancellation identity mismatch")
	}
	req, err := cellnRequest(ctx, http.MethodPost, "/v1/executions/"+id+"/cancel", nil)
	if err != nil {
		return false, err
	}
	resp, err := cellnHTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return true, nil
	}
	if resp.StatusCode == http.StatusAccepted {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("Celln: cancellation refused (HTTP %d)", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 262145))
	if err != nil || len(body) > 262144 {
		return false, fmt.Errorf("Celln: invalid cancellation response size")
	}
	var record executionRecord
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&record) != nil || d.Decode(new(any)) != io.EOF || record.RequestID != id || !slices.Contains([]string{"Succeeded", "Failed", "Refused", "Cancelled"}, record.Phase) {
		return false, fmt.Errorf("Celln: cancellation has no correlated terminal record")
	}
	if record.Receipt != nil {
		if err := validateCellnReceipt(run, record); err != nil {
			return false, err
		}
		encoded, _ := json.Marshal(record.Receipt)
		if err := r.updateStatusWithRetry(ctx, run, func(ar *sympoziumv1alpha1.AgentRun) { ar.Status.CellnReceipt = string(encoded) }); err != nil {
			return false, err
		}
	} else if record.Phase == "Succeeded" {
		return false, fmt.Errorf("Celln: success without receipt during cancellation")
	}
	return true, nil
}
