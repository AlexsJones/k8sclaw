package apiserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const cellnCapabilityVersion = "celln.dev/capabilities-v1alpha1"

// Discovery credentials have no execution authority. Never fall back to the
// controller token or public health/TCP if authenticated discovery fails.
func cellnCapabilityStatus() CapabilityStatus {
	refuse := func(reason string) CapabilityStatus { return CapabilityStatus{Reason: reason} }
	if os.Getenv("CELLN_ENABLED") != "true" {
		return refuse("Celln is disabled")
	}
	origin := os.Getenv("CELLN_ROUTER_URL")
	if origin == "" {
		origin = defaultCellnRouterURL
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return refuse("Celln capability URL is invalid")
	}
	if u.Scheme == "http" && os.Getenv("CELLN_ALLOW_INSECURE_HTTP") != "true" {
		return refuse("Celln requires HTTPS or explicit plaintext acknowledgement")
	}
	file, err := os.Open(os.Getenv("CELLN_CAPABILITY_TOKEN_FILE"))
	if err != nil {
		return refuse("Celln read-only credential unavailable")
	}
	bytes, err := io.ReadAll(io.LimitReader(file, 4097))
	file.Close()
	token := strings.TrimSpace(string(bytes))
	if err != nil || len(bytes) > 4096 || len(token) < 24 {
		return refuse("Celln read-only credential invalid")
	}
	for _, b := range []byte(token) {
		if b < 33 || b > 126 {
			return refuse("Celln read-only credential invalid")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/capabilities"
	u.RawPath = ""
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return refuse("Celln capability URL is invalid")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return refuse("Celln authenticated capability probe failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return refuse("Celln authenticated capability probe refused or unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil || len(body) > 2*1024*1024 {
		return refuse("Celln capability response invalid or oversized")
	}
	var report cellnRouterCapabilities
	if json.Unmarshal(body, &report) != nil || report.APIVersion != cellnCapabilityVersion || !report.PreflightOnly || report.ArtifactReadiness != "not_checked" || len(report.Nodes) > 32 {
		return refuse("Celln capability contract is incompatible")
	}
	eligible := 0
	for _, node := range report.Nodes {
		if !node.PreflightEligible {
			continue
		}
		if node.Report == nil || !node.Report.eligible() {
			return refuse("Celln capability eligibility is inconsistent")
		}
		eligible++
	}
	if eligible != report.EligibleNodes {
		return refuse("Celln capability eligibility is inconsistent")
	}
	if eligible == 0 {
		return refuse("Celln has no authenticated compatible node with available KVM capacity")
	}
	return CapabilityStatus{Available: true, Reason: "Node preflight eligible only; selected Harness, approved tools, model grants and warm artifacts still require validation"}
}

type cellnRouterCapabilities struct {
	APIVersion        string `json:"apiVersion"`
	PreflightOnly     bool   `json:"preflightOnly"`
	ArtifactReadiness string `json:"artifactReadiness"`
	EligibleNodes     int    `json:"eligibleNodes"`
	Nodes             []struct {
		PreflightEligible bool                         `json:"preflightEligible"`
		Report            *cellnDispatcherCapabilities `json:"report"`
	} `json:"nodes"`
}

type cellnDispatcherCapabilities struct {
	APIVersion        string   `json:"apiVersion"`
	PreflightOnly     bool     `json:"preflightOnly"`
	ArtifactReadiness string   `json:"artifactReadiness"`
	RequestVersions   []string `json:"requestVersions"`
	Node              struct {
		KVM    bool    `json:"kvm"`
		CPU    bool    `json:"cpu_virtualization"`
		Kernel bool    `json:"guest_kernel"`
		Motes  bool    `json:"mote_store"`
		Tools  bool    `json:"tool_store"`
		Live   *uint32 `json:"live_cells"`
		Max    *uint32 `json:"max_cells"`
		Memory *uint64 `json:"memory_bytes"`
	} `json:"node"`
}

func (r *cellnDispatcherCapabilities) eligible() bool {
	supported := false
	for _, version := range r.RequestVersions {
		supported = supported || version == "celln.dev/v1alpha1"
	}
	n := r.Node
	return r.APIVersion == cellnCapabilityVersion && r.PreflightOnly && r.ArtifactReadiness == "not_checked" && supported && n.KVM && n.CPU && n.Kernel && n.Motes && n.Tools && n.Live != nil && n.Max != nil && n.Memory != nil && *n.Live < *n.Max && *n.Memory > 0
}
