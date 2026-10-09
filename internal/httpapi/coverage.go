package httpapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
)

func (s *Server) coverageProduct(w http.ResponseWriter, r *http.Request) (string, bool) {
	product := strings.TrimSpace(r.URL.Query().Get("product"))
	_, info, err := s.store.ProductSource(r.Context(), product)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("unknown product"))
		return "", false
	}
	if downloadfilter.ForProduct(info.SourceID, info.ProductKey) == nil {
		writeError(w, http.StatusBadRequest, errors.New("band coverage unavailable for product"))
		return "", false
	}
	return product, true
}

func (s *Server) coverageYear(w http.ResponseWriter, r *http.Request) {
	product, ok := s.coverageProduct(w, r)
	if !ok {
		return
	}
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil || year < 1970 || year > 2100 {
		writeError(w, http.StatusBadRequest, errors.New("invalid year"))
		return
	}
	if err := s.store.EnqueueMissingCalendarYear(r.Context(), product, year); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	payload, err := s.store.BandCoverageYear(r.Context(), product, year)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeCoverage(w, r, payload)
}

func (s *Server) coverageMonth(w http.ResponseWriter, r *http.Request) {
	product, ok := s.coverageProduct(w, r)
	if !ok {
		return
	}
	year, yearErr := strconv.Atoi(r.URL.Query().Get("year"))
	month, monthErr := strconv.Atoi(r.URL.Query().Get("month"))
	band, bandErr := strconv.Atoi(r.URL.Query().Get("band"))
	mode := r.URL.Query().Get("time")
	if yearErr != nil || year < 1970 || year > 2100 || monthErr != nil || month < 1 || month > 12 ||
		bandErr != nil || band < 0 || (mode != "all" && mode != "hourly") {
		writeError(w, http.StatusBadRequest, errors.New("invalid coverage selection"))
		return
	}
	payload, err := s.store.BandCoverageMonth(r.Context(), product, year, month, band, mode == "hourly")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeCoverage(w, r, payload)
}

func (s *Server) coverageDay(w http.ResponseWriter, r *http.Request) {
	product, ok := s.coverageProduct(w, r)
	if !ok {
		return
	}
	day := strings.TrimSpace(r.URL.Query().Get("day"))
	at, err := time.Parse(time.DateOnly, day)
	if err != nil || at.Format(time.DateOnly) != day {
		writeError(w, http.StatusBadRequest, errors.New("invalid day"))
		return
	}
	if !at.After(time.Now().UTC()) {
		if err := s.store.EnqueueRange(r.Context(), product, at, at.AddDate(0, 0, 1), 100); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	payload, err := s.store.BandCoverageDay(r.Context(), product, day)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeCoverage(w, r, payload)
}

func (s *Server) coverageFiles(w http.ResponseWriter, r *http.Request) {
	product, ok := s.coverageProduct(w, r)
	if !ok {
		return
	}
	band, err := strconv.Atoi(r.URL.Query().Get("band"))
	if err != nil || band < 1 {
		writeError(w, http.StatusBadRequest, errors.New("invalid band"))
		return
	}
	files, err := s.store.BandCoverageFiles(r.Context(), product, r.URL.Query().Get("slot"), band)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func writeCoverage(w http.ResponseWriter, r *http.Request, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	digest := sha256.Sum256(data)
	etag := `W/"` + hex.EncodeToString(digest[:16]) + `"`
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if len(data) >= 1024 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(data); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if err := writer.Close(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		data = compressed.Bytes()
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
