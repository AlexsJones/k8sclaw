package controller

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func jsonWire(t *testing.T) executionRequest {
	t.Helper()
	bytes, err := os.ReadFile("../../test/integration/fixtures/celln-json-dispatch-request.json")
	if err != nil {
		t.Fatal(err)
	}
	var req executionRequest
	if err := json.Unmarshal(bytes, &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestCellnJSONWireMatchesProvenHostContract(t *testing.T) {
	req := jsonWire(t)
	if err := validateCellnRequest(req); err != nil {
		t.Fatal(err)
	}
	if req.Harness.JSON.System != "Use the explicitly lent tools." || req.Harness.JSON.MaxTurns != 3 || req.Harness.BorrowedTools[0].JSONStdio.ABI != "celln.json-stdio/v1" {
		t.Fatal("JSON wire fields were lost")
	}
	bytes, _ := json.Marshal(req.Harness)
	var roundTrip map[string]any
	_ = json.Unmarshal(bytes, &roundTrip)
	if roundTrip["json"].(map[string]any)["maxTurns"] != float64(3) {
		t.Fatal("wrong outbound JSON shape")
	}
	req.Harness.BorrowedTools = []api.CellnBorrowedTool{}
	if validateCellnRequest(req) != nil {
		t.Fatal("empty selection should grant no tools")
	}
	req.Harness.BorrowedTools = nil
	if validateCellnRequest(req) == nil {
		t.Fatal("null is not a valid Rust Vec wire representation")
	}
}

func TestCellnJSONContractRefusesVersionConfusionAndIgnoredLimits(t *testing.T) {
	for name, mutate := range map[string]func(*executionRequest){
		"reference version":     func(r *executionRequest) { r.APIVersion = "celln.dev/v1alpha2" },
		"reference contract":    func(r *executionRequest) { r.Harness.ContractVersion = "celln.reference-functions/v1" },
		"missing options":       func(r *executionRequest) { r.Harness.JSON = nil },
		"long persona":          func(r *executionRequest) { r.Harness.JSON.System = strings.Repeat("x", 2049) },
		"zero turns":            func(r *executionRequest) { r.Harness.JSON.MaxTurns = 0 },
		"excess turns":          func(r *executionRequest) { r.Harness.JSON.MaxTurns = 7 },
		"negative calls":        func(r *executionRequest) { r.Harness.JSON.MaxCalls = -1 },
		"excess calls":          func(r *executionRequest) { r.Harness.JSON.MaxCalls = 17 },
		"missing IO":            func(r *executionRequest) { r.Harness.BorrowedTools[0].JSONStdio = nil },
		"argv ABI":              func(r *executionRequest) { r.Harness.BorrowedTools[0].JSONStdio.ABI = "celln.argv/v1" },
		"schema path":           func(r *executionRequest) { r.Harness.BorrowedTools[0].JSONStdio.InputSchema = "/etc/secret" },
		"mutable output schema": func(r *executionRequest) { r.Harness.BorrowedTools[0].JSONStdio.OutputSchema = "latest" },
		"zero input":            func(r *executionRequest) { r.Harness.BorrowedTools[0].JSONStdio.InputBytes = 0 },
		"excess output":         func(r *executionRequest) { r.Harness.BorrowedTools[0].JSONStdio.OutputBytes = 65537 },
		"excess deadline":       func(r *executionRequest) { r.Harness.BorrowedTools[0].JSONStdio.TimeoutMs = 30001 },
		"broker as tool":        func(r *executionRequest) { r.Harness.BorrowedTools[0].Path = "/pilot-fetch" },
	} {
		t.Run(name, func(t *testing.T) {
			req := jsonWire(t)
			mutate(&req)
			if validateCellnRequest(req) == nil {
				t.Fatal("invalid contract accepted")
			}
		})
	}
}
