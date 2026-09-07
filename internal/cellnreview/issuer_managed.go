package cellnreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"k8s.io/apimachinery/pkg/types"
)

// ManagedIssuer is a host-local lifecycle component, not a readiness or dispatch
// service. Trusted deployment configuration supplies each Agent's authority
// sources and an uncached reader. Journal contents never select authority.
type ManagedIssuer struct {
	mu        sync.Mutex
	options   IssueOptions
	loaders   map[types.NamespacedName]cellnauthority.ModelLoader
	interval  time.Duration
	execute   runner
	started   bool
	ready     bool
	ctx       context.Context
	lastSweep time.Time
	lastError error
}

func NewManagedIssuer(options IssueOptions, loaders map[types.NamespacedName]cellnauthority.ModelLoader, interval time.Duration) (*ManagedIssuer, error) {
	if !filepath.IsAbs(options.PolicyRoot) || !filepath.IsAbs(options.Binary) || options.ProfileLifetime < time.Millisecond || options.ProfileLifetime > 5*time.Minute || options.ProfileLifetime%time.Millisecond != 0 || interval < time.Second || interval > 30*time.Second {
		return nil, fmt.Errorf("managed issuer requires absolute host paths, bounded profiles and a 1s..30s sweep interval")
	}
	if len(options.ComposerPublisher) != 64 {
		return nil, fmt.Errorf("exact composer publisher required")
	}
	if _, err := hex.DecodeString(options.ComposerPublisher); err != nil {
		return nil, err
	}
	if len(loaders) == 0 || len(loaders) > 1024 {
		return nil, fmt.Errorf("1..1024 configured Agent authority bindings required")
	}
	m := &ManagedIssuer{options: options, interval: interval, loaders: make(map[types.NamespacedName]cellnauthority.ModelLoader)}
	for key, loader := range loaders {
		if key.Namespace == "" || key.Name == "" || loader.Selection.Reader == nil {
			return nil, fmt.Errorf("invalid configured Agent authority binding")
		}
		m.loaders[key] = loader
	}
	m.execute = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		return runBudget(ctx, 45*time.Second, binary, args...)
	}
	return m, nil
}

// Start runs recovery before opening the local provisioning gate. Failed sweeps
// close it and retry on the next tick. Cancellation closes it permanently; host
// expiry remains enforced if this process dies and cannot perform cleanup.
func (m *ManagedIssuer) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return fmt.Errorf("managed issuer already started")
	}
	m.started, m.ctx = true, ctx
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.ready = false; m.mu.Unlock() }()
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		m.ready = false
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := m.sweep(checkCtx)
		cancel()
		m.lastError = err
		if err == nil && ctx.Err() == nil {
			m.ready, m.lastSweep = true, time.Now()
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Status describes the local provisioning gate only, not artifact or runtime
// readiness. Bound sweep freshness even if an embedding scheduler is stalled.
func (m *ManagedIssuer) Status() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.available(), m.lastError
}

func (m *ManagedIssuer) available() bool {
	return m.ready && m.ctx != nil && m.ctx.Err() == nil && time.Since(m.lastSweep) <= m.interval+30*time.Second
}

func (m *ManagedIssuer) Issue(ctx context.Context, frozen cellnauthority.FrozenSelection, approval cellnauthority.ModelApproval, artifacts cellnauthority.ExecutionArtifacts) (*IssuedSelection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available() {
		return nil, fmt.Errorf("managed issuer provisioning gate is closed")
	}
	key := types.NamespacedName{Namespace: frozen.Snapshot.Agent.Namespace, Name: frozen.Snapshot.Agent.Name}
	loader, ok := m.loaders[key]
	if !ok {
		return nil, fmt.Errorf("Agent has no configured issuer authority binding")
	}
	issueCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	return issue(issueCtx, loader, frozen, approval, artifacts, m.options, m.execute)
}

func boundedEntries(dir string) ([]os.DirEntry, error) {
	f, err := os.Open(dir)
	if os.IsNotExist(err) {
		return nil, nil
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
		return nil, fmt.Errorf("managed issuer directory exceeds 1024 entries")
	}
	return entries, nil
}

// Caller serializes managed operations. The OS lock also excludes operator CLI
// issuance while the complete sweep observes and shrinks local authority.
func (m *ManagedIssuer) sweep(ctx context.Context) error {
	root := m.options.PolicyRoot
	unlock, err := lockIssuer(root)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := recoverPending(root); err != nil {
		return err
	}
	entries, err := boundedEntries(filepath.Join(root, "sympozium-issuer-journal"))
	if err != nil {
		return err
	}
	clock, clockErr := readProfileClock(ctx, m.execute, m.options.Binary)
	errs := []error{clockErr}
	tracked := make(map[string]bool)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".record-") {
			continue
		}
		r, err := readIssuerRecord(filepath.Join(root, "sympozium-issuer-journal", entry.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if r.State != "issued" {
			continue
		}
		tracked[r.Profile+".json"] = true
		data, readErr := readLimit(filepath.Join(root, "trusted-model-profiles", r.Profile+".json"), 65536)
		if readErr != nil && !os.IsNotExist(readErr) {
			errs = append(errs, readErr)
			continue
		}
		if readErr == nil {
			h := sha256.Sum256(data)
			if "sha256:"+hex.EncodeToString(h[:]) != r.ProfileSHA256 {
				errs = append(errs, fmt.Errorf("tracked profile bytes changed"))
				continue
			}
		}
		var p struct {
			APIVersion string        `json:"apiVersion"`
			Expiry     profileExpiry `json:"expiry"`
		}
		decodeErr := json.Unmarshal(data, &p)
		lifetime := p.Expiry.ExpiresAt - p.Expiry.IssuedAt
		bounded := decodeErr == nil && p.APIVersion == "celln.dev/model-issuer-profile-v2" && lifetime > 0 && lifetime <= 300000
		key := types.NamespacedName{Namespace: r.Frozen.Snapshot.Agent.Namespace, Name: r.Frozen.Snapshot.Agent.Name}
		loader, configured := m.loaders[key]
		if !bounded || clockErr != nil || !configured || p.Expiry.check(clock, time.Duration(lifetime)*time.Millisecond) != nil {
			r.State = "withdrawing"
			if err := writeIssuerRecord(root, r); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := withdrawProfile(root, *r.Issued); err != nil {
				errs = append(errs, err)
				continue
			}
			r.State = "withdrawn"
			if err := writeIssuerRecord(root, r); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if _, err := reconcileIssued(ctx, root, r.Profile, r.ProfileSHA256, loader); err != nil {
			errs = append(errs, err)
		}
	}
	profiles, err := boundedEntries(filepath.Join(root, "trusted-model-profiles"))
	if err != nil {
		errs = append(errs, err)
	}
	for _, p := range profiles {
		if !tracked[p.Name()] {
			errs = append(errs, fmt.Errorf("untracked host profile requires operator reconciliation"))
		}
	}
	if ctx.Err() != nil {
		errs = append(errs, ctx.Err())
	}
	return errors.Join(errs...)
}
