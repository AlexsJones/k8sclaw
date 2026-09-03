//go:build ignore

// sync-harness-defaults regenerates the reuse-values fallback dict in
// charts/sympozium/templates/harness-examples.yaml from the harnessExamples
// section of charts/sympozium/values.yaml, which is the only file an operator
// edits to change an example adapter's name, image, or default enablement.
//
// The fallback exists because a Helm release upgraded with --reuse-values can
// carry an older, partially-stale values object that is missing this block
// (or one of its adapter keys) entirely; the template still needs a literal
// default to merge over. Keeping that literal in sync by hand is exactly the
// kind of drift that caused a live runtime to be hand-patched outside Helm
// (see git history around the hermes-session-v0-20-6 AgentRuntime), so it is
// generated instead.
//
// Run via `make helm-sync`. `make helm-sync-check` (part of CI) fails the
// build if this file's generated block does not match what's committed.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

const (
	valuesPath   = "charts/sympozium/values.yaml"
	templatePath = "charts/sympozium/templates/harness-examples.yaml"
	beginMarker  = "{{/* sync-harness-defaults:begin */}}\n"
	endMarker    = "{{/* sync-harness-defaults:end */}}\n"
)

type adapter struct {
	Enabled bool   `json:"enabled"`
	Name    string `json:"name"`
	Image   string `json:"image"`
}

type harnessExamples struct {
	Enabled bool `json:"enabled"`
	Policy  struct {
		Name string `json:"name"`
	} `json:"policy"`
	Pi            adapter `json:"pi"`
	PiSession     adapter `json:"piSession"`
	Hermes        adapter `json:"hermes"`
	HermesSession adapter `json:"hermesSession"`
}

type values struct {
	HarnessExamples harnessExamples `json:"harnessExamples"`
}

func main() {
	raw, err := os.ReadFile(valuesPath)
	if err != nil {
		fatalf("read %s: %v", valuesPath, err)
	}
	var v values
	if err := yaml.Unmarshal(raw, &v); err != nil {
		fatalf("parse %s: %v", valuesPath, err)
	}

	block := renderBlock(v.HarnessExamples)

	template, err := os.ReadFile(templatePath)
	if err != nil {
		fatalf("read %s: %v", templatePath, err)
	}
	updated, err := replaceBetweenMarkers(string(template), block)
	if err != nil {
		fatalf("%s: %v", templatePath, err)
	}
	if err := os.WriteFile(templatePath, []byte(updated), 0o644); err != nil {
		fatalf("write %s: %v", templatePath, err)
	}
}

func renderBlock(h harnessExamples) string {
	var b strings.Builder
	fmt.Fprintln(&b, `{{- $defaults := dict`)
	fmt.Fprintf(&b, "  %q %t\n", "enabled", h.Enabled)
	fmt.Fprintf(&b, "  %q (dict %q %q)\n", "policy", "name", h.Policy.Name)
	renderAdapter(&b, "pi", h.Pi)
	renderAdapter(&b, "piSession", h.PiSession)
	renderAdapter(&b, "hermes", h.Hermes)
	renderAdapter(&b, "hermesSession", h.HermesSession, true)
	fmt.Fprintln(&b, `}}`)
	return b.String()
}

func renderAdapter(b *strings.Builder, key string, a adapter, last ...bool) {
	fmt.Fprintf(b, "  %q (dict\n", key)
	fmt.Fprintf(b, "    %q %t\n", "enabled", a.Enabled)
	fmt.Fprintf(b, "    %q %q\n", "name", a.Name)
	close := ")"
	fmt.Fprintf(b, "    %q %q%s\n", "image", a.Image, close)
}

func replaceBetweenMarkers(content, block string) (string, error) {
	beginIdx := strings.Index(content, beginMarker)
	if beginIdx == -1 {
		return "", fmt.Errorf("missing %q marker", strings.TrimSpace(beginMarker))
	}
	bodyStart := beginIdx + len(beginMarker)
	endIdx := strings.Index(content[bodyStart:], endMarker)
	if endIdx == -1 {
		return "", fmt.Errorf("missing %q marker", strings.TrimSpace(endMarker))
	}
	endIdx += bodyStart

	var out bytes.Buffer
	out.WriteString(content[:bodyStart])
	out.WriteString(block)
	out.WriteString(content[endIdx:])
	return out.String(), nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sync-harness-defaults: "+format+"\n", args...)
	os.Exit(1)
}
