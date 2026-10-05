package httpapi

import (
	"compress/gzip"
	"net/http"
	"strings"
)

type gzipResponseWriter struct {
	http.ResponseWriter
	writer      *gzip.Writer
	compress    bool
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	if status >= 200 && status != http.StatusNoContent && status != http.StatusPartialContent && status != http.StatusNotModified &&
		w.Header().Get("Content-Encoding") == "" && compressibleContentType(contentType) {
		w.compress = true
		w.Header().Del("Content-Length")
		w.Header().Set("Content-Encoding", "gzip")
		appendVary(w.Header(), "Accept-Encoding")
		w.writer = gzip.NewWriter(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.compress {
		return w.writer.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

func (w *gzipResponseWriter) Flush() {
	if w.compress {
		_ = w.writer.Flush()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func compressibleContentType(contentType string) bool {
	return strings.HasPrefix(contentType, "text/") ||
		strings.HasPrefix(contentType, "application/json") ||
		strings.HasPrefix(contentType, "application/javascript") ||
		strings.HasPrefix(contentType, "image/svg+xml")
}

func appendVary(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for _, item := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

func withCompression(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/stream") {
			appendVary(w.Header(), "Accept-Encoding")
		}
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || !strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip") || strings.HasSuffix(r.URL.Path, "/stream") {
			next.ServeHTTP(w, r)
			return
		}
		wrapped := &gzipResponseWriter{ResponseWriter: w}
		defer func() {
			if wrapped.writer != nil {
				_ = wrapped.writer.Close()
			}
		}()
		next.ServeHTTP(wrapped, r)
	})
}
