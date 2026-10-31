package central

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

type CloudSatMetadataClient struct{ client *http.Client }

func NewCloudSatMetadataClient(socket string) *CloudSatMetadataClient {
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &CloudSatMetadataClient{client: &http.Client{Transport: t}}
}

func (c *CloudSatMetadataClient) Fetch(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("invalid metadata size limit")
	}
	payload, err := json.Marshal(struct {
		Source string `json:"source_id"`
		URL    string `json:"url"`
	}{"cloudsat", endpoint})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://central/v1/fetch", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CloudSat metadata gateway: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CloudSat metadata gateway HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("CloudSat metadata response exceeds limit")
	}
	return data, nil
}
