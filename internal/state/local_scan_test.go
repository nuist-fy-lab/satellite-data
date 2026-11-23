package state

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceLocalFileScan(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "archive.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	checkLocalFileScan(t, store)
}

func checkLocalFileScan(t *testing.T, store *Store) {
	t.Helper()
	ctx := t.Context()
	const root = "/fixture/archive"
	const count = 267
	sha := strings.Repeat("a", 64)
	for _, name := range []string{"sample-000.nc", "sample-001.nc", "sample-002.nc", "sample-003.nc"} {
		if err := store.RecordVerifiedArchiveFile(ctx, root+"/"+name, 10, 123, sha, "era5", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	interrupted := errors.New("archive directory disappeared")
	scan := func(fail bool) func(func(ScannedLocalFile) error) ([]string, error) {
		return func(add func(ScannedLocalFile) error) ([]string, error) {
			for index := count - 1; index >= 0; index-- {
				name := fmt.Sprintf("sample-%03d.nc", index)
				item := ScannedLocalFile{LocalFile: LocalFile{Path: root + "/" + name, Filename: name,
					Size: 10, MtimeNS: 123, SourceID: "era5", State: "unverified"}, CheckVerification: true}
				switch index {
				case 1:
					item.MtimeNS++
				case 2:
					item.CheckVerification = false
				case 3:
					item.State = "error"
				}
				if err := add(item); err != nil {
					return nil, err
				}
			}
			if fail {
				return nil, interrupted
			}
			return []string{root + "/inaccessible"}, nil
		}
	}
	last := ""
	observe := func(file LocalFile) {
		if file.Path <= last {
			t.Errorf("inventory order: %q after %q", file.Path, last)
		}
		last = file.Path
	}
	if changed, err := store.ReplaceLocalFileScan(ctx, []string{root}, scan(false), observe); err != nil || !changed {
		t.Fatalf("publish: changed=%t err=%v", changed, err)
	}
	last = ""
	if changed, err := store.ReplaceLocalFileScan(ctx, []string{root}, scan(false), observe); err != nil || changed {
		t.Fatalf("unchanged inventory: changed=%t err=%v", changed, err)
	}
	if changed, err := store.ReplaceLocalFileScan(ctx, []string{root}, scan(true), observe); !errors.Is(err, interrupted) || changed {
		t.Fatalf("interrupted inventory: changed=%t err=%v", changed, err)
	}
	status, err := store.ArchiveScanStatus(ctx)
	if err != nil || status.Files != count || status.Verified != 1 || status.Errors != 1 || status.Unverified != count-2 || len(status.SkippedPaths) != 1 {
		t.Fatalf("published inventory: %+v err=%v", status, err)
	}
}
