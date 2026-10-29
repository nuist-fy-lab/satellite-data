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
	commandName := "download create"
	if inclusive {
		commandName = "download submit"
	}
	set := flag.NewFlagSet(commandName, flag.ContinueOnError)
	source := set.String("source", "", "source ID; inferred from product")
	collection := set.String("collection", "", "collection ID (compatible alias)")
	var products commaValues
	set.Var(&products, "product", "complete product key; repeatable or comma-separated for a multi-product batch")
	start := set.String("start", "", "first UTC date")
	end := set.String("end", "", "last UTC date (inclusive for submit; exclusive for create)")
	day := set.String("day", "", "one UTC date")
	band := set.Int("band", 0, "band for products with a band profile")
	hourly := set.Bool("hourly", false, "select every UTC hour")
	jsonOutput := set.Bool("json", false, "print JSON")
	file := set.String("inputs-file", "", "on-demand selection inputs JSON; dates remain centre-owned")
	scenesFile := set.String("scenes-file", "", "JSON map of UTC date to selected HH:MM times for sparse scene downloads")
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
	if len(products) > 1 && *collection != "" {
		return errors.New("use repeated --product or --collection, not both")
	}
	if len(products) > 1 && *source != "" {
		return errors.New("multi-product orders infer each source; omit --source")
	}
	firstProduct := ""
	if len(products) > 0 {
		firstProduct = products[0]
	}
	sourceID, key, err := resolveProduct(*source, *collection, firstProduct)
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
	var scenes map[string][]string
	if *scenesFile != "" {
		if *hourly || len(times) > 0 {
			return errors.New("use --scenes-file or --time/--hourly")
		}
		sf, err := os.Open(*scenesFile)
		if err != nil {
			return errors.New("scenes file must be a nonempty UTC-day map in JSON (max 1 MiB)")
		}
		defer sf.Close()
		raw, err := io.ReadAll(io.LimitReader(sf, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 || len(raw) == 0 || json.Unmarshal(raw, &scenes) != nil || len(scenes) == 0 {
			return errors.New("scenes file must be a nonempty UTC-day map in JSON (max 1 MiB)")
		}
		for day := range scenes {
			at, err := time.Parse(time.DateOnly, day)
			if err != nil || at.Format(time.DateOnly) != day || at.Before(first) || !at.Before(last) {
				return fmt.Errorf("scene date %q lies outside the requested UTC interval", day)
			}
		}
	}
	if *hourly {
		for hour := 0; hour < 24; hour++ {
			times = append(times, fmt.Sprintf("%02d:00", hour))
		}
	}
	selection, err := (downloadfilter.Selection{Band: *band, Times: times, Scenes: scenes}).Normalize(sourceID, key)
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
	if len(products) > 1 {
		keys := make([]string, 0, len(products))
		for _, product := range products {
			itemSource, resolved, err := resolveProduct("", "", product)
			if err != nil {
				return err
			}
			if itemSource != "era5" && len(inputs) != 0 {
				return errors.New("on-demand input flags are not supported for this product")
			}
			itemSelection, err := (downloadfilter.Selection{Band: *band, Times: times, Scenes: scenes}).Normalize(itemSource, resolved)
			if err != nil {
				return err
			}
			if itemSelection.Band != selection.Band {
				return errors.New("multi-product orders require compatible band selection across all products")
			}
			keys = append(keys, resolved)
		}
		var response struct {
			Group struct {
				ID        int64             `json:"id"`
				State     string            `json:"state"`
				Products  int               `json:"products"`
				Downloads []downloadRequest `json:"downloads"`
			} `json:"group"`
			Created bool `json:"created"`
		}
		if err := c.post("/api/v1/downloads/batch", map[string]any{
			"products": keys, "start": startValue, "end": endValue,
			"band": selection.Band, "times": selection.Times, "scenes": selection.Scenes, "inputs": inputs,
		}, &response); err != nil {
			return err
		}
		if response.Group.ID <= 0 || len(response.Group.Downloads) != len(keys) {
			return errors.New("invalid batch response; inspect download batch status before resubmitting")
		}
		if *jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(response)
		}
		fmt.Printf("批量订单 #%d · %d 个产品 · 状态 %s · %s\n", response.Group.ID,
			response.Group.Products, response.Group.State, map[bool]string{true: "已创建", false: "已去重复用"}[response.Created])
		for _, child := range response.Group.Downloads {
			printDownload(child)
		}
		return nil
	}
	var payload struct {
		Download downloadRequest `json:"download"`
	}
	if err := c.post("/api/v1/downloads", map[string]any{
		"source": sourceID, "collection": key, "start": startValue, "end": endValue,
		"band": selection.Band, "times": selection.Times, "scenes": selection.Scenes, "inputs": inputs,
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
