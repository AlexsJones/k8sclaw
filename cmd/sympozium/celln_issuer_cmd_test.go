package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIssuerServiceConfigRequiresExplicitBoundedAuthority(t *testing.T) {
	for _, mode := range []string{"valid", "unknown", "legacy", "too-long", "relative", "duplicate-agent", "duplicate-source", "missing-source", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			ref := func(name string) map[string]string { return map[string]string{"namespace": "operators", "name": name} }
			binding := map[string]any{"agent": ref("agent"), "operatorGrants": ref("operator"), "runtimeGrants": ref("runtime"), "agentGrants": ref("agent-grants"), "modelPolicy": ref("model")}
			config := map[string]any{"apiVersion": "sympozium.ai/celln-issuer-service-v1", "listen": "127.0.0.1:8788", "certificateFile": "/operator/cert", "privateKeyFile": "/operator/key", "tokenFile": "/operator/token", "cellnBinary": "/operator/celln", "policyRoot": "/operator/root", "composerPublisher": "unused-by-config-parse", "profileLifetimeMs": 300000, "sweepIntervalMs": 5000, "bindings": []any{binding}}
			switch mode {
			case "unknown":
				config["allowInsecure"] = true
			case "legacy":
				config["profileLifetimeMs"] = 0
			case "too-long":
				config["profileLifetimeMs"] = 300001
			case "relative":
				config["tokenFile"] = "token"
			case "duplicate-agent":
				config["bindings"] = []any{binding, binding}
			case "duplicate-source":
				binding["modelPolicy"] = ref("operator")
			case "missing-source":
				delete(binding, "runtimeGrants")
			}
			data, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "trailing" {
				data = append(data, []byte(" {}")...)
			}
			path := filepath.Join(t.TempDir(), "issuer.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = readIssuerServiceConfig(path)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("wrong configuration acceptance: %v", err)
			}
		})
	}
}
