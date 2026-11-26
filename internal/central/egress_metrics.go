package central

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/egress"
)

type ProxyEgressStatus struct {
	SampledAt time.Time     `json:"sampled_at"`
	Nodes     []ProxyEgress `json:"nodes"`
}

type ProxyEgress struct {
	Protocol string `json:"protocol"`
	Active   int    `json:"active_connections"`
	egress.Rate
}

// ConfigureProxyEgressMetrics accepts the read-only collector's private socket.
func (s *Service) ConfigureProxyEgressMetrics(socket string) {
	if !filepath.IsAbs(socket) {
		return
	}
	s.proxyEgress = &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// DownloadProxyEgressStatus is optional and only used by the admin dashboard.
func (s *Service) DownloadProxyEgressStatus(ctx context.Context) *ProxyEgressStatus {
	if s.proxyEgress == nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://metrics/snapshot", nil)
	if err != nil {
		return nil
	}
	resp, err := s.proxyEgress.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var snapshot ProxyEgressStatus
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&snapshot) != nil {
		return nil
	}
	age := time.Since(snapshot.SampledAt)
	if age < -5*time.Second || age > 15*time.Second || len(snapshot.Nodes) == 0 || len(snapshot.Nodes) > 3 {
		return nil
	}
	seen := make(map[string]bool, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		switch node.Protocol {
		case "vless", "hysteria2", "other":
		default:
			return nil
		}
		if seen[node.Protocol] || node.Active < 0 ||
			(node.DownloadMbps != nil && *node.DownloadMbps < 0) || (node.SmoothedMbps != nil && *node.SmoothedMbps < 0) {
			return nil
		}
		seen[node.Protocol] = true
	}
	return &snapshot
}
