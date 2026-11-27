// Package egress manages satellite download routes over local SOCKS5 tunnels.
// The public-IP identity, rather than the SOCKS listener, owns each quota.
package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Route struct {
	ID       string `json:"id"`
	Proxy    string `json:"proxy"`
	PublicIP string `json:"public_ip"`
	// Backup routes are probed using actual transfers and do not add worker slots.
	Backup  bool     `json:"backup,omitempty"`
	Sources []string `json:"sources,omitempty"`
}

type Config struct {
	Routes                   []Route        `json:"routes"`
	PerIPLimits              map[string]int `json:"per_ip_limits"`
	ExploreFastRouteCapacity bool           `json:"explore_fast_route_capacity,omitempty"`
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
		if !r.Backup && len(r.Sources) != 0 || r.Backup && len(r.Sources) == 0 {
			return nil, errors.New("satellite backup routes require an explicit list of sources")
		}
		for _, source := range r.Sources {
			if _, known := DefaultPerIPLimits[source]; !known {
				return nil, errors.New("unknown satellite backup route source")
			}
		}
		ids[r.ID], proxies[r.Proxy] = true, true
	}
	if cfg.UniquePublicIPs() == 0 {
		return nil, errors.New("satellite download egress requires at least one primary route")
	}
	for source, limit := range cfg.PerIPLimits {
		if _, known := DefaultPerIPLimits[source]; !known || limit < 1 || limit > 128 {
			return nil, errors.New("satellite per-IP download limit must be between 1 and 128")
		}
	}
	return &cfg, nil
}

// DirectProxy is the configured direct download path, if the site has one.
func (c *Config) DirectProxy() string {
	if c != nil {
		for _, route := range c.Routes {
			if route.ID == "direct" && !route.Backup {
				return route.Proxy
			}
		}
	}
	return ""
}

func (c *Config) UniquePublicIPs() int {
	if c == nil {
		return 1
	}
	seen := make(map[string]bool)
	for _, r := range c.Routes {
		if !r.Backup {
			seen[r.PublicIP] = true
		}
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
	mu              sync.Mutex
	cfg             *Config
	next            map[string]int
	active          map[string]map[string]int
	blocked         map[string]time.Time
	transfers       map[string]*routeTransfers
	sourceStats     map[string]map[string]*sourceRouteMetric
	lastProbe       map[string]time.Time
	lastBackupProbe map[string]time.Time
	uniqueRouteIPs  map[string]int
	now             func() time.Time
}

type routeTransfers struct {
	active int
	bytes  uint64
	rate   trafficRate
}

type trafficRate struct {
	lastAt    time.Time
	lastBytes uint64
	rate      *float64
	smoothed  float64
}

func (r *trafficRate) sample(now time.Time, bytes uint64) {
	interval := now.Sub(r.lastAt)
	switch {
	case r.lastAt.IsZero() || interval > sourceRateMaxInterval:
		r.lastAt, r.lastBytes, r.rate = now, bytes, nil
	case interval >= 4*time.Second:
		rate := float64(bytes-r.lastBytes) * 8 / interval.Seconds() / 1e6
		if r.rate == nil {
			r.smoothed = rate
		} else {
			r.smoothed += (1 - math.Exp(-interval.Seconds()/30)) * (rate - r.smoothed)
		}
		r.lastAt, r.lastBytes, r.rate = now, bytes, &rate
	}
}

func (r *trafficRate) status() Rate {
	if r.rate == nil {
		return Rate{}
	}
	smoothed := r.smoothed
	return Rate{DownloadMbps: r.rate, SmoothedMbps: &smoothed}
}

type sourceRouteMetric struct {
	active       int
	bytes        uint64
	sampleAt     time.Time
	sampleBytes  uint64
	emaBps       float64
	hasSample    bool
	lastObserved time.Time
	lastTrial    time.Time
	blockedUntil time.Time
	traffic      trafficRate
	capacity     capacityTrial
}

const (
	sourceRateSampleInterval     = 2 * time.Second
	sourceRateReleaseMinInterval = 1 * time.Second
	sourceRateMaxInterval        = 2 * time.Minute
	sourceRateHalfLifeSeconds    = 20.0
	sourceRouteProbeInterval     = 2 * time.Minute
	sourceProbeCooldown          = 15 * time.Second
	backupMinSpeedRatio          = 1.20
	slowRouteRatio               = 0.20
	loadPenaltyFactor            = 0.5
)

func NewPool(cfg *Config) *Pool {
	if cfg == nil {
		return nil
	}
	transfers := make(map[string]*routeTransfers, len(cfg.Routes))
	uniqueRouteIPs := make(map[string]int)
	for _, route := range cfg.Routes {
		transfers[route.ID] = &routeTransfers{}
		uniqueRouteIPs[route.PublicIP]++
	}
	return &Pool{
		cfg:             cfg,
		next:            make(map[string]int),
		active:          make(map[string]map[string]int),
		blocked:         make(map[string]time.Time),
		transfers:       transfers,
		sourceStats:     make(map[string]map[string]*sourceRouteMetric),
		lastProbe:       make(map[string]time.Time),
		lastBackupProbe: make(map[string]time.Time),
		uniqueRouteIPs:  uniqueRouteIPs,
		now:             time.Now,
	}
}

func (p *Pool) clock() time.Time {
	if p != nil && p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *Pool) metricLocked(source, routeID string) *sourceRouteMetric {
	byRoute := p.sourceStats[source]
	if byRoute == nil {
		byRoute = make(map[string]*sourceRouteMetric, len(p.cfg.Routes))
		p.sourceStats[source] = byRoute
	}
	m := byRoute[routeID]
	if m == nil {
		m = &sourceRouteMetric{}
		byRoute[routeID] = m
	}
	return m
}

func (p *Pool) sampleSourceLocked(m *sourceRouteMetric, now time.Time, onRelease bool) {
	if m == nil || m.active <= 0 || m.sampleAt.IsZero() {
		return
	}
	elapsed := now.Sub(m.sampleAt)
	minInterval := sourceRateSampleInterval
	if onRelease && m.bytes > m.sampleBytes {
		minInterval = sourceRateReleaseMinInterval
	}
	if elapsed < minInterval {
		return
	}
	if elapsed > sourceRateMaxInterval {
		m.sampleAt = now
		m.sampleBytes = m.bytes
		return
	}
	perConnBps := float64(m.bytes-m.sampleBytes) / elapsed.Seconds() / float64(m.active)
	if !m.hasSample || now.Sub(m.lastObserved) >= sourceRateMaxInterval {
		m.emaBps = perConnBps
		m.hasSample = true
	} else {
		alpha := 1 - math.Exp(-elapsed.Seconds()/sourceRateHalfLifeSeconds)
		m.emaBps += alpha * (perConnBps - m.emaBps)
	}
	m.sampleAt = now
	m.sampleBytes = m.bytes
	m.lastObserved = now
}

type routeSelection struct {
	index      int
	reason     string
	primaryID  string
	primaryBps float64
}

const primaryThroughput = "primary_throughput"

func (p *Pool) routeAvailableLocked(source string, route Route, now time.Time) bool {
	if now.Before(p.blocked[route.ID]) {
		return false
	}
	if !route.Backup {
		return true
	}
	if !slices.Contains(route.Sources, source) {
		return false
	}
	m := p.sourceStats[source][route.ID]
	return m == nil || !now.Before(m.blockedUntil)
}

func (p *Pool) referenceSpeedLocked(source string, now time.Time) float64 {
	var best float64
	for _, route := range p.cfg.Routes {
		m := p.sourceStats[source][route.ID]
		if m != nil && m.hasSample && now.Sub(m.lastObserved) < sourceRouteProbeInterval && p.routeAvailableLocked(source, route, now) {
			best = max(best, m.emaBps)
		}
	}
	return best
}

func (m *sourceRouteMetric) slowAgainst(best float64) bool {
	return m != nil && m.hasSample && m.emaBps < best*slowRouteRatio
}

func (p *Pool) extraCapacityLocked(route Route, m *sourceRouteMetric, bestBps float64, now time.Time) bool {
	if !p.cfg.ExploreFastRouteCapacity || route.Backup || m == nil || p.uniqueRouteIPs[route.PublicIP] != 1 {
		return false
	}
	if !m.capacity.trialUntil.IsZero() {
		return true
	}
	return bestBps > 0 && m.hasSample && m.emaBps >= bestBps*capacityFastRatio && m.capacity.canStart(now, m.bytes)
}

func (p *Pool) selectRouteLocked(source string, limit int, now time.Time) (routeSelection, bool) {
	for _, m := range p.sourceStats[source] {
		if m.active > 0 {
			p.sampleSourceLocked(m, now, false)
		}
		if p.cfg.ExploreFastRouteCapacity {
			m.capacity.observe(now, m.active, limit, m.bytes)
		}
	}
	// Include healthy routes at their per-IP limit in the comparison. Their
	// temporary lack of slots should not raise the concurrency on slower lines.
	bestBps := p.referenceSpeedLocked(source, now)
	n := len(p.cfg.Routes)
	eligible := make([]int, 0, n)
	var backups []int
	start := p.next[source]
	for offset := 0; offset < n; offset++ {
		idx := (start + offset) % n
		route := p.cfg.Routes[idx]
		if !p.routeAvailableLocked(source, route, now) {
			continue
		}
		m := p.sourceStats[source][route.ID]
		activeOnIP := p.active[source][route.PublicIP]
		if activeOnIP > limit || (activeOnIP == limit && !p.extraCapacityLocked(route, m, bestBps, now)) {
			continue
		}
		if m != nil && m.active >= 1 && m.slowAgainst(bestBps) {
			continue
		}
		if route.Backup {
			backups = append(backups, idx)
			continue
		}
		eligible = append(eligible, idx)
	}
	if len(eligible) == 0 {
		index, _ := p.selectBackupLocked(source, backups, now, 0)
		return routeSelection{index: index, reason: "backup_no_primary_available"}, index >= 0
	}
	primary, reason := p.selectPrimaryLocked(source, eligible, now)
	primaryID := p.cfg.Routes[primary].ID
	m := p.metricLocked(source, primaryID)
	if reason == primaryThroughput && m.hasSample {
		if backup, backupReason := p.selectBackupLocked(source, backups, now, m.emaBps); backup >= 0 {
			return routeSelection{index: backup, reason: backupReason, primaryID: primaryID, primaryBps: m.emaBps}, true
		}
	}
	return routeSelection{index: primary, reason: reason}, true
}

func (p *Pool) selectPrimaryLocked(source string, eligible []int, now time.Time) (int, string) {

	// Priority 1: explore unmeasured idle routes first, spreading across IPs.
	bestUnmeasuredIdx := -1
	bestUnmeasuredActive := 0
	for _, idx := range eligible {
		route := p.cfg.Routes[idx]
		m := p.metricLocked(source, route.ID)
		if m.hasSample || m.active != 0 {
			continue
		}
		activeOnIP := p.active[source][route.PublicIP]
		if bestUnmeasuredIdx == -1 || activeOnIP < bestUnmeasuredActive {
			bestUnmeasuredIdx = idx
			bestUnmeasuredActive = activeOnIP
		}
	}
	if bestUnmeasuredIdx != -1 {
		return bestUnmeasuredIdx, "primary_initial_probe"
	}

	// Priority 2: periodically probe one stale idle route (rate-limited by cooldown).
	if p.lastProbe[source].IsZero() || now.Sub(p.lastProbe[source]) >= sourceProbeCooldown {
		bestProbeIdx := -1
		var oldestObserved time.Time
		for _, idx := range eligible {
			route := p.cfg.Routes[idx]
			m := p.metricLocked(source, route.ID)
			if m.active == 0 && m.hasSample && now.Sub(m.lastObserved) >= sourceRouteProbeInterval {
				if bestProbeIdx == -1 || m.lastObserved.Before(oldestObserved) {
					bestProbeIdx = idx
					oldestObserved = m.lastObserved
				}
			}
		}
		if bestProbeIdx != -1 {
			p.lastProbe[source] = now
			return bestProbeIdx, "primary_reprobe"
		}
	}

	// Priority 3: select highest live-throughput route adjusted by per-IP load.
	var sumBps float64
	var measuredCount int
	for _, idx := range eligible {
		m := p.metricLocked(source, p.cfg.Routes[idx].ID)
		if m.hasSample {
			sumBps += m.emaBps
			measuredCount++
		}
	}
	fallbackBps := 1.0
	if measuredCount > 0 {
		fallbackBps = sumBps / float64(measuredCount)
	}

	bestIdx := -1
	bestScore := -1.0
	bestActiveOnIP := 0
	for _, idx := range eligible {
		route := p.cfg.Routes[idx]
		m := p.metricLocked(source, route.ID)
		bps := fallbackBps
		if m.hasSample {
			bps = m.emaBps
		}
		activeOnIP := p.active[source][route.PublicIP]
		score := bps / (1.0 + loadPenaltyFactor*float64(activeOnIP))
		if bestIdx == -1 || score > bestScore || (score == bestScore && activeOnIP < bestActiveOnIP) {
			bestIdx = idx
			bestScore = score
			bestActiveOnIP = activeOnIP
		}
	}
	return bestIdx, primaryThroughput
}

// A backup must clearly beat the selected primary on recent file traffic.
// A single occasional trial measures an idle backup again when network conditions change.
func (p *Pool) selectBackupLocked(source string, eligible []int, now time.Time, primaryBps float64) (int, string) {
	best, trial := -1, -1
	var bestBps float64
	var oldestTrial time.Time
	for _, idx := range eligible {
		m := p.metricLocked(source, p.cfg.Routes[idx].ID)
		if m.hasSample && now.Sub(m.lastObserved) < sourceRouteProbeInterval && m.emaBps > primaryBps*backupMinSpeedRatio {
			if m.emaBps > bestBps {
				best, bestBps = idx, m.emaBps
			}
			continue
		}
		if m.active == 0 && (m.lastTrial.IsZero() || now.Sub(m.lastTrial) >= sourceRouteProbeInterval) &&
			(!m.hasSample || now.Sub(m.lastObserved) >= sourceRouteProbeInterval) &&
			(trial == -1 || m.lastTrial.Before(oldestTrial)) {
			trial, oldestTrial = idx, m.lastTrial
		}
	}
	if trial >= 0 && (p.lastBackupProbe[source].IsZero() || now.Sub(p.lastBackupProbe[source]) >= sourceRouteProbeInterval) {
		p.lastBackupProbe[source] = now
		p.metricLocked(source, p.cfg.Routes[trial].ID).lastTrial = now
		return trial, "backup_probe"
	}
	return best, "backup_faster"
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
		now := p.clock()
		if choice, ok := p.selectRouteLocked(source, limit, now); ok {
			route := p.cfg.Routes[choice.index]
			m := p.metricLocked(source, route.ID)
			p.active[source][route.PublicIP]++
			activeOnIP := p.active[source][route.PublicIP]
			measured, measuredBps := m.hasSample, m.emaBps
			p.transfers[route.ID].active++
			if m.active == 0 {
				m.sampleAt = now
				m.sampleBytes = m.bytes
			}
			m.active++
			if p.cfg.ExploreFastRouteCapacity && activeOnIP > limit && m.capacity.trialUntil.IsZero() {
				m.capacity.start(now, m.bytes)
			}
			if p.cfg.ExploreFastRouteCapacity {
				m.capacity.observe(now, m.active, limit, m.bytes)
			}
			p.next[source] = (choice.index + 1) % len(p.cfg.Routes)
			p.mu.Unlock()
			attrs := []slog.Attr{
				slog.Time("event_at", now.UTC()), slog.String("source", source), slog.String("route", route.ID),
				slog.Bool("backup", route.Backup), slog.String("reason", choice.reason),
				slog.Int("ip_active_transfers", activeOnIP), slog.Int("ip_concurrency_limit", limit),
			}
			if activeOnIP > limit {
				attrs = append(attrs, slog.Bool("extra_capacity", true))
			}
			if measured {
				attrs = append(attrs, slog.Float64("estimated_mbps_per_connection", measuredBps*8/1e6))
			}
			if choice.primaryID != "" {
				attrs = append(attrs, slog.String("primary_route", choice.primaryID),
					slog.Float64("primary_estimated_mbps_per_connection", choice.primaryBps*8/1e6))
			}
			slog.LogAttrs(ctx, slog.LevelInfo, "download egress selected", attrs...)
			var once sync.Once
			return route, func() {
				once.Do(func() {
					p.mu.Lock()
					releasedAt := p.clock()
					p.sampleSourceLocked(m, releasedAt, true)
					if p.cfg.ExploreFastRouteCapacity {
						m.capacity.observe(releasedAt, m.active, limit, m.bytes)
					}
					m.active--
					if p.cfg.ExploreFastRouteCapacity {
						m.capacity.observe(releasedAt, m.active, limit, m.bytes)
					}
					p.active[source][route.PublicIP]--
					p.transfers[route.ID].active--
					p.mu.Unlock()
				})
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

// MarkSourceFailure keeps a provider-specific upstream response from cooling
// an otherwise healthy shared backup for all providers.
func (p *Pool) MarkSourceFailure(source, routeID string, status int) {
	if p == nil || routeID == "" {
		return
	}
	p.mu.Lock()
	now := p.clock()
	until := now.Add(sourceRouteProbeInterval)
	m := p.metricLocked(source, routeID)
	m.blockedUntil = until
	m.hasSample = false
	m.lastTrial = now
	if !m.capacity.trialUntil.IsZero() {
		m.capacity.finish(now)
	}
	p.mu.Unlock()
	slog.Warn("download egress cooldown", "event_at", now.UTC(), "source", source, "route", routeID,
		"scope", "source", "reason", "upstream_http_status", "http_status", status, "blocked_until", until.UTC())
}

func (p *Pool) MarkNetworkFailure(source, routeID string) {
	if p == nil || routeID == "" {
		return
	}
	p.mu.Lock()
	now := p.clock()
	cooldown := 30 * time.Second
	for _, route := range p.cfg.Routes {
		if route.ID != routeID || !route.Backup {
			continue
		}
		cooldown = sourceRouteProbeInterval
		for _, source := range route.Sources {
			m := p.metricLocked(source, routeID)
			m.hasSample = false
			m.lastTrial = now
		}
		break
	}
	until := now.Add(cooldown)
	p.blocked[routeID] = until
	for _, routes := range p.sourceStats {
		if m := routes[routeID]; m != nil && !m.capacity.trialUntil.IsZero() {
			m.capacity.finish(now)
		}
	}
	p.mu.Unlock()
	slog.Warn("download egress cooldown", "event_at", now.UTC(), "source", source, "route", routeID,
		"scope", "route", "reason", "upstream_request_failed", "blocked_until", until.UTC())
}

// RecordBytes counts downloaded file data for the specific route, even when
// multiple routes share a public IP and its concurrency budget.
func (p *Pool) RecordBytes(routeID string, n int) {
	p.RecordSourceBytes("", routeID, n)
}

// RecordSourceBytes counts downloaded file data for the route and updates the
// per-(source, route) live throughput estimator used by Acquire.
func (p *Pool) RecordSourceBytes(source, routeID string, n int) {
	if p == nil || n <= 0 || routeID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if route := p.transfers[routeID]; route != nil {
		route.bytes += uint64(n)
	}
	if source != "" {
		m := p.metricLocked(source, routeID)
		m.bytes += uint64(n)
		p.sampleSourceLocked(m, p.clock(), false)
	}
}

type Rate struct {
	DownloadMbps *float64 `json:"download_mbps,omitempty"`
	SmoothedMbps *float64 `json:"smoothed_mbps,omitempty"`
}

type Status struct {
	ID              string            `json:"id"`
	PublicIP        string            `json:"public_ip"`
	Backup          bool              `json:"backup,omitempty"`
	Sources         []string          `json:"sources,omitempty"`
	Blocked         bool              `json:"blocked"`
	BlockedBySource map[string]bool   `json:"blocked_by_source,omitempty"`
	SlowBySource    map[string]bool   `json:"slow_by_source,omitempty"`
	ExtraBySource   map[string]string `json:"extra_by_source,omitempty"`
	ActiveBySrc     map[string]int    `json:"active_by_source"`
	RatesBySource   map[string]Rate   `json:"rates_by_source,omitempty"`
	ActiveTransfers int               `json:"active_transfers"`
	Rate
}

func (p *Pool) sampleTrafficLocked(now time.Time) {
	for _, route := range p.transfers {
		route.rate.sample(now, route.bytes)
	}
	for source, metrics := range p.sourceStats {
		for _, m := range metrics {
			m.traffic.sample(now, m.bytes)
			if p.cfg.ExploreFastRouteCapacity {
				m.capacity.observe(now, m.active, p.cfg.Limit(source), m.bytes)
			}
		}
	}
}

// MonitorTraffic samples the dashboard rates even when no viewer is connected.
func (p *Pool) MonitorTraffic(ctx context.Context) {
	if p == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		p.sampleTrafficLocked(p.clock())
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Pool) Snapshot() []Status {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	p.sampleTrafficLocked(now)
	referenceSpeeds := make(map[string]float64, len(p.sourceStats))
	for source := range p.sourceStats {
		referenceSpeeds[source] = p.referenceSpeedLocked(source, now)
	}
	out := make([]Status, 0, len(p.cfg.Routes))
	for _, r := range p.cfg.Routes {
		route := p.transfers[r.ID]
		entry := Status{
			ID: r.ID, PublicIP: r.PublicIP, Backup: r.Backup, Sources: slices.Clone(r.Sources),
			Blocked: now.Before(p.blocked[r.ID]), ActiveTransfers: route.active,
			ActiveBySrc: make(map[string]int), RatesBySource: make(map[string]Rate),
			BlockedBySource: make(map[string]bool), SlowBySource: make(map[string]bool), Rate: route.rate.status(),
		}
		for source, metrics := range p.sourceStats {
			if m := metrics[r.ID]; m != nil {
				entry.ActiveBySrc[source] = m.active
				entry.RatesBySource[source] = m.traffic.status()
				if now.Before(m.blockedUntil) {
					entry.BlockedBySource[source] = true
				}
				if m.slowAgainst(referenceSpeeds[source]) && p.routeAvailableLocked(source, r, now) {
					entry.SlowBySource[source] = true
				}
				if !m.capacity.trialUntil.IsZero() {
					if entry.ExtraBySource == nil {
						entry.ExtraBySource = make(map[string]string)
					}
					entry.ExtraBySource[source] = "trial"
					if m.capacity.confirmed {
						entry.ExtraBySource[source] = "confirmed"
					}
				}
			}
		}
		out = append(out, entry)
	}
	return out
}
