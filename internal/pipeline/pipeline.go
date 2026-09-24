package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/archiveevent"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/auth"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/config"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/model"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/store"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/transfer"
)

type Runtime struct {
	cfg      config.Config
	store    *store.Store
	auth     *auth.Registry
	provider map[string]config.Provider
	clients  map[string]*transfer.HTTP
	events   *archiveevent.Notifier
	wg       sync.WaitGroup
}

func New(cfg config.Config, state *store.Store, authRegistry *auth.Registry) (*Runtime, error) {
	if state == nil || authRegistry == nil {
		return nil, errors.New("pipeline requires store and auth registry")
	}
	providers := make(map[string]config.Provider, len(cfg.Providers))
	clients := make(map[string]*transfer.HTTP, len(cfg.Providers))
	for _, item := range cfg.Providers {
		if strings.TrimSpace(item.SourceID) == "" {
			return nil, errors.New("provider source id is empty")
		}
		if _, exists := providers[item.SourceID]; exists {
			return nil, fmt.Errorf("duplicate provider %q", item.SourceID)
		}
		providers[item.SourceID] = item
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = max(128, item.TransferConcurrency*16)
		transport.MaxIdleConnsPerHost = max(16, item.TransferConcurrency*4)
		transport.IdleConnTimeout = 90 * time.Second
		clients[item.SourceID] = transfer.NewHTTP(&http.Client{
			Transport: transport,
			Timeout:   item.RequestTimeout,
		})
	}
	return &Runtime{
		cfg: cfg, store: state, auth: authRegistry, provider: providers, clients: clients,
		events: archiveevent.New(cfg.ArchiveEventSocket),
	}, nil
}

func (r *Runtime) Run(ctx context.Context) {
	go r.events.Run(ctx)
	for _, item := range r.cfg.Providers {
		if !item.Enabled {
			continue
		}
		item := item
		for worker := 0; worker < item.TransferConcurrency; worker++ {
			r.wg.Add(1)
			go func(worker int) {
				defer r.wg.Done()
				r.transferLoop(ctx, item, worker)
			}(worker)
		}
		for worker := 0; worker < item.VerifyConcurrency; worker++ {
			r.wg.Add(1)
			go func(worker int) {
				defer r.wg.Done()
				r.verifyLoop(ctx, item, worker)
			}(worker)
		}
	}
	go func() {
		<-ctx.Done()
		r.wg.Wait()
	}()
}

func (r *Runtime) Provider(sourceID string) (config.Provider, bool) {
	item, ok := r.provider[sourceID]
	return item, ok
}

func (r *Runtime) ProviderStatuses(ctx context.Context) ([]model.ProviderStatus, error) {
	out := make([]model.ProviderStatus, 0, len(r.cfg.Providers))
	for _, item := range r.cfg.Providers {
		counts, err := r.store.StateCounts(ctx, item.SourceID)
		if err != nil {
			return nil, err
		}
		out = append(out, model.ProviderStatus{
			SourceID: item.SourceID, Enabled: item.Enabled,
			TransferConcurrency: item.TransferConcurrency, VerifyConcurrency: item.VerifyConcurrency,
			AuthRef: item.AuthRef, AuthConfigured: r.auth.Configured(item.AuthRef), States: counts,
		})
	}
	return out, nil
}

func (r *Runtime) ValidateTask(spec model.TaskSpec) error {
	provider, ok := r.provider[spec.SourceID]
	if !ok {
		return fmt.Errorf("unknown provider %q", spec.SourceID)
	}
	if !provider.Enabled {
		return fmt.Errorf("provider %q is disabled", spec.SourceID)
	}
	if strings.TrimSpace(spec.AuthRef) != strings.TrimSpace(provider.AuthRef) {
		return fmt.Errorf("provider %q requires auth_ref %q", spec.SourceID, provider.AuthRef)
	}
	parsed, err := url.Parse(spec.URL)
	if err != nil || parsed.Scheme != "https" {
		return errors.New("asset URL must use HTTPS")
	}
	if !allowedHost(spec.SourceID, parsed.Hostname()) {
		return fmt.Errorf("asset host %q is not allowed for provider %q", parsed.Hostname(), spec.SourceID)
	}
	if strings.TrimSpace(spec.Name) == "" || strings.TrimSpace(spec.CollectionID) == "" || strings.TrimSpace(spec.GranuleID) == "" {
		return errors.New("name, collection_id and granule_id are required")
	}
	if spec.Size < 0 {
		return errors.New("asset size must not be negative")
	}
	return nil
}

func (r *Runtime) transferLoop(ctx context.Context, provider config.Provider, worker int) {
	poll := time.NewTimer(0)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
		if worker == 0 && provider.AuthRef != "" && r.auth.Configured(provider.AuthRef) {
			if err := r.store.ReleaseWaitingAuth(ctx, provider.SourceID); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("release provider auth wait", "provider", provider.SourceID, "worker", worker, "error", err)
			}
		}
		task, ok, err := r.store.ClaimTransfer(ctx, provider.SourceID)
		if err != nil {
			slog.Warn("claim provider transfer", "provider", provider.SourceID, "worker", worker, "error", err)
			poll.Reset(500 * time.Millisecond)
			continue
		}
		if !ok {
			poll.Reset(150 * time.Millisecond)
			continue
		}
		if provider.AuthRef != "" && !r.auth.Configured(provider.AuthRef) {
			_ = r.store.WaitForAuth(context.WithoutCancel(ctx), task.ID, fmt.Errorf("%s credentials are not configured", provider.AuthRef))
			poll.Reset(25 * time.Millisecond)
			continue
		}
		authorizer, err := r.auth.Resolve(provider.AuthRef)
		if err != nil {
			_ = r.store.Fail(context.WithoutCancel(ctx), task.ID, err)
			poll.Reset(25 * time.Millisecond)
			continue
		}
		requestCtx, cancel := context.WithTimeout(ctx, provider.RequestTimeout)
		result, err := r.clients[provider.SourceID].Download(requestCtx, task, r.cfg.DownloadRoot, authorizer)
		cancel()
		if err != nil {
			r.handleTransferError(ctx, provider, task, err)
			poll.Reset(25 * time.Millisecond)
			continue
		}
		if err := r.store.MarkDownloaded(context.WithoutCancel(ctx), task.ID, result.Path, result.Size); err != nil {
			slog.Warn("checkpoint provider transfer", "provider", provider.SourceID, "task", task.ID, "error", err)
		}
		poll.Reset(10 * time.Millisecond)
	}
}

func (r *Runtime) handleTransferError(ctx context.Context, provider config.Provider, task model.Task, cause error) {
	if auth.IsCredentialError(cause) {
		_ = r.store.WaitForAuth(context.WithoutCancel(ctx), task.ID, cause)
		return
	}
	var httpErr *transfer.HTTPError
	if errors.As(cause, &httpErr) {
		if provider.AuthRef != "" && (httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden) {
			_ = r.store.WaitForAuth(context.WithoutCancel(ctx), task.ID, cause)
			return
		}
		if httpErr.Status == http.StatusNotFound || httpErr.Status == http.StatusGone {
			_ = r.store.Fail(context.WithoutCancel(ctx), task.ID, cause)
			return
		}
	}
	var integrity *transfer.IntegrityError
	if errors.As(cause, &integrity) {
		_ = r.store.RetryTransfer(context.WithoutCancel(ctx), task.ID, cause, retryDelay(task.Attempts))
		return
	}
	_ = r.store.RetryTransfer(context.WithoutCancel(ctx), task.ID, cause, retryDelay(task.Attempts))
}

func (r *Runtime) verifyLoop(ctx context.Context, provider config.Provider, worker int) {
	poll := time.NewTimer(0)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
		task, ok, err := r.store.ClaimVerify(ctx, provider.SourceID)
		if err != nil {
			slog.Warn("claim provider verification", "provider", provider.SourceID, "worker", worker, "error", err)
			poll.Reset(500 * time.Millisecond)
			continue
		}
		if !ok {
			poll.Reset(150 * time.Millisecond)
			continue
		}
		result, err := transfer.Promote(task, r.cfg.ArchiveRoot)
		if err != nil {
			var integrity *transfer.IntegrityError
			if errors.As(err, &integrity) || errors.Is(err, os.ErrNotExist) {
				_ = os.Remove(task.StagingPath)
				_ = r.store.Redownload(context.WithoutCancel(ctx), task.ID, err, retryDelay(task.Attempts))
			} else {
				_ = r.store.RetryVerification(context.WithoutCancel(ctx), task.ID, err, retryDelay(task.Attempts))
			}
			poll.Reset(25 * time.Millisecond)
			continue
		}
		if err := r.store.Complete(context.WithoutCancel(ctx), task.ID, result.Path, result.SHA256, result.Size); err != nil {
			slog.Warn("checkpoint provider promotion", "provider", provider.SourceID, "task", task.ID, "error", err)
		} else {
			r.events.Notify(archiveevent.Event{
				TaskID: task.ID, SourceID: task.SourceID, CollectionID: task.CollectionID,
				GranuleID: task.GranuleID, Filename: task.Name, ArchivePath: result.Path,
				Size: result.Size, SHA256: result.SHA256,
			})
		}
		poll.Reset(10 * time.Millisecond)
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	exponent := min(attempt-1, 7)
	seconds := 5 * math.Pow(2, float64(exponent))
	delay := time.Duration(seconds) * time.Second
	if delay > 10*time.Minute {
		return 10 * time.Minute
	}
	return delay
}

func allowedHost(sourceID, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	switch sourceID {
	case "goes":
		return host == "storage.googleapis.com"
	case "modis", "viirs":
		return host == "data.laadsdaac.earthdatacloud.nasa.gov"
	case "mtg":
		return host == "api.eumetsat.int"
	case "earthcare":
		return host == "catalog.maap.eo.esa.int"
	case "calipso":
		return host == "data.asdc.earthdata.nasa.gov"
	case "cloudsat":
		return host == "dpc-thredds.cira.colostate.edu"
	case "himawari8":
		return host == "noaa-himawari8.s3.amazonaws.com"
	case "himawari9":
		return host == "noaa-himawari9.s3.amazonaws.com"
	default:
		return false
	}
}
