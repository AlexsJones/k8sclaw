package helmchart

import (
	"io"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestDefaultPostgresMCPServerStartsSuspended(t *testing.T) {
	ch, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	values, err := chartutil.ToRenderValues(ch, map[string]any{}, chartutil.ReleaseOptions{
		Name:      "sympozium",
		Namespace: "test-system",
		IsInstall: true,
	}, chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatalf("ToRenderValues() error = %v", err)
	}

	rendered, err := engine.Render(ch, values)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	manifest, ok := rendered["sympozium/templates/default-mcp-servers.yaml"]
	if !ok {
		t.Fatal("default MCP server manifest was not rendered")
	}

	decoder := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	for {
		var object struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Suspended bool `json:"suspended"`
			} `json:"spec"`
		}
		if err := decoder.Decode(&object); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode rendered manifest: %v", err)
		}
		if object.Kind == "MCPServer" && object.Metadata.Name == "postgres" {
			if !object.Spec.Suspended {
				t.Fatal("default postgres MCPServer must be suspended until DATABASE_URI is configured")
			}
			return
		}
	}

	t.Fatal("rendered chart does not contain the default postgres MCPServer")
}
