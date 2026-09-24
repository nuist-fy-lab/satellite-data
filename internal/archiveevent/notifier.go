package archiveevent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

type Event struct {
	TaskID       int64  `json:"task_id"`
	SourceID     string `json:"source_id"`
	CollectionID string `json:"collection_id"`
	GranuleID    string `json:"granule_id"`
	Filename     string `json:"filename"`
	ArchivePath  string `json:"archive_path"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
}

type Notifier struct {
	socket string
	queue  chan Event
}

func New(socket string) *Notifier {
	return &Notifier{socket: strings.TrimSpace(socket), queue: make(chan Event, 1024)}
}

func (n *Notifier) Run(ctx context.Context) {
	if n == nil || n.socket == "" {
		return
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", n.socket)
			},
		},
		Timeout: 2 * time.Second,
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-n.queue:
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/archive-event", bytes.NewReader(payload))
			if err != nil {
				continue
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				slog.Debug("archive event notification unavailable", "error", err)
				continue
			}
			_ = resp.Body.Close()
		}
	}
}

func (n *Notifier) Notify(event Event) {
	if n == nil || n.socket == "" {
		return
	}
	select {
	case n.queue <- event:
	default:
		// The event is only a low-latency hint. The satellite-data periodic local
		// scan remains authoritative, so transfer promotion must never block here.
	}
}
