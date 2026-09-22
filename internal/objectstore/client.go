package objectstore

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Object struct {
	Name    string
	Size    int64
	ETag    string
	Updated time.Time
}

type Page struct {
	Objects   []Object
	Prefixes  []string
	NextToken string
	Truncated bool
}

type Client struct {
	http *http.Client
}

func NewClient(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	return &Client{http: client}
}

type s3ListResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

func (c *Client) ListS3(ctx context.Context, bucket, prefix, delimiter, token string) (Page, error) {
	return c.listS3(ctx, bucket, prefix, delimiter, token, "")
}

// ListS3After is equivalent to ListS3 but starts lexicographically after the
// supplied key on the first page. This maps to S3 ListObjectsV2 StartAfter and
// is useful for time-ordered archives where scanning an entire day would be
// unnecessarily expensive.
func (c *Client) ListS3After(ctx context.Context, bucket, prefix, delimiter, token, startAfter string) (Page, error) {
	return c.listS3(ctx, bucket, prefix, delimiter, token, startAfter)
}

func (c *Client) listS3(ctx context.Context, bucket, prefix, delimiter, token, startAfter string) (Page, error) {
	u, _ := url.Parse("https://" + bucket + ".s3.amazonaws.com/")
	q := u.Query()
	q.Set("list-type", "2")
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if delimiter != "" {
		q.Set("delimiter", delimiter)
	}
	if token != "" {
		q.Set("continuation-token", token)
	} else if startAfter != "" {
		q.Set("start-after", startAfter)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Page{}, fmt.Errorf("S3 list %s returned HTTP %d", bucket, resp.StatusCode)
	}

	var payload s3ListResult
	if err := xml.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Page{}, fmt.Errorf("decode S3 listing: %w", err)
	}
	page := Page{NextToken: payload.NextContinuationToken, Truncated: payload.IsTruncated}
	page.Objects = make([]Object, 0, len(payload.Contents))
	for _, item := range payload.Contents {
		updated, _ := time.Parse(time.RFC3339, item.LastModified)
		page.Objects = append(page.Objects, Object{
			Name:    item.Key,
			Size:    item.Size,
			ETag:    strings.Trim(item.ETag, "\""),
			Updated: updated,
		})
	}
	page.Prefixes = make([]string, 0, len(payload.CommonPrefixes))
	for _, p := range payload.CommonPrefixes {
		page.Prefixes = append(page.Prefixes, p.Prefix)
	}
	return page, nil
}

type gcsListResult struct {
	NextPageToken string   `json:"nextPageToken"`
	Prefixes      []string `json:"prefixes"`
	Items         []struct {
		Name    string `json:"name"`
		Size    string `json:"size"`
		ETag    string `json:"etag"`
		Updated string `json:"updated"`
	} `json:"items"`
}

func (c *Client) ListGCS(ctx context.Context, bucket, prefix, delimiter, token string) (Page, error) {
	u, _ := url.Parse("https://storage.googleapis.com/storage/v1/b/" + url.PathEscape(bucket) + "/o")
	q := u.Query()
	q.Set("maxResults", "1000")
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if delimiter != "" {
		q.Set("delimiter", delimiter)
	}
	if token != "" {
		q.Set("pageToken", token)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Page{}, fmt.Errorf("GCS list %s returned HTTP %d", bucket, resp.StatusCode)
	}

	var payload gcsListResult
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Page{}, fmt.Errorf("decode GCS listing: %w", err)
	}
	page := Page{Prefixes: payload.Prefixes, NextToken: payload.NextPageToken, Truncated: payload.NextPageToken != ""}
	page.Objects = make([]Object, 0, len(payload.Items))
	for _, item := range payload.Items {
		var size int64
		_, _ = fmt.Sscan(item.Size, &size)
		updated, _ := time.Parse(time.RFC3339Nano, item.Updated)
		page.Objects = append(page.Objects, Object{Name: item.Name, Size: size, ETag: item.ETag, Updated: updated})
	}
	return page, nil
}

func S3ObjectURL(bucket, object string) string {
	return "https://" + bucket + ".s3.amazonaws.com/" + escapeObjectPath(object)
}

func GCSObjectURL(bucket, object string) string {
	return "https://storage.googleapis.com/" + url.PathEscape(bucket) + "/" + escapeObjectPath(object)
}

func escapeObjectPath(object string) string {
	clean := strings.TrimPrefix(path.Clean("/"+object), "/")
	parts := strings.Split(clean, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
