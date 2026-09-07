package cellnreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

type IssuerReconciliation struct {
	APIVersion string `json:"apiVersion"`
	Profile    string `json:"profile"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
}

// ReconcileIssued performs one current-approval observation against a durable
// record. Caller configuration supplies the trusted APIReader/source locations;
// a saved report cannot select its own authority sources. API failure shrinks
// authority by withdrawing, never by trusting stale approval. This is neither
// a lease nor an autonomous watcher, and never dispatches/reissues anything.
func ReconcileIssued(ctx context.Context, root, profile, digest string, loader cellnauthority.ModelLoader) (*IssuerReconciliation, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("absolute operator root required")
	}
	if err := validProfileIdentity(profile, digest); err != nil {
		return nil, err
	}
	unlock, err := lockIssuer(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := recoverPending(root); err != nil {
		return nil, err
	}
	return reconcileIssued(ctx, root, profile, digest, loader)
}

// Caller holds the issuer lock and has completed recovery.
func reconcileIssued(ctx context.Context, root, profile, digest string, loader cellnauthority.ModelLoader) (*IssuerReconciliation, error) {
	record, err := readIssuerRecord(filepath.Join(root, "sympozium-issuer-journal", profile+".json"))
	if err != nil {
		return nil, err
	}
	if record.ProfileSHA256 != digest {
		return nil, fmt.Errorf("issuer journal revision mismatch")
	}
	result := &IssuerReconciliation{APIVersion: "sympozium.ai/celln-issuer-reconciliation-v1", Profile: profile, State: record.State, Reason: "already-withdrawn"}
	if record.State == "withdrawn" {
		return result, nil
	}
	if record.State != "issued" {
		return nil, fmt.Errorf("issuer recovery did not reach a terminal state")
	}
	reason := ""
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := loader.Revalidate(checkCtx, record.Frozen, record.Candidate.Approval); err != nil {
		reason = "approval-changed-or-unavailable"
	}
	path := filepath.Join(root, "trusted-model-profiles", profile+".json")
	data, readErr := readLimit(path, 65536)
	if os.IsNotExist(readErr) {
		reason = "profile-absent"
	} else if readErr != nil {
		return nil, readErr
	} else {
		h := sha256.Sum256(data)
		if "sha256:"+hex.EncodeToString(h[:]) != digest {
			return nil, fmt.Errorf("profile revision changed; refusing unrelated deletion")
		}
		// Rotation of token contents at the same independently selected path is
		// allowed. Retargeting/removing the selected host mapping is not.
		var hostProfile struct {
			CredentialFile string `json:"credentialFile"`
		}
		if err := json.Unmarshal(data, &hostProfile); err != nil {
			return nil, fmt.Errorf("invalid host profile")
		}
		credential, _, err := readCredentialMapping(root, record.Candidate.Approval.Policy.CredentialProfile)
		if err != nil || credential != hostProfile.CredentialFile {
			reason = "host-credential-mapping-changed-or-unavailable"
		}
	}
	if reason == "" {
		result.Reason = "approval-observed-current"
		return result, nil
	}
	record.State = "withdrawing"
	if err := writeIssuerRecord(root, record); err != nil {
		return nil, err
	}
	if err := withdrawProfile(root, *record.Issued); err != nil {
		return nil, err
	}
	record.State = "withdrawn"
	if err := writeIssuerRecord(root, record); err != nil {
		return nil, err
	}
	result.State, result.Reason = "withdrawn", reason
	return result, nil
}
