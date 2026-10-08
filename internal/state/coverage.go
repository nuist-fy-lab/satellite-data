package state

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/slotaxis"
)

// Coverage v1 uses positional JSON arrays. Band totals are
// [band, official, complete, hourlyOfficial, hourlyComplete, partial,
//
//	hourlyPartial, error, hourlyError, processing, hourlyProcessing].
type BandTotals [11]int

// Times are [actual RFC3339Nano, nominal minute of day (-1 for events)].
// Cells are [time index, band, official assets, local OK assets, errors, processing].
type CoverageDay struct {
	Version       int      `json:"v"`
	Day           string   `json:"day"`
	OfficialState int      `json:"official_state"`
	Indexing      bool     `json:"indexing"`
	Mode          string   `json:"mode"`
	Cadence       int      `json:"cadence"`
	Bands         []int    `json:"bands"`
	Times         [][2]any `json:"times"`
	Cells         [][6]int `json:"cells"`
}

type CoverageYear struct {
	Version int   `json:"v"`
	Year    int   `json:"year"`
	Bands   []int `json:"bands"`
	// [ordinal, day status, official, complete, hourlyOfficial, hourlyComplete,
	//  officialGap, missing, error, processing, indexing, updatedAt, bandTotals,
	//  officialState, timelineMode]
	Days [][]any `json:"days"`
}

type CoverageMonth struct {
	Version int   `json:"v"`
	Year    int   `json:"year"`
	Month   int   `json:"month"`
	Bands   []int `json:"bands"`
	// [day of month, officialState, indexing, timelineMode, bandTotals,
	//  [hour, official assets, OK assets, partial slots, errors, processing]...]
	Days [][]any `json:"days"`
}

type CoverageFile struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	State    string `json:"state"`
}

type coverageKey struct{ product, day string }
type coverageEntry struct {
	revision      string
	totals        []BandTotals
	officialState int
	mode          string
}
type coverageCacheItem struct {
	key   coverageKey
	entry coverageEntry
}

const coverageCacheLimit = 4096

func (s *Store) cachedCoverage(key coverageKey, revision string) (coverageEntry, bool) {
	s.coverageMu.Lock()
	defer s.coverageMu.Unlock()
	element := s.coverageCache[key]
	if element == nil {
		return coverageEntry{}, false
	}
	item := element.Value.(coverageCacheItem)
	if item.entry.revision != revision {
		return coverageEntry{}, false
	}
	s.coverageLRU.MoveToFront(element)
	return item.entry, true
}

func (s *Store) rememberCoverage(key coverageKey, entry coverageEntry) {
	s.coverageMu.Lock()
	defer s.coverageMu.Unlock()
	if s.coverageCache == nil {
		s.coverageCache = make(map[coverageKey]*list.Element)
	}
	if element := s.coverageCache[key]; element != nil {
		element.Value = coverageCacheItem{key, entry}
		s.coverageLRU.MoveToFront(element)
		return
	}
	s.coverageCache[key] = s.coverageLRU.PushFront(coverageCacheItem{key, entry})
	if s.coverageLRU.Len() > coverageCacheLimit {
		oldest := s.coverageLRU.Back()
		delete(s.coverageCache, oldest.Value.(coverageCacheItem).key)
		s.coverageLRU.Remove(oldest)
	}
}

type assetKey struct{ timestamp, filename string }
type slotBand struct {
	timestamp string
	band      int
}
type assetState struct {
	band  int
	flags uint8
}

const (
	flagOK = 1 << iota
	flagError
	flagProcessing
)

func coverageProfile(product model.Product) (*downloadfilter.Profile, []int, error) {
	profile := downloadfilter.ForProduct(product.SourceID, product.ProductKey)
	if profile == nil {
		return nil, nil, errors.New("product has no band coverage profile")
	}
	bands := make([]int, 0, len(profile.Bands))
	for _, band := range profile.Bands {
		bands = append(bands, band.Number)
	}
	return profile, bands, nil
}

func (s *Store) coverageDay(ctx context.Context, product model.Product, day string) (CoverageDay, error) {
	profile, bands, err := coverageProfile(product)
	if err != nil {
		return CoverageDay{}, err
	}
	result := CoverageDay{Version: 1, Day: day, Bands: bands, Cadence: ProfileFor(product).CadenceMinutes,
		Mode: "events", Times: make([][2]any, 0), Cells: make([][6]int, 0)}
	var state string
	err = s.db.QueryRowContext(ctx, `SELECT state FROM official_days WHERE product_key=? AND day=?`, product.ProductKey, day).Scan(&state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return CoverageDay{}, err
	}
	if err == nil {
		result.OfficialState = 2
		if state == "official_missing" {
			result.OfficialState = 1
		}
	}
	var working int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM official_work WHERE product_key=? AND day=?`, product.ProductKey, day).Scan(&working); err != nil {
		return CoverageDay{}, err
	}
	result.Indexing = working > 0
	if result.OfficialState == 0 || result.OfficialState == 1 {
		if result.Cadence > 0 && 1440%result.Cadence == 0 {
			result.Mode = "grid"
		}
		return result, nil
	}

	// The day expression is indexed. Memory is bounded by one day's assets,
	// independently of the number of years in the archive.
	rows, err := s.db.QueryContext(ctx, `SELECT slot_ts,filename FROM official_assets
		WHERE product_key=? AND substr(slot_ts,1,10)=? ORDER BY slot_ts,filename`, product.ProductKey, day)
	if err != nil {
		return CoverageDay{}, err
	}
	assets := make(map[assetKey]*assetState)
	observed := make(map[string]time.Time)
	unmatched := false
	for rows.Next() {
		var ts, filename string
		if err := rows.Scan(&ts, &filename); err != nil {
			rows.Close()
			return CoverageDay{}, err
		}
		band := profile.BandForAsset(filename)
		if band == 0 {
			unmatched = true
			continue
		}
		key := assetKey{ts, filename}
		if _, exists := assets[key]; !exists {
			assets[key] = &assetState{band: band}
		}
		if _, exists := observed[ts]; !exists {
			if at, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				observed[ts] = at
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return CoverageDay{}, err
	}
	rows.Close()
	if len(assets) == 0 && (unmatched || result.OfficialState == 2) {
		result.OfficialState = 3
		return result, nil
	}
	rows, err = s.db.QueryContext(ctx, `SELECT slot_ts,filename,state FROM local_slots
		WHERE product_key=? AND substr(slot_ts,1,10)=?`, product.ProductKey, day)
	if err != nil {
		return CoverageDay{}, err
	}
	for rows.Next() {
		var ts, filename, state string
		if err := rows.Scan(&ts, &filename, &state); err != nil {
			rows.Close()
			return CoverageDay{}, err
		}
		asset := assets[assetKey{ts, filename}]
		if asset == nil {
			continue
		}
		switch state {
		case "ok":
			asset.flags |= flagOK
		case "error":
			asset.flags |= flagError
		case "processing":
			asset.flags |= flagProcessing
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return CoverageDay{}, err
	}
	rows.Close()
	intent, err := s.calendarIntentForDay(ctx, product.ProductKey, day)
	if err != nil {
		return CoverageDay{}, err
	}
	if intent != nil {
		for key, asset := range assets {
			if asset.flags&(flagOK|flagError) != 0 {
				continue
			}
			if at, parseErr := time.Parse(time.RFC3339Nano, key.timestamp); parseErr == nil && intent.matches(at, key.band) {
				asset.flags |= flagProcessing
			}
		}
	}

	axis := slotaxis.Resolve(product.SourceID, product.ProductKey, product.Cadence)
	timestamps := make([]string, 0, len(observed))
	times := make([]time.Time, 0, len(observed))
	for ts, at := range observed {
		timestamps = append(timestamps, ts)
		times = append(times, at)
	}
	sort.Strings(timestamps)
	tolerance := axis.Tolerance(times)
	result.Mode = axis.ClassifyWithin(times, tolerance)
	if result.Mode == "grid" {
		for _, ts := range timestamps {
			at, ok := observed[ts]
			if !ok {
				continue
			}
			nominal, matched := axis.NominalWithin(at, tolerance)
			if !matched || nominal.Format(time.DateOnly) != day {
				result.Mode = "events"
				break
			}
		}
	}
	index := make(map[string]int, len(timestamps))
	for _, ts := range timestamps {
		minute := -1
		if result.Mode == "grid" {
			if nominal, ok := axis.NominalWithin(observed[ts], tolerance); ok {
				minute = nominal.Hour()*60 + nominal.Minute()
			}
		}
		index[ts] = len(result.Times)
		result.Times = append(result.Times, [2]any{ts, minute})
	}
	cells := make(map[slotBand]*[6]int)
	for key, asset := range assets {
		k := slotBand{key.timestamp, asset.band}
		cell := cells[k]
		if cell == nil {
			cell = &[6]int{index[key.timestamp], asset.band}
			cells[k] = cell
		}
		cell[2]++
		if asset.flags&flagOK != 0 {
			cell[3]++
		}
		if asset.flags&flagError != 0 {
			cell[4]++
		}
		if asset.flags&flagProcessing != 0 {
			cell[5]++
		}
	}
	for _, cell := range cells {
		result.Cells = append(result.Cells, *cell)
	}
	sort.Slice(result.Cells, func(i, j int) bool {
		if result.Cells[i][0] != result.Cells[j][0] {
			return result.Cells[i][0] < result.Cells[j][0]
		}
		return result.Cells[i][1] < result.Cells[j][1]
	})
	return result, nil
}

func coverageTotals(day CoverageDay, hourlyOnly bool) []BandTotals {
	byBand := make(map[int]*BandTotals, len(day.Bands))
	type hourKey struct{ band, minute int }
	type outcome struct{ complete, partial, err, processing bool }
	hourSlots := make(map[hourKey]*outcome)
	for _, cell := range day.Cells {
		minute := day.Times[cell[0]][1].(int)
		if hourlyOnly && (minute < 0 || minute%60 != 0) {
			continue
		}
		band := cell[1]
		total := byBand[band]
		if total == nil {
			total = &BandTotals{band}
			byBand[band] = total
		}
		total[1]++
		complete := cell[2] > 0 && cell[3] >= cell[2] && cell[4] == 0
		if complete {
			total[2]++
		} else if cell[3] > 0 {
			total[5]++
		}
		if cell[4] > 0 {
			total[7]++
		}
		if cell[5] > 0 {
			total[9]++
		}
		if minute >= 0 && minute%60 == 0 {
			key := hourKey{band, minute}
			entry := hourSlots[key]
			if entry == nil {
				entry = &outcome{complete: true}
				hourSlots[key] = entry
			}
			entry.complete = entry.complete && complete
			entry.partial = entry.partial || cell[3] > 0
			entry.err = entry.err || cell[4] > 0
			entry.processing = entry.processing || cell[5] > 0
		}
	}
	for key, entry := range hourSlots {
		total := byBand[key.band]
		total[3]++
		if entry.complete {
			total[4]++
		} else if entry.partial {
			total[6]++
		}
		if entry.err {
			total[8]++
		}
		if entry.processing {
			total[10]++
		}
	}
	result := make([]BandTotals, 0, len(byBand))
	for _, total := range byBand {
		if hourlyOnly {
			total[1], total[2], total[5], total[7], total[9] = total[3], total[4], total[6], total[8], total[10]
		}
		result = append(result, *total)
	}
	sort.Slice(result, func(i, j int) bool { return result[i][0] < result[j][0] })
	return result
}

func (s *Store) cachedCoverageTotals(ctx context.Context, product model.Product, day YearDay) (coverageEntry, error) {
	if day.OfficialCount == 0 {
		state := 0
		if day.Status == "official_missing" {
			state = 1
		}
		return coverageEntry{totals: []BandTotals{}, officialState: state, mode: slotaxis.Resolve(product.SourceID, product.ProductKey, product.Cadence).Mode()}, nil
	}
	revision := product.Cadence
	if day.UpdatedAt != nil {
		revision += *day.UpdatedAt
	}
	key := coverageKey{product.ProductKey, day.Day}
	if day.DownloadCount == 0 {
		if entry, ok := s.cachedCoverage(key, revision); ok {
			return entry, nil
		}
	}
	coverage, err := s.coverageDay(ctx, product, day.Day)
	if err != nil {
		return coverageEntry{}, err
	}
	totals := coverageTotals(coverage, false)
	entry := coverageEntry{revision: revision, totals: totals, officialState: coverage.OfficialState, mode: coverage.Mode}
	if day.DownloadCount == 0 {
		s.rememberCoverage(key, entry)
	}
	return entry, nil
}

func (s *Store) BandCoverageYear(ctx context.Context, productKey string, year int) (CoverageYear, error) {
	_, product, err := s.ProductSource(ctx, productKey)
	if err != nil {
		return CoverageYear{}, err
	}
	_, bands, err := coverageProfile(product)
	if err != nil {
		return CoverageYear{}, err
	}
	calendar, err := s.Year(ctx, productKey, year)
	if err != nil {
		return CoverageYear{}, err
	}
	result := CoverageYear{Version: 1, Year: year, Bands: bands, Days: make([][]any, 0, len(calendar.Days))}
	for _, day := range calendar.Days {
		at, err := time.Parse(time.DateOnly, day.Day)
		if err != nil {
			return CoverageYear{}, err
		}
		entry, err := s.cachedCoverageTotals(ctx, product, day)
		if err != nil {
			return CoverageYear{}, err
		}
		updated := ""
		if day.UpdatedAt != nil {
			updated = *day.UpdatedAt
		}
		result.Days = append(result.Days, []any{at.YearDay() - 1, day.Status,
			day.OfficialCount, day.LocalOKCount, day.HourlyOfficialCount, day.HourlyLocalOKCount,
			day.OfficialGapCount, day.MissingCount, day.ErrorCount, day.ProcessingCount,
			day.Indexing, updated, entry.totals, entry.officialState, entry.mode})
	}
	return result, nil
}

func (s *Store) BandCoverageMonth(ctx context.Context, productKey string, year int, month, band int, hourly bool) (CoverageMonth, error) {
	if year < 1970 || year > 2100 || month < 1 || month > 12 {
		return CoverageMonth{}, errors.New("invalid month")
	}
	_, product, err := s.ProductSource(ctx, productKey)
	if err != nil {
		return CoverageMonth{}, err
	}
	_, bands, err := coverageProfile(product)
	if err != nil {
		return CoverageMonth{}, err
	}
	if band != 0 && !containsBand(bands, band) {
		return CoverageMonth{}, errors.New("invalid band")
	}
	result := CoverageMonth{Version: 1, Year: year, Month: month, Bands: bands, Days: make([][]any, 0, 31)}
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	for at := start; at.Month() == start.Month(); at = at.AddDate(0, 0, 1) {
		day, err := s.coverageDay(ctx, product, at.Format(time.DateOnly))
		if err != nil {
			return CoverageMonth{}, err
		}
		totals := coverageTotals(day, hourly)
		hours := [24][6]int{}
		for h := range hours {
			hours[h][0] = h
		}
		for _, cell := range day.Cells {
			if band != 0 && cell[1] != band {
				continue
			}
			minute := day.Times[cell[0]][1].(int)
			if minute < 0 {
				actual, err := time.Parse(time.RFC3339Nano, day.Times[cell[0]][0].(string))
				if err != nil {
					continue
				}
				minute = actual.UTC().Hour()*60 + actual.UTC().Minute()
			}
			if hourly && (day.Mode != "grid" || minute%60 != 0) {
				continue
			}
			hour := &hours[minute/60]
			hour[1] += cell[2]
			hour[2] += cell[3]
			if cell[3] > 0 && cell[3] < cell[2] {
				hour[3]++
			}
			hour[4] += cell[4]
			hour[5] += cell[5]
		}
		result.Days = append(result.Days, []any{at.Day(), day.OfficialState, day.Indexing, day.Mode, totals, hours})
	}
	return result, nil
}

func containsBand(bands []int, band int) bool {
	for _, n := range bands {
		if n == band {
			return true
		}
	}
	return false
}

func (s *Store) BandCoverageDay(ctx context.Context, productKey, day string) (CoverageDay, error) {
	if at, err := time.Parse(time.DateOnly, day); err != nil || at.Format(time.DateOnly) != day {
		return CoverageDay{}, errors.New("invalid day")
	}
	_, product, err := s.ProductSource(ctx, productKey)
	if err != nil {
		return CoverageDay{}, err
	}
	return s.coverageDay(ctx, product, day)
}

func (s *Store) BandCoverageFiles(ctx context.Context, productKey, slot string, band int) ([]CoverageFile, error) {
	at, err := time.Parse(time.RFC3339Nano, slot)
	if err != nil || !strings.HasSuffix(slot, "Z") || at.Format(time.DateOnly) != slot[:10] {
		return nil, errors.New("invalid slot")
	}
	_, product, err := s.ProductSource(ctx, productKey)
	if err != nil {
		return nil, err
	}
	profile, bands, err := coverageProfile(product)
	if err != nil {
		return nil, err
	}
	if !containsBand(bands, band) {
		return nil, errors.New("invalid band")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT filename,size FROM official_assets
		WHERE product_key=? AND slot_ts=? ORDER BY filename`, productKey, slot)
	if err != nil {
		return nil, err
	}
	files := make([]CoverageFile, 0)
	byFilename := make(map[string]int)
	for rows.Next() {
		var file CoverageFile
		if err := rows.Scan(&file.Filename, &file.Size); err != nil {
			rows.Close()
			return nil, err
		}
		if profile.BandForAsset(file.Filename) != band {
			continue
		}
		if _, duplicate := byFilename[file.Filename]; duplicate {
			continue
		}
		byFilename[file.Filename] = len(files)
		file.State = "missing"
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT filename,state FROM local_slots WHERE product_key=? AND slot_ts=?`, productKey, slot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var filename, state string
		if err := rows.Scan(&filename, &state); err != nil {
			return nil, err
		}
		index, ok := byFilename[filename]
		if !ok {
			continue
		}
		current := files[index].State
		if state == "error" || (state == "processing" && current != "error") || (state == "ok" && current == "missing") {
			files[index].State = state
		}
	}
	return files, rows.Err()
}
