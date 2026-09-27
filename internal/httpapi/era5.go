package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/nuist-fy-lab/satellite-data/internal/central"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
)

func (s *Server) era5Provider(w http.ResponseWriter) *era5.Provider {
	if s.registry == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("ERA5 provider unavailable"))
		return nil
	}
	p, err := s.registry.Provider("era5")
	if err == nil {
		if cds, ok := p.(*era5.Provider); ok {
			return cds
		}
	}
	writeError(w, http.StatusServiceUnavailable, errors.New("ERA5 provider unavailable"))
	return nil
}

func (s *Server) era5Schema(w http.ResponseWriter, r *http.Request) {
	p := s.era5Provider(w)
	if p == nil {
		return
	}
	process, err := p.Process(r.Context(), r.URL.Query().Get("collection"))
	if err != nil {
		writeERA5Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, process)
}

func (s *Server) era5Submit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.authorizeAdminMutation(w, r) {
		return
	}
	var submission era5.Submission
	if err := decodeRequestJSON(w, r, &submission); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.era5Provider(w) == nil {
		return
	}
	if s.central == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central access store unavailable"))
		return
	}
	job, err := s.central.Submit(r.Context(), submission)
	if err != nil {
		writeERA5Error(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/era5/requests/"+job.ID)
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) era5Job(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, err := s.adminUser(r); err != nil {
		s.writeAdminError(w, r)
		return
	}
	if s.era5Provider(w) == nil {
		return
	}
	if s.central == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central access store unavailable"))
		return
	}
	job, err := s.central.Job(r.Context(), r.PathValue("id"))
	if err != nil {
		writeERA5Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) era5Result(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, err := s.adminUser(r); err != nil {
		s.writeAdminError(w, r)
		return
	}
	if s.era5Provider(w) == nil {
		return
	}
	if s.central == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central access store unavailable"))
		return
	}
	asset, err := s.central.Result(r.Context(), r.PathValue("id"))
	if err != nil {
		writeERA5Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}

func writeERA5Error(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var response *era5.HTTPError
	switch {
	case errors.Is(err, central.ErrNoRoute):
		status = http.StatusServiceUnavailable
	case errors.Is(err, central.ErrChangedCredential):
		status = http.StatusConflict
	case errors.Is(err, sql.ErrNoRows):
		status = http.StatusNotFound
		err = errors.New("CDS centre job route not found")
	case errors.Is(err, era5.ErrCredentialsRequired):
		status = http.StatusServiceUnavailable
	case errors.Is(err, provider.ErrUnsupportedCollection):
		status = http.StatusBadRequest
	case errors.Is(err, era5.ErrInvalidRequest):
		status = http.StatusBadRequest
	case errors.As(err, &response):
		if response.Status == http.StatusBadRequest || response.Status == http.StatusUnprocessableEntity {
			status = http.StatusBadRequest
		}
		if response.Status == http.StatusNotFound {
			status = http.StatusNotFound
		}
		if response.Status == http.StatusConflict {
			status = http.StatusConflict
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeError(w, status, err)
}
