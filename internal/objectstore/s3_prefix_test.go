package objectstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type prefixTransport func(*http.Request) (*http.Response, error)

func (f prefixTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed bool
}

type cancellableBody struct {
	ctx    context.Context
	closed bool
}

func (b *cancellableBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b *cancellableBody) Close() error             { b.closed = true; return nil }

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestOpenS3PrefixRangeAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name, contentRange, body string
		length                   int64
		wantErr                  error
	}{
		{"short object", "bytes 0-2/3", "abc", 3, nil},
		{"bounded prefix", "bytes 0-3/8", "abcdEXCESS", -1, ErrS3PrefixLimit},
		{"truncated response", "bytes 0-3/8", "ab", -1, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(tc.body)}
			client := NewClient(&http.Client{Transport: prefixTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://test-bucket.s3.amazonaws.com/a/b%20c" || r.Header.Get("Range") != "bytes=0-3" || r.Header.Get("If-Match") != `"abc"` {
					t.Fatalf("request: %+v %s", r.Header, r.URL)
				}
				return &http.Response{StatusCode: 206, ContentLength: tc.length, Header: http.Header{"Content-Range": {tc.contentRange}, "Etag": {`"abc"`}}, Body: body}, nil
			})})
			stream, err := client.OpenS3Prefix(t.Context(), "test-bucket", "a/b c", 4, "abc")
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(stream)
			if !errors.Is(err, tc.wantErr) || len(data) > 4 {
				t.Fatalf("read %q %v", data, err)
			}
			if err := stream.Close(); err != nil || !body.closed {
				t.Fatal("body not closed")
			}
		})
	}
}

func TestOpenS3PrefixRejectsInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		status             int
		contentRange, etag string
		length             int64
	}{
		{200, "", "abc", 4}, {404, "", "abc", 4}, {412, "", "abc", 4},
		{206, "bytes 1-4/8", "abc", 4}, {206, "bytes 0-4/8", "abc", 5},
		{206, "bytes 0-3/8", "other", 4}, {206, "bytes 0-3/*", "abc", 4}, {206, "bytes 0-3/8", "abc", 3},
	} {
		body := &trackedBody{Reader: strings.NewReader("xxxx")}
		client := NewClient(&http.Client{Transport: prefixTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, ContentLength: tc.length, Header: http.Header{"Content-Range": {tc.contentRange}, "Etag": {tc.etag}}, Body: body}, nil
		})})
		if _, err := client.OpenS3Prefix(t.Context(), "test-bucket", "a", 4, "abc"); err == nil || !body.closed {
			t.Fatalf("accepted response %+v or body leaked", tc)
		}
	}
}

func TestOpenS3PrefixCancellationAndValidation(t *testing.T) {
	entered := make(chan struct{})
	client := NewClient(&http.Client{Transport: prefixTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := client.OpenS3Prefix(ctx, "test-bucket", "a", 4, ""); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	for _, tc := range []struct {
		bucket, key string
		limit       int64
	}{{"invalid/host", "a", 4}, {"test-bucket", "../a", 4}, {"test-bucket", "a", 0}, {"test-bucket", "a", maxS3PrefixBytes + 1}} {
		if _, err := client.OpenS3Prefix(t.Context(), tc.bucket, tc.key, tc.limit, ""); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}

func TestOpenS3PrefixCancelDuringReadAndEarlyClose(t *testing.T) {
	var body *cancellableBody
	client := NewClient(&http.Client{Transport: prefixTransport(func(r *http.Request) (*http.Response, error) {
		body = &cancellableBody{ctx: r.Context()}
		return &http.Response{StatusCode: 206, ContentLength: 4, Header: http.Header{"Content-Range": {"bytes 0-3/8"}}, Body: body}, nil
	})})
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := client.OpenS3Prefix(ctx, "test-bucket", "a", 4, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := stream.Read(make([]byte, 1)); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("read cancel: %v", err)
	}
	if err := stream.Close(); err != nil || !body.closed {
		t.Fatal("body not closed")
	}
	stream, err = client.OpenS3Prefix(t.Context(), "test-bucket", "a", 4, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil || !body.closed {
		t.Fatal("early close did not close body")
	}
}

func TestOpenS3PrefixHTTPReadCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-3/8")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := prefixTransport(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		return http.DefaultTransport.RoundTrip(clone)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := NewClient(&http.Client{Transport: transport}).OpenS3Prefix(ctx, "test-bucket", "a", 4, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	<-started
	done := make(chan error, 1)
	go func() { _, err := stream.Read(make([]byte, 1)); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("HTTP read cancel: %v", err)
	}
}
