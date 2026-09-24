package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/access"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/auth"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/model"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/pipeline"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/store"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/vault"
)

type Server struct {
	store    *store.Store
	pipeline *pipeline.Runtime
	vault    *vault.Vault
}

func New(state *store.Store, runtime *pipeline.Runtime, secretVault *vault.Vault) http.Handler {
	s := &Server{store: state, pipeline: runtime, vault: secretVault}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/providers", s.providers)
	mux.HandleFunc("POST /v1/tasks", s.enqueue)
	mux.HandleFunc("GET /v1/tasks", s.tasks)
	mux.HandleFunc("POST /v1/tasks/status", s.taskStatuses)
	mux.HandleFunc("GET /v1/tasks/{id}", s.task)
	mux.HandleFunc("POST /v1/tasks/{id}/retry", s.retry)
	mux.HandleFunc("GET /v1/access", s.accessPools)
	mux.HandleFunc("POST /v1/credentials", s.upsertCredential)
	mux.HandleFunc("DELETE /v1/credentials/{id}", s.deleteCredential)
	mux.HandleFunc("POST /v1/proxies", s.upsertProxy)
	mux.HandleFunc("DELETE /v1/proxies/{id}", s.deleteProxy)
	mux.HandleFunc("POST /v1/accounts", s.upsertAccount)
	mux.HandleFunc("DELETE /v1/accounts/{id}", s.deleteAccount)
	return withHeaders(mux)
}

func (s *Server) accessPools(w http.ResponseWriter, r *http.Request) {
	credentials, err := s.store.Credentials(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	proxies, err := s.store.Proxies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	accounts, err := s.store.Accounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": credentials, "proxies": proxies, "accounts": accounts})
}

func (s *Server) upsertCredential(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID       int64       `json:"id,omitempty"`
		Alias    string      `json:"alias"`
		Kind     string      `json:"kind"`
		Secret   auth.Secret `json:"secret"`
		Enabled  bool        `json:"enabled"`
		Priority int         `json:"priority"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var encrypted []byte
	var hint string
	secretEmpty := request.Secret == (auth.Secret{})
	if request.ID > 0 && secretEmpty {
		var existingKind string
		var err error
		encrypted, existingKind, err = s.store.CredentialSecret(r.Context(), request.ID)
		if err != nil {
			writeError(w, http.StatusNotFound, errors.New("credential not found"))
			return
		}
		if request.Kind == "" {
			request.Kind = existingKind
		}
		if request.Kind != existingKind {
			writeError(w, http.StatusBadRequest, errors.New("changing credential kind requires a new secret"))
			return
		}
		existing, _ := s.store.Credential(r.Context(), request.ID)
		hint = existing.SecretHint
	} else {
		if err := access.ValidateSecret(request.Kind, request.Secret); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		plain, err := json.Marshal(request.Secret)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		encrypted, err = s.vault.Seal(plain)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("credential vault unavailable"))
			return
		}
		hint = access.SecretHint(request.Kind, request.Secret)
	}
	item, err := s.store.UpsertCredential(r.Context(), request.ID, request.Alias, request.Kind, encrypted, hint, request.Enabled, request.Priority)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential": item})
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteCredential(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, errors.New("credential not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) upsertProxy(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID        int64    `json:"id,omitempty"`
		Alias     string   `json:"alias"`
		URL       string   `json:"url,omitempty"`
		Providers []string `json:"providers"`
		Enabled   bool     `json:"enabled"`
		Priority  int      `json:"priority"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	for _, provider := range request.Providers {
		if provider == "*" {
			continue
		}
		if _, err := access.ProviderKind(provider); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	var encrypted []byte
	var scheme, hint string
	if strings.TrimSpace(request.URL) == "" && request.ID > 0 {
		var err error
		encrypted, err = s.store.ProxySecret(r.Context(), request.ID)
		if err != nil {
			writeError(w, http.StatusNotFound, errors.New("proxy not found"))
			return
		}
		existing, _ := s.store.Proxy(r.Context(), request.ID)
		scheme, hint = existing.Scheme, existing.EndpointHint
	} else {
		parsed, endpoint, err := access.ParseProxy(request.URL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		scheme, hint = parsed.Scheme, endpoint
		encrypted, err = s.vault.Seal([]byte(parsed.String()))
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("credential vault unavailable"))
			return
		}
	}
	item, err := s.store.UpsertProxy(r.Context(), request.ID, request.Alias, encrypted, scheme, hint, request.Providers, request.Enabled, request.Priority)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxy": item})
}

func (s *Server) deleteProxy(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteProxy(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, errors.New("proxy not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) upsertAccount(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID           int64  `json:"id,omitempty"`
		Alias        string `json:"alias"`
		SourceID     string `json:"source_id"`
		CredentialID *int64 `json:"credential_id,omitempty"`
		ProxyMode    string `json:"proxy_mode"`
		ProxyID      *int64 `json:"proxy_id,omitempty"`
		Enabled      bool   `json:"enabled"`
		Priority     int    `json:"priority"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	requiredKind, err := access.ProviderKind(request.SourceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if requiredKind != "" {
		if request.CredentialID == nil {
			writeError(w, http.StatusBadRequest, errors.New("provider requires a credential"))
			return
		}
		credential, err := s.store.Credential(r.Context(), *request.CredentialID)
		if err != nil || credential.Kind != requiredKind {
			writeError(w, http.StatusBadRequest, errors.New("credential kind does not match provider"))
			return
		}
	} else if request.CredentialID != nil {
		writeError(w, http.StatusBadRequest, errors.New("anonymous provider must not bind a credential"))
		return
	}
	item, err := s.store.UpsertAccount(r.Context(), request.ID, request.Alias, request.SourceID, request.CredentialID, request.ProxyID, request.ProxyMode, request.Enabled, request.Priority)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": item})
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteAccount(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, errors.New("account not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) taskStatuses(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var payload struct {
		IDs []int64 `json:"ids"`
	}
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid task status JSON"))
		return
	}
	items, err := s.store.TasksByIDs(r.Context(), payload.IDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	items, err := s.pipeline.ProviderStatuses(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": items})
}

func (s *Server) enqueue(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var spec model.TaskSpec
	if err := decoder.Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid task JSON"))
		return
	}
	if strings.TrimSpace(spec.Key) == "" {
		spec.Key = taskKey(spec)
	}
	if err := s.pipeline.ValidateTask(spec); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "disabled") {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	task, created, err := s.store.Enqueue(r.Context(), spec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"created": created, "task": task})
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.Tasks(r.Context(), r.URL.Query().Get("source"), r.URL.Query().Get("state"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items})
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid task id"))
		return
	}
	item, err := s.store.Task(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": item})
}

func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid task id"))
		return
	}
	if err := s.store.RetryTask(r.Context(), id); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	item, err := s.store.Task(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": item})
}

func taskKey(spec model.TaskSpec) string {
	hash := sha256.New()
	for _, value := range []string{spec.SourceID, spec.CollectionID, spec.GranuleID, spec.AssetID, spec.Name} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid request JSON")
	}
	return nil
}

func pathID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
