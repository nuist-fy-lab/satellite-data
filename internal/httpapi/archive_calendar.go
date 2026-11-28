package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func calendarSourceIDs(raw string) ([]string, error) {
	var ids []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" || seen[id] {
			continue
		}
		if len(ids) == 32 {
			return nil, errors.New("too many sources")
		}
		ids = append(ids, id)
		seen[id] = true
	}
	return ids, nil
}

func calendarFamily(source string) string {
	family := strings.TrimRight(source, "0123456789")
	if family == "goes" || family == "himawari" {
		return family
	}
	return source
}

func (s *Server) calendarCatalog(w http.ResponseWriter, r *http.Request, family string) {
	sources := s.registry.Sources()
	byFamily := make(map[string][]string)
	for _, source := range sources {
		if source.ID != "era5" {
			id := calendarFamily(source.ID)
			byFamily[id] = append(byFamily[id], source.ID)
		}
	}
	if len(byFamily[family]) == 0 {
		family = ""
		if len(byFamily["goes"]) > 0 {
			family = "goes"
		} else {
			for _, source := range sources {
				if source.ID != "era5" {
					family = calendarFamily(source.ID)
					break
				}
			}
		}
	}
	stream := &ndjsonWriter{w: w}
	if err := stream.encode(struct {
		Sources []model.Source `json:"sources"`
		Family  string         `json:"family"`
	}{sources, family}); err != nil || stream.flush() != nil {
		return
	}
	products, err := s.store.ProductsBySources(r.Context(), byFamily[family])
	if err != nil {
		stream.failure("product catalog stream interrupted")
		return
	}
	for i, item := range products {
		if stream.encode(provider.NormalizeProduct(item)) != nil || ((i+1)%24 == 0 && stream.flush() != nil) {
			return
		}
	}
	stream.finish()
}

func (s *Server) archiveCalendar(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	query := r.URL.Query()
	year, err := strconv.Atoi(query.Get("year"))
	if err != nil || year < 1970 || year > 2100 {
		writeError(w, http.StatusBadRequest, errors.New("invalid year"))
		return
	}
	sources, err := calendarSourceIDs(query.Get("sources"))
	if err != nil || len(sources) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("expected 1 to 32 sources"))
		return
	}
	if query.Get("stream") != "1" {
		result := []state.ArchiveCalendarProduct{}
		err := s.store.VisitArchiveCalendar(r.Context(), sources, year, func(row state.ArchiveCalendarProduct) error {
			result = append(result, row)
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeCoverage(w, r, result)
		return
	}
	stream := &ndjsonWriter{w: w}
	visit := func(row state.ArchiveCalendarProduct) error {
		if !stream.started {
			w.Header().Set("Server-Timing", fmt.Sprintf("calendar;dur=%.1f", float64(time.Since(started))/float64(time.Millisecond)))
		}
		if err := stream.encode(row); err != nil {
			return err
		}
		// Flush the first calendar on its own, then amortize the cost of
		// writes and compression over the next several products.
		if !stream.started || stream.buffer.Len() >= 4096 {
			return stream.flush()
		}
		return nil
	}
	if err := s.store.VisitArchiveCalendar(r.Context(), sources, year, visit); err != nil {
		if !stream.started {
			writeError(w, http.StatusInternalServerError, err)
		} else if r.Context().Err() == nil {
			stream.failure("archive calendar stream interrupted")
		}
		return
	}
	// Mark the snapshot complete so a cut connection is safe to retry.
	stream.finish()
}
