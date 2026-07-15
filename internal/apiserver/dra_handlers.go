package apiserver

import (
	"net/http"
	"sort"

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// llmfitDRADriver is the DRA driver whose ResourceSlices we surface. The
// integration is deliberately string-only (no llmfit-dra code imported):
// slices are upstream resource.k8s.io objects, and a cluster without the
// driver simply yields an empty inventory.
const llmfitDRADriver = "llmfit.ai"

// draNodesResponse is the response for GET /api/v1/dra/nodes.
type draNodesResponse struct {
	// Available is false when the resource.k8s.io API (or permission to
	// read it) is absent — distinguishing "no DRA on this cluster" from
	// "DRA present, zero devices".
	Available bool             `json:"available"`
	Nodes     []draNodeSummary `json:"nodes"`
	Total     int              `json:"total"`
}

type draNodeSummary struct {
	NodeName string      `json:"nodeName"`
	Devices  []draDevice `json:"devices"`
}

// draDevice is one published device, flattened from slice attributes for
// UI consumption (topology node decoration).
type draDevice struct {
	Name               string `json:"name"`
	Kind               string `json:"kind"` // gpu | npu | cpu | nic
	Model              string `json:"model,omitempty"`
	Vendor             string `json:"vendor,omitempty"`
	MemoryGi           int64  `json:"memoryGi,omitempty"`
	MemoryBandwidthGBs int64  `json:"memoryBandwidthGBs,omitempty"`
	ComputeTFLOPS      int64  `json:"computeTFLOPS,omitempty"`
	Healthy            bool   `json:"healthy"`
	HealthReason       string `json:"healthReason,omitempty"`
	// PCIAddress (full-domain form "0000:c3:00.0") is the join key against
	// energy-collector power readings, which identify a device by node+PCI.
	PCIAddress string `json:"pciAddress,omitempty"`
	// NIC-only link facts (fabric endpoints).
	LinkLayer string `json:"linkLayer,omitempty"`
	RateGbps  int64  `json:"rateGbps,omitempty"`
}

// listDRANodes returns the per-node accelerator/NIC inventory published by
// llmfit-dra ResourceSlices.
// GET /api/v1/dra/nodes
func (s *Server) listDRANodes(w http.ResponseWriter, r *http.Request) {
	slices, err := s.kube.ResourceV1().ResourceSlices().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		// A cluster without the resource.k8s.io API group (404) or without
		// RBAC for it (403) is a normal deployment, not a server error:
		// report the inventory as unavailable and let the UI omit it.
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			writeJSON(w, draNodesResponse{Available: false, Nodes: []draNodeSummary{}})
			return
		}
		http.Error(w, "listing resourceslices: "+err.Error(), http.StatusInternalServerError)
		return
	}

	byNode := map[string][]draDevice{}
	for i := range slices.Items {
		sl := &slices.Items[i]
		if sl.Spec.Driver != llmfitDRADriver || sl.Spec.NodeName == nil || *sl.Spec.NodeName == "" {
			continue
		}
		for _, dev := range sl.Spec.Devices {
			byNode[*sl.Spec.NodeName] = append(byNode[*sl.Spec.NodeName], flattenDevice(dev))
		}
	}

	resp := draNodesResponse{Available: true, Nodes: []draNodeSummary{}}
	for node, devs := range byNode {
		sort.Slice(devs, func(i, j int) bool { return devs[i].Name < devs[j].Name })
		resp.Nodes = append(resp.Nodes, draNodeSummary{NodeName: node, Devices: devs})
	}
	sort.Slice(resp.Nodes, func(i, j int) bool { return resp.Nodes[i].NodeName < resp.Nodes[j].NodeName })
	resp.Total = len(resp.Nodes)
	writeJSON(w, resp)
}

// flattenDevice lifts the attributes the UI cares about out of the slice's
// attribute map. Attribute names are unqualified (scoped to the llmfit.ai
// driver by DRA convention); absent attributes stay zero-valued.
func flattenDevice(dev resourceapi.Device) draDevice {
	out := draDevice{Name: dev.Name}
	if v := strAttr(dev, "kind"); v != nil {
		out.Kind = *v
	}
	if v := strAttr(dev, "model"); v != nil {
		out.Model = *v
	}
	if v := strAttr(dev, "vendor"); v != nil {
		out.Vendor = *v
	}
	if v := strAttr(dev, "linkLayer"); v != nil {
		out.LinkLayer = *v
	}
	if v := strAttr(dev, "pciAddress"); v != nil {
		out.PCIAddress = *v
	}
	if v := strAttr(dev, "healthReason"); v != nil {
		out.HealthReason = *v
	}
	if v := boolAttr(dev, "healthy"); v != nil {
		out.Healthy = *v
	}
	if v := intAttr(dev, "memoryBandwidthGBs"); v != nil {
		out.MemoryBandwidthGBs = *v
	}
	if v := intAttr(dev, "computeTFLOPS"); v != nil {
		out.ComputeTFLOPS = *v
	}
	if v := intAttr(dev, "rateGbps"); v != nil {
		out.RateGbps = *v
	}
	if cap, ok := dev.Capacity["memory"]; ok {
		out.MemoryGi = cap.Value.Value() / (1024 * 1024 * 1024)
	}
	return out
}

func strAttr(dev resourceapi.Device, name string) *string {
	if a, ok := dev.Attributes[resourceapi.QualifiedName(name)]; ok {
		return a.StringValue
	}
	return nil
}

func boolAttr(dev resourceapi.Device, name string) *bool {
	if a, ok := dev.Attributes[resourceapi.QualifiedName(name)]; ok {
		return a.BoolValue
	}
	return nil
}

func intAttr(dev resourceapi.Device, name string) *int64 {
	if a, ok := dev.Attributes[resourceapi.QualifiedName(name)]; ok {
		return a.IntValue
	}
	return nil
}
