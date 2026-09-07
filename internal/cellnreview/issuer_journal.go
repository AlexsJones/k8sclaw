package cellnreview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

// A pending record is durable before a profile can authorize anything. The
// atomic transition to issued records the full outcome before returning it.
// Recovery never runs a task or reissues a grant: it only removes incomplete
// profile authority and retains the record for reconciliation.
type IssuerRecord struct {
	APIVersion    string                            `json:"apiVersion"`
	State         string                            `json:"state"`
	Profile       string                            `json:"profile"`
	ProfileSHA256 string                            `json:"profileSHA256"`
	Frozen        cellnauthority.FrozenSelection    `json:"frozen"`
	Candidate     cellnauthority.ExecutionCandidate `json:"candidate"`
	Issued        *IssuedSelection                  `json:"issued,omitempty"`
}

func writeIssuerRecord(root string, record IssuerRecord) error {
	if err := validRecord(record); err != nil {
		return err
	}
	dir := filepath.Join(root, "sympozium-issuer-journal")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Persist the newly created directory entry before any profile is created.
	if err := syncDirectory(root); err != nil {
		return err
	}
	path := filepath.Join(dir, record.Profile+".json")
	if previous, err := readIssuerRecord(path); err == nil {
		if previous.ProfileSHA256 != record.ProfileSHA256 {
			return fmt.Errorf("issuer journal identity collision")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > 1048576 {
		return fmt.Errorf("issuer record exceeds 1 MiB")
	}
	f, err := os.CreateTemp(dir, ".record-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func validRecord(r IssuerRecord) error {
	if r.APIVersion != "sympozium.ai/celln-issuer-record-v1" || (r.State != "pending" && r.State != "issued" && r.State != "withdrawing" && r.State != "withdrawn") {
		return fmt.Errorf("invalid issuer journal record")
	}
	if err := validProfileIdentity(r.Profile, r.ProfileSHA256); err != nil {
		return err
	}
	if r.State == "issued" && (r.Issued == nil || r.Issued.Profile != r.Profile || r.Issued.ProfileSHA256 != r.ProfileSHA256) {
		return fmt.Errorf("incomplete committed issuer record")
	}
	return nil
}

func readIssuerRecord(path string) (IssuerRecord, error) {
	var r IssuerRecord
	raw, err := readLimit(path, 1048576)
	if err != nil {
		return r, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return r, fmt.Errorf("invalid issuer journal JSON")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return r, fmt.Errorf("trailing issuer journal JSON")
	}
	if err := validRecord(r); err != nil {
		return r, err
	}
	if filepath.Base(path) != r.Profile+".json" {
		return r, fmt.Errorf("issuer journal path mismatch")
	}
	return r, nil
}

// RecoverPending is a local fail-closed recovery step, not a live policy watcher.
// It works without Kubernetes. Committed records are preserved for subsequent
// approval revalidation; their existence is not current execution permission.
func RecoverPending(root string) ([]string, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("absolute operator root required")
	}
	unlock, err := lockIssuer(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return recoverPending(root)
}

func recoverPending(root string) ([]string, error) {
	dir := filepath.Join(root, "sympozium-issuer-journal")
	f, err := os.Open(dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(1025)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 1024 {
		return nil, fmt.Errorf("issuer journal exceeds recovery bound; operator reconciliation required")
	}
	recovered := []string{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".record-") {
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return recovered, fmt.Errorf("unexpected issuer journal entry")
		}
		r, err := readIssuerRecord(filepath.Join(dir, entry.Name()))
		if err != nil {
			return recovered, err
		}
		if r.State != "pending" && r.State != "withdrawing" {
			continue
		}
		if err := withdrawProfile(root, IssuedSelection{Profile: r.Profile, ProfileSHA256: r.ProfileSHA256}); err != nil {
			return recovered, err
		}
		r.State = "withdrawn"
		if err := writeIssuerRecord(root, r); err != nil {
			return recovered, err
		}
		recovered = append(recovered, r.Profile)
	}
	return recovered, nil
}

// ReadIssuance returns durable history, never authorization to dispatch/replay.
func ReadIssuance(root, profile, digest string) (IssuerRecord, error) {
	if !filepath.IsAbs(root) {
		return IssuerRecord{}, fmt.Errorf("absolute operator root required")
	}
	if err := validProfileIdentity(profile, digest); err != nil {
		return IssuerRecord{}, err
	}
	unlock, err := lockIssuer(root)
	if err != nil {
		return IssuerRecord{}, err
	}
	defer unlock()
	r, err := readIssuerRecord(filepath.Join(root, "sympozium-issuer-journal", profile+".json"))
	if err != nil {
		return r, err
	}
	if r.ProfileSHA256 != digest {
		return IssuerRecord{}, fmt.Errorf("issuer journal revision mismatch")
	}
	return r, nil
}
