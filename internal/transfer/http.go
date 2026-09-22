package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

var contentRangePattern = regexp.MustCompile(`(?i)^bytes\s+(\d+)-(\d+)/(\d+|\*)$`)

type Result struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ResumedFrom int64  `json:"resumed_from"`
	SHA256      string `json:"sha256"`
}

type HTTP struct {
	client      *http.Client
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

func NewHTTP(client *http.Client) *HTTP {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 128
		transport.MaxIdleConnsPerHost = 16
		transport.IdleConnTimeout = 90 * time.Second
		client = &http.Client{Transport: transport}
	}
	return &HTTP{
		client:      client,
		maxAttempts: 8,
		baseDelay:   2 * time.Second,
		maxDelay:    2 * time.Minute,
	}
}

func (h *HTTP) Download(ctx context.Context, asset model.Asset, destinationDir string) (Result, error) {
	if asset.URL == "" {
		return Result{}, errors.New("asset URL is empty")
	}
	name, err := safeName(asset.Name)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(destinationDir, 0o750); err != nil {
		return Result{}, err
	}
	finalPath := filepath.Join(destinationDir, name)
	partPath := finalPath + ".part"

	if info, err := os.Stat(finalPath); err == nil {
		if asset.Size <= 0 || info.Size() == asset.Size {
			return Result{Path: finalPath, Size: info.Size(), ResumedFrom: info.Size()}, nil
		}
		return Result{}, fmt.Errorf("existing file %s has size %d, expected %d", name, info.Size(), asset.Size)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}

	resumeFrom := int64(0)
	if info, err := os.Stat(partPath); err == nil {
		resumeFrom = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	if asset.Size > 0 && resumeFrom > asset.Size {
		if err := os.Remove(partPath); err != nil {
			return Result{}, err
		}
		resumeFrom = 0
	}
	if asset.Size > 0 && resumeFrom == asset.Size && resumeFrom > 0 {
		if err := os.Rename(partPath, finalPath); err != nil {
			return Result{}, err
		}
		digest, err := fileSHA256(finalPath)
		return Result{Path: finalPath, Size: resumeFrom, ResumedFrom: resumeFrom, SHA256: digest}, err
	}

	resp, err := h.getWithRetry(ctx, asset.URL, resumeFrom)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	appendMode := resumeFrom > 0 && resp.StatusCode == http.StatusPartialContent
	if resumeFrom > 0 && resp.StatusCode == http.StatusOK {
		resumeFrom = 0
		appendMode = false
	}
	remoteSize := asset.Size
	if resp.StatusCode == http.StatusPartialContent {
		start, _, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != resumeFrom {
			return Result{}, errors.New("origin returned a mismatched Content-Range")
		}
		if total > 0 {
			if remoteSize > 0 && total != remoteSize {
				return Result{}, fmt.Errorf("remote object changed size from %d to %d", remoteSize, total)
			}
			remoteSize = total
		}
	} else if resp.ContentLength >= 0 {
		remoteSize = resp.ContentLength
	}

	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	handle, err := os.OpenFile(partPath, flags, 0o640)
	if err != nil {
		return Result{}, err
	}
	_, copyErr := io.CopyBuffer(handle, resp.Body, make([]byte, 1<<20))
	closeErr := handle.Close()
	if copyErr != nil {
		return Result{}, fmt.Errorf("download body: %w", copyErr)
	}
	if closeErr != nil {
		return Result{}, closeErr
	}

	info, err := os.Stat(partPath)
	if err != nil {
		return Result{}, err
	}
	if remoteSize > 0 && info.Size() != remoteSize {
		return Result{}, fmt.Errorf("downloaded size %d does not match expected size %d", info.Size(), remoteSize)
	}
	digest, err := fileSHA256(partPath)
	if err != nil {
		return Result{}, err
	}
	if err := os.Rename(partPath, finalPath); err != nil {
		return Result{}, err
	}
	return Result{Path: finalPath, Size: info.Size(), ResumedFrom: resumeFrom, SHA256: digest}, nil
}

func (h *HTTP) getWithRetry(ctx context.Context, locator string, resumeFrom int64) (*http.Response, error) {
	for attempt := 0; attempt < h.maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, locator, nil)
		if err != nil {
			return nil, err
		}
		if resumeFrom > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
		}
		resp, err := h.client.Do(req)
		if err == nil && !retryable(resp.StatusCode) {
			return resp, nil
		}
		if err == nil {
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			_ = resp.Body.Close()
			if attempt+1 == h.maxAttempts {
				return nil, fmt.Errorf("download returned HTTP %d after %d attempts", resp.StatusCode, h.maxAttempts)
			}
			if err := wait(ctx, h.retryDelay(attempt, retryAfter)); err != nil {
				return nil, err
			}
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt+1 == h.maxAttempts {
			return nil, fmt.Errorf("download failed after %d attempts: %w", h.maxAttempts, err)
		}
		if err := wait(ctx, h.retryDelay(attempt, 0)); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("retry loop exhausted")
}

func retryable(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (h *HTTP) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	delay := h.baseDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay >= h.maxDelay {
			delay = h.maxDelay
			break
		}
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > 0 {
		delay += time.Duration(rand.Int64N(int64(delay/2) + 1))
	}
	return delay
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(time.Now()) {
		return 0
	}
	return time.Until(when)
}

func parseContentRange(value string) (int64, int64, int64, bool) {
	match := contentRangePattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 4 {
		return 0, 0, 0, false
	}
	start, err1 := strconv.ParseInt(match[1], 10, 64)
	end, err2 := strconv.ParseInt(match[2], 10, 64)
	if err1 != nil || err2 != nil || end < start {
		return 0, 0, 0, false
	}
	if match[3] == "*" {
		return start, end, 0, true
	}
	total, err := strconv.ParseInt(match[3], 10, 64)
	return start, end, total, err == nil && total >= 0
}

func safeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", errors.New("asset name is not a safe filename")
	}
	return name, nil
}

func fileSHA256(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, handle); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
