package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func parseCLIWindow(start, end, day string) (time.Time, time.Time, error) {
	if day != "" {
		if start != "" || end != "" {
			return time.Time{}, time.Time{}, errors.New("use --day or --start/--end, not both")
		}
		at, err := time.Parse(time.DateOnly, day)
		if err != nil || at.Format(time.DateOnly) != day {
			return time.Time{}, time.Time{}, errors.New("--day must be YYYY-MM-DD")
		}
		return at, at.AddDate(0, 0, 1), nil
	}
	parse := func(value string) (time.Time, error) {
		if at, err := time.Parse(time.RFC3339, value); err == nil {
			return at.UTC(), nil
		}
		return time.Parse(time.DateOnly, value)
	}
	first, err := parse(start)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("--start must be RFC3339 or YYYY-MM-DD")
	}
	last, err := parse(end)
	if err != nil || !last.After(first) {
		return time.Time{}, time.Time{}, errors.New("--end must be after --start (exclusive); use --day for one UTC day")
	}
	return first, last, nil
}

func (c *client) queryProducts(args []string) error {
	set := flag.NewFlagSet("query list", flag.ContinueOnError)
	source := set.String("source", "", "source ID; inferred from --product")
	collection := set.String("collection", "", "collection ID (compatible alias)")
	product := set.String("product", "", "complete product key")
	start := set.String("start", "", "start RFC3339 or YYYY-MM-DD")
	end := set.String("end", "", "exclusive end RFC3339 or YYYY-MM-DD")
	day := set.String("day", "", "one UTC day YYYY-MM-DD")
	band := set.Int("band", 0, "band for a product with a band profile")
	jsonOutput := set.Bool("json", false, "print JSON without transfer URLs or access references")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	sourceID, key, err := resolveProduct(*source, *collection, *product)
	if err != nil {
		return err
	}
	if sourceID == "era5" {
		if *band != 0 || *start != "" || *end != "" || *day != "" {
			return errors.New("ERA5 has no published granule listing; query its schema without date flags, then use `satellite download submit --product " + key + " --inputs-file request.json`")
		}
		return c.era5Schema([]string{"--collection", key})
	}
	first, last, err := parseCLIWindow(*start, *end, *day)
	if err != nil {
		return err
	}
	granules := []model.Granule{}
	{
		values := url.Values{"source": {sourceID}, "collection": {key}, "start": {first.Format(time.RFC3339Nano)}, "end": {last.Format(time.RFC3339Nano)}, "band": {strconv.Itoa(*band)}}
		var payload struct {
			Granules []model.Granule `json:"granules"`
		}
		if err := c.get("/api/v1/search?"+values.Encode(), &payload); err != nil {
			return fmt.Errorf("query %s: %w", key, err)
		}
		for _, granule := range payload.Granules {
			assets := []model.Asset{}
			for _, asset := range granule.Assets {
				// CLI queries report assets, not signed transfer URLs or access identities.
				asset.URL, asset.AuthRef, asset.Metadata = "", "", nil
				assets = append(assets, asset)
			}
			granule.Assets = assets
			granules = append(granules, granule)
		}
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(granules)
	}
	for _, granule := range granules {
		fmt.Printf("%s  assets=%d  %s\n", granule.Start.Format(time.RFC3339), len(granule.Assets), granule.ID)
	}
	if len(granules) == 0 {
		fmt.Fprintln(os.Stderr, "No granules found in this UTC interval; no order was created.")
	}
	return nil
}
