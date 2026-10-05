package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
)

type commaValues []string

func (v *commaValues) String() string { return strings.Join(*v, ",") }
func (v *commaValues) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return errors.New("empty selection value")
		}
		*v = append(*v, item)
	}
	return nil
}

func (c *client) dataOrder(args []string, inclusive bool) error {
	set := flag.NewFlagSet("download submit", flag.ContinueOnError)
	source := set.String("source", "", "source ID; inferred from product")
	collection := set.String("collection", "", "collection ID (compatible alias)")
	product := set.String("product", "", "complete product key")
	start := set.String("start", "", "first UTC date")
	end := set.String("end", "", "last UTC date (inclusive for submit; exclusive for create)")
	day := set.String("day", "", "one UTC date")
	band := set.Int("band", 0, "band for products with a band profile")
	hourly := set.Bool("hourly", false, "select every UTC hour")
	jsonOutput := set.Bool("json", false, "print JSON")
	file := set.String("inputs-file", "", "on-demand selection inputs JSON; dates remain centre-owned")
	area := set.String("area", "", "north,west,south,east (on-demand products)")
	format := set.String("data-format", "", "on-demand data format")
	var times repeatedTimes
	var variables, pressures commaValues
	set.Var(&times, "time", "UTC time HH:MM; repeatable")
	set.Var(&variables, "variable", "on-demand variables; repeatable or comma-separated")
	set.Var(&pressures, "pressure-level", "pressure levels; repeatable or comma-separated")
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
	if *day != "" {
		if *start != "" || *end != "" {
			return errors.New("use --day or --start/--end, not both")
		}
		*start, *end = *day, *day
		if !inclusive {
			at, err := time.Parse(time.DateOnly, *day)
			if err != nil {
				return errors.New("--day must be YYYY-MM-DD")
			}
			*end = at.AddDate(0, 0, 1).Format(time.DateOnly)
		}
	}
	var first, last time.Time
	if inclusive {
		first, err = time.Parse(time.DateOnly, *start)
		if err != nil || first.Format(time.DateOnly) != *start {
			return errors.New("--start must be YYYY-MM-DD")
		}
		if *end == "" {
			*end = *start
		}
		last, err = time.Parse(time.DateOnly, *end)
		if err != nil || last.Format(time.DateOnly) != *end || last.Before(first) {
			return errors.New("--end must be YYYY-MM-DD and not before --start")
		}
		last = last.AddDate(0, 0, 1)
	} else {
		first, last, err = parseCLIWindow(*start, *end, "")
		if err != nil {
			return err
		}
	}
	if *hourly && len(times) > 0 {
		return errors.New("use --hourly or --time, not both")
	}
	if *hourly {
		for hour := 0; hour < 24; hour++ {
			times = append(times, fmt.Sprintf("%02d:00", hour))
		}
	}
	selection, err := (downloadfilter.Selection{Band: *band, Times: times}).Normalize(sourceID, key)
	if err != nil {
		return err
	}
	inputs := make(map[string]json.RawMessage)
	if *file != "" {
		if len(variables)+len(pressures) > 0 || *area != "" || *format != "" {
			return errors.New("use --inputs-file or on-demand input flags, not both")
		}
		f, err := os.Open(*file)
		if err != nil {
			return errors.New("cannot open inputs file")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &inputs) != nil || len(inputs) == 0 {
			return errors.New("inputs file must contain one nonempty JSON object (max 1 MiB)")
		}
	} else {
		put := func(key string, value any) { inputs[key], _ = json.Marshal(value) }
		if len(variables) > 0 {
			put("variable", variables)
		}
		if len(pressures) > 0 {
			put("pressure_level", pressures)
		}
		if *format != "" {
			put("data_format", *format)
		}
		if *area != "" {
			coords := []float64{}
			for _, value := range strings.Split(*area, ",") {
				coord, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil {
					return errors.New("--area must contain four numeric coordinates")
				}
				coords = append(coords, coord)
			}
			encoded, err := json.Marshal(coords)
			if err != nil || len(coords) != 4 {
				return errors.New("--area must contain four finite coordinates")
			}
			inputs["area"] = encoded
		}
	}
	if sourceID != "era5" && len(inputs) != 0 {
		return errors.New("on-demand input flags are not supported for this product")
	}
	startValue, endValue := first.Format(time.RFC3339), last.Format(time.RFC3339)
	if inclusive {
		startValue, endValue = first.Format(time.DateOnly), last.Format(time.DateOnly)
	}
	var payload struct {
		Download downloadRequest `json:"download"`
	}
	if err := c.post("/api/v1/downloads", map[string]any{
		"source": sourceID, "collection": key, "start": startValue, "end": endValue,
		"band": selection.Band, "times": selection.Times, "inputs": inputs,
	}, &payload); err != nil {
		return err
	}
	if payload.Download.ID <= 0 {
		return errors.New("centre returned an invalid order ID; inspect order list before repeating the submission")
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(payload.Download)
	}
	printDownload(payload.Download)
	return nil
}
