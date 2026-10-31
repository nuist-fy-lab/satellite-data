package cloudsat

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type orbitCacheEntry struct {
	start, end, last time.Time
	expires          time.Time
}

func (p *Provider) orbitCoverage(ctx context.Context, product string, item dataset, filenameStart time.Time) (time.Time, time.Time, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, time.Time{}, time.Time{}, err
	}
	key := item.URLPath + "\x00" + modifiedDate(item.Dates) + "\x00" + item.DataSize.Value + "\x00" + item.DataSize.Units
	p.cacheMu.Lock()
	if e, ok := p.cache[key]; ok && time.Now().Before(e.expires) {
		p.cacheMu.Unlock()
		return e.start, e.end, e.last, nil
	}
	p.cacheMu.Unlock()
	select {
	case p.metadataGate <- struct{}{}:
	case <-ctx.Done():
		return time.Time{}, time.Time{}, time.Time{}, ctx.Err()
	}
	defer func() { <-p.metadataGate }()
	name := strings.SplitN(product, ".", 2)[0]
	field := "/" + name + "/Geolocation_Fields/"
	endpoint := "https://dpc-thredds.cira.colostate.edu/thredds/dodsC/" + escapePath(item.URLPath) + ".dods?" + url.QueryEscape(field+"Profile_time") + "," + url.QueryEscape(field+"UTC_start")
	data, err := p.metadata.Fetch(ctx, endpoint, 1<<20)
	if err != nil {
		return time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("CloudSat orbit metadata %s: %w", item.Name, err)
	}
	start, end, last, err := parseOrbitDODS(data, name, filenameStart)
	if err != nil {
		return time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("CloudSat orbit metadata %s: %w", item.Name, err)
	}
	p.cacheMu.Lock()
	if len(p.cache) >= 512 {
		clear(p.cache)
	}
	p.cache[key] = orbitCacheEntry{start, end, last, time.Now().Add(5 * time.Minute)}
	p.cacheMu.Unlock()
	return start, end, last, nil
}

func parseOrbitDODS(data []byte, product string, filenameStart time.Time) (time.Time, time.Time, time.Time, error) {
	fail := func() (time.Time, time.Time, time.Time, error) {
		return time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid profile time response")
	}
	header, body, ok := bytes.Cut(data, []byte("\nData:\n"))
	if !ok {
		return fail()
	}
	field := regexp.QuoteMeta("/" + product + "/Geolocation_Fields/")
	re := regexp.MustCompile(`(?m)^\s*Float32 ` + field + `Profile_time\[nray = ([0-9]+)\];\s*\n\s*Float32 ` + field + `UTC_start;`)
	m := re.FindSubmatch(header)
	if m == nil {
		return fail()
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil || n < 1 || n > 200000 || len(body) != 8+4*n+4 || binary.BigEndian.Uint32(body[:4]) != uint32(n) || binary.BigEndian.Uint32(body[4:8]) != uint32(n) {
		return fail()
	}
	seconds := func(raw []byte) float64 { return float64(math.Float32frombits(binary.BigEndian.Uint32(raw))) }
	utc := seconds(body[8+4*n:])
	if math.IsNaN(utc) || math.IsInf(utc, 0) || utc < 0 || utc > 86400 {
		return fail()
	}
	min, max := math.Inf(1), math.Inf(-1)
	for i := 0; i < n; i++ {
		v := seconds(body[8+4*i:])
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 6000 {
			return fail()
		}
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	if seconds(body[8:12]) != 0 {
		return fail()
	}
	day := time.Date(filenameStart.UTC().Year(), filenameStart.UTC().Month(), filenameStart.UTC().Day(), 0, 0, 0, 0, time.UTC)
	first := day.Add(time.Duration(math.Round(utc * 1e9)))
	if first.Before(filenameStart) || !first.Before(filenameStart.Add(time.Second)) {
		return fail()
	}
	start := first.Add(time.Duration(math.Round(min * 1e9)))
	last := first.Add(time.Duration(math.Round(max * 1e9)))
	return start, last.Add(time.Nanosecond), last, nil
}
