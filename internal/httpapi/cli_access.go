package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/nuist-fy-lab/satellite-data/internal/central"
	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
)

const cliAccessPath = "/api/v1/cli/access"

type cliCredentialView struct {
	ID        int64  `json:"id"`
	Alias     string `json:"alias"`
	Kind      string `json:"kind"`
	Enabled   bool   `json:"enabled"`
	State     string `json:"state"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type cliProxyView struct {
	ID        int64    `json:"id"`
	Alias     string   `json:"alias"`
	Scheme    string   `json:"scheme"`
	Providers []string `json:"providers"`
	Enabled   bool     `json:"enabled"`
	State     string   `json:"state"`
}

type cliProfileView struct {
	central.AccountSpec
	State string `json:"state"`
}

func accessProfileView(account downloader.Account) cliProfileView {
	return cliProfileView{AccountSpec: central.AccountSpec{
		ID: account.ID, Alias: account.Alias, SourceID: account.SourceID, CredentialID: account.CredentialID,
		ProxyID: account.ProxyID, ProxyMode: account.ProxyMode, Enabled: account.Enabled, Priority: account.Priority,
	}, State: account.State}
}

func (s *Server) cliAccessList(w http.ResponseWriter, r *http.Request) {
	if s.central == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central access store unavailable"))
		return
	}
	pools, err := s.central.Store.Pools(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central access store unavailable"))
		return
	}
	credentials := make([]cliCredentialView, 0, len(pools.Credentials))
	for _, item := range pools.Credentials {
		credentials = append(credentials, cliCredentialView{
			ID: item.ID, Alias: item.Alias, Kind: item.Kind, Enabled: item.Enabled, State: item.State, ExpiresAt: item.ExpiresAt,
		})
	}
	proxies := make([]cliProxyView, 0, len(pools.Proxies))
	for _, item := range pools.Proxies {
		proxies = append(proxies, cliProxyView{
			ID: item.ID, Alias: item.Alias, Scheme: item.Scheme, Providers: item.Providers, Enabled: item.Enabled, State: item.State,
		})
	}
	profiles := make([]cliProfileView, 0, len(pools.Accounts))
	for _, item := range pools.Accounts {
		profiles = append(profiles, accessProfileView(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": credentials, "proxies": proxies, "profiles": profiles})
}

func (s *Server) cliAccessSave(w http.ResponseWriter, r *http.Request) {
	if s.central == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central access store unavailable"))
		return
	}
	var spec central.AccountSpec
	if err := decodeRequestJSON(w, r, &spec); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	profile, err := s.central.Store.UpsertProfile(r.Context(), central.ProfileSpec{AccountSpec: spec})
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, errors.New("access profile not found"))
		return
	}
	if err != nil {
		centralPoolError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accessProfileView(profile))
}

func (s *Server) cliAccessDelete(w http.ResponseWriter, r *http.Request) {
	if s.central == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central access store unavailable"))
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid access profile ID"))
		return
	}
	err = s.central.Store.Delete(r.Context(), "accounts", id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, errors.New("access profile not found"))
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central access store unavailable"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
