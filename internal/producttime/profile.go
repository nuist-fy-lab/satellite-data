// Package producttime describes how a catalogue product represents time.
// File duration on a polar orbit is not a recurring scan schedule. Composite
// windows are taken from official granule coverage, never an invented epoch.
package producttime

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/slotaxis"
)

type Profile struct {
	Kind           string `json:"kind"`
	Label          string `json:"label"`
	CadenceMinutes int    `json:"cadence_minutes,omitempty"`
	Days           int    `json:"days,omitempty"`
}

var nDays = regexp.MustCompile(`(?i)\b(\d+)[ -]days?\b`)

func Resolve(p model.Product) Profile {
	cadence := strings.ToLower(strings.TrimSpace(p.Cadence))
	title := strings.ToUpper(p.DisplayName)
	switch {
	case cadence == "1mo" || cadence == "monthly" || strings.Contains(title, "MONTHLY"):
		return Profile{Kind: "monthly", Label: "月产品"}
	case cadence == "1y" || cadence == "annual" || strings.Contains(title, "ANNUAL") || strings.Contains(title, "YEARLY"):
		return Profile{Kind: "annual", Label: "年产品"}
	case cadence == "dekad" || cadence == "1dekad" || strings.Contains(title, "DEKAD") || strings.Contains(title, "旬"):
		return Profile{Kind: "dekadal", Label: "旬产品"}
	case cadence == "1w" || cadence == "weekly" || strings.Contains(title, "WEEKLY"):
		return Profile{Kind: "weekly", Label: "周产品"}
	case cadence == "1d" || cadence == "daily" || strings.Contains(title, "DAILY"):
		// An eight-day rolling composite published daily remains a daily product.
		return Profile{Kind: "daily", Label: "日产品"}
	}
	days := 0
	if strings.HasSuffix(cadence, "d") {
		days, _ = strconv.Atoi(strings.TrimSuffix(cadence, "d"))
	}
	if days == 0 {
		if match := nDays.FindStringSubmatch(p.DisplayName); len(match) == 2 {
			days, _ = strconv.Atoi(match[1])
		}
	}
	if days > 1 && days <= 366 {
		return Profile{Kind: "composite", Label: strconv.Itoa(days) + "日合成", Days: days}
	}
	if strings.EqualFold(p.AreaType, "ORBIT") || strings.EqualFold(p.Level, "ORBIT") || p.SourceID == "cloudsat" || p.SourceID == "calipso" {
		return Profile{Kind: "orbit", Label: "轨道产品"}
	}
	geo := strings.HasPrefix(p.SourceID, "goes") || strings.HasPrefix(p.SourceID, "himawari") || p.SourceID == "eumetsat"
	axis := slotaxis.Resolve(p.SourceID, p.ProductKey, p.Cadence)
	if geo && axis.CadenceMinutes > 0 {
		return Profile{Kind: "scan", Label: "静止卫星时次", CadenceMinutes: axis.CadenceMinutes}
	}
	return Profile{Kind: "observation", Label: "观测区间"}
}

func (p Profile) IsPeriod() bool {
	switch p.Kind {
	case "daily", "weekly", "dekadal", "monthly", "annual", "composite":
		return true
	}
	return false
}

// ValidateRange keeps UTC product dates independent of the display timezone.
// Weekly and multi-day products use coverage intersections, because their
// official anchors and year-end windows differ between collections.
func (p Profile) ValidateRange(start, end time.Time) error {
	if start.IsZero() || !end.After(start) {
		return errors.New("invalid product time interval")
	}
	if !p.IsPeriod() {
		return nil
	}
	for _, at := range []time.Time{start.UTC(), end.UTC()} {
		if !at.Equal(at.Truncate(24 * time.Hour)) {
			return errors.New("period products require whole UTC dates")
		}
		if p.Kind == "monthly" && at.Day() != 1 {
			return errors.New("monthly products require whole calendar months")
		}
		if p.Kind == "annual" && (at.Month() != time.January || at.Day() != 1) {
			return errors.New("annual products require whole calendar years")
		}
		if p.Kind == "dekadal" && at.Day() != 1 && at.Day() != 11 && at.Day() != 21 {
			return errors.New("dekadal products require whole calendar dekads")
		}
	}
	return nil
}

func (p Profile) ValidateTimes(times []string, scenes map[string][]string) error {
	if p.Kind != "scan" && len(times) > 0 {
		return errors.New("this product uses coverage intervals, not recurring scan times")
	}
	if p.IsPeriod() && len(scenes) > 0 {
		return errors.New("period products use coverage intervals, not minute scenes")
	}
	for _, value := range times {
		at, err := time.Parse("15:04", value)
		if err != nil {
			return err
		}
		if p.CadenceMinutes > 0 && (at.Hour()*60+at.Minute())%p.CadenceMinutes != 0 {
			return errors.New("requested time is outside this product's nominal scan schedule")
		}
	}
	return nil
}

// OwnsCoverage includes a file already in progress at the beginning of a
// request, but assigns it only to that first chunk. Later chunks own new starts.
func OwnsCoverage(start, end, requestStart, cursor, chunkEnd time.Time) bool {
	if !start.Before(chunkEnd) {
		return false
	}
	if !start.Before(cursor) {
		return true
	}
	return cursor.Equal(requestStart) && end.After(requestStart)
}
