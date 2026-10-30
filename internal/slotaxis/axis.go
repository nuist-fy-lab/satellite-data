package slotaxis

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed profiles.json
var rawProfiles []byte

type rule struct {
	SourcePrefix         string `json:"source_prefix"`
	ProductPattern       string `json:"product_pattern"`
	CadenceMinutes       int    `json:"cadence_minutes"`
	BaseToleranceSeconds int    `json:"base_tolerance_seconds"`
	MaxToleranceSeconds  int    `json:"max_tolerance_seconds"`
	productRE            *regexp.Regexp
}

var rules = loadRules()

func loadRules() []rule {
	var values []rule
	if err := json.Unmarshal(rawProfiles, &values); err != nil {
		panic(err)
	}
	for i := range values {
		r := &values[i]
		var err error
		r.productRE, err = regexp.Compile(r.ProductPattern)
		if err != nil || r.SourcePrefix == "" || r.CadenceMinutes < 1 || r.BaseToleranceSeconds < 1 || r.MaxToleranceSeconds < r.BaseToleranceSeconds || 1440%r.CadenceMinutes != 0 {
			panic("invalid slot policy")
		}
	}
	return values
}

type Axis struct {
	CadenceMinutes int
	Base           time.Duration
	Max            time.Duration
}

func Resolve(source, collection, cadence string) Axis {
	if axis, ok := Known(source, collection); ok {
		return axis
	}
	if duration, err := time.ParseDuration(cadence); err == nil && duration >= time.Minute && duration%time.Minute == 0 {
		minutes := int(duration / time.Minute)
		if 1440%minutes == 0 {
			limit := min(180*time.Second, duration/2-time.Second)
			if limit > 0 {
				return Axis{CadenceMinutes: minutes, Base: min(time.Minute, limit), Max: limit}
			}
		}
	}
	return Axis{Base: time.Minute, Max: time.Minute}
}

// Known distinguishes explicit product scan policies from cadence guesses.
func Known(source, collection string) (Axis, bool) {
	product, ok := strings.CutPrefix(collection, source+":")
	if ok {
		for _, r := range rules {
			if strings.HasPrefix(source, r.SourcePrefix) && r.productRE.MatchString(product) {
				return Axis{CadenceMinutes: r.CadenceMinutes, Base: time.Duration(r.BaseToleranceSeconds) * time.Second, Max: time.Duration(r.MaxToleranceSeconds) * time.Second}, true
			}
		}
	}
	return Axis{}, false
}

// WithGOESMode uses the mode recorded in each file. The current catalogue's
// nominal ten-minute full disk must not erase historical 15/5-minute scans.
func (a Axis) WithGOESMode(source, collection, mode string) Axis {
	if !strings.HasPrefix(source, "goes") || !strings.HasSuffix(collection, "F") || a.CadenceMinutes == 0 {
		return a
	}
	switch mode {
	case "3":
		a.CadenceMinutes = 15
	case "4":
		a.CadenceMinutes = 5
		a.Max = min(a.Max, 2*time.Minute)
	case "6":
		a.CadenceMinutes = 10
	default:
		// An unknown mode retains actual event minutes instead of forcing a grid.
		a.CadenceMinutes = 0
	}
	return a
}

func (a Axis) Mode() string {
	if a.CadenceMinutes > 0 {
		return "grid"
	}
	return "events"
}

func distance(a, b time.Time) time.Duration {
	d := a.Sub(b)
	if d < 0 {
		return -d
	}
	return d
}

func median(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values[len(values)/2]
}

func (a Axis) Tolerance(observed []time.Time) time.Duration {
	limit := a.Max
	if a.CadenceMinutes > 0 {
		limit = min(limit, time.Duration(a.CadenceMinutes)*time.Minute/2-time.Second)
	}
	if limit < 0 {
		limit = 0
	}
	base := min(a.Base, limit)
	if a.CadenceMinutes == 0 || len(observed) < 3 {
		return base
	}
	offsets := make([]time.Duration, 0, len(observed))
	step := time.Duration(a.CadenceMinutes) * time.Minute
	for _, at := range observed {
		nominal := at.UTC().Round(step)
		offset := distance(at, nominal)
		if offset <= limit {
			offsets = append(offsets, offset)
		}
	}
	if len(offsets) < 3 {
		return base
	}
	mid := median(offsets)
	deviation := make([]time.Duration, 0, len(offsets))
	for _, offset := range offsets {
		deviation = append(deviation, absDuration(offset-mid))
	}
	return min(limit, max(base, mid+3*median(deviation)+15*time.Second))
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

// Nominal never snaps an out-of-tolerance scan into a fixed slot.
func (a Axis) Nominal(at time.Time, observed []time.Time) (time.Time, bool) {
	return a.NominalWithin(at, a.Tolerance(observed))
}

func (a Axis) NominalWithin(at time.Time, tolerance time.Duration) (time.Time, bool) {
	if a.CadenceMinutes == 0 {
		return time.Time{}, false
	}
	step := time.Duration(a.CadenceMinutes) * time.Minute
	nominal := at.UTC().Round(step)
	return nominal, distance(at, nominal) <= tolerance
}

func (a Axis) Classify(observed []time.Time) string {
	return a.ClassifyWithin(observed, a.Tolerance(observed))
}

func (a Axis) ClassifyWithin(observed []time.Time, tolerance time.Duration) string {
	if a.CadenceMinutes == 0 {
		return "events"
	}
	for _, at := range observed {
		if _, ok := a.NominalWithin(at, tolerance); !ok {
			return "events"
		}
	}
	return "grid"
}

// Match returns the requested nominal UTC slot. For event products the actual
// time is retained on the event axis, while neighbor isolation avoids one
// event being claimed by two near-duplicate requested times.
func (a Axis) Match(at time.Time, times []string, observed []time.Time) (time.Time, bool) {
	return a.MatchWithin(at, times, observed, a.Tolerance(observed))
}

func (a Axis) MatchWithin(at time.Time, times []string, observed []time.Time, tolerance time.Duration) (time.Time, bool) {
	if len(times) == 0 {
		return at.UTC(), true
	}
	if a.CadenceMinutes > 0 {
		nominal, ok := a.NominalWithin(at, tolerance)
		if !ok {
			return time.Time{}, false
		}
		for _, value := range times {
			if nominal.Format("15:04") == value {
				return nominal, true
			}
		}
		return time.Time{}, false
	}
	base := at.UTC().Truncate(24 * time.Hour)
	best := time.Time{}
	bestDelta := time.Duration(1<<63 - 1)
	for _, value := range times {
		clock, err := time.Parse("15:04", value)
		if err != nil {
			continue
		}
		for _, delta := range []int{-1, 0, 1} {
			want := base.AddDate(0, 0, delta).Add(time.Duration(clock.Hour())*time.Hour + time.Duration(clock.Minute())*time.Minute)
			if d := distance(at, want); d < bestDelta {
				best, bestDelta = want, d
			}
		}
	}
	limit := tolerance
	for _, neighbor := range observed {
		gap := distance(at, neighbor)
		if gap > 0 {
			limit = min(limit, max(0, gap/2-time.Second))
		}
	}
	return best, !best.IsZero() && bestDelta <= limit
}
