package controller

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

var cellnHash = regexp.MustCompile(`^blake3:[0-9a-f]{64}$`)
var cellnInputName = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

func validateCellnRequest(req executionRequest) error {
	bad := func() error { return fmt.Errorf("Celln: invalid immutable execution request or limits") }
	if !slices.Contains([]string{"celln.dev/v1alpha1", "celln.dev/v1alpha2", "celln.dev/v1alpha3"}, req.APIVersion) || req.ID == "" || req.Workload.ID == "" || req.Workload.Caller == "" ||
		!req.Execution.RequireHardwareIsolation || !slices.Contains([]string{"agent", "tool"}, req.Execution.Lane) ||
		!slices.Contains([]string{"none", "read-only", "read-write"}, req.Capabilities.Workspace) ||
		req.Capabilities.TimeoutMs == 0 || req.Capabilities.TimeoutMs > uint64((24*time.Hour)/time.Millisecond) ||
		req.Capabilities.MemoryBytes == 0 || req.Capabilities.MemoryBytes > cellnMemoryBytes ||
		req.Capabilities.OutputBytes == 0 || req.Capabilities.OutputBytes > cellnOutputBytes {
		return bad()
	}
	if !validCellnHarness(req) {
		return bad()
	}
	if req.Forge != nil {
		if req.Mote != nil || len(req.Tools) != 0 || req.Invocation != nil || len(req.Inputs) != 0 || req.Execution.Lane != "agent" || strings.TrimSpace(req.Forge.Task) == "" {
			return bad()
		}
		return nil
	}
	if req.Mote == nil || !cellnHash.MatchString(req.Mote.Hash) || req.Invocation == nil || len(req.Tools) == 0 || len(req.Tools) > 16 || len(req.Inputs) > 16 || len(req.Invocation.Args) > 128 {
		return bad()
	}
	aliases := map[string]bool{}
	for _, tool := range req.Tools {
		if !cellnHash.MatchString(tool.Hash) || !strings.HasPrefix(tool.Alias, "/") || aliases[tool.Alias] || (tool.Closure != nil && !cellnHash.MatchString(tool.Closure.Hash)) {
			return bad()
		}
		aliases[tool.Alias] = true
	}
	if !aliases[req.Invocation.Alias] {
		return bad()
	}
	for _, arg := range req.Invocation.Args {
		if strings.ContainsRune(arg, '\x00') {
			return bad()
		}
	}
	names := map[string]bool{}
	var inputBytes int64
	for _, input := range req.Inputs {
		if !cellnHash.MatchString(input.Hash) || !cellnInputName.MatchString(input.Name) || input.Name == "." || input.Name == ".." || names[input.Name] || input.Bytes <= 0 || input.Bytes > 65536 || strings.TrimSpace(input.MediaType) == "" {
			return bad()
		}
		names[input.Name] = true
		inputBytes += input.Bytes
	}
	if inputBytes > 65536 || (inputBytes > 0 && req.Capabilities.Workspace == "none") {
		return bad()
	}
	return nil
}

// The dispatch record's phase and output text are not sufficient evidence of
// success. Correlate the complete receipt with the frozen request, not today's
// mutable AgentRun spec. Artifact contents are still verified by Celln's store.
func validateCellnReceipt(run *sympoziumv1alpha1.AgentRun, record executionRecord) error {
	bad := func() error { return fmt.Errorf("Celln: invalid or mismatched terminal receipt") }
	r := record.Receipt
	if r == nil || !slices.Contains([]string{"celln.dev/v1alpha1", "celln.dev/v1alpha2", "celln.dev/v1alpha3"}, r.APIVersion) || r.RequestID != run.Status.CellnActionID || record.RequestID != r.RequestID ||
		r.Phase != strings.ToLower(record.Phase) || !slices.Contains([]string{"succeeded", "failed", "cancelled"}, r.Phase) || strings.TrimSpace(r.Node) == "" || strings.TrimSpace(r.CellID) == "" {
		return bad()
	}
	start, err := time.Parse(time.RFC3339, r.StartedAt)
	if err != nil {
		return bad()
	}
	end, err := time.Parse(time.RFC3339, r.CompletedAt)
	if err != nil || end.Before(start) {
		return bad()
	}
	var req executionRequest
	if json.Unmarshal([]byte(run.Status.CellnRequest), &req) != nil || validateCellnRequest(req) != nil || req.ID != r.RequestID || req.APIVersion != r.APIVersion {
		return bad()
	}
	if len(r.Resolved.Tools) != 1 || !cellnHash.MatchString(r.Resolved.Tools[0]) {
		return bad()
	}
	if req.Mote != nil {
		if r.Resolved.Mote == nil || *r.Resolved.Mote != req.Mote.Hash {
			return bad()
		}
		matched := false
		for _, tool := range req.Tools {
			if tool.Alias == req.Invocation.Alias && tool.Hash == r.Resolved.Tools[0] {
				matched = true
			}
		}
		if !matched {
			return bad()
		}
	} else if r.Resolved.Mote != nil {
		return bad()
	}
	inputs := make([]string, 0, len(req.Inputs))
	for _, input := range req.Inputs {
		inputs = append(inputs, input.Hash)
	}
	if !slices.Equal(inputs, r.Resolved.Inputs) {
		return bad()
	}
	// The wrapper is a lossy UTF-8 display of raw artifact bytes; invalid
	// sequences can expand threefold. It is never a content-addressed input.
	if uint64(len(record.Output)) > 3*req.Capabilities.OutputBytes {
		return bad()
	}
	if r.Output == nil {
		if record.Output != "" {
			return bad()
		}
	} else if !cellnHash.MatchString(r.Output.Hash) || strings.TrimSpace(r.Output.MediaType) == "" || r.Output.Bytes == 0 || r.Output.Bytes > req.Capabilities.OutputBytes {
		return bad()
	}
	return nil
}
