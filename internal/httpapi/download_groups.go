package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadcoord"
	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func (s *Server) createDownloadGroup(w http.ResponseWriter, r *http.Request) {
	user, ok := s.orderUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Products []string                   `json:"products"`
		Start    string                     `json:"start"`
		End      string                     `json:"end"`
		Band     int                        `json:"band"`
		Times    []string                   `json:"times"`
		Inputs   map[string]json.RawMessage `json:"inputs"`
	}
	if err := decodeRequestJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Products) < 1 || len(req.Products) > 32 {
		writeError(w, http.StatusBadRequest, errors.New("select 1–32 products"))
		return
	}
	start, err := parseTime(req.Start)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid start"))
		return
	}
	end, err := parseTime(req.End)
	if err != nil || !end.After(start) {
		writeError(w, http.StatusBadRequest, errors.New("invalid end"))
		return
	}
	specs := make([]state.GroupProduct, 0, len(req.Products))
	seen := make(map[string]bool, len(req.Products))
	for _, key := range req.Products {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			writeError(w, http.StatusBadRequest, errors.New("blank or duplicated product"))
			return
		}
		seen[key] = true
		source, _, err := s.store.ProductSource(r.Context(), key)
		if err != nil {
			writeError(w, http.StatusNotFound, errors.New("unknown product "+key))
			return
		}
		providerID, ok := downloadcoord.ProviderForSource(source)
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("unsupported source "+source))
			return
		}
		selection, err := (downloadfilter.Selection{Band: req.Band, Times: req.Times}).Normalize(source, key)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if source != "era5" && len(req.Inputs) > 0 {
			writeError(w, http.StatusBadRequest, errors.New("on-demand inputs cannot apply to ordinary archived products"))
			return
		}
		if source == "era5" && req.Band != 0 {
			writeError(w, http.StatusBadRequest, errors.New("ERA5 uses variables, not bands"))
			return
		}
		specs = append(specs, state.GroupProduct{Source: source, Provider: providerID,
			Collection: key, Selection: selection, Inputs: req.Inputs})
	}
	group, created, err := s.store.EnqueueDownloadGroup(r.Context(), user.ID, start, end, specs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if created {
		for _, spec := range specs {
			if spec.Source != "era5" {
				// Catalogue indexing never blocks persistent order scheduling.
				_ = s.store.EnqueueRange(r.Context(), spec.Collection, start, end, 200)
			}
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"group": group, "created": created})
}

func (s *Server) downloadGroup(w http.ResponseWriter, r *http.Request) {
	user, ok := s.orderUser(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid group id"))
		return
	}
	group, err := s.store.DownloadGroupOwned(r.Context(), id, user.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("download group not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": group})
}

func (s *Server) downloadGroups(w http.ResponseWriter, r *http.Request) {
	user, ok := s.orderUser(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.DownloadGroupsByOwner(r.Context(), user.ID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": items})
}
