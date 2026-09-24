package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/auth"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/model"
)

var contentRangePattern = regexp.MustCompile(`(?i)^bytes\s+(\d+)-(\d+)/(\d+|\*)$`)

type HTTPError struct {
	Status int
}

func (e *HTTPError) Error() string { return fmt.Sprintf("download returned HTTP %d", e.Status) }

type IntegrityError struct {
	Detail string
}

func (e *IntegrityError) Error() string { return e.Detail }

type DownloadResult struct {
	Path string
	Size int64
}

type PromoteResult struct {
	Path   string
	Size   int64
	SHA256 string
}

type HTTP struct {
	client *http.Client
}

func NewHTTP(timeoutClient *http.Client) *HTTP {
	if timeoutClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 256
		transport.MaxIdleConnsPerHost = 32
		transport.IdleConnTimeout = 90 * time.Second
		timeoutClient = &http.Client{Transport: transport}
	}
	return &HTTP{client: timeoutClient}
}

func (h *HTTP) Download(ctx context.Context, task model.Task, downloadRoot string, authorizer auth.Authorizer) (DownloadResult, error) {
	name, err := safeName(task.Name)
	if err != nil {
		return DownloadResult{}, err
	}
	if _, err := httpsURL(task.URL); err != nil {
		return DownloadResult{}, err
	}
	dir := filepath.Join(downloadRoot, "staging", task.SourceID, strconv.FormatInt(task.ID, 10))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return DownloadResult{}, err
	}
	partPath := filepath.Join(dir, name+".part")
	readyPath := filepath.Join(dir, name+".downloaded")

	if info, err := os.Stat(readyPath); err == nil {
		if task.Size <= 0 || info.Size() == task.Size {
			return DownloadResult{Path: readyPath, Size: info.Size()}, nil
		}
		if err := os.Remove(readyPath); err != nil {
			return DownloadResult{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return DownloadResult{}, err
	}

	resumeFrom := int64(0)
	if info, err := os.Stat(partPath); err == nil {
		resumeFrom = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return DownloadResult{}, err
	}
	if task.Size > 0 && resumeFrom > task.Size {
		if err := os.Remove(partPath); err != nil {
			return DownloadResult{}, err
		}
		resumeFrom = 0
	}

	resp, err := h.request(ctx, task.URL, resumeFrom, authorizer)
	if err != nil {
		return DownloadResult{}, err
	}
	if resumeFrom > 0 && resp.StatusCode == http.StatusOK {
		_ = resp.Body.Close()
		resumeFrom = 0
		resp, err = h.request(ctx, task.URL, 0, authorizer)
		if err != nil {
			return DownloadResult{}, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DownloadResult{}, &HTTPError{Status: resp.StatusCode}
	}

	remoteSize := task.Size
	appendMode := resumeFrom > 0 && resp.StatusCode == http.StatusPartialContent
	if resp.StatusCode == http.StatusPartialContent {
		start, _, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != resumeFrom {
			return DownloadResult{}, &IntegrityError{Detail: "origin returned a mismatched Content-Range"}
		}
		if total > 0 {
			if remoteSize > 0 && total != remoteSize {
				return DownloadResult{}, &IntegrityError{Detail: fmt.Sprintf("remote object changed size from %d to %d", remoteSize, total)}
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
		return DownloadResult{}, err
	}
	_, copyErr := io.CopyBuffer(handle, resp.Body, make([]byte, 1<<20))
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if copyErr != nil {
		return DownloadResult{}, fmt.Errorf("download body: %w", copyErr)
	}
	if syncErr != nil {
		return DownloadResult{}, syncErr
	}
	if closeErr != nil {
		return DownloadResult{}, closeErr
	}
	info, err := os.Stat(partPath)
	if err != nil {
		return DownloadResult{}, err
	}
	if remoteSize > 0 && info.Size() != remoteSize {
		return DownloadResult{}, &IntegrityError{Detail: fmt.Sprintf("downloaded size %d does not match expected size %d", info.Size(), remoteSize)}
	}
	if err := os.Rename(partPath, readyPath); err != nil {
		return DownloadResult{}, err
	}
	_ = syncDir(dir)
	return DownloadResult{Path: readyPath, Size: info.Size()}, nil
}

func (h *HTTP) request(ctx context.Context, locator string, resumeFrom int64, authorizer auth.Authorizer) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, locator, nil)
	if err != nil {
		return nil, err
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}
	if authorizer != nil {
		if err := authorizer.Authorize(ctx, req); err != nil {
			return nil, err
		}
	}
	return h.client.Do(req)
}

func Promote(task model.Task, archiveRoot string) (PromoteResult, error) {
	name, err := safeName(task.Name)
	if err != nil {
		return PromoteResult{}, err
	}
	relative, err := safeRelativeDir(task.RelativeDir)
	if err != nil {
		return PromoteResult{}, err
	}
	if strings.TrimSpace(task.StagingPath) == "" {
		return PromoteResult{}, &IntegrityError{Detail: "staging path is empty"}
	}
	info, err := os.Stat(task.StagingPath)
	if err != nil {
		return PromoteResult{}, err
	}
	if task.Size > 0 && info.Size() != task.Size {
		return PromoteResult{}, &IntegrityError{Detail: fmt.Sprintf("staging size %d does not match expected size %d", info.Size(), task.Size)}
	}
	digest, err := fileSHA256(task.StagingPath)
	if err != nil {
		return PromoteResult{}, err
	}

	targetDir := filepath.Join(archiveRoot, relative)
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return PromoteResult{}, err
	}
	target := filepath.Join(targetDir, name)
	if existing, err := os.Stat(target); err == nil && existing.Size() == info.Size() {
		existingDigest, digestErr := fileSHA256(target)
		if digestErr == nil && existingDigest == digest {
			_ = os.Remove(task.StagingPath)
			return PromoteResult{Path: target, Size: existing.Size(), SHA256: digest}, nil
		}
	}

	temp := filepath.Join(targetDir, fmt.Sprintf(".%s.task-%d.tmp", name, task.ID))
	_ = os.Remove(temp)
	if err := copyFile(task.StagingPath, temp); err != nil {
		_ = os.Remove(temp)
		return PromoteResult{}, err
	}
	tempDigest, err := fileSHA256(temp)
	if err != nil {
		_ = os.Remove(temp)
		return PromoteResult{}, err
	}
	if tempDigest != digest {
		_ = os.Remove(temp)
		return PromoteResult{}, &IntegrityError{Detail: "archive copy SHA-256 mismatch"}
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return PromoteResult{}, err
	}
	if err := syncDir(targetDir); err != nil {
		return PromoteResult{}, err
	}
	if err := os.Remove(task.StagingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return PromoteResult{}, err
	}
	_ = syncDir(filepath.Dir(task.StagingPath))
	return PromoteResult{Path: target, Size: info.Size(), SHA256: digest}, nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyBuffer(out, in, make([]byte, 1<<20))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func fileSHA256(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, handle, make([]byte, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func safeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return "", errors.New("asset name must be a plain filename")
	}
	return name, nil
}

func safeRelativeDir(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("relative archive directory is required")
	}
	if filepath.IsAbs(value) {
		return "", errors.New("relative archive directory must not be absolute")
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("relative archive directory escapes archive root")
	}
	return clean, nil
}

func httpsURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, errors.New("asset URL must use HTTPS")
	}
	return parsed, nil
}

func parseContentRange(value string) (start, end, total int64, ok bool) {
	match := contentRangePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, 0, 0, false
	}
	start, err1 := strconv.ParseInt(match[1], 10, 64)
	end, err2 := strconv.ParseInt(match[2], 10, 64)
	if err1 != nil || err2 != nil || end < start {
		return 0, 0, 0, false
	}
	if match[3] != "*" {
		total, err1 = strconv.ParseInt(match[3], 10, 64)
		if err1 != nil || total <= end {
			return 0, 0, 0, false
		}
	}
	return start, end, total, true
}

func syncDir(path string) error {
	handle, err := os.Open(path)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
