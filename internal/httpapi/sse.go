package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	last    map[string][]byte
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming is not supported")
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return nil, err
	}
	flusher.Flush()
	return &sseWriter{w: w, flusher: flusher, last: make(map[string][]byte)}, nil
}

func (s *sseWriter) event(name string, payload any, changedOnly bool) (bool, error) {
	return s.eventKey(name, name, payload, changedOnly)
}

func (s *sseWriter) eventKey(name, key string, payload any, changedOnly bool) (bool, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	if changedOnly && bytes.Equal(encoded, s.last[key]) {
		return false, nil
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, encoded); err != nil {
		return false, err
	}
	s.flusher.Flush()
	s.last[key] = append(s.last[key][:0], encoded...)
	return true, nil
}

func (s *sseWriter) heartbeat() error {
	_, err := fmt.Fprint(s.w, ": heartbeat\n\n")
	if err == nil {
		s.flusher.Flush()
	}
	return err
}
