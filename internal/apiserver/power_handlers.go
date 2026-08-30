package apiserver

import (
	"net/http"

	"github.com/sympozium-ai/sympozium/internal/collector"
)

// powerResponse is the response for GET /api/v1/power.
//
// Available is false when no energy collector is discoverable — distinguishing
// "no collector installed on this cluster" from "collector present, reporting
// zero accelerators". The UI omits the power surface entirely in the former
// case rather than rendering a misleading 0 W.
type powerResponse struct {
	Available bool                    `json:"available"`
	Endpoint  string                  `json:"endpoint,omitempty"`
	Snapshot  *collector.Snapshot     `json:"snapshot,omitempty"`
	Devices   []collector.DevicePower `json:"devices"`
}

// listPower returns per-accelerator power draw from the discovered energy
// collector.
//
// This endpoint fails open in every degraded case: no collector, an
// unreachable collector, or a malformed response all yield
// {"available":false} with HTTP 200. Power telemetry is decoration on the
// node views — its absence must never surface as a broken page, and a
// fabricated 0 W would be worse than nothing.
//
// GET /api/v1/power
func (s *Server) listPower(w http.ResponseWriter, r *http.Request) {
	if s.powerClient == nil {
		writeJSON(w, powerResponse{Available: false, Devices: []collector.DevicePower{}})
		return
	}
	snap, ep := s.powerClient.Fetch(r.Context())
	if snap == nil {
		writeJSON(w, powerResponse{Available: false, Devices: []collector.DevicePower{}})
		return
	}
	resp := powerResponse{
		Available: true,
		Snapshot:  snap,
		Devices:   snap.Devices,
	}
	if ep != nil {
		resp.Endpoint = ep.String()
	}
	writeJSON(w, resp)
}
