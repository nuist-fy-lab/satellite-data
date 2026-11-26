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
	Paths     []ProxyWAN    `json:"paths,omitempty"`
}

type ProxyEgress struct {
	Protocol string `json:"protocol"`
	Active   int    `json:"active_connections"`
	egress.Rate
}

type ProxyWAN struct {
	ProxyEgress
	WAN     string `json:"wan"`
	Healthy bool   `json:"healthy"`
	Slots   int    `json:"slots"`
}

func (p ProxyEgress) valid() bool {
	return p.Active >= 0 && (p.DownloadMbps == nil || *p.DownloadMbps >= 0) &&
		(p.SmoothedMbps == nil || *p.SmoothedMbps >= 0)
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
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&snapshot) != nil {
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
		if seen[node.Protocol] || !node.valid() {
			return nil
		}
		seen[node.Protocol] = true
	}
	seenPaths := make(map[string]bool)
	totalSlots := 0
	for _, path := range snapshot.Paths {
		key := path.WAN + "/" + path.Protocol
		if len(path.WAN) != 5 || path.WAN[:4] != "vwan" || path.WAN[4] < '1' || path.WAN[4] > '9' ||
			(path.Protocol != "vless" && path.Protocol != "hysteria2") || !path.valid() || seenPaths[key] ||
			path.Slots < 0 || path.Slots > 6 {
			snapshot.Paths = nil
			break
		}
		totalSlots += path.Slots
		seenPaths[key] = true
	}
	if totalSlots > 6 {
		snapshot.Paths = nil
	}
	return &snapshot
}
