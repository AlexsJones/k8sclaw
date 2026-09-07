package controller

import (
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"regexp"
	"strings"
)

func cellnHarnessModelSupported(m sympoziumv1alpha1.ModelSpec) bool {
	return m.Provider == "deepseek" && m.AuthSecretRef == "" && (m.BaseURL == "" || m.BaseURL == "https://api.deepseek.com") && (m.Thinking == "" || m.Thinking == "off") && len(m.ProviderHeaders) == 0 && m.ProviderHeadersSecretRef == "" && m.ModelRef == "" && len(m.NodeSelector) == 0
}

var cellnFunctionName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var cellnMemberComponent = regexp.MustCompile(`^[a-zA-Z0-9_.+-]+$`)

func validCellnHarness(req executionRequest) bool {
	h := req.Harness
	if h == nil {
		return req.APIVersion == "celln.dev/v1alpha1"
	}
	if !validCellnHarnessContract(req.APIVersion, h) || !cellnHash.MatchString(h.ModelGrant.Hash) || len(h.Model) == 0 || len(h.Model) > 128 || len(h.Task) > 2048 || strings.TrimSpace(h.Task) == "" || strings.ContainsRune(h.Task, '\x00') || req.Mote == nil || req.Forge != nil || len(req.Inputs) != 0 || len(req.Tools) != 1 || req.Tools[0].Closure == nil || req.Invocation == nil || len(req.Invocation.Args) != 0 || req.Execution.Lane != "agent" || !req.Execution.RequireHardwareIsolation || req.Capabilities.Workspace != "none" || len(req.Capabilities.Egress) != 1 || req.Capabilities.Egress[0] != "https://api.deepseek.com" {
		return false
	}
	names, paths := map[string]bool{}, map[string]bool{}
	for _, tool := range h.BorrowedTools {
		if !cellnFunctionName.MatchString(tool.Name) || names[tool.Name] || paths[tool.Path] || !strings.HasPrefix(tool.Path, "/") || len(tool.Path) > 256 || !cellnHash.MatchString(tool.Hash) || len(tool.Description) == 0 || len(tool.Description) > 512 {
			return false
		}
		for _, component := range strings.Split(tool.Path[1:], "/") {
			if component == "." || component == ".." || !cellnMemberComponent.MatchString(component) {
				return false
			}
		}
		names[tool.Name], paths[tool.Path] = true, true
	}
	return true
}

func validCellnHarnessContract(version string, h *executionHarness) bool {
	switch {
	case version == "celln.dev/v1alpha2" && h.ContractVersion == "celln.reference-functions/v1":
		if h.JSON != nil || len(h.BorrowedTools) != 2 {
			return false
		}
		for _, tool := range h.BorrowedTools {
			if tool.JSONStdio != nil {
				return false
			}
		}
		return true
	case version == "celln.dev/v1alpha3" && h.ContractVersion == "celln.json-tools/v1":
		j := h.JSON
		if j == nil || j.MaxTurns < 1 || j.MaxTurns > 6 || j.MaxCalls < 0 || j.MaxCalls > 16 || len(j.System) > 2048 || strings.ContainsRune(j.System, '\x00') || h.BorrowedTools == nil || len(h.BorrowedTools) > 16 {
			return false
		}
		for _, tool := range h.BorrowedTools {
			io := tool.JSONStdio
			if tool.Path == "/pilot-fetch" || io == nil || io.ABI != "celln.json-stdio/v1" || !cellnHash.MatchString(io.InputSchema) || !cellnHash.MatchString(io.OutputSchema) || io.InputBytes < 1 || io.InputBytes > 65536 || io.OutputBytes < 1 || io.OutputBytes > 65536 || io.TimeoutMs < 1 || io.TimeoutMs > 30000 {
				return false
			}
		}
		return true
	default:
		return false
	}
}
