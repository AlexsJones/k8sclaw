package controller

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Credentials belong to controller deployment configuration, never AgentRun
// task text. Read the mounted file for every call so rotation takes effect.
func cellnRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	base, err := url.Parse(cellnRouterURL())
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("Celln: invalid router URL")
	}
	loopback := base.Hostname() == "localhost"
	if ip := net.ParseIP(base.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && (loopback || os.Getenv("CELLN_ALLOW_INSECURE_HTTP") == "true")) {
		return nil, fmt.Errorf("Celln: router requires HTTPS; plaintext non-loopback requires CELLN_ALLOW_INSECURE_HTTP=true")
	}
	filename := os.Getenv("CELLN_TOKEN_FILE")
	if filename == "" {
		return nil, fmt.Errorf("Celln: CELLN_TOKEN_FILE must name an operator-provisioned credential")
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("Celln: cannot read dispatcher credential")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	token := strings.TrimSpace(string(data))
	if err != nil || len(data) > 4096 || len(token) < 24 || strings.ContainsAny(token, "\r\n\t ") {
		return nil, fmt.Errorf("Celln: invalid dispatcher credential")
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base.String(), "/")+path, body)
	if err != nil {
		return nil, fmt.Errorf("Celln: cannot build dispatcher request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// Never follow redirects with a credential or reinterpret a redirected POST as
// GET. A moved service needs an explicit operator configuration update.
var cellnHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}
