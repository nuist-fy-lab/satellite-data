package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nuist-fy-lab/satellite-data/internal/central"
)

func (s *Server) centralMutation(w http.ResponseWriter, r *http.Request) bool {
	if !s.authorizeAdminMutation(w, r) {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	if s.central == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central credential vault unavailable"))
		return false
	}
	return true
}
func (s *Server) centralCredential(w http.ResponseWriter, r *http.Request) {
	if !s.centralMutation(w, r) {
		return
	}
	var payload central.CredentialSpec
	if err := decodeRequestJSON(w, r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.central.Store.UpsertCredential(r.Context(), payload)
	if err != nil {
		centralPoolError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential": item})
}
func (s *Server) centralProxy(w http.ResponseWriter, r *http.Request) {
	if !s.centralMutation(w, r) {
		return
	}
	var payload central.ProxySpec
	if err := decodeRequestJSON(w, r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.central.Store.UpsertProxy(r.Context(), payload)
	if err != nil {
		centralPoolError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxy": item})
}
func (s *Server) centralAccount(w http.ResponseWriter, r *http.Request) {
	if !s.centralMutation(w, r) {
		return
	}
	var payload central.AccountSpec
	if err := decodeRequestJSON(w, r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.central.Store.UpsertAccount(r.Context(), payload)
	if err != nil {
		centralPoolError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": item})
}
func (s *Server) centralProfile(w http.ResponseWriter, r *http.Request) {
	if !s.centralMutation(w, r) {
		return
	}
	var payload central.ProfileSpec
	if err := decodeRequestJSON(w, r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.central.Store.UpsertProfile(r.Context(), payload)
	if err != nil {
		centralPoolError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": item})
}
func (s *Server) centralDelete(w http.ResponseWriter, r *http.Request) {
	if !s.centralMutation(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid central item id"))
		return
	}
	if err := s.central.Store.Delete(r.Context(), r.PathValue("kind"), id); err != nil {
		centralPoolError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func centralPoolError(w http.ResponseWriter, err error) {
	message := err.Error()
	if strings.Contains(message, "UNIQUE constraint") {
		message = "alias already exists in the central pool"
	}
	if strings.Contains(message, "constraint") || strings.Contains(message, "SQLITE") {
		message = "central pool update rejected"
	}
	writeError(w, http.StatusBadRequest, errors.New(message))
}
