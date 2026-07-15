package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// svc builds a Service carrying the energy-collector role label.
func svc(ns, name string, port int32, labelled bool, ann map[string]string) *corev1.Service {
	labels := map[string]string{}
	if labelled {
		labels[RoleLabel] = RoleEnergy
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, Annotations: ann},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: port}}},
	}
}

func TestDiscoverFindsLabelledService(t *testing.T) {
	kube := fake.NewSimpleClientset(svc("collector-ns", "c", 9744, true, nil))
	c := New(kube, []string{"collector-ns"}, time.Second)

	ep := c.Discover(context.Background())
	if ep == nil {
		t.Fatal("expected discovery to find the labelled service")
	}
	if ep.Port != 9744 || ep.Path != DefaultPath {
		t.Fatalf("got %+v, want port 9744 and default path", ep)
	}
}

// The namespace allowlist is the anti-forgery control: a collector outside it
// must be invisible no matter how it labels itself.
func TestDiscoverIgnoresServiceOutsideAllowlist(t *testing.T) {
	kube := fake.NewSimpleClientset(svc("tenant-ns", "evil", 9744, true, nil))
	c := New(kube, []string{"sympozium-system"}, time.Second)

	if ep := c.Discover(context.Background()); ep != nil {
		t.Fatalf("service outside the allowlist must not be discovered, got %+v", ep)
	}
}

func TestDiscoverIgnoresUnlabelledService(t *testing.T) {
	kube := fake.NewSimpleClientset(svc("collector-ns", "c", 9744, false, nil))
	c := New(kube, []string{"collector-ns"}, time.Second)

	if ep := c.Discover(context.Background()); ep != nil {
		t.Fatalf("unlabelled service must not be discovered, got %+v", ep)
	}
}

func TestDiscoverHonoursAnnotationOverrides(t *testing.T) {
	kube := fake.NewSimpleClientset(svc("collector-ns", "c", 9744, true, map[string]string{
		PortAnnotation: "9999",
		PathAnnotation: "custom/fleet", // no leading slash — must be normalised
	}))
	c := New(kube, []string{"collector-ns"}, time.Second)

	ep := c.Discover(context.Background())
	if ep == nil {
		t.Fatal("expected discovery")
	}
	if ep.Port != 9999 || ep.Path != "/custom/fleet" {
		t.Fatalf("got port=%d path=%q, want 9999 and /custom/fleet", ep.Port, ep.Path)
	}
}

func TestEmptyAllowlistDisablesDiscovery(t *testing.T) {
	kube := fake.NewSimpleClientset(svc("collector-ns", "c", 9744, true, nil))
	c := New(kube, nil, time.Second)

	if ep := c.Discover(context.Background()); ep != nil {
		t.Fatal("empty allowlist must disable discovery entirely")
	}
}

func TestFetchWithNoCollectorReturnsNil(t *testing.T) {
	c := New(fake.NewSimpleClientset(), []string{"collector-ns"}, time.Second)

	snap, _ := c.Fetch(context.Background())
	if snap != nil {
		t.Fatalf("absent collector must yield nil, got %+v", snap)
	}
}

func TestNormaliseRealFleetPayload(t *testing.T) {
	// A verbatim capture from a live collector: one measured GPU and one
	// runtime-suspended NPU.
	body := `{"scrapedAt":"2026-07-15T12:52:15.934148098Z","agentsTotal":1,"agentsUp":1,
	 "totalWatts":25.05,"staleDevices":0,"devices":[
	  {"node":"n1","kind":"gpu","pci":"0000:c3:00.0","vendorId":"1002","deviceId":"1586",
	   "driver":"amdgpu","powerWatts":25.05,
	   "components":{"cpu_cores":10.365,"gfx":8.245,"npu":0,"socket":25.502},
	   "suspended":false,"stale":false},
	  {"node":"n1","kind":"npu","pci":"0000:c4:00.1","vendorId":"1022","deviceId":"17f0",
	   "driver":"amdxdna","powerWatts":0,"suspended":true,"stale":false}]}`

	snap := fetchFrom(t, body)
	if len(snap.Devices) != 2 {
		t.Fatalf("got %d devices, want 2", len(snap.Devices))
	}

	gpu := snap.Devices[0]
	if gpu.PowerMilliwatts != 25050 {
		t.Errorf("gpu power = %d mW, want 25050", gpu.PowerMilliwatts)
	}
	if !gpu.Measured {
		t.Error("an awake gpu with a real reading must be Measured")
	}
	if gpu.Components["gfx"] != 8245 {
		t.Errorf("gfx component = %d mW, want 8245", gpu.Components["gfx"])
	}

	// The suspended NPU's 0 W is synthetic — it must not be reported as a
	// measurement, and must not contribute to the fleet total.
	npu := snap.Devices[1]
	if npu.Measured {
		t.Error("suspended device must not be reported as Measured")
	}
	if snap.TotalMilliwatts != 25050 {
		t.Errorf("total = %d mW, want 25050 (suspended device excluded)", snap.TotalMilliwatts)
	}
}

// The collector serializes an empty device list as null, not []. Decoding must
// yield an empty slice so callers (and the UI) never see a nil.
func TestNormaliseHandlesNullDevices(t *testing.T) {
	snap := fetchFrom(t, `{"scrapedAt":"2026-07-15T12:52:15Z","agentsTotal":0,"agentsUp":0,
	 "totalWatts":0,"staleDevices":0,"devices":null}`)

	if snap.Devices == nil {
		t.Fatal("null devices must normalise to an empty slice, not nil")
	}
	if len(snap.Devices) != 0 {
		t.Fatalf("got %d devices, want 0", len(snap.Devices))
	}
}

// A hostile or broken peer must not poison the total. Out-of-range values are
// dropped, not clamped into something plausible.
func TestNormaliseRejectsImplausibleValues(t *testing.T) {
	snap := fetchFrom(t, `{"scrapedAt":"2026-07-15T12:52:15Z","agentsTotal":1,"agentsUp":1,
	 "totalWatts":0,"staleDevices":0,"devices":[
	  {"node":"n1","kind":"gpu","pci":"0000:01:00.0","powerWatts":1e9,"suspended":false,"stale":false},
	  {"node":"n1","kind":"gpu","pci":"0000:02:00.0","powerWatts":-5,"suspended":false,"stale":false}]}`)

	for _, d := range snap.Devices {
		if d.Measured {
			t.Errorf("device %s: out-of-range reading must not be Measured", d.Address)
		}
	}
	if snap.TotalMilliwatts != 0 {
		t.Errorf("total = %d mW, want 0 — rejected values must not contribute", snap.TotalMilliwatts)
	}
}

// A device the collector could not refresh keeps its last-known value but is
// excluded from the total, so a gap is visible rather than silently summed.
func TestNormaliseExcludesStaleFromTotal(t *testing.T) {
	snap := fetchFrom(t, `{"scrapedAt":"2026-07-15T12:52:15Z","agentsTotal":1,"agentsUp":0,
	 "totalWatts":0,"staleDevices":1,"devices":[
	  {"node":"n1","kind":"gpu","pci":"0000:01:00.0","powerWatts":40,"suspended":false,"stale":true}]}`)

	if len(snap.Devices) != 1 {
		t.Fatal("stale devices must stay listed")
	}
	if snap.Devices[0].PowerMilliwatts != 40000 {
		t.Error("stale device must retain its last-known value")
	}
	if snap.TotalMilliwatts != 0 {
		t.Errorf("total = %d mW, want 0 — stale must not contribute", snap.TotalMilliwatts)
	}
}

func TestNormaliseDropsDevicesWithoutIdentity(t *testing.T) {
	snap := fetchFrom(t, `{"scrapedAt":"2026-07-15T12:52:15Z","agentsTotal":1,"agentsUp":1,
	 "totalWatts":10,"staleDevices":0,"devices":[
	  {"node":"","kind":"gpu","pci":"","powerWatts":10,"suspended":false,"stale":false}]}`)

	if len(snap.Devices) != 0 {
		t.Fatalf("device with no node+pci identity is unjoinable and must be dropped, got %d", len(snap.Devices))
	}
}

func TestFetchFailsOpenOnBadResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, ""},
		{"malformed json", http.StatusOK, "{not json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			if snap, _ := c.Fetch(context.Background()); snap != nil {
				t.Fatalf("bad response must fail open to nil, got %+v", snap)
			}
		})
	}
}

// newTestClient wires a Client to an httptest server via a fake Service, so
// discovery and fetch are exercised together.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	host, port := hostPort(t, srv)
	kube := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      host,
			Namespace: "collector-ns",
			Labels:    map[string]string{RoleLabel: RoleEnergy},
			Annotations: map[string]string{
				PortAnnotation: port,
				PathAnnotation: "/api/v1/fleet",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 1}}},
	})
	c := New(kube, []string{"collector-ns"}, time.Nanosecond)
	// Point the client's transport at the test server regardless of the
	// cluster-DNS name the endpoint resolves to.
	c.http = srv.Client()
	c.http.Transport = rewriteTransport{base: srv.URL}
	return c
}

func fetchFrom(t *testing.T, body string) *Snapshot {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	snap, _ := newTestClient(t, srv).Fetch(context.Background())
	if snap == nil {
		t.Fatal("expected a snapshot")
	}
	return snap
}

// rewriteTransport sends every request to the test server, standing in for
// cluster DNS.
type rewriteTransport struct{ base string }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	req := r.Clone(r.Context())
	u, err := url.Parse(rt.base + r.URL.Path)
	if err != nil {
		return nil, err
	}
	req.URL = u
	req.Host = u.Host
	return http.DefaultTransport.RoundTrip(req)
}

func hostPort(t *testing.T, srv *httptest.Server) (string, string) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname(), u.Port()
}
