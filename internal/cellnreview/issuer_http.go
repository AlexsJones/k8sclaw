package cellnreview

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

type IssuerRequest struct {
	APIVersion string                            `json:"apiVersion"`
	Frozen     cellnauthority.FrozenSelection    `json:"frozen"`
	Approval   cellnauthority.ModelApproval      `json:"approval"`
	Artifacts  cellnauthority.ExecutionArtifacts `json:"artifacts"`
}

type IssuerResponse struct {
	APIVersion        string           `json:"apiVersion"`
	Issued            *IssuedSelection `json:"issued"`
	Executed          bool             `json:"executed"`
	ArtifactReadiness string           `json:"artifactReadiness"`
}

func issuerToken(path string) ([]byte, error) {
	raw, err := readLimit(path, 4096)
	if err != nil {
		return nil, fmt.Errorf("issuer credential unavailable")
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 24 {
		return nil, fmt.Errorf("invalid issuer credential")
	}
	for _, b := range []byte(token) {
		if b < 33 || b > 126 {
			return nil, fmt.Errorf("invalid issuer credential")
		}
	}
	return []byte(token), nil
}

// NewIssuerHandler is a controller-only host boundary, never a tenant endpoint.
// It accepts immutable selection observations, not host paths, approval-source
// configuration, credentials, executables to run or a readiness assertion.
func NewIssuerHandler(m *ManagedIssuer, tokenFile string) (http.Handler, error) {
	if m == nil || !filepath.IsAbs(tokenFile) {
		return nil, fmt.Errorf("managed issuer and absolute token file required")
	}
	if _, err := issuerToken(tokenFile); err != nil {
		return nil, err
	}
	slots := make(chan struct{}, 2)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Refuse plaintext even if accidentally mounted on an HTTP listener.
		if r.TLS == nil {
			http.Error(w, "TLS required", http.StatusUpgradeRequired)
			return
		}
		credentials := r.Header.Values("Authorization")
		if len(credentials) != 1 || !strings.HasPrefix(credentials[0], "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token, err := issuerToken(tokenFile)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		want, got := sha256.Sum256(token), sha256.Sum256([]byte(strings.TrimPrefix(credentials[0], "Bearer ")))
		if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "issuer busy", http.StatusTooManyRequests)
			return
		}
		if r.URL.RawQuery != "" {
			http.Error(w, "query parameters unsupported", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/v1/issuer/status" && r.Method == http.MethodGet {
			ready, _ := m.Status()
			w.Header().Set("Content-Type", "application/json")
			if !ready {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "sympozium.ai/celln-issuer-status-v1", "provisioningGateOpen": ready, "executionAuthorized": false, "artifactReadiness": "not_checked"})
			return
		}
		if r.URL.Path != "/v1/issuances" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method unsupported", http.StatusMethodNotAllowed)
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" || r.Header.Get("Content-Encoding") != "" {
			http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		defer r.Body.Close()
		var request IssuerRequest
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&request) != nil || d.Decode(new(any)) != io.EOF || request.APIVersion != "sympozium.ai/celln-issuer-request-v1" {
			http.Error(w, "invalid bounded issuance request", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		issued, err := m.Issue(ctx, request.Frozen, request.Approval, request.Artifacts)
		if err != nil {
			http.Error(w, "issuance refused", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// A lost response is an ambiguous delivery, not authority to renew or
		// dispatch. The durable outcome permits an identical bounded retry.
		_ = json.NewEncoder(w).Encode(IssuerResponse{APIVersion: "sympozium.ai/celln-issuer-response-v1", Issued: issued, ArtifactReadiness: "not_checked"})
	}), nil
}

// ServeIssuer owns the supplied listener and joins both lifecycle goroutines.
// TLS is mandatory; controller credentials rotate via the operator token file.
func ServeIssuer(ctx context.Context, listener net.Listener, m *ManagedIssuer, tokenFile, certificateFile, privateKeyFile string) error {
	defer listener.Close()
	handler, err := NewIssuerHandler(m, tokenFile)
	if err != nil {
		return err
	}
	certificate, err := tls.LoadX509KeyPair(certificateFile, privateKeyFile)
	if err != nil {
		return fmt.Errorf("issuer TLS identity unavailable: %w", err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192,
		TLSConfig:   &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}},
		BaseContext: func(net.Listener) context.Context { return serveCtx },
	}
	managedDone, serverDone := make(chan error, 1), make(chan error, 1)
	go func() { managedDone <- m.Start(serveCtx) }()
	go func() { serverDone <- server.ServeTLS(listener, "", "") }()
	var managedErr, serverErr error
	var managedStopped, serverStopped bool
	select {
	case <-ctx.Done():
	case managedErr = <-managedDone:
		managedStopped = true
	case serverErr = <-serverDone:
		serverStopped = true
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := server.Shutdown(shutdownCtx)
	stop()
	if shutdownErr != nil {
		_ = server.Close()
	}
	if !managedStopped {
		managedErr = <-managedDone
	}
	if !serverStopped {
		serverErr = <-serverDone
	}
	if errors.Is(serverErr, http.ErrServerClosed) {
		serverErr = nil
	}
	return errors.Join(managedErr, serverErr, shutdownErr)
}
