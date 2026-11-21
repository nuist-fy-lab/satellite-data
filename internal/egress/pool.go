// Package egress manages satellite download routes over local SOCKS5 tunnels.
// The public-IP identity, rather than the SOCKS listener, owns each quota.
package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Route struct {
	ID       string `json:"id"`
	Proxy    string `json:"proxy"`
	PublicIP string `json:"public_ip"`
}

type Config struct {
	Routes      []Route        `json:"routes"`
	PerIPLimits map[string]int `json:"per_ip_limits"`
}

// Safe source-specific defaults. Operators may lower the per-IP ceiling in
// the shared config without changing the downloader's worker scheduling.
var DefaultPerIPLimits = map[string]int{
	"goes": 8, "himawari8": 4, "himawari9": 4,
	"modis": 4, "viirs": 4, "mtg": 2, "iasi": 2,
	"iasi-ng": 2, "earthcare": 2, "calipso": 3,
	"cloudsat": 2, "era5": 2,
}

func Load(filename string) (*Config, error) {
	if strings.TrimSpace(filename) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read satellite download egress: %w", err)
	}
	if len(data) > 65536 {
		return nil, errors.New("satellite download egress configuration exceeds 64 KiB")
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse satellite download egress: %w", err)
	}
	if len(cfg.Routes) == 0 || len(cfg.Routes) > 20 {
		return nil, errors.New("satellite download egress needs 1 to 20 routes")
	}
	ids, proxies := make(map[string]bool), make(map[string]bool)
	for _, r := range cfg.Routes {
		ip := net.ParseIP(strings.TrimSpace(r.PublicIP))
		if r.ID == "" || ids[r.ID] || ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			return nil, errors.New("satellite download egress needs unique route IDs and valid public IPs")
		}
		u, err := url.Parse(r.Proxy)
		if err != nil || u.Scheme != "socks5" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || proxies[r.Proxy] {
			return nil, errors.New("satellite download egress needs unique local SOCKS5 endpoints without credentials")
		}
		addr := net.ParseIP(u.Hostname())
		port, portErr := strconv.ParseUint(u.Port(), 10, 16)
		if addr == nil || !addr.IsLoopback() || portErr != nil || port == 0 {
			return nil, errors.New("satellite download SOCKS5 endpoints must bind to loopback")
		}
		ids[r.ID], proxies[r.Proxy] = true, true
	}
	for source, limit := range cfg.PerIPLimits {
		if _, known := DefaultPerIPLimits[source]; !known || limit < 1 || limit > 128 {
			return nil, errors.New("satellite per-IP download limit must be between 1 and 128")
		}
	}
	return &cfg, nil
}

func (c *Config) UniquePublicIPs() int {
	if c == nil {
		return 1
	}
	seen := make(map[string]bool)
	for _, r := range c.Routes {
		seen[r.PublicIP] = true
	}
	return len(seen)
}

func (c *Config) Limit(source string) int {
	base := DefaultPerIPLimits[source]
	if base < 1 {
		return 1
	}
	if c != nil && c.PerIPLimits[source] > 0 && c.PerIPLimits[source] < base {
		return c.PerIPLimits[source]
	}
	return base
}

type Pool struct {
	mu        sync.Mutex
	cfg       *Config
	next      map[string]int
	active    map[string]map[string]int
	blocked   map[string]time.Time
	transfers map[string]*routeTransfers
}

type routeTransfers struct {
	active    int
	bytes     uint64
	lastAt    time.Time
	lastBytes uint64
	rate      *float64
	smoothed  float64
}

func NewPool(cfg *Config) *Pool {
	if cfg == nil {
		return nil
	}
	transfers := make(map[string]*routeTransfers, len(cfg.Routes))
	for _, route := range cfg.Routes {
		transfers[route.ID] = &routeTransfers{}
	}
	return &Pool{
		cfg: cfg, next: make(map[string]int), active: make(map[string]map[string]int),
		blocked: make(map[string]time.Time), transfers: transfers,
	}
}

// Acquire keeps one file transfer pinned to one public IP. Two tunnels with
// the same NAT address share a single budget, even when their ports differ.
// A failing route cools down without rerouting a partially streamed response.
func (p *Pool) Acquire(ctx context.Context, source string) (Route, func(), error) {
	if p == nil {
		return Route{}, func() {}, nil
	}
	limit := p.cfg.Limit(source)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		if p.active[source] == nil {
			p.active[source] = make(map[string]int)
		}
		for offset := 0; offset < len(p.cfg.Routes); offset++ {
			idx := (p.next[source] + offset) % len(p.cfg.Routes)
			route := p.cfg.Routes[idx]
			if time.Now().Before(p.blocked[route.ID]) || p.active[source][route.PublicIP] >= limit {
				continue
			}
			p.active[source][route.PublicIP]++
			p.transfers[route.ID].active++
			p.next[source] = (idx + 1) % len(p.cfg.Routes)
			p.mu.Unlock()
			return route, func() {
				p.mu.Lock()
				p.active[source][route.PublicIP]--
				p.transfers[route.ID].active--
				p.mu.Unlock()
			}, nil
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return Route{}, nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *Pool) MarkNetworkFailure(routeID string) {
	if p == nil || routeID == "" {
		return
	}
	p.mu.Lock()
	p.blocked[routeID] = time.Now().Add(30 * time.Second)
	p.mu.Unlock()
}

// RecordBytes counts downloaded file data for the specific route, even when
// multiple routes share a public IP and its concurrency budget.
func (p *Pool) RecordBytes(routeID string, n int) {
	if p == nil || n <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if route := p.transfers[routeID]; route != nil {
		route.bytes += uint64(n)
	}
}

type Status struct {
	ID              string         `json:"id"`
	PublicIP        string         `json:"public_ip"`
	Blocked         bool           `json:"blocked"`
	ActiveBySrc     map[string]int `json:"active_by_source"`
	ActiveTransfers int            `json:"active_transfers"`
	DownloadMbps    *float64       `json:"download_mbps,omitempty"`
	SmoothedMbps    *float64       `json:"smoothed_mbps,omitempty"`
}

func (p *Pool) Snapshot() []Status {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	out := make([]Status, 0, len(p.cfg.Routes))
	for _, r := range p.cfg.Routes {
		route := p.transfers[r.ID]
		entry := Status{
			ID: r.ID, PublicIP: r.PublicIP, Blocked: now.Before(p.blocked[r.ID]),
			ActiveBySrc: make(map[string]int), ActiveTransfers: route.active,
		}
		interval := now.Sub(route.lastAt)
		switch {
		case route.lastAt.IsZero() || interval > 2*time.Minute:
			route.lastAt, route.lastBytes, route.rate = now, route.bytes, nil
		case interval >= 4*time.Second:
			rate := float64(route.bytes-route.lastBytes) * 8 / interval.Seconds() / 1e6
			if route.rate == nil {
				route.smoothed = rate
			} else {
				route.smoothed += (1 - math.Exp(-interval.Seconds()/30)) * (rate - route.smoothed)
			}
			route.lastAt, route.lastBytes, route.rate = now, route.bytes, &rate
		}
		if route.rate != nil {
			smoothed := route.smoothed
			entry.DownloadMbps, entry.SmoothedMbps = route.rate, &smoothed
		}
		for src, counts := range p.active {
			entry.ActiveBySrc[src] = counts[r.PublicIP]
		}
		out = append(out, entry)
	}
	return out
}
