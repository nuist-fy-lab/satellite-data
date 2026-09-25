package linkmon

import (
	"context"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type Target struct {
	SourceID string `json:"source_id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

var defaultTargets = []Target{
	{SourceID: "goes16", Name: "GOES-16", Host: "storage.googleapis.com", Port: 443},
	{SourceID: "goes17", Name: "GOES-17", Host: "storage.googleapis.com", Port: 443},
	{SourceID: "goes18", Name: "GOES-18", Host: "storage.googleapis.com", Port: 443},
	{SourceID: "goes19", Name: "GOES-19", Host: "storage.googleapis.com", Port: 443},
	{SourceID: "himawari8", Name: "Himawari-8", Host: "noaa-himawari8.s3.amazonaws.com", Port: 443},
	{SourceID: "himawari9", Name: "Himawari-9", Host: "noaa-himawari9.s3.amazonaws.com", Port: 443},
	{SourceID: "modis", Name: "MODIS", Host: "ladsweb.modaps.eosdis.nasa.gov", Port: 443},
	{SourceID: "viirs", Name: "VIIRS", Host: "ladsweb.modaps.eosdis.nasa.gov", Port: 443},
	{SourceID: "mtg", Name: "MTG", Host: "api.eumetsat.int", Port: 443},
	{SourceID: "earthcare", Name: "EarthCARE", Host: "catalog.maap.eo.esa.int", Port: 443},
	{SourceID: "calipso", Name: "CALIPSO", Host: "data.asdc.earthdata.nasa.gov", Port: 443},
}

func Targets() []Target {
	return append([]Target(nil), defaultTargets...)
}

type Monitor struct {
	store    *state.Store
	interval time.Duration
	timeout  time.Duration
	attempts int
	targets  []Target
}

func New(store *state.Store) *Monitor {
	return &Monitor{
		store:    store,
		interval: 15 * time.Second,
		timeout:  2500 * time.Millisecond,
		attempts: 4,
		targets:  Targets(),
	}
}

func (m *Monitor) Run(ctx context.Context) {
	if m == nil || m.store == nil {
		return
	}
	go func() {
		m.probeAll(ctx)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.probeAll(ctx)
			}
		}
	}()
	go func() {
		_ = m.store.DeleteLinkSamplesBefore(context.WithoutCancel(ctx), time.Now().UTC().Add(-8*24*time.Hour))
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = m.store.DeleteLinkSamplesBefore(context.WithoutCancel(ctx), time.Now().UTC().Add(-8*24*time.Hour))
			}
		}
	}()
}

func (m *Monitor) probeAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, target := range m.targets {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			sample := m.probe(ctx, target)
			_ = m.store.RecordLinkSample(context.WithoutCancel(ctx), sample)
		}()
	}
	wg.Wait()
}

func (m *Monitor) probe(ctx context.Context, target Target) state.LinkSample {
	latencies := make([]float64, 0, m.attempts)
	resolvedIP := ""
	lastError := ""
	address := net.JoinHostPort(target.Host, portString(target.Port))
	for attempt := 0; attempt < m.attempts; attempt++ {
		if ctx.Err() != nil {
			break
		}
		dialer := &net.Dialer{Timeout: m.timeout, KeepAlive: -1}
		started := time.Now()
		conn, err := dialer.DialContext(ctx, "tcp", address)
		elapsed := time.Since(started)
		if err != nil {
			lastError = err.Error()
		} else {
			latencies = append(latencies, float64(elapsed.Microseconds())/1000)
			if resolvedIP == "" {
				resolvedIP = remoteIP(conn.RemoteAddr())
			}
			_ = conn.Close()
		}
		if attempt+1 < m.attempts {
			select {
			case <-ctx.Done():
				return sampleFromLatencies(target, latencies, resolvedIP, lastError, m.attempts)
			case <-time.After(75 * time.Millisecond):
			}
		}
	}
	return sampleFromLatencies(target, latencies, resolvedIP, lastError, m.attempts)
}

func sampleFromLatencies(target Target, latencies []float64, resolvedIP, lastError string, attempts int) state.LinkSample {
	successes := len(latencies)
	sample := state.LinkSample{
		SourceID:     target.SourceID,
		SampledAt:    time.Now().UTC(),
		TargetHost:   target.Host,
		ResolvedIP:   resolvedIP,
		LossPct:      100 * float64(attempts-successes) / float64(attempts),
		SuccessCount: successes,
		AttemptCount: attempts,
		LastError:    lastError,
	}
	if successes > 0 {
		value := mean(latencies)
		sample.LatencyMS = &value
	}
	if successes > 1 {
		value := jitter(latencies)
		sample.JitterMS = &value
	}
	return sample
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func jitter(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	var total float64
	for i := 1; i < len(values); i++ {
		total += math.Abs(values[i] - values[i-1])
	}
	return total / float64(len(values)-1)
}

func remoteIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err == nil {
		return host
	}
	return strings.TrimSpace(addr.String())
}

func portString(port int) string {
	if port <= 0 {
		port = 443
	}
	return strconv.Itoa(port)
}
