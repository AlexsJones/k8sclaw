package controller

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAPIServerChartExplicitlyDisablesUI(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required")
	}
	for _, enabled := range []string{"true", "false"} {
		out, err := exec.Command("helm", "template", "startup", "../../charts/sympozium", "--show-only", "templates/apiserver-deployment.yaml", "--set", "nats.enabled=false,apiserver.webUI.enabled="+enabled).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		text := string(out)
		if !strings.Contains(text, "- --event-bus-url=\n") {
			t.Fatal("disabled NATS must pass an explicitly empty URL")
		}
		if enabled == "false" && !strings.Contains(text, "- --serve-ui=false\n") {
			t.Fatal("binary defaults UI on; chart must explicitly disable it")
		}
		if enabled == "true" && (!strings.Contains(text, "- --serve-ui\n") || strings.Contains(text, "--serve-ui=false")) {
			t.Fatal("enabled UI flag missing")
		}
	}
}
