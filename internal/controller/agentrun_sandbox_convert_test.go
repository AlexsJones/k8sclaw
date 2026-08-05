package controller

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// ── converter completeness guards ─────────────────────────────────────────────
//
// containerToMap and podSpecToMap are hand-written allowlists (see the rationale
// in agentrun_sandbox.go). A field the converter does not handle produces no
// output key and no error.
//
// These tests populate every field of the source struct by reflection, run the
// converter, and fail on any field that produced no output key. Omissions are
// declared in the tables below with a reason.
//
// Granularity: the guard walks top-level fields only. Nested structs
// (SecurityContext, EnvVarSource, …) are covered by the pod-spec diff in
// agentrun_backend_parity_test.go, which compares fully rendered pod specs.

// omittedContainerFields lists corev1.Container fields containerToMap does not
// convert. Key: Go field name. Value: reason.
//
// An entry for a field Sympozium sets means that field does not reach the Sandbox
// CR.
var omittedContainerFields = map[string]string{
	"Ports":                    "unused: task-mode pods expose no ports; server mode uses a Deployment, not a Sandbox CR",
	"LivenessProbe":            "unused: only server-mode containers define probes",
	"ReadinessProbe":           "unused: only server-mode containers define probes",
	"StartupProbe":             "unused: buildContainers defines no startup probes",
	"Lifecycle":                "unused: buildContainers sets no container lifecycle hooks (AgentRun lifecycle hooks are separate containers)",
	"TerminationMessagePath":   "unused: kubelet default is correct; the agent reports results over /ipc",
	"TerminationMessagePolicy": "unused: kubelet default is correct",
	"Stdin":                    "unused: agent pods are not interactive",
	"StdinOnce":                "unused: agent pods are not interactive",
	"TTY":                      "unused: agent pods are not interactive",
	"VolumeDevices":            "unused: no raw block devices are mounted into agent pods",
	"ResizePolicy":             "unused: agent pods are short-lived; in-place resize is not used",
	"RestartPolicyRules":       "unused: buildContainers sets no per-exit-code restart rules",
}

// omittedPodSpecFields lists corev1.PodSpec fields podSpecToMap does not convert.
// buildAgentPodTemplate is the only producer of this PodSpec, so an entry here
// means buildAgentPodTemplate does not set the field.
var omittedPodSpecFields = map[string]string{
	"EphemeralContainers":       "unused: debug containers are attached ad hoc by operators, never rendered by the controller",
	"Affinity":                  "unused: placement is expressed via nodeSelector",
	"SchedulerName":             "unused: default scheduler",
	"SchedulingGates":           "unused: no gated scheduling",
	"ResourceClaims":            "unused: DRA claims are on the Model serving path, not agent pods",
	"Resources":                 "unused: pod-level resources are alpha; agent pods size per container",
	"Subdomain":                 "unused: agent pods are not addressable by DNS name",
	"Hostname":                  "unused: agent pods are not addressable by DNS name",
	"SetHostnameAsFQDN":         "unused: agent pods are not addressable by DNS name",
	"HostAliases":               "unused: no /etc/hosts overrides",
	"DNSConfig":                 "unused: dnsPolicy alone covers host-network runs",
	"ReadinessGates":            "unused: no custom readiness conditions",
	"Overhead":                  "unused: set by the RuntimeClass admission controller, not by us",
	"PreemptionPolicy":          "unused: default preemption",
	"Priority":                  "unused: priority is expressed via priorityClassName",
	"TopologySpreadConstraints": "unused: single-pod workload, nothing to spread",
	"EnableServiceLinks":        "unused: default is fine; the agent addresses services by URL",
	"ShareProcessNamespace":     "unused: sidecars do not need to see the agent's processes",
	"DeprecatedServiceAccount":  "deprecated alias of ServiceAccountName, which is converted",
	"HostUsers":                 "unused: no user-namespace remapping",
	"OS":                        "unused: linux-only",
	"HostnameOverride":          "unused: agent pods are not addressable by DNS name",
	"WorkloadRef":               "unused: agent pods are owned by the AgentRun, not an external workload object",
}

func TestContainerToMap_CoversEveryField(t *testing.T) {
	var c corev1.Container
	fillStruct(t, reflect.ValueOf(&c).Elem(), 0)

	m := containerToMap(c)
	assertConverterCoversFields(t, "containerToMap", reflect.TypeOf(c), m, omittedContainerFields)
}

func TestPodSpecToMap_CoversEveryField(t *testing.T) {
	var spec corev1.PodSpec
	fillStruct(t, reflect.ValueOf(&spec).Elem(), 0)

	m := podSpecToMap(spec)
	assertConverterCoversFields(t, "podSpecToMap", reflect.TypeOf(spec), m, omittedPodSpecFields)
}

// TestVolumeToMap_IsLossless pins that volumeToMap round-trips rather than
// filtering. buildVolumes passes agentRun.Spec.Volumes through verbatim, so the
// source may be any member of the VolumeSource union, including types added by
// later Kubernetes releases.
func TestVolumeToMap_IsLossless(t *testing.T) {
	// A user-supplied CSI volume — the case buildVolumes calls out.
	readOnly := true
	v := corev1.Volume{
		Name: "vault-secrets",
		VolumeSource: corev1.VolumeSource{
			CSI: &corev1.CSIVolumeSource{
				Driver:   "secrets-store.csi.k8s.io",
				ReadOnly: &readOnly,
				VolumeAttributes: map[string]string{
					"secretProviderClass": "vault-database",
				},
			},
		},
	}

	m := volumeToMap(v)

	if got := m["name"]; got != "vault-secrets" {
		t.Errorf("name = %v, want vault-secrets", got)
	}
	csi, ok := m["csi"].(map[string]interface{})
	if !ok {
		t.Fatalf("volumeToMap dropped the csi source; got keys %v", mapKeys(m))
	}
	if got := csi["driver"]; got != "secrets-store.csi.k8s.io" {
		t.Errorf("csi.driver = %v, want secrets-store.csi.k8s.io", got)
	}
	attrs, ok := csi["volumeAttributes"].(map[string]interface{})
	if !ok {
		t.Fatalf("csi.volumeAttributes missing; got %v", csi)
	}
	if got := attrs["secretProviderClass"]; got != "vault-database" {
		t.Errorf("csi.volumeAttributes.secretProviderClass = %v, want vault-database", got)
	}
}

// TestOmitTablesHaveReasons requires each omit-table entry to state a reason.
func TestOmitTablesHaveReasons(t *testing.T) {
	for name, table := range map[string]map[string]string{
		"omittedContainerFields": omittedContainerFields,
		"omittedPodSpecFields":   omittedPodSpecFields,
	} {
		for field, reason := range table {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s[%q] has an empty reason — say why the field is not converted", name, field)
			}
		}
	}
}

// TestOmitTablesHaveNoStaleEntries fails when an omit table names a field that no
// longer exists on the struct, or one the converter now emits.
func TestOmitTablesHaveNoStaleEntries(t *testing.T) {
	var c corev1.Container
	fillStruct(t, reflect.ValueOf(&c).Elem(), 0)
	assertOmitTableIsCurrent(t, "omittedContainerFields", reflect.TypeOf(c), containerToMap(c), omittedContainerFields)

	var spec corev1.PodSpec
	fillStruct(t, reflect.ValueOf(&spec).Elem(), 0)
	assertOmitTableIsCurrent(t, "omittedPodSpecFields", reflect.TypeOf(spec), podSpecToMap(spec), omittedPodSpecFields)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// assertConverterCoversFields fails for every field of typ whose JSON key is
// absent from out and which is not declared in omit.
func assertConverterCoversFields(
	t *testing.T,
	converter string,
	typ reflect.Type,
	out map[string]interface{},
	omit map[string]string,
) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		key := jsonKey(f)
		if key == "" || key == "-" {
			continue
		}
		if _, converted := out[key]; converted {
			continue
		}
		if reason, declared := omit[f.Name]; declared {
			t.Logf("%s omits %s (%q): %s", converter, f.Name, key, reason)
			continue
		}
		t.Errorf("%s does not convert %s (json:%q).\n"+
			"Convert it, or add it to the omit table with a reason.",
			converter, f.Name, key)
	}
}

// assertOmitTableIsCurrent fails on omit entries naming a field that no longer
// exists, or one the converter now emits.
func assertOmitTableIsCurrent(
	t *testing.T,
	table string,
	typ reflect.Type,
	out map[string]interface{},
	omit map[string]string,
) {
	t.Helper()
	for field := range omit {
		f, ok := typ.FieldByName(field)
		if !ok {
			t.Errorf("%s[%q]: no such field on %s — delete the entry", table, field, typ.Name())
			continue
		}
		if key := jsonKey(f); key != "" {
			if _, converted := out[key]; converted {
				t.Errorf("%s[%q]: the converter now emits %q — delete the entry", table, field, key)
			}
		}
	}
}

func jsonKey(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

func mapKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
