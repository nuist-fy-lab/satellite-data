package himawari

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

// Basic information block from the JP01 B01 sample documented in himawari-scenes.md.
const jp01BasicBlock = "ARoBCwAASGltYXdhcmktOQAAAAAAAE1TQwAAAAAAAAAAAAAAAABKUDAxUlQAALBGSAHAcu1AWhVqCsBy7UAyVHYYwHLtQAcGAAAAutsAAABNATEuMwAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAASFNfSDA5XzIwMjQwMTAxXzAwMDBfQjAxX0pQMDFfUjEwX1MwMTAxLkRBVAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestObservationHeader(t *testing.T) {
	header, err := base64.StdEncoding.DecodeString(jp01BasicBlock)
	if err != nil {
		t.Fatal(err)
	}
	start, end, err := parseObservationHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2024, 1, 1, 0, 0, 13, 524560000, time.UTC)
	wantEnd := time.Date(2024, 1, 1, 0, 1, 49, 839252000, time.UTC)
	if start.Sub(wantStart).Abs() > time.Microsecond || end.Sub(wantEnd).Abs() > time.Microsecond {
		t.Fatalf("start=%s end=%s", start, end)
	}
	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { return b[:69] },
		func(b []byte) []byte { b[5] = 2; return b },
		func(b []byte) []byte { b[1] = 0; return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint64(b[46:54], math.Float64bits(math.NaN())); return b },
		func(b []byte) []byte { copy(b[54:62], b[46:54]); return b },
	} {
		if _, _, err := parseObservationHeader(mutate(append([]byte(nil), header...))); err == nil {
			t.Fatal("accepted invalid header")
		}
	}
}

func TestObservationHeaderIdentity(t *testing.T) {
	header, err := base64.StdEncoding.DecodeString(jp01BasicBlock)
	if err != nil {
		t.Fatal(err)
	}
	name := "HS_H09_20240101_0000_B01_JP01_R10_S0101.DAT"
	if err := validateObservationIdentity(header, "AHI-L1b-Japan/2024/01/01/0000/"+name+".bz2"); err != nil {
		t.Fatal(err)
	}
	if err := validateObservationIdentity(header, name); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []string{
		strings.Replace(name, "JP01", "R301", 1),
		strings.Replace(name, "B01", "B02", 1),
		strings.Replace(name, "JP01", "JP02", 1),
		strings.Replace(name, "20240101", "20240102", 1),
		strings.Replace(name, "H09", "H08", 1),
		strings.Replace(name, "0000", "0010", 1),
	} {
		if err := validateObservationIdentity(header, wrong+".bz2"); err == nil {
			t.Fatalf("accepted mismatched object %s", wrong)
		}
	}
	for _, change := range []func([]byte){
		func(b []byte) { copy(b[38:42], "JP02") },
		func(b []byte) { copy(b[6:22], []byte("Himawari-8\x00\x00\x00\x00\x00\x00")) },
		func(b []byte) { binary.LittleEndian.PutUint16(b[44:46], 10) },
	} {
		modified := append([]byte(nil), header...)
		change(modified)
		if err := validateObservationIdentity(modified, name); err == nil {
			t.Fatal("accepted mismatched header field")
		}
	}
}

func TestMismatchedObservationIsNotCached(t *testing.T) {
	data, _ := base64.StdEncoding.DecodeString(identityFixtures["HS_H09_20240101_0000_B01_JP01_R10_S0101.DAT"])
	var calls int
	p := &Provider{openObservation: func(context.Context, model.Asset) (io.ReadCloser, error) {
		calls++
		return io.NopCloser(bytes.NewReader(data)), nil
	}}
	a := model.Asset{ID: "AHI-L1b-Japan/2024/01/01/0000/HS_H09_20240101_0000_B01_JP02_R10_S0101.DAT.bz2", ETag: "version"}
	for i := 0; i < 2; i++ {
		if _, err := p.resolveObservations(context.Background(), []model.Asset{a}); err == nil {
			t.Fatal("accepted wrong observation")
		}
	}
	if calls != 2 {
		t.Fatalf("header reads=%d, want 2", calls)
	}
}
