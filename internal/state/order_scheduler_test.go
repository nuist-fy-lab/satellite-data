package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLongOrderYieldsToAnotherOrderOfTheSameProvider(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	long, err := s.EnqueueDownloadRequest(t.Context(), "goes18", "goes", "goes18:GLM-L2-LCFA", first, first.AddDate(1, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	short, err := s.EnqueueDownloadRequest(t.Context(), "goes19", "goes", "goes19:GLM-L2-LCFA", first, first.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.ClaimDownloadRequest(t.Context(), "goes")
	if err != nil || !ok || claimed.ID != long.ID {
		t.Fatalf("first claim: %+v %v %v", claimed, ok, err)
	}
	if err := s.AdvanceDownloadRequest(t.Context(), long.ID, first.Add(time.Hour), 0, nil, false); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err = s.ClaimDownloadRequest(t.Context(), "goes")
	if err != nil || !ok || claimed.ID != short.ID {
		t.Fatalf("long order starved a later order: %+v %v %v", claimed, ok, err)
	}
}
