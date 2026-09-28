package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/archiveevent"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type officialEarthCARE struct{ assets []model.Asset }

func (p officialEarthCARE) Source() model.Source                                    { return model.Source{ID: "earthcare"} }
func (p officialEarthCARE) Collections(context.Context) ([]model.Collection, error) { return nil, nil }
func (p officialEarthCARE) Search(_ context.Context, q model.Query) ([]model.Granule, error) {
	return []model.Granule{{ID: "earthcare:official-123", CollectionID: q.CollectionID,
		Start: q.Start.Add(time.Hour), End: q.End, Assets: p.assets}}, nil
}

func TestSharedRootsRequireChecksumForCompletion(t *testing.T) {
	base := t.TempDir()
	local := filepath.Join(base, "satellite")
	earth := filepath.Join(base, "EarthCare")
	era := filepath.Join(base, "ERA5")
	for _, root := range []string{local, earth, era} {
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	h5 := []byte("\x89HDF\r\n\x1a\n" + "verified payload")
	name := "ECA_EXBC_CPR_FMR_2A_20260710T020939Z_20260710T040101Z_12014E.h5"
	path := filepath.Join(earth, "CPR_FMR_2A", "20260710", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, h5, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(era, "broken.nc"), []byte("not netcdf"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(era, "unknown"), []byte("unknown"), 0o640); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(base, "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assets := []model.Asset{{Name: name, Size: int64(len(h5))}}
	idx := New(provider.NewRegistry(officialEarthCARE{assets: assets}), store, local, earth, era)
	idx.scanLocal(t.Context())
	status, err := store.ArchiveScanStatus(t.Context())
	if err != nil || status.Files != 3 || status.Verified != 0 || status.Unverified != 2 || status.Errors != 1 {
		t.Fatalf("unverified historical files: %+v (%v)", status, err)
	}
	invalid := archiveevent.Event{SourceID: "earthcare", CollectionID: "earthcare:official-collection:CPR_FMR_2A", GranuleID: "earthcare:official-123", ArchivePath: path, Filename: name, Size: int64(len(h5)), SHA256: hex.EncodeToString(make([]byte, 32))}
	wrongOfficialSize := invalid
	wrongOfficialSize.Size++
	if err := idx.RecordArchiveEvent(context.Background(), wrongOfficialSize); err == nil {
		t.Fatal("file with wrong official size accepted")
	}
	if err := idx.RecordArchiveEvent(context.Background(), invalid); err == nil {
		t.Fatal("incorrect digest accepted")
	}
	valid := invalid
	digest := sha256.Sum256(h5)
	valid.SHA256 = hex.EncodeToString(digest[:])
	wrongOfficialItem := valid
	wrongOfficialItem.GranuleID = "earthcare:another-item"
	if err := idx.RecordArchiveEvent(context.Background(), wrongOfficialItem); err == nil {
		t.Fatal("file with wrong official item accepted")
	}
	if err := idx.RecordArchiveEvent(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	idx.scanLocal(t.Context())
	status, err = store.ArchiveScanStatus(t.Context())
	if err != nil || status.Verified != 1 || status.Unverified != 1 || status.Errors != 1 {
		t.Fatalf("verified archive was not indexed: %+v (%v)", status, err)
	}
	valid.ArchivePath = filepath.Join(t.TempDir(), "file.h5")
	if err := idx.RecordArchiveEvent(context.Background(), valid); err == nil {
		t.Fatal("path outside shared archive accepted")
	}
	outside := filepath.Join(base, "external")
	if err := os.Mkdir(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(earth, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, name), h5, 0o640); err != nil {
		t.Fatal(err)
	}
	valid.ArchivePath = filepath.Join(earth, "alias", name)
	if err := idx.RecordArchiveEvent(context.Background(), valid); err == nil {
		t.Fatal("archive path escaped via symlink")
	}
}

func TestERA5ArchiveRequiresCDSResultManifest(t *testing.T) {
	base := t.TempDir()
	for _, dir := range []string{"satellite", "EarthCare", "ERA5/CDS/dataset/2024/01/01/job-1"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(base, "ERA5", "CDS", "dataset", "2024", "01", "01", "job-1", "file.nc")
	content := []byte("CDF\x02" + "generated result")
	if err := os.WriteFile(path, content, 0o640); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(base, "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	idx := New(nil, store, filepath.Join(base, "satellite"), filepath.Join(base, "EarthCare"), filepath.Join(base, "ERA5"))
	digest := sha256.Sum256(content)
	event := archiveevent.Event{TaskID: 19, SourceID: "era5", CollectionID: "era5:dataset", GranuleID: "era5:job-1", Filename: "file.nc", ArchivePath: path, Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	if err := idx.RecordArchiveEvent(t.Context(), event); err == nil {
		t.Fatal("unmatched CDS result was accepted")
	}
	idx.scanLocal(t.Context())
	status, err := store.ArchiveScanStatus(t.Context())
	if err != nil || status.Verified != 0 || status.Unverified != 1 {
		t.Fatalf("unmatched CDS file: %+v (%v)", status, err)
	}
}

func TestMissingSharedArchiveRootDoesNotDropLocalIndex(t *testing.T) {
	base := t.TempDir()
	local := filepath.Join(base, "satellite")
	if err := os.MkdirAll(local, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "local.dat"), []byte("data"), 0o640); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(base, "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	New(nil, store, local).scanLocal(t.Context())
	status, err := store.ArchiveScanStatus(t.Context())
	if err != nil || status.Files != 1 {
		t.Fatalf("initial index: %+v (%v)", status, err)
	}
	New(nil, store, local, filepath.Join(base, "missing-root")).scanLocal(t.Context())
	status, err = store.ArchiveScanStatus(t.Context())
	if err != nil || status.Files != 1 {
		t.Fatalf("missing shared root erased old index: %+v (%v)", status, err)
	}
}

func TestHistoricalEarthCAREQueuesOnlyMatchingOfficialProductDay(t *testing.T) {
	base := t.TempDir()
	local, earth := filepath.Join(base, "satellite"), filepath.Join(base, "EarthCare")
	for _, root := range []string{local, earth} {
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(earth, "CPR_FMR_2A", "20260710")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{
		"ECA_EXBC_CPR_FMR_2A_20260710T020939Z_20260710T040101Z_12014E.h5",
		"ECA_EXBC_CPR_FMR_2A_20260709T020939Z_20260709T040101Z_12014E.h5",
	} {
		if err := os.WriteFile(filepath.Join(dir, filename), []byte("\x89HDF\r\n\x1a\ncontent"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	s, err := state.Open(filepath.Join(base, "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := model.Product{ProductKey: "earthcare:official:CPR_FMR_2A", SourceID: "earthcare", DataType: "CPR_FMR_2A"}
	if err := s.UpsertProducts(t.Context(), "earthcare", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	i := New(nil, s, local, earth)
	i.scanLocal(t.Context())
	batch, err := s.ClaimBatch(t.Context(), 7)
	if err != nil || batch.Product != product.ProductKey || len(batch.Days) != 1 || batch.Days[0] != "2026-07-10" {
		t.Fatalf("expected one official EarthCARE lookup: %+v (%v)", batch, err)
	}
}

func TestUnreadableSharedDirectoryDoesNotRemainVerified(t *testing.T) {
	base := t.TempDir()
	local, earth := filepath.Join(base, "satellite"), filepath.Join(base, "EarthCare")
	for _, dir := range []string{local, earth} {
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	name := "ECA_EXBC_CPR_FMR_2A_20260710T020939Z_20260710T040101Z_12014E.h5"
	locked := filepath.Join(earth, "CPR_FMR_2A")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("\x89HDF\r\n\x1a\ncontent")
	file := filepath.Join(locked, name)
	if err := os.WriteFile(file, content, 0o640); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(base, "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	idx := New(nil, store, local, earth)
	idx.scanLocal(t.Context())
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("cannot simulate unreadable directory as current user")
	}
	idx.scanLocal(t.Context())
	status, err := store.ArchiveScanStatus(t.Context())
	if err != nil || status.Files != 1 || status.Unverified != 1 || status.Verified != 0 || len(status.SkippedPaths) != 1 {
		t.Fatalf("inaccessible archive should retain only unverified metadata: %+v (%v)", status, err)
	}
}
