package cellnreview

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

// ErrIssuerOutcomeUnknown requires retaining the exact frozen identity. It is
// not permission to renew approval, create another execution or dispatch twice.
var ErrIssuerOutcomeUnknown = errors.New("issuer outcome unknown; preserve frozen identity")

type IssuerClientOptions struct{ URL, TokenFile, CAFile string }

type IssuerClient struct {
	endpoint  string
	tokenFile string
	http      *http.Client
}

func NewIssuerClient(o IssuerClientOptions) (*IssuerClient, error) {
	u, err := url.Parse(o.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || !filepath.IsAbs(o.TokenFile) {
		return nil, fmt.Errorf("issuer requires an explicit HTTPS origin and absolute controller credential path")
	}
	var roots *x509.CertPool
	if o.CAFile != "" {
		if !filepath.IsAbs(o.CAFile) {
			return nil, fmt.Errorf("absolute issuer CA path required")
		}
		pem, err := readLimit(o.CAFile, 1<<20)
		if err != nil {
			return nil, fmt.Errorf("issuer CA unavailable")
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("invalid issuer CA bundle")
		}
	}
	transport := &http.Transport{
		// Do not send controller credentials through ambient proxy settings.
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 95 * time.Second,
		IdleConnTimeout: 30 * time.Second, MaxIdleConns: 2, MaxConnsPerHost: 2, DisableCompression: true,
	}
	client := &http.Client{Transport: transport, Timeout: 95 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &IssuerClient{endpoint: strings.TrimRight(u.String(), "/") + "/v1/issuances", tokenFile: o.TokenFile, http: client}, nil
}

func (c *IssuerClient) CloseIdleConnections() { c.http.CloseIdleConnections() }

// Issue independently constructs the expected request from current API approval,
// sends exactly one provisioning POST, validates its entire returned identity,
// and rechecks approval before returning. It never executes or retries a task.
func (c *IssuerClient) Issue(ctx context.Context, loader cellnauthority.ModelLoader, frozen cellnauthority.FrozenSelection, approval cellnauthority.ModelApproval, artifacts cellnauthority.ExecutionArtifacts) (*IssuedSelection, error) {
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	expected, err := loader.BuildExecution(ctx, frozen, approval, artifacts)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(IssuerRequest{APIVersion: "sympozium.ai/celln-issuer-request-v1", Frozen: frozen, Approval: approval, Artifacts: artifacts})
	if err != nil || len(body) > 1<<20 {
		return nil, fmt.Errorf("remote issuance request exceeds supported encoding bound")
	}
	token, err := issuerToken(c.tokenFile)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cannot construct issuer request")
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, ErrIssuerOutcomeUnknown
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("issuer refused provisioning (HTTP %d); do not change frozen identity", response.StatusCode)
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || response.Header.Get("Content-Encoding") != "" {
		return nil, fmt.Errorf("issuer response must be uncompressed JSON")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return nil, ErrIssuerOutcomeUnknown
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("issuer response exceeds 1 MiB")
	}
	var report struct {
		APIVersion        string           `json:"apiVersion"`
		Issued            *IssuedSelection `json:"issued"`
		Executed          *bool            `json:"executed"`
		ArtifactReadiness string           `json:"artifactReadiness"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&report) != nil || d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid issuer response")
	}
	if report.APIVersion != "sympozium.ai/celln-issuer-response-v1" || report.Executed == nil || *report.Executed || report.ArtifactReadiness != "not_checked" || report.Issued == nil {
		return nil, fmt.Errorf("invalid issuer response contract")
	}
	if err := validateRemoteIssuance(*expected, *report.Issued); err != nil {
		return nil, err
	}
	if err := loader.Revalidate(ctx, frozen, approval); err != nil {
		return nil, fmt.Errorf("approval changed after remote provisioning: %w", err)
	}
	return report.Issued, nil
}

func validateRemoteIssuance(expected cellnauthority.ExecutionCandidate, issued IssuedSelection) error {
	if issued.APIVersion != "sympozium.ai/celln-issued-selection-v1" || issued.Candidate.APIVersion != expected.APIVersion || !reflect.DeepEqual(issued.Candidate.Approval, expected.Approval) || !artifactHash(issued.Grant) {
		return fmt.Errorf("issuer substituted candidate or grant identity")
	}
	if err := validProfileIdentity(issued.Profile, issued.ProfileSHA256); err != nil {
		return err
	}
	placeholder := "blake3:" + strings.Repeat("0", 64)
	want, err := comparableIssuedRequest(expected.Request, placeholder)
	if err != nil {
		return err
	}
	observed, err := comparableIssuedRequest(issued.Candidate.Request, placeholder)
	if err != nil || !bytes.Equal(want, observed) {
		return fmt.Errorf("issuer changed the frozen candidate request")
	}
	actual, err := comparableIssuedRequest(issued.Request, issued.Grant)
	if err != nil || !bytes.Equal(want, actual) {
		return fmt.Errorf("issuer changed the reviewed execution request")
	}
	return nil
}

// These are the three explicit serde defaults emitted by the current Rust
// declared-request contract. Do not discard arbitrary unknown fields: comparison
// rejects every difference except the grant self-reference and these defaults.
func comparableIssuedRequest(raw []byte, grant string) ([]byte, error) {
	if len(raw) > 65536 {
		return nil, fmt.Errorf("execution request exceeds 64 KiB")
	}
	var value map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF || value == nil {
		return nil, fmt.Errorf("invalid execution request")
	}
	harness, ok := value["harness"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing Harness")
	}
	ref, ok := harness["modelGrant"].(map[string]any)
	if !ok || len(ref) != 1 || ref["hash"] != grant {
		return nil, fmt.Errorf("issued model grant mismatch")
	}
	ref["hash"] = "<bound-self-reference>"
	if _, exists := value["forge"]; !exists {
		value["forge"] = nil
	}
	if _, exists := value["inputs"]; !exists {
		value["inputs"] = []any{}
	}
	invocation, ok := value["invocation"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing invocation")
	}
	if _, exists := invocation["args"]; !exists {
		invocation["args"] = []any{}
	}
	return json.Marshal(value)
}
