package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type Indexer struct {
	registry     *provider.Registry
	store        *state.Store
	archiveRoot  string
	catalogWrite sync.Mutex
}

func New(registry *provider.Registry, store *state.Store, archiveRoot string) *Indexer {
	return &Indexer{registry: registry, store: store, archiveRoot: archiveRoot}
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
		case <-ticker.C:
			i.scanLocal(ctx)
		}
	}
}

func (i *Indexer) scanLocal(ctx context.Context) {
	if err := os.MkdirAll(i.archiveRoot, 0o750); err != nil {
		slog.Warn("create local archive root", "root", i.archiveRoot, "error", err)
		return
	}
	files := make([]state.LocalFile, 0, 1024)
	err := filepath.WalkDir(i.archiveRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".part") || strings.HasPrefix(name, ".") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, state.LocalFile{
			Path: path, Filename: name, Size: info.Size(), MtimeNS: info.ModTime().UnixNano(),
		})
		return nil
	})
	if err != nil {
		slog.Warn("scan local archive", "root", i.archiveRoot, "error", err)
		return
	}
	sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })
	hash := sha256.New()
	for _, file := range files {
		_, _ = hash.Write([]byte(file.Path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(time.Unix(0, file.MtimeNS).UTC().Format(time.RFC3339Nano)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(strings.TrimSpace(strings.ToLower(file.Filename))))
		_, _ = hash.Write([]byte{0})
	}
	fingerprint := hex.EncodeToString(hash.Sum(nil))
	changed, err := i.store.ReplaceLocalFiles(ctx, files, fingerprint)
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("persist local archive index", "root", i.archiveRoot, "error", err)
		return
	}
	if changed {
		slog.Info("local archive index refreshed", "root", i.archiveRoot, "files", len(files))
	}
}
