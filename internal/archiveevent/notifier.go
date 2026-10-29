package archiveevent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/store"
)

type Notifier struct {
	socket string
	store  *store.Store
	wake   chan struct{}
}

func New(socket string, state *store.Store) *Notifier {
	return &Notifier{socket: strings.TrimSpace(socket), store: state, wake: make(chan struct{}, 1)}
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
		Timeout: 10 * time.Minute,
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		pending, err := n.store.PendingArchiveEvents(ctx, 16)
		if err != nil && ctx.Err() == nil {
			slog.Warn("load pending archive notifications", "error", err)
		}
		for _, task := range pending {
			event := Event{
				TaskID: task.ID, SourceID: task.SourceID, CollectionID: task.CollectionID,
				GranuleID: task.GranuleID, Filename: task.Name, ArchivePath: task.ArchivePath,
				Size: task.ObservedSize, SHA256: task.SHA256,
			}
			payload, err := json.Marshal(event)
			if err == nil {
				var req *http.Request
				req, err = http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/archive-event", bytes.NewReader(payload))
				if err == nil {
					req.Header.Set("Content-Type", "application/json")
					var resp *http.Response
					resp, err = client.Do(req)
					if err == nil {
						_ = resp.Body.Close()
						if resp.StatusCode != http.StatusNoContent {
							err = fmt.Errorf("HTTP %d", resp.StatusCode)
						}
					}
				}
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("archive event verification pending", "task", task.ID, "error", err)
				if err := n.store.RetryArchiveEvent(ctx, task.ID, 5*time.Minute); err != nil {
					slog.Warn("schedule archive verification retry", "task", task.ID, "error", err)
				}
				continue
			}
			if err := n.store.AcknowledgeArchiveEvent(ctx, task.ID); err != nil {
				slog.Warn("acknowledge archive verification", "task", task.ID, "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-n.wake:
		case <-ticker.C:
		}
	}
}

func (n *Notifier) Notify() {
	if n == nil || n.socket == "" {
		return
	}
	select {
	case n.wake <- struct{}{}:
	default:
		// The outbox holds the event until the centre acknowledges verification.
	}
}
