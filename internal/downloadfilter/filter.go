package downloadfilter

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed profiles.json
var profilesJSON []byte

type Band struct {
	Number     int    `json:"number"`
	Wavelength string `json:"wavelength"`
	Resolution string `json:"resolution"`
}

type Profile struct {
	SourcePrefix     string `json:"source_prefix"`
	ProductPattern   string `json:"product_pattern"`
	AssetBandPattern string `json:"asset_band_pattern"`
	Bands            []Band `json:"bands"`
	productRE        *regexp.Regexp
	bandRE           map[int]*regexp.Regexp
	bandMatcher      *regexp.Regexp
}

var profiles = loadProfiles()

func loadProfiles() []Profile {
	var loaded []Profile
	if err := json.Unmarshal(profilesJSON, &loaded); err != nil {
		panic(err)
	}
	for i := range loaded {
		p := &loaded[i]
		if p.SourcePrefix == "" || !strings.Contains(p.AssetBandPattern, "{band}") || len(p.Bands) == 0 {
			panic("invalid download profile")
		}
		var err error
		p.productRE, err = regexp.Compile(p.ProductPattern)
		if err != nil {
			panic(err)
		}
		p.bandRE = make(map[int]*regexp.Regexp, len(p.Bands))
		p.bandMatcher, err = regexp.Compile(strings.ReplaceAll(p.AssetBandPattern, "{band}", `([0-9]{2})`))
		if err != nil {
			panic(err)
		}
		for _, band := range p.Bands {
			if band.Number < 1 || band.Resolution == "" || p.bandRE[band.Number] != nil {
				panic("invalid download band")
			}
			pattern := strings.ReplaceAll(p.AssetBandPattern, "{band}", fmt.Sprintf("%02d", band.Number))
			p.bandRE[band.Number], err = regexp.Compile(pattern)
			if err != nil {
				panic(err)
			}
		}
	}
	return loaded
}

// BandForAsset identifies a band in one pass, including products whose band
// naming convention differs from GOES. Zero means this asset is not a band file.
func (p *Profile) BandForAsset(filename string) int {
	if p == nil {
		return 0
	}
	parts := p.bandMatcher.FindStringSubmatch(filename)
	if len(parts) != 2 {
		return 0
	}
	number := int(parts[1][0]-'0')*10 + int(parts[1][1]-'0')
	if p.bandRE[number] == nil {
		return 0
	}
	return number
}

func ForProduct(source, collection string) *Profile {
	product, ok := strings.CutPrefix(collection, source+":")
	if !ok {
		return nil
	}
	for i := range profiles {
		p := &profiles[i]
		if strings.HasPrefix(source, p.SourcePrefix) && p.productRE.MatchString(product) {
			return p
		}
	}
	return nil
}

// Selection is optional; an empty selection retains the original full-range behavior.
// Times are UTC scan-start minutes, not the time at which a download begins.
type Selection struct {
	Band   int                 `json:"band,omitempty"`
	Times  []string            `json:"times,omitempty"`
	Scenes map[string][]string `json:"scenes,omitempty"`
}

func (s Selection) Normalize(source, collection string) (Selection, error) {
	if s.Band < 0 {
		return Selection{}, errors.New("band must be positive")
	}
	if s.Band != 0 {
		profile := ForProduct(source, collection)
		if profile == nil || profile.bandRE[s.Band] == nil {
			return Selection{}, errors.New("band not supported for this product")
		}
	}
	if len(s.Scenes) > 0 {
		if len(s.Times) > 0 {
			return Selection{}, errors.New("use per-day scenes or recurring UTC times, not both")
		}
		if len(s.Scenes) > 5000 {
			return Selection{}, errors.New("scene manifest exceeds 5000 UTC days")
		}
		normalized := make(map[string][]string, len(s.Scenes))
		for day, times := range s.Scenes {
			parsed, err := time.Parse(time.DateOnly, day)
			if err != nil || parsed.Format(time.DateOnly) != day || len(times) == 0 {
				return Selection{}, fmt.Errorf("invalid or empty scene date %q", day)
			}
			if len(times) > 288 {
				return Selection{}, fmt.Errorf("too many scene times on %s", day)
			}
			seen := make(map[string]bool, len(times))
			for _, value := range times {
				t, err := time.Parse("15:04", value)
				if err != nil || t.Format("15:04") != value {
					return Selection{}, fmt.Errorf("invalid scene time %q on %s", value, day)
				}
				seen[value] = true
			}
			normalized[day] = make([]string, 0, len(seen))
			for value := range seen {
				normalized[day] = append(normalized[day], value)
			}
			sort.Strings(normalized[day])
		}
		s.Scenes = normalized
	}
	seen := make(map[string]bool, len(s.Times))
	for _, value := range s.Times {
		t, err := time.Parse("15:04", value)
		if err != nil || t.Format("15:04") != value {
			return Selection{}, fmt.Errorf("invalid UTC time %q; use HH:MM", value)
		}
		seen[value] = true
	}
	if len(seen) > 0 {
		s.Times = make([]string, 0, len(seen))
		for value := range seen {
			s.Times = append(s.Times, value)
		}
		sort.Strings(s.Times)
	}
	return s, nil
}

// TimesForDay preserves recurring selection for ordinary requests, and
// selects only explicitly named scenes for research data manifests.
func (s Selection) TimesForDay(day string) ([]string, bool) {
	if len(s.Scenes) == 0 {
		return s.Times, true
	}
	times, ok := s.Scenes[day]
	return times, ok
}

// NextSceneDay skips years of unselected observations without remote queries.
func (s Selection) NextSceneDay(cursor, end time.Time) (time.Time, bool) {
	if len(s.Scenes) == 0 {
		return cursor, cursor.Before(end)
	}
	var next time.Time
	for day := range s.Scenes {
		at, err := time.Parse(time.DateOnly, day)
		if err != nil || at.Before(cursor.UTC().Truncate(24*time.Hour)) || !at.Before(end) {
			continue
		}
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	return next, !next.IsZero()
}

func (s Selection) MatchesAsset(source, collection, filename string) bool {
	if s.Band == 0 {
		return true
	}
	profile := ForProduct(source, collection)
	if profile == nil {
		return false
	}
	return profile.bandRE[s.Band] != nil && profile.bandRE[s.Band].MatchString(filename)
}
