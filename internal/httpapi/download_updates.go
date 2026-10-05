package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type downloadSummary struct {
	ID           int64     `json:"id"`
	CollectionID string    `json:"collection_id"`
	State        string    `json:"state"`
	FilesDone    int       `json:"files_done"`
	AssetsQueued int       `json:"assets_queued"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	Band         int       `json:"band,omitempty"`
	TimesCount   int       `json:"times_count,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
}

func downloadCursor(at time.Time, id int64) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(id, 10)
}

func parseDownloadCursor(raw string) (time.Time, int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, 0, nil
	}
	stamp, idText, ok := strings.Cut(raw, "|")
	if !ok {
		return time.Time{}, 0, errors.New("invalid download cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, 0, errors.New("invalid download cursor")
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id < 0 {
		return time.Time{}, 0, errors.New("invalid download cursor")
	}
	return at.UTC(), id, nil
}

func summarizeDownloads(items []state.DownloadRequest) ([]downloadSummary, time.Time, int64) {
	out := make([]downloadSummary, 0, len(items))
	var cursorAt time.Time
	var cursorID int64
	for _, item := range items {
		out = append(out, downloadSummary{
			ID: item.ID, CollectionID: item.CollectionID, State: item.State,
			FilesDone: item.FilesDone, AssetsQueued: item.AssetsQueued,
			Start: item.Start, End: item.End, Band: item.Selection.Band,
			TimesCount: len(item.Selection.Times), LastError: item.LastError,
		})
		if item.UpdatedAt.After(cursorAt) || item.UpdatedAt.Equal(cursorAt) && item.ID > cursorID {
			cursorAt, cursorID = item.UpdatedAt, item.ID
		}
	}
	return out, cursorAt, cursorID
}

func (s *Server) downloadUpdates(w http.ResponseWriter, r *http.Request) {
	user, ok := s.orderUser(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	since, sinceID, err := parseDownloadCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var items []state.DownloadRequest
	if since.IsZero() {
		if user.Role == "admin" {
			items, err = s.store.DownloadRequests(r.Context(), limit)
		} else {
			items, err = s.store.DownloadRequestsOwned(r.Context(), limit, user.ID)
		}
	} else if user.Role == "admin" {
		items, err = s.store.DownloadRequestUpdates(r.Context(), limit, since, sinceID)
	} else {
		items, err = s.store.DownloadRequestUpdatesOwned(r.Context(), limit, since, sinceID, user.ID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	summaries, cursorAt, cursorID := summarizeDownloads(items)
	if cursorAt.IsZero() {
		cursorAt, cursorID = since, sinceID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"downloads": summaries, "cursor": downloadCursor(cursorAt, cursorID), "has_more": len(items) == limit,
	})
}

func (s *Server) downloaderProviderStream(w http.ResponseWriter, r *http.Request) {
	if s.downloader == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("satellite downloader is not configured"))
		return
	}
	stream, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	items, loadErr := s.downloader.Providers(r.Context())
	payload := struct {
		Providers []downloader.ProviderStatus `json:"providers"`
		Error     string                      `json:"error,omitempty"`
	}{Providers: items}
	if loadErr != nil {
		payload.Error = loadErr.Error()
	}
	if _, err := stream.event("providers", payload, false); err != nil {
		return
	}
	for _, item := range items {
		encoded, _ := json.Marshal(item)
		stream.last["provider:"+item.SourceID] = encoded
	}
	lastError := payload.Error
	updates := time.NewTicker(time.Second)
	heartbeat := time.NewTicker(15 * time.Second)
	defer updates.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates.C:
			items, err := s.downloader.Providers(r.Context())
			errorText := ""
			if err != nil {
				errorText = err.Error()
			}
			if errorText != lastError {
				if _, writeErr := stream.event("provider_error", map[string]string{"error": errorText}, false); writeErr != nil {
					return
				}
				lastError = errorText
			}
			for _, item := range items {
				if _, writeErr := stream.eventKey("provider", "provider:"+item.SourceID, item, true); writeErr != nil {
					return
				}
			}
		case <-heartbeat.C:
			if err := stream.heartbeat(); err != nil {
				return
			}
		}
	}
}
