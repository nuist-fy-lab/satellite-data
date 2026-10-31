package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadcoord"
	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/producttime"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func (s *Server) createDownloadGroup(w http.ResponseWriter, r *http.Request) {
	s.submitDownloadTask(w, r, false)
}

func (s *Server) createDownloadTask(w http.ResponseWriter, r *http.Request) {
	s.submitDownloadTask(w, r, true)
}

type downloadProductOptions struct {
	Start      string               `json:"start"`
	End        string               `json:"end"`
	Band       *int                 `json:"band"`
	Times      *[]string            `json:"times"`
	Scenes     *map[string][]string `json:"scenes"`
	GranuleIDs json.RawMessage      `json:"granule_ids"`
}

func parseGranuleIDs(raw json.RawMessage) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var ids []string
	if string(raw) == "null" || json.Unmarshal(raw, &ids) != nil || len(ids) == 0 {
		return nil, errors.New("granule_ids must be a nonempty array")
	}
	return ids, nil
}

func (s *Server) submitDownloadTask(w http.ResponseWriter, r *http.Request, taskResponse bool) {
	user, ok := s.orderUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Products   []string                          `json:"products"`
		Start      string                            `json:"start"`
		End        string                            `json:"end"`
		Band       int                               `json:"band"`
		Times      []string                          `json:"times"`
		Scenes     map[string][]string               `json:"scenes"`
		GranuleIDs json.RawMessage                   `json:"granule_ids"`
		Inputs     map[string]json.RawMessage        `json:"inputs"`
		Options    map[string]downloadProductOptions `json:"options"`
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
	products, err := s.store.DownloadTaskProducts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	byKey := make(map[string]model.Product, len(products))
	for _, product := range products {
		byKey[product.ProductKey] = product
	}
	for _, key := range req.Products {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			writeError(w, http.StatusBadRequest, errors.New("blank or duplicated product"))
			return
		}
		seen[key] = true
		from, until := start, end
		band, times, scenes := req.Band, req.Times, req.Scenes
		granuleRaw := req.GranuleIDs
		if options, exists := req.Options[key]; exists {
			if options.Start != "" {
				from, err = parseTime(options.Start)
				if err != nil {
					writeError(w, http.StatusBadRequest, errors.New("invalid product start"))
					return
				}
			}
			if options.End != "" {
				until, err = parseTime(options.End)
				if err != nil {
					writeError(w, http.StatusBadRequest, errors.New("invalid product end"))
					return
				}
			}
			if options.Band != nil {
				band = *options.Band
			}
			if options.Times != nil {
				times = *options.Times
			}
			if options.Scenes != nil {
				scenes = *options.Scenes
			}
			if options.GranuleIDs != nil {
				granuleRaw = options.GranuleIDs
			}
		}
		granuleIDs, err := parseGranuleIDs(granuleRaw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !until.After(from) {
			writeError(w, http.StatusBadRequest, errors.New("product end must follow start"))
			return
		}
		product, exists := byKey[key]
		if !exists {
			writeError(w, http.StatusNotFound, errors.New("unknown product "+key))
			return
		}
		if taskResponse {
			profile := producttime.Resolve(provider.NormalizeProduct(product))
			if err := profile.ValidateRange(from, until); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if err := profile.ValidateTimes(times, scenes); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		source := product.SourceID
		if len(granuleIDs) > 0 && !availableGranuleProduct(source, key) {
			writeError(w, http.StatusBadRequest, errors.New("granule selection unavailable for product"))
			return
		}
		providerID, ok := downloadcoord.ProviderForSource(source)
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("unsupported source "+source))
			return
		}
		selection, err := (downloadfilter.Selection{Band: band, Times: times, Scenes: scenes, GranuleIDs: granuleIDs}).Normalize(source, key)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		for day := range selection.Scenes {
			at, _ := time.Parse(time.DateOnly, day)
			if at.Before(from) || !at.Before(until) {
				writeError(w, http.StatusBadRequest, errors.New("scene date lies outside the requested UTC interval"))
				return
			}
		}
		if source != "era5" && len(req.Inputs) > 0 {
			writeError(w, http.StatusBadRequest, errors.New("on-demand inputs cannot apply to ordinary archived products"))
			return
		}
		if source == "era5" && band != 0 {
			writeError(w, http.StatusBadRequest, errors.New("ERA5 uses variables, not bands"))
			return
		}
		specs = append(specs, state.GroupProduct{Source: source, Provider: providerID,
			Collection: key, Start: &from, End: &until, Selection: selection, Inputs: req.Inputs})
	}
	for key := range req.Options {
		if !seen[key] {
			writeError(w, http.StatusBadRequest, errors.New("product settings must belong to the selected products"))
			return
		}
	}
	owner := user.ID
	if owner == "" && !s.portalAuth {
		owner = "local"
	}
	group, created, err := s.store.EnqueueDownloadGroup(r.Context(), owner, start, end, specs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Order discovery is already scheduled by downloadcoord. Previously this
	// synchronous indexing step held a committed response behind a busy
	// SQLite writer for >30 seconds, leading reverse proxies to return 502
	// even though the order was safely persisted.
	if taskResponse {
		tasks, err := s.downloadTaskViews(r.Context(), []state.DownloadGroup{group})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"task": tasks[0], "created": created})
		return
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
