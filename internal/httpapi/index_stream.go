package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
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
	Days     any      `json:"days"`
	Removed  []string `json:"removed,omitempty"`
	Active   bool     `json:"active"`
}

func (s *Server) indexStream(w http.ResponseWriter, r *http.Request) {
	productKey := strings.TrimSpace(r.URL.Query().Get("product"))
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
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
	watch := r.URL.Query().Get("watch") == "1"
	stream, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	previous := map[string][]byte{}
	first := true
	lastActive := false

	emit := func() (bool, error) {
		if watch {
			status, err := s.store.ProductArchiveIndexStatus(r.Context(), productKey, year)
			if err != nil {
				return false, err
			}
			if _, err := stream.event("archive", status, true); err != nil {
				return false, err
			}
		}
		next := map[string][]byte{}
		removed := []string{}
		active := false
		changed := false
		var payload indexStreamUpdate
		payload.Coverage = coverage

		if coverage {
			yearData, err := s.store.BandCoverageYear(r.Context(), productKey, year)
			if err != nil {
				return false, err
			}
			payload.Bands = yearData.Bands
			rows := make([][]any, 0)
			for _, row := range yearData.Days {
				if len(row) < 11 {
					continue
				}
				ordinal, _ := row[0].(int)
				day := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, ordinal).Format(time.DateOnly)
				encoded, _ := json.Marshal(row)
				next[day] = encoded
				if first || !bytes.Equal(previous[day], encoded) {
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
			rows := make([]state.YearDay, 0)
			for _, row := range yearData.Days {
				encoded, _ := json.Marshal(row)
				next[row.Day] = encoded
				if first || !bytes.Equal(previous[row.Day], encoded) {
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

		if first || changed || active != lastActive {
			if _, err := stream.event("index", payload, false); err != nil {
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
		return
	}
	if !active && !watch {
		_, _ = stream.event("complete", map[string]any{"active": false}, false)
		return
	}

	updates := time.NewTicker(2 * time.Second)
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
				return
			}
			if !active && !watch {
				_, _ = stream.event("complete", map[string]any{"active": false}, false)
				return
			}
		case <-heartbeat.C:
			if err := stream.heartbeat(); err != nil {
				return
			}
		}
	}
}
