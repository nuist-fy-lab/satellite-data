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
	Kind             string `json:"kind"`
	Label            string `json:"label"`
	CadenceMinutes   int    `json:"cadence_minutes,omitempty"`
	SelectionMinutes int    `json:"selection_minutes,omitempty"`
	SelectionBasis   string `json:"selection_basis,omitempty"`
	Days             int    `json:"days,omitempty"`
	Semantic         string `json:"semantic,omitempty"`
	Subtype          string `json:"subtype,omitempty"`
	SemanticLabel    string `json:"semantic_label,omitempty"`
	WindowMinutes    int    `json:"window_minutes,omitempty"`
}

var nDays = regexp.MustCompile(`(?i)\b(\d+)[ -]days?\b`)

func Resolve(p model.Product) Profile {
	profile := resolveSchedule(p)
	profile = applySemantics(p, profile)
	if profile.SemanticLabel == "" {
		switch profile.Kind {
		case "scan":
			profile.SemanticLabel = "扫描时间"
		case "orbit":
			profile.SemanticLabel = "沿轨观测"
		case "daily":
			profile.SemanticLabel = "日产品"
		case "monthly":
			profile.SemanticLabel = "月产品"
		case "annual":
			profile.SemanticLabel = "年产品"
		case "composite":
			profile.SemanticLabel = profile.Label
		}
	}
	return profile
}

func resolveSchedule(p model.Product) Profile {
	cadence := strings.ToLower(strings.TrimSpace(p.Cadence))
	title := strings.ToUpper(p.DisplayName)
	switch {
	case cadence == "1mo" || cadence == "monthly" || strings.Contains(title, "MONTHLY"):
		return Profile{Kind: "monthly", Label: "月产品"}
	case cadence == "1y" || cadence == "annual" || strings.Contains(title, "ANNUAL") || strings.Contains(title, "YEARLY"):
		return Profile{Kind: "annual", Label: "年产品"}
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
		profile := Profile{Kind: "orbit", Label: "轨道产品"}
		if p.SourceID == "cloudsat" {
			profile.SelectionBasis = "file_start"
		}
		return profile
	}
	axis, known := slotaxis.Known(p.SourceID, p.ProductKey)
	// Only the documented FCI full-disk collections use this fixed cadence.
	if p.SourceID == "mtg" && cadence == "10m" && (p.ProductKey == "mtg:EO:EUM:DAT:0662" || p.ProductKey == "mtg:EO:EUM:DAT:0665") {
		axis, known = slotaxis.Resolve(p.SourceID, p.ProductKey, p.Cadence), true
	}
	if known {
		profile := Profile{Kind: "scan", Label: "扫描时次", CadenceMinutes: axis.CadenceMinutes, SelectionMinutes: axis.CadenceMinutes}
		if strings.HasPrefix(p.SourceID, "goes") && strings.HasSuffix(p.ProductKey, "F") {
			profile.SelectionMinutes = 5
		}
		return profile
	}
	return Profile{Kind: "observation", Label: "数据时间"}
}

func (p Profile) IsPeriod() bool {
	switch p.Kind {
	case "daily", "monthly", "annual", "composite":
		return true
	}
	return false
}

// ValidateRange keeps UTC product dates independent of the display timezone.
// Multi-day products use coverage intersections, because their
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
	all := append([]string(nil), times...)
	for _, selected := range scenes {
		all = append(all, selected...)
	}
	step := p.SelectionMinutes
	if step == 0 {
		step = p.CadenceMinutes
	}
	for _, value := range all {
		at, err := time.Parse("15:04", value)
		if err != nil {
			return err
		}
		if step > 0 && (at.Hour()*60+at.Minute())%step != 0 {
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
