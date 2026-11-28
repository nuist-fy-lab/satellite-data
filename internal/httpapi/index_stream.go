package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type indexStreamUpdate struct {
	Coverage bool     `json:"coverage"`
	Bands    []int    `json:"bands,omitempty"`
	Pending  int      `json:"pending,omitempty"`
	Days     any      `json:"days"`
	Removed  []string `json:"removed,omitempty"`
	Active   bool     `json:"active"`
	Cursor   string   `json:"cursor,omitempty"`
	Reset    bool     `json:"reset,omitempty"`
	Ready    bool     `json:"ready,omitempty"`
}

func (s *Server) indexStream(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	productKey := strings.TrimSpace(query.Get("product"))
	year, err := strconv.Atoi(query.Get("year"))
	if err != nil || year < 1970 || year > 2100 {
		writeError(w, http.StatusBadRequest, errors.New("invalid year"))
		return
	}
	_, product, err := s.store.ProductSource(r.Context(), productKey)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("unknown product"))
		return
	}
	coverage := downloadfilter.ForProduct(product.SourceID, product.ProductKey) != nil
	watch := query.Get("watch") == "1"
	progressive := query.Get("progressive") == "1"
	watchArchive := watch && (!progressive || query.Get("archive") != "0")
	sinceCursor := strings.TrimSpace(query.Get("cursor"))
	sinceRevision := ""
	if progressive {
		coverage = coverage && query.Get("summary") != "1"
		sinceRevision = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
		if sinceRevision == "" {
			sinceRevision = query.Get("revision")
		}
		sinceCursor = ""
	} else if sinceCursor == "" {
		sinceCursor = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	stream, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	previous := map[string][]byte{}
	first := true
	lastActive := false
	lastRevision := ""
	var lastArchive time.Time
	warming := false

	emitArchive := func() error {
		status, err := s.store.ProductArchiveIndexStatus(r.Context(), productKey, year)
		if err != nil {
			return err
		}
		_, err = stream.event("archive", status, true)
		return err
	}

	emit := func() (bool, error) {
		if watchArchive && !progressive {
			if err := emitArchive(); err != nil {
				return false, err
			}
		}
		next := map[string][]byte{}
		removed := []string{}
		active := false
		changed := false
		maxUpdated := ""
		var payload indexStreamUpdate
		payload.Coverage = coverage
		digest := sha256.New()
		represented := 0

		if coverage {
			yearData, err := s.store.BandCoverageYear(r.Context(), productKey, year)
			if err != nil {
				return false, err
			}
			payload.Bands = yearData.Bands
			represented = len(yearData.Days)
			payload.Pending = yearData.Pending
			warming = yearData.Pending > 0
			if yearData.Pending > 0 {
				active = true
			}
			rows := make([][]any, 0)
			for _, row := range yearData.Days {
				if len(row) < 11 {
					continue
				}
				ordinal, _ := row[0].(int)
				day := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, ordinal).Format(time.DateOnly)
				updated := ""
				if len(row) > 11 {
					updated, _ = row[11].(string)
				}
				if updated > maxUpdated {
					maxUpdated = updated
				}
				encoded, _ := json.Marshal(row)
				next[day] = encoded
				if progressive {
					_, _ = digest.Write(encoded)
				}
				if first {
					if sinceCursor == "" || updated == "" || updated > sinceCursor {
						rows = append(rows, row)
						changed = true
					}
				} else if !bytes.Equal(previous[day], encoded) {
					rows = append(rows, row)
					changed = true
				}
				if indexing, ok := row[10].(int); ok && indexing > 0 {
					active = true
				}
			}
			payload.Days = rows
		} else {
			yearData, err := s.store.Year(r.Context(), productKey, year)
			if err != nil {
				return false, err
			}
			represented = len(yearData.Days)
			rows := make([]state.YearDay, 0)
			for _, row := range yearData.Days {
				updated := ""
				if row.UpdatedAt != nil {
					updated = *row.UpdatedAt
				}
				if updated > maxUpdated {
					maxUpdated = updated
				}
				encoded, _ := json.Marshal(row)
				next[row.Day] = encoded
				if progressive {
					_, _ = digest.Write(encoded)
				}
				if first {
					if sinceCursor == "" || updated == "" || updated > sinceCursor {
						rows = append(rows, row)
						changed = true
					}
				} else if !bytes.Equal(previous[row.Day], encoded) {
					rows = append(rows, row)
					changed = true
				}
				if row.Indexing > 0 {
					active = true
				}
			}
			payload.Days = rows
		}

		for day := range previous {
			if _, ok := next[day]; !ok {
				removed = append(removed, day)
				changed = true
			}
		}
		payload.Removed = removed
		payload.Active = active
		payload.Cursor = maxUpdated

		if progressive {
			meta := indexStreamUpdate{Coverage: coverage, Bands: payload.Bands, Pending: payload.Pending, Active: active}
			encoded, _ := json.Marshal(meta)
			_, _ = digest.Write(encoded)
			revision := hex.EncodeToString(digest.Sum(nil)[:16])
			if first {
				if sinceRevision == revision {
					if _, err := stream.eventWithID("sync", "sync", revision, meta, false); err != nil {
						return false, err
					}
				} else if err := sendIndexYear(stream, payload, revision); err != nil {
					return false, err
				}
				s.queueCalendarYear(productKey, year, represented)
			} else if revision != lastRevision {
				payload.Cursor, payload.Bands = "", nil
				if _, err := stream.eventWithID("index", "index", revision, payload, false); err != nil {
					return false, err
				}
			}
			lastRevision = revision
			if watchArchive && time.Since(lastArchive) >= 15*time.Second {
				lastArchive = time.Now()
				if err := emitArchive(); err != nil && r.Context().Err() == nil {
					slog.Warn("index stream archive status", "product", productKey, "year", year, "error", err)
				}
			}
		} else if first && sinceCursor != "" && !changed && !active {
			if _, err := stream.eventWithID("sync", "sync", maxUpdated, map[string]any{
				"coverage": coverage,
				"active":   active,
				"cursor":   maxUpdated,
			}, false); err != nil {
				return false, err
			}
		} else if first || changed || active != lastActive {
			if _, err := stream.eventWithID("index", "index", maxUpdated, payload, false); err != nil {
				return false, err
			}
		}
		previous = next
		first = false
		lastActive = active
		return active, nil
	}

	active, err := emit()
	if err != nil {
		if r.Context().Err() == nil {
			slog.Warn("index stream initial snapshot", "product", productKey, "year", year, "error", err)
		}
		return
	}
	if !active && !watch {
		_, _ = stream.event("complete", map[string]any{"active": false}, false)
		return
	}

	// A yearly snapshot can involve substantial PostgreSQL work. In particular,
	// avoid hammering it twice per second per viewer during index warmup.
	interval := func() time.Duration {
		if warming {
			return 2 * time.Second
		}
		return 5 * time.Second
	}
	updates := time.NewTimer(interval())
	heartbeat := time.NewTicker(15 * time.Second)
	defer updates.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates.C:
			active, err := emit()
			if err != nil {
				if r.Context().Err() == nil {
					slog.Warn("index stream refresh", "product", productKey, "year", year, "error", err)
				}
				return
			}
			if !active && !watch {
				_, _ = stream.event("complete", map[string]any{"active": false}, false)
				return
			}
			updates.Reset(interval())
		case <-heartbeat.C:
			if err := stream.heartbeat(); err != nil {
				return
			}
		}
	}
}

func sendIndexYear(stream *sseWriter, payload indexStreamUpdate, revision string) error {
	payload.Reset = true
	payload.Cursor = ""
	switch rows := payload.Days.(type) {
	case []state.YearDay:
		return sendIndexYearRows(stream, payload, rows, revision)
	case [][]any:
		return sendIndexYearRows(stream, payload, rows, revision)
	}
	return errors.New("unsupported calendar rows")
}

func sendIndexYearRows[T any](stream *sseWriter, payload indexStreamUpdate, rows []T, revision string) error {
	// Smaller first batch lets the visible grid paint without waiting for the
	// whole year. Flush larger subsequent batches to keep compression useful.
	size := 28
	for len(rows) > size {
		payload.Days = rows[:size]
		id := ""
		if payload.Reset {
			id = "pending"
		}
		if _, err := stream.eventWithID("year", "year", id, payload, false); err != nil {
			return err
		}
		rows = rows[size:]
		size = 84
		payload.Reset, payload.Bands = false, nil
	}
	payload.Days, payload.Ready = rows, true
	_, err := stream.eventWithID("year", "year", revision, payload, false)
	return err
}
