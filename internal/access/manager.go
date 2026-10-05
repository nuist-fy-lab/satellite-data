package access

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/auth"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/transfer"
)

var ErrNoRoute = errors.New("no usable central access route")
var ErrCentralUnavailable = errors.New("central access service unavailable")

type Route struct {
	AccountID             int64
	CredentialID, ProxyID *int64
	Authorizer            auth.Authorizer
	Transfer              *transfer.HTTP
}

type Manager struct {
	client    *http.Client
	mu        sync.Mutex
	providers map[string]bool
	checked   time.Time
}

func NewManager(socket string) (*Manager, error) {
	if socket == "" {
		return nil, errors.New("central socket is required")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &Manager{client: &http.Client{Transport: transport}}, nil
}

func (m *Manager) status(ctx context.Context, source string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Since(m.checked) < 5*time.Second {
		return m.providers[source], nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://central/v1/access/status", nil)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrCentralUnavailable, err)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrCentralUnavailable, err)
	}
	defer resp.Body.Close()
	var status struct {
		Providers map[string]bool `json:"providers"`
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%w: status %s", ErrCentralUnavailable, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return false, fmt.Errorf("%w: invalid status response: %v", ErrCentralUnavailable, err)
	}
	m.providers, m.checked = status.Providers, time.Now()
	return m.providers[source], nil
}

func (m *Manager) Resolve(ctx context.Context, source, _ string, timeout time.Duration) (Route, error) {
	configured, err := m.status(ctx, source)
	if err != nil {
		return Route{}, err
	}
	if !configured {
		return Route{}, ErrNoRoute
	}
	client := &http.Client{Transport: &centralTransport{manager: m, source: source}, Timeout: timeout}
	return Route{Transfer: transfer.NewHTTP(client)}, nil
}

func (m *Manager) HasUsableRoute(ctx context.Context, source, _ string, _ time.Duration) bool {
	configured, _ := m.status(ctx, source)
	return configured
}
func (m *Manager) Configured(ctx context.Context, source, _ string) bool {
	configured, _ := m.status(ctx, source)
	return configured
}
func (m *Manager) MarkSuccess(context.Context, Route)              {}
func (m *Manager) MarkAuthFailure(context.Context, Route, string)  { m.invalidate() }
func (m *Manager) MarkProxyFailure(context.Context, Route, string) { m.invalidate() }
func (m *Manager) invalidate()                                     { m.mu.Lock(); m.checked = time.Time{}; m.mu.Unlock() }

type centralTransport struct {
	manager *Manager
	source  string
}

func (t *centralTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return nil, errors.New("central transfer requires GET")
	}
	data, err := json.Marshal(struct {
		SourceID string `json:"source_id"`
		URL      string `json:"url"`
		Range    string `json:"range,omitempty"`
		IfRange  string `json:"if_range,omitempty"`
	}{t.source, req.URL.String(), req.Header.Get("Range"), req.Header.Get("If-Range")})
	if err != nil {
		return nil, err
	}
	forward, err := http.NewRequestWithContext(req.Context(), http.MethodPost, "http://central/v1/fetch", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	forward.Header.Set("Content-Type", "application/json")
	resp, err := t.manager.client.Do(forward)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch: %v", ErrCentralUnavailable, err)
	}
	resp.Request = req
	return resp, nil
}
