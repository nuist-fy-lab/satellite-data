package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/linkmon"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type downloadMonitorEntry struct {
	SourceID string                       `json:"source_id"`
	Latest   *state.DownloadMonitorLatest `json:"latest,omitempty"`
	Points   []state.DownloadMonitorPoint `json:"points"`
}

type linkMonitorEntry struct {
	SourceID string            `json:"source_id"`
	Name     string            `json:"name"`
	Target   string            `json:"target"`
	Port     int               `json:"port"`
	Latest   *state.LinkSample `json:"latest,omitempty"`
	Points   []state.LinkPoint `json:"points"`
}

func parseMonitorWindow(raw string, allowRealtime bool) (string, monitorWindow, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		name = "1h"
	}
	if allowRealtime && name == "realtime" {
		return name, monitorWindow{duration: 5 * time.Minute, bucket: 15 * time.Second}, nil
	}
	window, ok := monitorWindows[name]
	if !ok {
		if allowRealtime {
			return "", monitorWindow{}, errors.New("unsupported download monitor window")
		}
		return "", monitorWindow{}, errors.New("unsupported monitor window")
	}
	return name, window, nil
}

func parseMonitorSince(r *http.Request) (time.Time, bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("since"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if raw == "" {
		return time.Time{}, false, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false, errors.New("invalid monitor cursor")
	}
	return parsed.UTC(), true, nil
}

func (s *Server) buildDownloadMonitorPayload(ctx context.Context, name string, window monitorWindow, since time.Time, hasSince, strictAfter bool) (map[string]any, []downloadMonitorEntry, time.Time, error) {
	seriesSince := time.Now().UTC().Add(-window.duration)
	if hasSince && since.After(seriesSince) {
		seriesSince = since.UTC()
	}
	latest, err := s.store.LatestDownloadSamples(ctx)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	points, err := s.store.DownloadSeries(ctx, seriesSince, window.bucket)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	bySource := make(map[string]*downloadMonitorEntry)
	var maxAt time.Time
	for _, item := range latest {
		sample := item
		if sample.SampledAt.After(maxAt) {
			maxAt = sample.SampledAt
		}
		bySource[item.SourceID] = &downloadMonitorEntry{SourceID: item.SourceID, Latest: &sample, Points: []state.DownloadMonitorPoint{}}
	}
	for source, series := range points {
		if bySource[source] == nil {
			bySource[source] = &downloadMonitorEntry{SourceID: source}
		}
		filtered := series
		if strictAfter && hasSince {
			filtered = make([]state.DownloadMonitorPoint, 0, len(series))
			for _, p := range series {
				if p.SampledAt.After(since) {
					filtered = append(filtered, p)
				}
			}
		}
		if filtered == nil {
			filtered = []state.DownloadMonitorPoint{}
		}
		for _, p := range filtered {
			if p.SampledAt.After(maxAt) {
				maxAt = p.SampledAt
			}
		}
		bySource[source].Points = filtered
	}
	keys := make([]string, 0, len(bySource))
	for key := range bySource {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]downloadMonitorEntry, 0, len(keys))
	for _, key := range keys {
		items = append(items, *bySource[key])
	}
	payload := map[string]any{
		"window":                  name,
		"bucket_seconds":          int(window.bucket.Seconds()),
		"sample_interval_seconds": 15,
		"generated_at":            time.Now().UTC(),
		"providers":               items,
	}
	if !maxAt.IsZero() {
		payload["cursor"] = maxAt.UTC().Format(time.RFC3339Nano)
	}
	return payload, items, maxAt, nil
}

func (s *Server) buildLinkMonitorPayload(ctx context.Context, windowName string, window monitorWindow, since time.Time, hasSince, strictAfter bool) (map[string]any, []linkMonitorEntry, time.Time, error) {
	seriesSince := time.Now().UTC().Add(-window.duration)
	if hasSince && since.After(seriesSince) {
		seriesSince = since.UTC()
	}
	latest, err := s.store.LatestLinkSamples(ctx)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	series, err := s.store.LinkSeries(ctx, seriesSince, window.bucket)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	latestBySource := make(map[string]state.LinkSample, len(latest))
	var maxAt time.Time
	for _, item := range latest {
		latestBySource[item.SourceID] = item
		if item.SampledAt.After(maxAt) {
			maxAt = item.SampledAt
		}
	}
	items := make([]linkMonitorEntry, 0, len(linkmon.Targets()))
	for _, target := range linkmon.Targets() {
		pts := series[target.SourceID]
		if strictAfter && hasSince && len(pts) > 0 {
			filtered := make([]state.LinkPoint, 0, len(pts))
			for _, p := range pts {
				if p.SampledAt.After(since) {
					filtered = append(filtered, p)
				}
			}
			pts = filtered
		}
		if pts == nil {
			pts = []state.LinkPoint{}
		}
		for _, p := range pts {
			if p.SampledAt.After(maxAt) {
				maxAt = p.SampledAt
			}
		}
		item := linkMonitorEntry{
			SourceID: target.SourceID,
			Name:     target.Name,
			Target:   target.Host,
			Port:     target.Port,
			Points:   pts,
		}
		if current, exists := latestBySource[target.SourceID]; exists {
			value := current
			item.Latest = &value
		}
		items = append(items, item)
	}
	payload := map[string]any{
		"origin":                  "c2ln1",
		"window":                  windowName,
		"bucket_seconds":          int(window.bucket.Seconds()),
		"sample_interval_seconds": 15,
		"generated_at":            time.Now().UTC(),
		"links":                   items,
	}
	if !maxAt.IsZero() {
		payload["cursor"] = maxAt.UTC().Format(time.RFC3339Nano)
	}
	return payload, items, maxAt, nil
}

func monitorStateHash(v any) string {
	encoded, _ := json.Marshal(v)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16])
}

func (s *Server) downloadMonitorStream(w http.ResponseWriter, r *http.Request) {
	name, window, err := parseMonitorWindow(r.URL.Query().Get("window"), true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	since, hasSince, err := parseMonitorSince(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	payload, items, maxAt, err := s.buildDownloadMonitorPayload(r.Context(), name, window, since, hasSince, hasSince)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	stream, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	eventName := "snapshot"
	if hasSince {
		eventName = "delta"
	}
	eventID := ""
	if !maxAt.IsZero() {
		eventID = maxAt.UTC().Format(time.RFC3339Nano)
	}
	if _, err := stream.eventWithID(eventName, eventName, eventID, payload, false); err != nil {
		return
	}

	prevHash := monitorStateHash(items)
	prevMax := maxAt
	interval := 5 * time.Second
	if name == "realtime" {
		interval = 2 * time.Second
	}
	updates := time.NewTicker(interval)
	heartbeat := time.NewTicker(15 * time.Second)
	defer updates.Stop()
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates.C:
			cursor := prevMax
			hasCursor := !prevMax.IsZero()
			nextPayload, nextItems, nextMax, err := s.buildDownloadMonitorPayload(r.Context(), name, window, cursor, hasCursor, hasCursor)
			if err != nil {
				continue
			}
			nextHash := monitorStateHash(nextItems)
			if nextHash == prevHash && !nextMax.After(prevMax) {
				continue
			}
			nextID := ""
			if !nextMax.IsZero() {
				nextID = nextMax.UTC().Format(time.RFC3339Nano)
			}
			if _, err := stream.eventWithID("delta", "", nextID, nextPayload, false); err != nil {
				return
			}
			prevHash = nextHash
			if nextMax.After(prevMax) {
				prevMax = nextMax
			}
		case <-heartbeat.C:
			if err := stream.heartbeat(); err != nil {
				return
			}
		}
	}
}

func (s *Server) linkMonitorStream(w http.ResponseWriter, r *http.Request) {
	windowName, window, err := parseMonitorWindow(r.URL.Query().Get("window"), false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	since, hasSince, err := parseMonitorSince(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	payload, items, maxAt, err := s.buildLinkMonitorPayload(r.Context(), windowName, window, since, hasSince, hasSince)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	stream, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	eventName := "snapshot"
	if hasSince {
		eventName = "delta"
	}
	eventID := ""
	if !maxAt.IsZero() {
		eventID = maxAt.UTC().Format(time.RFC3339Nano)
	}
	if _, err := stream.eventWithID(eventName, eventName, eventID, payload, false); err != nil {
		return
	}

	prevHash := monitorStateHash(items)
	prevMax := maxAt
	updates := time.NewTicker(5 * time.Second)
	heartbeat := time.NewTicker(15 * time.Second)
	defer updates.Stop()
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates.C:
			cursor := prevMax
			hasCursor := !prevMax.IsZero()
			nextPayload, nextItems, nextMax, err := s.buildLinkMonitorPayload(r.Context(), windowName, window, cursor, hasCursor, hasCursor)
			if err != nil {
				continue
			}
			nextHash := monitorStateHash(nextItems)
			if nextHash == prevHash && !nextMax.After(prevMax) {
				continue
			}
			nextID := ""
			if !nextMax.IsZero() {
				nextID = nextMax.UTC().Format(time.RFC3339Nano)
			}
			if _, err := stream.eventWithID("delta", "", nextID, nextPayload, false); err != nil {
				return
			}
			prevHash = nextHash
			if nextMax.After(prevMax) {
				prevMax = nextMax
			}
		case <-heartbeat.C:
			if err := stream.heartbeat(); err != nil {
				return
			}
		}
	}
}

func (s *Server) accessDownloadEgressStream(w http.ResponseWriter, r *http.Request) {
	if _, err := s.adminUser(r); err != nil {
		s.writeAdminError(w, r)
		return
	}
	if s.central == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central access unavailable"))
		return
	}
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	if since == "" {
		since = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	stream, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	routes := s.central.DownloadEgressStatus()
	proxy := s.central.DownloadProxyEgressStatus(r.Context())
	hash := monitorStateHash(struct {
		Routes any `json:"routes"`
		Proxy  any `json:"proxy"`
	}{routes, proxy})
	if since != "" && since == hash {
		if _, err := stream.eventWithID("sync", "sync", hash, map[string]any{"cursor": hash}, false); err != nil {
			return
		}
	} else {
		if _, err := stream.eventWithID("egress", "egress", hash, map[string]any{"routes": routes, "proxy": proxy, "cursor": hash}, false); err != nil {
			return
		}
	}

	prevHash := hash
	updates := time.NewTicker(2 * time.Second)
	heartbeat := time.NewTicker(15 * time.Second)
	defer updates.Stop()
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates.C:
			nextRoutes := s.central.DownloadEgressStatus()
			nextProxy := s.central.DownloadProxyEgressStatus(r.Context())
			nextHash := monitorStateHash(struct {
				Routes any `json:"routes"`
				Proxy  any `json:"proxy"`
			}{nextRoutes, nextProxy})
			if nextHash == prevHash {
				continue
			}
			if _, err := stream.eventWithID("egress", "", nextHash, map[string]any{"routes": nextRoutes, "proxy": nextProxy, "cursor": nextHash}, false); err != nil {
				return
			}
			prevHash = nextHash
		case <-heartbeat.C:
			if err := stream.heartbeat(); err != nil {
				return
			}
		}
	}
}

func writeNDJSONChunks[T any](w http.ResponseWriter, items []T, chunkSize int) {
	if chunkSize <= 0 {
		chunkSize = 24
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, hasFlusher := w.(http.Flusher)
	for i := 0; i < len(items); i += chunkSize {
		end := i + chunkSize
		if end > len(items) {
			end = len(items)
		}
		for _, item := range items[i:end] {
			line, err := json.Marshal(item)
			if err != nil {
				continue
			}
			_, _ = w.Write(line)
			_, _ = w.Write([]byte{'\n'})
		}
		if hasFlusher {
			flusher.Flush()
		}
	}
}
