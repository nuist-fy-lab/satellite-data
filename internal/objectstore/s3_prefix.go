package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var ErrS3PrefixLimit = errors.New("S3 object prefix limit reached")

const maxS3PrefixBytes = 2 << 20

var s3BucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var s3ContentRange = regexp.MustCompile(`^bytes 0-([0-9]+)/([0-9]+)$`)

// OpenS3Prefix reads at most maxBytes of a public S3 object. The caller must
// close the returned body. A longer object returns ErrS3PrefixLimit when its
// bounded prefix is exhausted; a complete shorter object returns io.EOF.
func (c *Client) OpenS3Prefix(ctx context.Context, bucket, key string, maxBytes int64, expectedETag string) (io.ReadCloser, error) {
	if !s3BucketName.MatchString(bucket) || strings.Contains(bucket, "..") || key == "" || key == "." || key == ".." || strings.HasPrefix(key, "../") || strings.HasPrefix(key, "/") || path.Clean(key) != key || maxBytes < 1 || maxBytes > maxS3PrefixBytes {
		return nil, errors.New("invalid S3 object prefix request")
	}
	etag := strings.Trim(expectedETag, `"`)
	if strings.ContainsAny(etag, "\r\n\"") {
		return nil, errors.New("invalid S3 ETag")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, S3ObjectURL(bucket, key), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", maxBytes-1))
	if etag != "" {
		req.Header.Set("If-Match", `"`+etag+`"`)
	}
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (io.ReadCloser, error) { resp.Body.Close(); return nil, err }
	if resp.StatusCode != http.StatusPartialContent {
		return fail(fmt.Errorf("S3 object range returned HTTP %d", resp.StatusCode))
	}
	if etag != "" && strings.Trim(resp.Header.Get("ETag"), `"`) != etag {
		return fail(errors.New("S3 object ETag mismatch"))
	}
	match := s3ContentRange.FindStringSubmatch(resp.Header.Get("Content-Range"))
	if match == nil {
		return fail(errors.New("invalid S3 Content-Range"))
	}
	end, e1 := strconv.ParseInt(match[1], 10, 64)
	total, e2 := strconv.ParseInt(match[2], 10, 64)
	if e1 != nil || e2 != nil || total < 1 || end != min(total, maxBytes)-1 || (resp.ContentLength >= 0 && resp.ContentLength != end+1) {
		return fail(errors.New("inconsistent S3 Content-Range"))
	}
	return &s3PrefixBody{body: resp.Body, remaining: end + 1, truncated: total > maxBytes}, nil
}

type s3PrefixBody struct {
	body      io.ReadCloser
	remaining int64
	truncated bool
}

func (b *s3PrefixBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		if b.truncated {
			return 0, ErrS3PrefixLimit
		}
		return 0, io.EOF
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.body.Read(p)
	b.remaining -= int64(n)
	if err == io.EOF && b.remaining > 0 {
		err = io.ErrUnexpectedEOF
	}
	if err == io.EOF && b.remaining == 0 && b.truncated {
		err = ErrS3PrefixLimit
	}
	return n, err
}

func (b *s3PrefixBody) Close() error { return b.body.Close() }
