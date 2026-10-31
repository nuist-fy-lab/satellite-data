package himawari

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestObservationReadFailuresAndLimits(t *testing.T) {
	p := &Provider{}
	if _, err := p.resolveObservations(context.Background(), make([]model.Asset, maxObservationObjects+1)); err == nil {
		t.Fatal("accepted too many assets")
	}
	p.openObservation = func(context.Context, model.Asset) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("incomplete")), nil
	}
	if _, err := p.resolveObservations(context.Background(), []model.Asset{{ID: "bad"}}); err == nil {
		t.Fatal("accepted invalid compressed header")
	}
	p.openObservation = func(ctx context.Context, _ model.Asset) (io.ReadCloser, error) { <-ctx.Done(); return nil, ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.resolveObservations(ctx, []model.Asset{{ID: "cancelled"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	reader := budgetReader{reader: strings.NewReader("abcd"), budget: &byteBudget{remaining: 3}}
	if _, err := io.ReadAll(reader); err == nil {
		t.Fatal("accepted exceeded query budget")
	}
}
