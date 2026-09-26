package downloadmon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type fakeClient struct {
	providers []downloader.ProviderStatus
	err       error
}

func (f *fakeClient) Providers(context.Context) ([]downloader.ProviderStatus, error) {
	return f.providers, f.err
}

func TestUnavailableDownloaderDoesNotRecordZero(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &fakeClient{providers: []downloader.ProviderStatus{{SourceID: "goes", Enabled: true, BytesPerSecond: 512}}}
	monitor := &Monitor{store: store, client: client, interval: 15 * time.Second}
	monitor.sample(t.Context())
	client.err = errors.New("socket unavailable")
	monitor.sample(t.Context())
	latest, err := store.LatestDownloadSamples(t.Context())
	if err != nil || len(latest) != 1 || latest[0].BytesPerSecond != 512 {
		t.Fatalf("latest after failure=%+v err=%v", latest, err)
	}
}
