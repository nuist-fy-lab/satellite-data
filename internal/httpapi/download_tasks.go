package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
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
	UpdatedAt time.Time             `json:"updated_at,omitempty"`
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

type downloadTaskSnapshot struct {
	items      []downloadTaskView
	itemHashes map[string]string
	orderKeys  []string
	total      int
	page       int
	pageSize   int
	pages      int
	hasMore    bool
	nextCursor string
	cursor     string
}

func (s *Server) buildDownloadTaskSnapshot(ctx context.Context, user authstore.User, useCursor bool, rawCursor string, page, limit int) (downloadTaskSnapshot, int, error) {
	var (
		tasks      []state.DownloadTaskListItem
		total      int
		hasMore    bool
		nextCursor string
		pageSize   = limit
		err        error
	)
	if useCursor {
		var cursorPtr *state.DownloadTaskCursor
		if rawCursor != "" {
			parsed, parseErr := state.ParseDownloadTaskCursor(rawCursor)
			if parseErr != nil {
				return downloadTaskSnapshot{}, http.StatusBadRequest, errors.New("invalid task cursor")
			}
			cursorPtr = &parsed
		}
		tasks, total, hasMore, nextCursor, err = s.store.DownloadTasksCursorPage(ctx, cursorPtr, pageSize, user.ID, user.Role == "admin")
		if err != nil {
			return downloadTaskSnapshot{}, http.StatusInternalServerError, err
		}
		page = 1
	} else {
		pageSize = state.DownloadTaskPageSize
		tasks, total, err = s.store.DownloadTasksPage(ctx, page, user.ID, user.Role == "admin")
		if err != nil {
			return downloadTaskSnapshot{}, http.StatusInternalServerError, err
		}
		hasMore = page*pageSize < total
		if hasMore && len(tasks) > 0 {
			last := tasks[len(tasks)-1]
			nextCursor = state.EncodeDownloadTaskCursor(last.Stamp, last.Kind, last.ID)
		}
	}

	groups := make([]state.DownloadGroup, len(tasks))
	for i := range tasks {
		groups[i] = tasks[i].DownloadGroup
	}
	items, err := s.downloadTaskViews(ctx, groups)
	if err != nil {
		return downloadTaskSnapshot{}, http.StatusInternalServerError, err
	}
	itemHashes := make(map[string]string, len(items))
	orderKeys := make([]string, len(items))
	overall := sha256.New()
	_, _ = fmt.Fprintf(overall, "t=%d;p=%d;s=%d;m=%t;n=%s;", total, page, pageSize, hasMore, nextCursor)
	for i := range items {
		items[i].Kind = tasks[i].Kind
		items[i].Key = tasks[i].Key
		items[i].RequestID = tasks[i].RequestID
		items[i].UpdatedAt = tasks[i].UpdatedAt
		encoded, _ := json.Marshal(items[i])
		sum := sha256.Sum256(encoded)
		itemHash := hex.EncodeToString(sum[:12])
		itemHashes[items[i].Key] = itemHash
		orderKeys[i] = items[i].Key
		_, _ = fmt.Fprintf(overall, "%s=%s;", items[i].Key, itemHash)
	}
	pages := (total + pageSize - 1) / pageSize
	syncCursor := hex.EncodeToString(overall.Sum(nil)[:16])
	return downloadTaskSnapshot{
		items:      items,
		itemHashes: itemHashes,
		orderKeys:  orderKeys,
		total:      total,
		page:       page,
		pageSize:   pageSize,
		pages:      pages,
		hasMore:    hasMore,
		nextCursor: nextCursor,
		cursor:     syncCursor,
	}, http.StatusOK, nil
}

func (s *Server) downloadTasks(w http.ResponseWriter, r *http.Request) {
	user, ok := s.orderUser(w, r)
	if !ok {
		return
	}
	useCursor := r.URL.Query().Has("cursor")
	rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	limit := state.DownloadTaskPageSize
	if useCursor {
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 {
				writeError(w, http.StatusBadRequest, errors.New("invalid task limit"))
				return
			}
			if parsed > 200 {
				parsed = 200
			}
			limit = parsed
		}
	}
	page := 1
	if !useCursor {
		if raw := r.URL.Query().Get("page"); raw != "" {
			var err error
			page, err = strconv.Atoi(raw)
			if err != nil || page < 1 || page > 1000000 {
				writeError(w, http.StatusBadRequest, errors.New("invalid task page"))
				return
			}
		}
	}
	snap, code, err := s.buildDownloadTaskSnapshot(r.Context(), user, useCursor, rawCursor, page, limit)
	if err != nil {
		writeError(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":       snap.items,
		"page":        snap.page,
		"page_size":   snap.pageSize,
		"total":       snap.total,
		"pages":       snap.pages,
		"has_more":    snap.hasMore,
		"next_cursor": snap.nextCursor,
		"cursor":      snap.cursor,
	})
}

func (s *Server) downloadTasksStream(w http.ResponseWriter, r *http.Request) {
	user, ok := s.orderUser(w, r)
	if !ok {
		return
	}
	limit := state.DownloadTaskPageSize
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, errors.New("invalid task limit"))
			return
		}
		limit = parsed
	}
	page := 1
	useCursor := true
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000000 {
			writeError(w, http.StatusBadRequest, errors.New("invalid task page"))
			return
		}
		if parsed > 1 {
			useCursor = false
		}
		page = parsed
	}
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	if since == "" {
		since = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}

	snap, code, err := s.buildDownloadTaskSnapshot(r.Context(), user, useCursor, "", page, limit)
	if err != nil {
		writeError(w, code, err)
		return
	}
	stream, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if since != "" && since == snap.cursor {
		if _, err := stream.eventWithID("sync", "sync", snap.cursor, map[string]any{
			"total":       snap.total,
			"page":        snap.page,
			"page_size":   snap.pageSize,
			"pages":       snap.pages,
			"has_more":    snap.hasMore,
			"next_cursor": snap.nextCursor,
			"cursor":      snap.cursor,
		}, false); err != nil {
			return
		}
	} else {
		if _, err := stream.eventWithID("tasks", "tasks", snap.cursor, map[string]any{
			"tasks":       snap.items,
			"total":       snap.total,
			"page":        snap.page,
			"page_size":   snap.pageSize,
			"pages":       snap.pages,
			"has_more":    snap.hasMore,
			"next_cursor": snap.nextCursor,
			"cursor":      snap.cursor,
		}, false); err != nil {
			return
		}
	}

	prevCursor := snap.cursor
	prevHashes := snap.itemHashes
	updates := time.NewTicker(2 * time.Second)
	heartbeat := time.NewTicker(15 * time.Second)
	defer updates.Stop()
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates.C:
			next, _, err := s.buildDownloadTaskSnapshot(r.Context(), user, useCursor, "", page, limit)
			if err != nil || next.cursor == prevCursor {
				continue
			}
			for _, item := range next.items {
				if prevHashes[item.Key] != next.itemHashes[item.Key] {
					if _, err := stream.eventWithID("task", "", next.cursor, map[string]any{
						"task":   item,
						"cursor": next.cursor,
					}, false); err != nil {
						return
					}
				}
			}
			var removed []string
			for oldKey := range prevHashes {
				if _, ok := next.itemHashes[oldKey]; !ok {
					removed = append(removed, oldKey)
				}
			}
			if len(removed) > 0 {
				if _, err := stream.eventWithID("tasks_removed", "", next.cursor, map[string]any{
					"keys":   removed,
					"cursor": next.cursor,
				}, false); err != nil {
					return
				}
			}
			if _, err := stream.eventWithID("tasks_meta", "", next.cursor, map[string]any{
				"total":       next.total,
				"page":        next.page,
				"page_size":   next.pageSize,
				"pages":       next.pages,
				"has_more":    next.hasMore,
				"next_cursor": next.nextCursor,
				"cursor":      next.cursor,
				"order":       next.orderKeys,
			}, false); err != nil {
				return
			}
			prevCursor = next.cursor
			prevHashes = next.itemHashes
		case <-heartbeat.C:
			if err := stream.heartbeat(); err != nil {
				return
			}
		}
	}
}
