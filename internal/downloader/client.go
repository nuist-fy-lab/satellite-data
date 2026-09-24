package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	socket string
	http   *http.Client
}

type TaskSpec struct {
	Key          string `json:"key,omitempty"`
	SourceID     string `json:"source_id"`
	CollectionID string `json:"collection_id"`
	GranuleID    string `json:"granule_id"`
	AssetID      string `json:"asset_id"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	AuthRef      string `json:"auth_ref,omitempty"`
	RelativeDir  string `json:"relative_dir"`
	Size         int64  `json:"size,omitempty"`
	ETag         string `json:"etag,omitempty"`
	MediaType    string `json:"media_type,omitempty"`
}

type Task struct {
	ID           int64  `json:"id"`
	Key          string `json:"key"`
	SourceID     string `json:"source_id"`
	CollectionID string `json:"collection_id"`
	GranuleID    string `json:"granule_id"`
	AssetID      string `json:"asset_id"`
	Name         string `json:"name"`
	State        string `json:"state"`
	LastError    string `json:"last_error,omitempty"`
}

type ProviderStatus struct {
	SourceID       string         `json:"source_id"`
	Enabled        bool           `json:"enabled"`
	AuthRef        string         `json:"auth_ref,omitempty"`
	AuthConfigured bool           `json:"auth_configured"`
	States         map[string]int `json:"states"`
}

func New(socket string) *Client {
	socket = strings.TrimSpace(socket)
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
		},
	}
	return &Client{
		socket: socket,
		http:   &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

func (c *Client) Enqueue(ctx context.Context, spec TaskSpec) (Task, bool, error) {
	var payload struct {
		Created bool `json:"created"`
		Task    Task `json:"task"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/tasks", spec, &payload); err != nil {
		return Task{}, false, err
	}
	return payload.Task, payload.Created, nil
}

func (c *Client) TaskStatuses(ctx context.Context, ids []int64) ([]Task, error) {
	var payload struct {
		Tasks []Task `json:"tasks"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/tasks/status", map[string]any{"ids": ids}, &payload); err != nil {
		return nil, err
	}
	return payload.Tasks, nil
}

func (c *Client) Providers(ctx context.Context) ([]ProviderStatus, error) {
	var payload struct {
		Providers []ProviderStatus `json:"providers"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/providers", nil, &payload); err != nil {
		return nil, err
	}
	return payload.Providers, nil
}

func (c *Client) Retry(ctx context.Context, id int64) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/tasks/"+strconv.FormatInt(id, 10)+"/retry", nil, nil)
}

func (c *Client) Health(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/healthz", nil, nil)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, target any) error {
	if c == nil || c.socket == "" {
		return errors.New("satellite downloader socket is not configured")
	}
	var requestBody *bytes.Reader
	if body == nil {
		requestBody = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, requestBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("downloader unavailable on %s: %w", c.socket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&problem)
		if problem.Error == "" {
			problem.Error = resp.Status
		}
		return errors.New(problem.Error)
	}
	if target == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(target)
}
