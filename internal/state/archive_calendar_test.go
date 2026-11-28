package state

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestArchiveCalendarUsesVerifiedLocalDaysAndCoalescesSpans(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, source := range []string{"modis", "viirs"} {
		if err := s.UpsertProducts(t.Context(), source, []model.Product{{ProductKey: source + ":a", SourceID: source}, {ProductKey: source + ":b", SourceID: source}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []struct{ product, time, state string }{
		{"modis:a", "2023-12-31T23:59:59Z", "ok"},
		{"modis:a", "2024-02-28T00:00:00Z", "ok"},
		{"modis:a", "2024-02-28T23:59:59.999Z", "ok"},
		{"modis:a", "2024-02-29T01:02:03Z", "ok"},
		{"modis:a", "2024-03-01T00:00:00Z", "ok"},
		{"modis:a", "2024-03-02T00:00:00Z", "error"},
		{"modis:a", "2024-12-31T23:59:59Z", "ok"},
		{"modis:b", "2025-01-01T00:00:00Z", "ok"},
		{"viirs:a", "2024-02-29T02:03:04Z", "ok"},
	} {
		if _, err := s.writer.ExecContext(t.Context(), `INSERT INTO local_slots
			(product_key,slot_ts,path,filename,size,mtime_ns,state,updated_at) VALUES(?,?,?,?,?,?,?,?)`,
			file.product, file.time, file.time, "file.nc", 7, 0, file.state, file.time); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ day, status string }{{"2024-03-01", "hourly_complete"}, {"2024-03-02", "complete"}} {
		if _, err := s.writer.Exec(`INSERT INTO day_summary(product_key,day,status,updated_at) VALUES('modis:a',?,?,?)`, row.day, row.status, row.day); err != nil {
			t.Fatal(err)
		}
	}
	var got []ArchiveCalendarProduct
	if err := s.VisitArchiveCalendar(t.Context(), []string{"modis"}, 2024, func(row ArchiveCalendarProduct) error {
		got = append(got, row)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []ArchiveCalendarProduct{{Product: "modis:a", Spans: [][3]int{{58, 60, 0}, {60, 61, 3}, {365, 366, 0}}}}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("archive snapshot = %+v, want %+v", got, want)
	}
	var pending int
	if err := s.db.QueryRow(`SELECT count(*) FROM official_work`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("read-only archive queued %d days: %v", pending, err)
	}
}
