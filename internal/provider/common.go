package provider

import (
	"fmt"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

const MaxInteractiveSearchWindow = 7 * 24 * time.Hour

func ValidateQuery(q model.Query) error {
	if strings.TrimSpace(q.CollectionID) == "" {
		return fmt.Errorf("collection id is required")
	}
	if q.Start.IsZero() || q.End.IsZero() {
		return fmt.Errorf("start and end are required")
	}
	if !q.End.After(q.Start) {
		return fmt.Errorf("end must be after start")
	}
	if q.End.Sub(q.Start) > MaxInteractiveSearchWindow {
		return fmt.Errorf("interactive search window exceeds %s; use a backfill job for larger ranges", MaxInteractiveSearchWindow)
	}
	return nil
}

func ProductMetadata(product string) (instrument, level string) {
	parts := strings.Split(product, "-")
	if len(parts) > 0 {
		instrument = parts[0]
	}
	for _, part := range parts {
		upper := strings.ToUpper(part)
		if strings.HasPrefix(upper, "L") && processingLevel.MatchString(part) {
			level = part
			break
		}
	}
	return instrument, level
}
