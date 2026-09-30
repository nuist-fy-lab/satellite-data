package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/archiveevent"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type Indexer struct {
	registry     *provider.Registry
	store        *state.Store
	archiveRoot  string
	sharedRoots  [3]string
	catalogWrite sync.Mutex
	localWake    chan struct{}
}

var earthCAREDate = regexp.MustCompile(`_(\d{8})T\d{6}Z`)

func New(registry *provider.Registry, store *state.Store, archiveRoot string, sharedRoots ...string) *Indexer {
	indexer := &Indexer{registry: registry, store: store, archiveRoot: filepath.Clean(archiveRoot), localWake: make(chan struct{}, 1)}
	for index, root := range sharedRoots {
		if index >= len(indexer.sharedRoots) {
			break
		}
		if strings.TrimSpace(root) != "" {
			indexer.sharedRoots[index] = filepath.Clean(root)
		}
	}
	return indexer
}

func (i *Indexer) TriggerLocalScan() {
	if i == nil || i.localWake == nil {
		return
	}
	select {
	case i.localWake <- struct{}{}:
	default:
	}
}

func (i *Indexer) RecordArchiveEvent(ctx context.Context, event archiveevent.Event) error {
	path := filepath.Clean(event.ArchivePath)
	if event.Size <= 0 || len(event.SHA256) != 64 || filepath.Base(path) != event.Filename {
		return errors.New("invalid archive event")
	}
	if _, err := hex.DecodeString(event.SHA256); err != nil {
		return err
	}
	assetID, err := i.verifyOfficialCorrespondence(ctx, event)
	if err != nil {
		return err
	}
	root := i.archiveRoot
	if event.SourceID == "earthcare" || event.SourceID == "era5" {
		index := 0
		if event.SourceID == "era5" {
			index = 1
		}
		if i.sharedRoots[index] == "" {
			return errors.New("shared archive root is not configured")
		}
		root = i.sharedRoots[index]
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("archive event path outside configured root")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || resolvedRel == "." || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) || filepath.IsAbs(resolvedRel) {
		return errors.New("archive event resolves outside configured root")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != event.Size {
		return errors.New("archive event file is not regular or has different size")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, f)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(event.SHA256) {
		return errors.New("archive event SHA-256 mismatch")
	}
	if err := i.store.RecordVerifiedArchiveFile(ctx, path, info.Size(), info.ModTime().UnixNano(), event.SHA256,
		event.SourceID, event.CollectionID, event.GranuleID, assetID); err != nil {
		return err
	}
	i.TriggerLocalScan()
	return nil
}

func (i *Indexer) verifyOfficialCorrespondence(ctx context.Context, event archiveevent.Event) (string, error) {
	switch event.SourceID {
	case "earthcare":
		if i.registry == nil {
			return "", errors.New("EarthCARE catalogue is unavailable")
		}
		match := earthCAREDate.FindStringSubmatch(event.Filename)
		if match == nil {
			return "", errors.New("EarthCARE filename has no official acquisition date")
		}
		day, err := time.Parse("20060102", match[1])
		if err != nil {
			return "", err
		}
		provider, err := i.registry.Provider("earthcare")
		if err != nil {
			return "", err
		}
		queryCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		granules, err := provider.Search(queryCtx, model.Query{CollectionID: event.CollectionID, Start: day, End: day.AddDate(0, 0, 1)})
		if err != nil {
			return "", fmt.Errorf("official EarthCARE STAC lookup: %w", err)
		}
		for _, granule := range granules {
			if granule.ID != event.GranuleID {
				continue
			}
			for _, asset := range granule.Assets {
				if asset.Name == event.Filename && asset.Size > 0 && asset.Size == event.Size {
					return asset.ID, nil
				}
			}
		}
		return "", errors.New("archive file has no exact official EarthCARE collection/item/asset and size match")
	case "era5":
		job, ok := strings.CutPrefix(event.GranuleID, "era5:")
		if !ok || job == "" || filepath.Base(filepath.Dir(event.ArchivePath)) != safeJobPath(job) {
			return "", errors.New("ERA5 archive has no matching CDS job directory")
		}
		assetID, matched, err := i.store.ERA5ResultEvidence(ctx, event.TaskID, job, event.CollectionID, event.Filename, event.Size)
		if err != nil {
			return "", err
		}
		if !matched {
			return "", errors.New("ERA5 archive has no matching official CDS job result")
		}
		return assetID, nil
	}
	return event.Filename, nil
}

var unsafeJobPathChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeJobPath(job string) string {
	return strings.Trim(unsafeJobPathChars.ReplaceAllString(strings.Trim(strings.TrimSpace(job), "."), "_"), "_")
}

func (i *Indexer) Run(ctx context.Context) {
	go i.catalogLoop(ctx)
	go i.officialLoop(ctx, 0)
	go i.officialLoop(ctx, 1)
	if strings.TrimSpace(i.archiveRoot) != "" {
		go i.localLoop(ctx)
	}
}

func (i *Indexer) catalogLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			failures := i.refreshCatalogs(ctx)
			delay := 6 * time.Hour
			if failures > 0 {
				delay = 30 * time.Second
			}
			timer.Reset(delay)
		}
	}
}

func (i *Indexer) refreshCatalogs(ctx context.Context) int {
	providers := i.registry.Providers()
	sem := make(chan struct{}, 4)
	results := make(chan bool, len(providers))
	var wg sync.WaitGroup
	for _, p := range providers {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results <- false
				return
			}
			defer func() { <-sem }()
			refreshCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			collections, err := p.Collections(refreshCtx)
			cancel()
			if err != nil {
				slog.Warn("refresh official product catalog", "source", p.Source().ID, "error", err)
				_ = i.store.CatalogError(context.Background(), p.Source().ID, err)
				results <- false
				return
			}
			source := p.Source()
			products := make([]model.Product, 0, len(collections))
			for _, collection := range collections {
				products = append(products, provider.ProductFromCollection(source, collection))
			}
			i.catalogWrite.Lock()
			persistCtx, persistCancel := context.WithTimeout(ctx, 30*time.Second)
			err = i.store.UpsertProducts(persistCtx, source.ID, products)
			persistCancel()
			i.catalogWrite.Unlock()
			if err != nil {
				slog.Warn("persist official product catalog", "source", source.ID, "error", err)
				_ = i.store.CatalogError(context.Background(), source.ID, err)
				results <- false
				return
			}
			slog.Info("official product catalog refreshed", "source", source.ID, "products", len(products))
			results <- true
		}()
	}
	wg.Wait()
	close(results)
	failures := 0
	for ok := range results {
		if !ok {
			failures++
		}
	}
	return failures
}

func (i *Indexer) officialLoop(ctx context.Context, worker int) {
	idle := time.NewTimer(0)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
		}
		batch, err := i.store.ClaimBatch(ctx, 7)
		if err != nil {
			slog.Warn("claim official slot work", "worker", worker, "error", err)
			idle.Reset(2 * time.Second)
			continue
		}
		if len(batch.Days) == 0 {
			idle.Reset(time.Second)
			continue
		}
		sourceID, _, err := i.store.ProductSource(ctx, batch.Product)
		if err != nil {
			_ = i.store.MarkBatchError(ctx, batch, err)
			idle.Reset(time.Second)
			continue
		}
		p, err := i.registry.Provider(sourceID)
		if err != nil {
			_ = i.store.MarkBatchError(ctx, batch, err)
			idle.Reset(time.Second)
			continue
		}
		start, _ := time.Parse(time.DateOnly, batch.Days[0])
		last, _ := time.Parse(time.DateOnly, batch.Days[len(batch.Days)-1])
		end := last.AddDate(0, 0, 1)
		queryCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		granules, queryErr := p.Search(queryCtx, model.Query{
			CollectionID: batch.Product,
			Start:        start,
			End:          end,
		})
		cancel()
		if queryErr != nil {
			slog.Warn("index official slots", "worker", worker, "product", batch.Product, "start", batch.Days[0], "end", batch.Days[len(batch.Days)-1], "error", queryErr)
			_ = i.store.MarkBatchError(ctx, batch, queryErr)
			idle.Reset(time.Second)
			continue
		}
		if err := i.store.SaveOfficialBatch(ctx, batch, granules); err != nil {
			slog.Warn("persist official slots", "worker", worker, "product", batch.Product, "error", err)
			_ = i.store.MarkBatchError(ctx, batch, err)
			idle.Reset(time.Second)
			continue
		}
		slog.Info("official slots indexed", "worker", worker, "product", batch.Product, "days", len(batch.Days), "granules", len(granules))
		idle.Reset(50 * time.Millisecond)
	}
}

func (i *Indexer) localLoop(ctx context.Context) {
	i.scanLocal(ctx)
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-i.localWake:
			i.scanLocal(ctx)
		case <-ticker.C:
			i.scanLocal(ctx)
		}
	}
}

func (i *Indexer) scanLocal(ctx context.Context) {
	if err := os.MkdirAll(i.archiveRoot, 0o777); err != nil {
		slog.Warn("create local archive root", "root", i.archiveRoot, "error", err)
		return
	}
	files := make([]state.LocalFile, 0, 1024)
	verified, err := i.store.VerifiedArchiveFiles(ctx)
	if err != nil {
		slog.Warn("load verified archive", "error", err)
		return
	}
	warnings := make([]string, 0)
	for _, root := range i.roots() {
		if err := i.walkArchiveRoot(ctx, root, root != i.archiveRoot, verified, &files, &warnings); err != nil {
			slog.Warn("scan local archive", "root", root, "error", err)
			return
		}
	}
	sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })
	hash := sha256.New()
	for _, root := range i.roots() {
		_, _ = hash.Write([]byte(root + "\x00"))
	}
	for _, file := range files {
		_, _ = hash.Write([]byte(file.Path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(time.Unix(0, file.MtimeNS).UTC().Format(time.RFC3339Nano)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(strconv.FormatInt(file.Size, 10)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(strings.TrimSpace(strings.ToLower(file.Filename))))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(file.State + "\x00" + file.Detail + "\x00"))
	}
	for _, path := range warnings {
		_, _ = hash.Write([]byte(path + "\x00"))
	}
	fingerprint := hex.EncodeToString(hash.Sum(nil))
	changed, err := i.store.ReplaceLocalFilesWithWarnings(ctx, files, fingerprint, warnings)
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("persist local archive index", "root", i.archiveRoot, "error", err)
		return
	}
	if changed {
		slog.Info("local archive index refreshed", "files", len(files), "unreadable_directories", len(warnings))
	}
	if i.sharedRoots[0] != "" {
		queued, err := i.queueHistoricalEarthCARE(ctx, files)
		if err != nil {
			slog.Warn("queue historical EarthCARE official catalog lookup", "error", err)
		} else if queued > 0 {
			slog.Info("queued official EarthCARE dates for historical archive", "days", queued)
		}
	}
}

func (i *Indexer) queueHistoricalEarthCARE(ctx context.Context, files []state.LocalFile) (int, error) {
	root := i.sharedRoots[0]
	unique := make(map[state.ArchiveProductDay]struct{})
	for _, file := range files {
		if file.State == "error" {
			continue
		}
		rel, err := filepath.Rel(root, file.Path)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 3 || len(parts[1]) != 8 {
			continue
		}
		match := earthCAREDate.FindStringSubmatch(file.Filename)
		if match == nil || match[1] != parts[1] {
			continue
		}
		day, err := time.Parse("20060102", parts[1])
		if err != nil || day.Format("20060102") != parts[1] {
			continue
		}
		unique[state.ArchiveProductDay{ProductType: parts[0], Day: day.Format(time.DateOnly)}] = struct{}{}
	}
	days := make([]state.ArchiveProductDay, 0, len(unique))
	for day := range unique {
		days = append(days, day)
	}
	sort.Slice(days, func(a, b int) bool {
		if days[a].Day == days[b].Day {
			return days[a].ProductType < days[b].ProductType
		}
		return days[a].Day < days[b].Day
	})
	return i.store.QueueEarthCAREOfficialDays(ctx, days)
}

func (i *Indexer) roots() []string {
	roots := []string{i.archiveRoot}
	for _, root := range i.sharedRoots {
		if root != "" && root != i.archiveRoot {
			roots = append(roots, root)
		}
	}
	return roots
}

func (i *Indexer) walkArchiveRoot(ctx context.Context, root string, shared bool, verified map[string]state.VerifiedArchiveFile, files *[]state.LocalFile, warnings *[]string) error {
	if info, err := os.Lstat(root); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("archive root is not a directory: %s", root)
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if shared && path != root && errors.Is(walkErr, os.ErrPermission) {
				if entry == nil {
					info, err := os.Lstat(path)
					if err != nil || !info.IsDir() {
						return walkErr
					}
				} else if !entry.IsDir() {
					return walkErr
				}
				known, err := i.store.KnownLocalFilesBelow(ctx, path)
				if err != nil {
					return err
				}
				for index := range known {
					known[index].State = "unverified"
					known[index].Detail = "directory inaccessible; previously indexed metadata only"
				}
				*files = append(*files, known...)
				*warnings = append(*warnings, path)
				slog.Warn("unreadable shared archive directory", "path", path)
				return filepath.SkipDir
			}
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if shared && path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_partial_quarantine") ||
				name == "transfer_logs" || name == "support" || name == "manifests" || name == "quarantine" || name == "_reorg_audit") {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() && entry.Type() != 0 {
			return nil
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".part") || strings.Contains(name, ".part.") || strings.HasSuffix(name, ".lock") ||
			strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".") || (shared && strings.EqualFold(filepath.Ext(name), ".zip")) {
			return nil
		}
		if root == i.sharedRoots[2] && !strings.EqualFold(filepath.Ext(name), ".bz2") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file := state.LocalFile{Path: path, Filename: name, Size: info.Size(), MtimeNS: info.ModTime().UnixNano(), State: "ok"}
		if shared {
			file.State, file.Detail = checkSharedFile(path, info.Size())
			if record, ok := verified[path]; ok && file.State != "error" && record.Size == file.Size && record.MtimeNS == file.MtimeNS {
				file.State, file.Detail = "ok", "sha256_verified:"+record.SHA256
			}
		}
		*files = append(*files, file)
		return nil
	})
}

func checkSharedFile(path string, size int64) (string, string) {
	if size <= 0 {
		return "error", "empty file"
	}
	var expected []byte
	switch strings.ToLower(filepath.Ext(path)) {
	case ".bz2":
		handle, err := os.Open(path)
		if err != nil {
			return "error", "cannot read file header"
		}
		defer handle.Close()
		var header [4]byte
		if _, err := io.ReadFull(handle, header[:]); err != nil {
			return "error", "file header is incomplete"
		}
		if string(header[:3]) != "BZh" || header[3] < '1' || header[3] > '9' {
			return "error", "invalid bzip2 header"
		}
		return "unverified", "format header checked; no authoritative checksum"
	case ".h5":
		expected = []byte{0x89, 'H', 'D', 'F', 0x0d, 0x0a, 0x1a, 0x0a}
	case ".nc":
	case ".grib":
		expected = []byte("GRIB")
	case ".hdr", ".eof":
		expected = []byte("<?xml")
	default:
		return "unverified", "unknown file format or no authoritative checksum"
	}
	if expected == nil {
		expected = make([]byte, 8)
	}
	handle, err := os.Open(path)
	if err != nil {
		return "error", "cannot read file header"
	}
	defer handle.Close()
	buffer := make([]byte, len(expected))
	if _, err := io.ReadFull(handle, buffer); err != nil {
		return "error", "file header is incomplete"
	}
	if strings.EqualFold(filepath.Ext(path), ".nc") {
		if !strings.HasPrefix(string(buffer), "CDF\x01") && !strings.HasPrefix(string(buffer), "CDF\x02") && !strings.HasPrefix(string(buffer), "CDF\x05") && string(buffer) != "\x89HDF\r\n\x1a\n" {
			return "error", "invalid NetCDF header"
		}
	} else if string(buffer) != string(expected) {
		return "error", "invalid file header"
	}
	return "unverified", "format header checked; no authoritative checksum"
}
