package archiveevent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
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

func Run(ctx context.Context, socket string, record func(context.Context, Event) error) error {
	socket = strings.TrimSpace(socket)
	if socket == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o750); err != nil {
		return err
	}
	listener, err := listen(socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /archive-event", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var event Event
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&event); err != nil {
			http.Error(w, "invalid event", http.StatusBadRequest)
			return
		}
		if event.TaskID <= 0 || strings.TrimSpace(event.Filename) == "" || strings.TrimSpace(event.ArchivePath) == "" {
			http.Error(w, "invalid event", http.StatusBadRequest)
			return
		}
		if record != nil {
			if err := record(r.Context(), event); err != nil {
				slog.Warn("archive event could not be verified", "source", event.SourceID, "task", event.TaskID, "error", err)
				http.Error(w, "archive verification failed", http.StatusUnprocessableEntity)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Minute}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func listen(socket string) (net.Listener, error) {
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("archive event path exists and is not a Unix socket")
		}
		conn, dialErr := net.DialTimeout("unix", socket, 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, errors.New("archive event socket is already active")
		}
		if err := os.Remove(socket); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o660); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}
