package cellnreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

type profileExpiry struct {
	BootID    string `json:"bootId"`
	IssuedAt  uint64 `json:"issuedAtBoottimeMs"`
	ExpiresAt uint64 `json:"expiresAtBoottimeMs"`
}

type profileClock struct {
	APIVersion          string `json:"apiVersion"`
	BootID              string `json:"bootId"`
	Now                 uint64 `json:"boottimeMs"`
	MaxLifetime         uint64 `json:"maxLifetimeMs"`
	ExecutionAuthorized bool   `json:"executionAuthorized"`
}

var hostBootID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func readProfileClock(ctx context.Context, execute runner, binary string) (profileClock, error) {
	var clock profileClock
	if err := verify(ctx, execute, binary, []string{"harness-profile-clock"}, &clock); err != nil {
		return clock, err
	}
	if clock.APIVersion != "celln.dev/model-profile-clock-v1" || clock.MaxLifetime != 300000 || clock.ExecutionAuthorized || !hostBootID.MatchString(clock.BootID) {
		return clock, fmt.Errorf("invalid host profile clock")
	}
	return clock, nil
}

func (e profileExpiry) check(clock profileClock, lifetime time.Duration) error {
	if !hostBootID.MatchString(e.BootID) || e.BootID != clock.BootID || e.ExpiresAt <= e.IssuedAt || e.ExpiresAt-e.IssuedAt != uint64(lifetime/time.Millisecond) || e.IssuedAt > clock.Now || clock.Now >= e.ExpiresAt {
		return fmt.Errorf("bounded profile expired, changed or belongs to another host boot")
	}
	return nil
}

// Called only under the issuer lock. The immutable window survives even a crash
// before profile publication. A retry cannot mint another start time, including
// after expiry; independently changed approval produces a different candidate.
func boundedProfile(ctx context.Context, execute runner, o IssueOptions, candidate cellnauthority.ExecutionCandidate, base []byte) ([]byte, error) {
	if o.ProfileLifetime < time.Millisecond || o.ProfileLifetime > 5*time.Minute || o.ProfileLifetime%time.Millisecond != 0 {
		return nil, fmt.Errorf("profile lifetime must be whole milliseconds in 1ms..5m")
	}
	clock, err := readProfileClock(ctx, execute, o.Binary)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(struct {
		Candidate cellnauthority.ExecutionCandidate `json:"candidate"`
		Profile   json.RawMessage                   `json:"profile"`
	}{candidate, base})
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(raw)
	identity := hex.EncodeToString(hash[:])
	dir := filepath.Join(o.PolicyRoot, "sympozium-issuer-windows")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := syncDirectory(o.PolicyRoot); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, identity+".json")
	var window struct {
		APIVersion string        `json:"apiVersion"`
		Identity   string        `json:"identity"`
		Expiry     profileExpiry `json:"expiry"`
	}
	stored, err := readLimit(path, 4096)
	if os.IsNotExist(err) {
		lifetime := uint64(o.ProfileLifetime / time.Millisecond)
		if clock.Now > math.MaxUint64-lifetime {
			return nil, fmt.Errorf("host profile clock overflow")
		}
		window.APIVersion, window.Identity = "sympozium.ai/celln-issuer-window-v1", identity
		window.Expiry = profileExpiry{clock.BootID, clock.Now, clock.Now + lifetime}
		data, err := json.Marshal(window)
		if err != nil {
			return nil, err
		}
		if err := publishProfile(path, data); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		d := json.NewDecoder(bytes.NewReader(stored))
		d.DisallowUnknownFields()
		if d.Decode(&window) != nil || d.Decode(new(any)) != io.EOF || window.APIVersion != "sympozium.ai/celln-issuer-window-v1" || window.Identity != identity {
			return nil, fmt.Errorf("invalid durable issuer window")
		}
	}
	if err := window.Expiry.check(clock, o.ProfileLifetime); err != nil {
		return nil, err
	}
	var profile map[string]any
	if err := json.Unmarshal(base, &profile); err != nil {
		return nil, err
	}
	profile["apiVersion"], profile["expiry"] = "celln.dev/model-issuer-profile-v2", window.Expiry
	return json.Marshal(profile)
}

func checkBoundedProfile(ctx context.Context, execute runner, o IssueOptions, data []byte) error {
	var profile struct {
		Expiry profileExpiry `json:"expiry"`
	}
	if err := json.Unmarshal(data, &profile); err != nil {
		return err
	}
	clock, err := readProfileClock(ctx, execute, o.Binary)
	if err != nil {
		return err
	}
	return profile.Expiry.check(clock, o.ProfileLifetime)
}
