package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/producttime"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type downloadTaskProduct struct {
	state.DownloadRequest
	Satellite   string              `json:"satellite"`
	Instrument  string              `json:"instrument"`
	ProductCode string              `json:"product_code"`
	ProductName string              `json:"product_name"`
	Version     string              `json:"version"`
	Resolution  string              `json:"resolution"`
	TimeProfile producttime.Profile `json:"time_profile"`
}

type downloadTaskView struct {
	state.DownloadGroup
	Kind      string                `json:"kind"`
	Key       string                `json:"key"`
	RequestID int64                 `json:"request_id,omitempty"`
	Downloads []downloadTaskProduct `json:"downloads"`
}

func (s *Server) downloadTaskViews(ctx context.Context, tasks []state.DownloadGroup) ([]downloadTaskView, error) {
	products, err := s.store.DownloadTaskProducts(ctx)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]model.Product, len(products))
	for _, p := range products {
		byKey[p.ProductKey] = provider.NormalizeProduct(p)
	}
	items := make([]downloadTaskView, 0, len(tasks))
	for _, task := range tasks {
		item := downloadTaskView{DownloadGroup: task, Kind: "group", Key: fmt.Sprintf("group:%d", task.ID), Downloads: make([]downloadTaskProduct, 0, len(task.Downloads))}
		for _, request := range task.Downloads {
			p := byKey[request.CollectionID]
			item.Downloads = append(item.Downloads, downloadTaskProduct{DownloadRequest: request,
				Satellite: p.Satellite, Instrument: p.Instrument, ProductCode: p.DataType, ProductName: p.DisplayName, Version: p.Version, Resolution: p.Resolution,
				TimeProfile: producttime.Resolve(p)})
		}
		if len(task.Downloads) == 1 {
			item.State = task.Downloads[0].State
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Server) downloadTasks(w http.ResponseWriter, r *http.Request) {
	user, ok := s.orderUser(w, r)
	if !ok {
		return
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 1000000 {
			writeError(w, http.StatusBadRequest, errors.New("invalid task page"))
			return
		}
	}
	tasks, total, err := s.store.DownloadTasksPage(r.Context(), page, user.ID, user.Role == "admin")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	groups := make([]state.DownloadGroup, len(tasks))
	for i := range tasks {
		groups[i] = tasks[i].DownloadGroup
	}
	items, err := s.downloadTaskViews(r.Context(), groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for i := range items {
		items[i].Kind = tasks[i].Kind
		items[i].Key = tasks[i].Key
		items[i].RequestID = tasks[i].RequestID
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items, "page": page,
		"page_size": state.DownloadTaskPageSize, "total": total,
		"pages": (total + state.DownloadTaskPageSize - 1) / state.DownloadTaskPageSize})
}
