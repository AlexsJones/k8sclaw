// Package collector consumes accelerator power telemetry from an out-of-tree
// energy collector discovered at runtime. Detection is runtime-only — never
// compile-time — so sympozium keeps working unchanged on clusters without one
// (the internal/dra rule, restated).
//
// The integration is deliberately vendor-neutral: discovery keys on the role
// label sympozium.ai/collector=energy, and the wire types below carry no
// implementation-specific vocabulary. Any component serving this shape works;
// no implementation is privileged by sympozium, and no implementation name
// appears in this package. The reference implementation's service name and
// path are Helm values, not code.
//
// Collector responses are untrusted input: a collector is an independent
// component, but it is still a network peer. Bodies are bounded, values are
// range-checked, and any failure degrades to "no data" rather than a zero —
// a fabricated zero is indistinguishable from an idle accelerator and would
// silently corrupt anything built on top.
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// RoleLabel is the discovery key. A Service carrying RoleLabel=RoleEnergy
	// in an allowlisted namespace is treated as an energy collector.
	RoleLabel  = "sympozium.ai/collector"
	RoleEnergy = "energy"

	// PortAnnotation and PathAnnotation let a collector override the defaults
	// on its own Service without sympozium knowing anything about it.
	PortAnnotation = "sympozium.ai/collector-port"
	PathAnnotation = "sympozium.ai/collector-path"

	// DefaultPath is the fleet snapshot path.
	DefaultPath = "/api/v1/fleet"

	// maxBodyBytes bounds a collector response. A fleet snapshot is one small
	// object per accelerator; anything larger is a bug or a hostile peer.
	maxBodyBytes = 4 << 20

	// MaxDevicePowerMilliwatts rejects implausible readings (10 kW — an order
	// of magnitude above any single accelerator). Mirrors the pricing
	// package's MaxRatePerMTokMicro bound: cheap, and it makes downstream
	// arithmetic provably sane.
	MaxDevicePowerMilliwatts = 10_000_000

	// recheckInterval is how often a negative discovery result is re-probed.
	// The collector may be installed after sympozium.
	recheckInterval = 30 * time.Second
)

// Snapshot is a point-in-time view of accelerator power across the fleet.
type Snapshot struct {
	// ScrapedAt is when the collector last refreshed its own view.
	ScrapedAt time.Time `json:"scrapedAt"`

	// AgentsTotal/AgentsUp report the collector's own node coverage. Up <
	// Total means some nodes are unrepresented — the UI says so rather than
	// implying the fleet total is complete.
	AgentsTotal int `json:"agentsTotal"`
	AgentsUp    int `json:"agentsUp"`

	// TotalMilliwatts is the sum over non-stale, non-suspended devices.
	TotalMilliwatts int64 `json:"totalMilliwatts"`

	// StaleDevices counts devices whose readings are last-known, not current.
	StaleDevices int `json:"staleDevices"`

	// Devices is one entry per accelerator, sorted by node then address.
	Devices []DevicePower `json:"devices"`
}

// DevicePower is one accelerator's power draw. Node+Address is the identity
// tuple; everything else is descriptive.
type DevicePower struct {
	// Node and Address (PCI, full-domain form "0000:c3:00.0") jointly
	// identify the device. This is the join key against DRA inventory.
	Node    string `json:"node"`
	Address string `json:"address"`

	// Kind is an open set (gpu, npu, ...). Do not assume it is closed:
	// new device classes appear without a protocol change.
	Kind   string `json:"kind"`
	Driver string `json:"driver,omitempty"`

	// VendorID/DeviceID are bare lowercase hex, no 0x prefix.
	VendorID string `json:"vendorId,omitempty"`
	DeviceID string `json:"deviceId,omitempty"`

	// PowerMilliwatts is the board/socket-scope draw. Meaningful only when
	// Suspended is false and Measured is true.
	PowerMilliwatts int64 `json:"powerMilliwatts"`

	// Components decomposes power where the silicon exposes it (e.g. gfx vs
	// socket on an APU). Absent keys are unmeasured, NOT zero.
	Components map[string]int64 `json:"components,omitempty"`

	// Suspended reports a runtime-PM-suspended device. Its power reads 0, but
	// that is a synthetic zero standing in for "asleep", not a measurement —
	// waking the device to measure it would defeat the purpose.
	Suspended bool `json:"suspended"`

	// Stale reports a last-known value the collector could not refresh.
	// Stale devices stay listed so a gap is visible rather than silent.
	Stale bool `json:"stale"`

	// Measured distinguishes a real 0 W reading from a device with no
	// readable power source at all (some iGPUs expose none). Without this,
	// "unmeasurable" and "idle" are the same JSON.
	Measured bool `json:"measured"`
}

// wire mirrors the collector's on-the-wire shape. It is decoded then
// normalised into Snapshot; keeping it separate means the public type is
// ours (integer milliwatts, explicit Measured) rather than the peer's.
type wireSnapshot struct {
	ScrapedAt    time.Time    `json:"scrapedAt"`
	AgentsTotal  int          `json:"agentsTotal"`
	AgentsUp     int          `json:"agentsUp"`
	TotalWatts   float64      `json:"totalWatts"`
	StaleDevices int          `json:"staleDevices"`
	Devices      []wireDevice `json:"devices"` // may be null
}

type wireDevice struct {
	Node       string             `json:"node"`
	Kind       string             `json:"kind"`
	PCI        string             `json:"pci"`
	VendorID   string             `json:"vendorId"`
	DeviceID   string             `json:"deviceId"`
	Driver     string             `json:"driver"`
	PowerWatts float64            `json:"powerWatts"`
	Components map[string]float64 `json:"components,omitempty"`
	Suspended  bool               `json:"suspended"`
	Stale      bool               `json:"stale"`
}

// wattsToMilliwatts converts and clamps. Negative or absurd values are
// rejected (ok=false) rather than clamped to a plausible-looking number.
func wattsToMilliwatts(w float64) (int64, bool) {
	if w < 0 || w != w { // negative or NaN
		return 0, false
	}
	mw := int64(w*1000 + 0.5)
	if mw > MaxDevicePowerMilliwatts {
		return 0, false
	}
	return mw, true
}

// Endpoint is a discovered collector.
type Endpoint struct {
	Namespace string
	Service   string
	Port      int32
	Path      string
}

func (e Endpoint) url() string {
	return fmt.Sprintf("http://%s.%s:%d%s", e.Service, e.Namespace, e.Port, e.Path)
}

// String renders the endpoint for logs and the capabilities payload.
func (e Endpoint) String() string {
	return fmt.Sprintf("%s/%s:%d%s", e.Namespace, e.Service, e.Port, e.Path)
}

// Client discovers an energy collector and fetches fleet snapshots from it.
// The zero value is not usable; call New.
type Client struct {
	kube       kubernetes.Interface
	namespaces []string
	http       *http.Client

	mu        sync.Mutex
	endpoint  *Endpoint
	checkedAt time.Time
	cached    *Snapshot
	cachedAt  time.Time
	cacheTTL  time.Duration
	disabled  bool
}

// New builds a Client that discovers collectors in the given namespaces.
//
// The namespace allowlist is a security control, not tidiness. Discovery keys
// on a label, and labels are cheap: cluster-wide discovery would let any actor
// able to create a Service stand up a fake collector and feed sympozium
// whatever numbers it liked. Restricting to namespaces an admin already
// controls is what makes the data trustworthy. Do not widen this to all
// namespaces for convenience.
func New(kube kubernetes.Interface, namespaces []string, cacheTTL time.Duration) *Client {
	if cacheTTL <= 0 {
		cacheTTL = 2 * time.Second
	}
	return &Client{
		kube:       kube,
		namespaces: namespaces,
		http:       &http.Client{Timeout: 5 * time.Second},
		cacheTTL:   cacheTTL,
		disabled:   len(namespaces) == 0,
	}
}

// Discover returns the collector endpoint, or nil when none is present.
// A negative result is re-probed on an interval; a positive one is cached
// until it fails.
func (c *Client) Discover(ctx context.Context) *Endpoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disabled {
		return nil
	}
	if c.endpoint != nil {
		return c.endpoint
	}
	if time.Since(c.checkedAt) < recheckInterval {
		return nil
	}
	c.checkedAt = time.Now()
	c.endpoint = c.probe(ctx)
	return c.endpoint
}

func (c *Client) probe(ctx context.Context) *Endpoint {
	sel := metav1.ListOptions{LabelSelector: RoleLabel + "=" + RoleEnergy}
	for _, ns := range c.namespaces {
		list, err := c.kube.CoreV1().Services(ns).List(ctx, sel)
		if err != nil || list == nil {
			continue
		}
		for i := range list.Items {
			if ep := endpointFor(&list.Items[i]); ep != nil {
				return ep
			}
		}
	}
	return nil
}

// endpointFor derives an endpoint from a labelled Service. A Service with no
// usable port is skipped rather than guessed at.
func endpointFor(svc *corev1.Service) *Endpoint {
	ep := &Endpoint{
		Namespace: svc.Namespace,
		Service:   svc.Name,
		Path:      DefaultPath,
	}
	if p := svc.Annotations[PathAnnotation]; p != "" {
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		ep.Path = p
	}
	if p := svc.Annotations[PortAnnotation]; p != "" {
		var n int32
		if _, err := fmt.Sscanf(p, "%d", &n); err == nil && n > 0 {
			ep.Port = n
		}
	}
	if ep.Port == 0 {
		for _, sp := range svc.Spec.Ports {
			if sp.Port > 0 {
				ep.Port = sp.Port
				break
			}
		}
	}
	if ep.Port == 0 {
		return nil
	}
	return ep
}

// Fetch returns the current fleet snapshot, or nil when no collector is
// present or the collector could not be read. Callers must fail open: absence
// of power data is never an error worth surfacing as a failure.
func (c *Client) Fetch(ctx context.Context) (*Snapshot, *Endpoint) {
	ep := c.Discover(ctx)
	if ep == nil {
		return nil, nil
	}

	c.mu.Lock()
	if c.cached != nil && time.Since(c.cachedAt) < c.cacheTTL {
		snap := c.cached
		c.mu.Unlock()
		return snap, ep
	}
	c.mu.Unlock()

	snap, err := c.fetchOnce(ctx, *ep)
	if err != nil {
		// Drop the endpoint so the next call re-discovers: the collector may
		// have moved or been uninstalled.
		c.mu.Lock()
		c.endpoint = nil
		c.checkedAt = time.Now()
		c.mu.Unlock()
		return nil, ep
	}

	c.mu.Lock()
	c.cached, c.cachedAt = snap, time.Now()
	c.mu.Unlock()
	return snap, ep
}

func (c *Client) fetchOnce(ctx context.Context, ep Endpoint) (*Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.url(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("collector status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	var w wireSnapshot
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	return normalise(w), nil
}

// normalise converts the wire shape into our own, dropping anything that
// fails validation. Total is recomputed locally rather than trusted: it must
// agree with the devices we actually accepted.
func normalise(w wireSnapshot) *Snapshot {
	s := &Snapshot{
		ScrapedAt:    w.ScrapedAt,
		AgentsTotal:  w.AgentsTotal,
		AgentsUp:     w.AgentsUp,
		StaleDevices: w.StaleDevices,
		Devices:      make([]DevicePower, 0, len(w.Devices)), // wire may send null
	}
	for _, d := range w.Devices {
		if d.Node == "" || d.PCI == "" {
			continue // no identity, nothing to join against
		}
		mw, ok := wattsToMilliwatts(d.PowerWatts)
		dev := DevicePower{
			Node:            d.Node,
			Address:         d.PCI,
			Kind:            d.Kind,
			Driver:          d.Driver,
			VendorID:        d.VendorID,
			DeviceID:        d.DeviceID,
			PowerMilliwatts: mw,
			Suspended:       d.Suspended,
			Stale:           d.Stale,
			// A suspended device's 0 W is synthetic; a rejected value is not a
			// measurement either. Both are "not measured".
			Measured: ok && !d.Suspended,
		}
		for name, cw := range d.Components {
			cmw, cok := wattsToMilliwatts(cw)
			if !cok {
				continue // absent, never zero
			}
			if dev.Components == nil {
				dev.Components = make(map[string]int64, len(d.Components))
			}
			dev.Components[name] = cmw
		}
		if dev.Measured && !dev.Stale {
			s.TotalMilliwatts += dev.PowerMilliwatts
		}
		s.Devices = append(s.Devices, dev)
	}
	sort.Slice(s.Devices, func(i, j int) bool {
		if s.Devices[i].Node != s.Devices[j].Node {
			return s.Devices[i].Node < s.Devices[j].Node
		}
		return s.Devices[i].Address < s.Devices[j].Address
	})
	return s
}
