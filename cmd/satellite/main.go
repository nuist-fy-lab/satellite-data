package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

type client struct {
	base string
	http *http.Client
}

func main() {
	server := envOr("SATELLITE_SERVER", "http://127.0.0.1:8080")
	args := os.Args[1:]
	if len(args) < 2 {
		usage()
		os.Exit(2)
	}
	c := &client{base: strings.TrimRight(server, "/"), http: &http.Client{Timeout: 2 * time.Minute}}
	var err error
	switch args[0] + " " + args[1] {
	case "source list":
		err = c.sourceList()
	case "catalog list":
		err = c.catalogList(args[2:])
	case "query list":
		err = c.query(args[2:])
	case "download create":
		err = c.downloadCreate(args[2:])
	case "download submit":
		err = c.downloadSubmit(args[2:])
	case "download list":
		err = c.downloadList(args[2:])
	case "download status":
		err = c.downloadStatus(args[2:])
	case "download providers":
		err = c.downloadProviders()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "satellite:", err)
		os.Exit(1)
	}
}

type downloadRequest struct {
	ID           int64  `json:"id"`
	SourceID     string `json:"source_id"`
	ProviderID   string `json:"provider_id"`
	CollectionID string `json:"collection_id"`
	Selection    struct {
		Band  int      `json:"band"`
		Times []string `json:"times"`
	} `json:"selection"`
	Start              time.Time `json:"start"`
	End                time.Time `json:"end"`
	Cursor             time.Time `json:"cursor"`
	State              string    `json:"state"`
	GranulesDiscovered int       `json:"granules_discovered"`
	AssetsQueued       int       `json:"assets_queued"`
	FilesDone          int       `json:"files_done"`
	FilesFailed        int       `json:"files_failed"`
	FilesWaitingAuth   int       `json:"files_waiting_auth"`
	LastError          string    `json:"last_error,omitempty"`
}

type repeatedTimes []string

func (r *repeatedTimes) String() string { return strings.Join(*r, ",") }
func (r *repeatedTimes) Set(value string) error {
	*r = append(*r, value)
	return nil
}

// Submit uses inclusive UTC calendar dates, matching nsmc-lab job create.
func (c *client) downloadSubmit(args []string) error {
	set := flag.NewFlagSet("download submit", flag.ContinueOnError)
	source := set.String("source", "", "source id")
	collection := set.String("collection", "", "collection id")
	start := set.String("start", "", "first UTC date YYYY-MM-DD (inclusive)")
	end := set.String("end", "", "last UTC date YYYY-MM-DD (inclusive)")
	band := set.Int("band", 0, "GOES ABI band (1-16)")
	hourly := set.Bool("hourly", false, "select every UTC hour at HH:00")
	jsonOutput := set.Bool("json", false, "print JSON")
	var times repeatedTimes
	set.Var(&times, "time", "UTC scan time HH:MM; repeatable")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *source == "" || *collection == "" {
		return errors.New("--source and --collection are required")
	}
	first, err := time.Parse(time.DateOnly, *start)
	if err != nil || first.Format(time.DateOnly) != *start {
		return errors.New("--start must be YYYY-MM-DD")
	}
	last, err := time.Parse(time.DateOnly, *end)
	if err != nil || last.Format(time.DateOnly) != *end || last.Before(first) {
		return errors.New("--end must be YYYY-MM-DD and not before --start")
	}
	if *hourly && len(times) > 0 {
		return errors.New("use --hourly or --time, not both")
	}
	if *hourly {
		for hour := 0; hour < 24; hour++ {
			times = append(times, fmt.Sprintf("%02d:00", hour))
		}
	}
	var payload struct {
		Download downloadRequest `json:"download"`
	}
	if err := c.post("/api/v1/downloads", map[string]any{
		"source": *source, "collection": *collection, "start": *start,
		"end": last.AddDate(0, 0, 1).Format(time.DateOnly), "band": *band, "times": times,
	}, &payload); err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(payload.Download)
	}
	printDownload(payload.Download)
	return nil
}

func (c *client) downloadCreate(args []string) error {
	set := flag.NewFlagSet("download create", flag.ContinueOnError)
	source := set.String("source", "", "source id")
	collection := set.String("collection", "", "collection id")
	start := set.String("start", "", "start RFC3339 or YYYY-MM-DD")
	end := set.String("end", "", "end RFC3339 or YYYY-MM-DD")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *source == "" || *collection == "" || *start == "" || *end == "" {
		return errors.New("--source, --collection, --start and --end are required")
	}
	var payload struct {
		Download downloadRequest `json:"download"`
	}
	request := map[string]string{
		"source": *source, "collection": *collection, "start": *start, "end": *end,
	}
	if err := c.post("/api/v1/downloads", request, &payload); err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(payload.Download)
	}
	printDownload(payload.Download)
	return nil
}

func (c *client) downloadList(args []string) error {
	set := flag.NewFlagSet("download list", flag.ContinueOnError)
	limit := set.Int("limit", 50, "maximum requests to return")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	var payload struct {
		Downloads []downloadRequest `json:"downloads"`
	}
	if err := c.get("/api/v1/downloads?limit="+strconv.Itoa(*limit), &payload); err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(payload.Downloads)
	}
	for _, item := range payload.Downloads {
		printDownload(item)
	}
	return nil
}

func (c *client) downloadStatus(args []string) error {
	set := flag.NewFlagSet("download status", flag.ContinueOnError)
	id := set.Int64("id", 0, "download request id")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *id <= 0 {
		return errors.New("--id is required")
	}
	var payload struct {
		Download downloadRequest `json:"download"`
	}
	if err := c.get("/api/v1/downloads/"+strconv.FormatInt(*id, 10), &payload); err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(payload.Download)
	}
	printDownload(payload.Download)
	return nil
}

func (c *client) downloadProviders() error {
	var payload struct {
		Providers []struct {
			SourceID       string         `json:"source_id"`
			Enabled        bool           `json:"enabled"`
			AuthRef        string         `json:"auth_ref"`
			AuthConfigured bool           `json:"auth_configured"`
			States         map[string]int `json:"states"`
		} `json:"providers"`
	}
	if err := c.get("/api/v1/downloader/providers", &payload); err != nil {
		return err
	}
	for _, item := range payload.Providers {
		fmt.Printf("%-12s enabled=%-5t auth=%-10s configured=%-5t states=%v\n",
			item.SourceID, item.Enabled, item.AuthRef, item.AuthConfigured, item.States)
	}
	return nil
}

func printDownload(item downloadRequest) {
	fmt.Printf("#%-5d %-11s %-10s files=%d/%d failed=%d auth=%d cursor=%s band=%d times=%d %s\n",
		item.ID, item.SourceID, item.State, item.FilesDone, item.AssetsQueued,
		item.FilesFailed, item.FilesWaitingAuth, item.Cursor.Format(time.RFC3339), item.Selection.Band, len(item.Selection.Times), item.CollectionID)
	if item.LastError != "" {
		fmt.Printf("  error: %s\n", item.LastError)
	}
}

func (c *client) sourceList() error {
	var payload struct {
		Sources []model.Source `json:"sources"`
	}
	if err := c.get("/api/v1/sources", &payload); err != nil {
		return err
	}
	for _, source := range payload.Sources {
		fmt.Printf("%-12s %-14s %-16s %s\n", source.ID, source.Tier, source.Name, source.Method)
	}
	return nil
}

func (c *client) catalogList(args []string) error {
	set := flag.NewFlagSet("catalog list", flag.ContinueOnError)
	source := set.String("source", "", "source id")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *source == "" {
		return errors.New("--source is required")
	}
	var payload struct {
		Collections []model.Collection `json:"collections"`
	}
	if err := c.get("/api/v1/catalog?source="+url.QueryEscape(*source), &payload); err != nil {
		return err
	}
	for _, collection := range payload.Collections {
		fmt.Printf("%-38s %-10s %-8s %-7s %s\n", collection.ID, collection.Instrument, collection.Level, collection.Cadence, collection.Product)
	}
	return nil
}

func (c *client) query(args []string) error {
	set := flag.NewFlagSet("query list", flag.ContinueOnError)
	source := set.String("source", "", "source id")
	collection := set.String("collection", "", "collection id")
	start := set.String("start", "", "start RFC3339 or YYYY-MM-DD")
	end := set.String("end", "", "end RFC3339 or YYYY-MM-DD")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *source == "" || *collection == "" || *start == "" || *end == "" {
		return errors.New("--source, --collection, --start and --end are required")
	}
	values := url.Values{
		"source":     {*source},
		"collection": {*collection},
		"start":      {*start},
		"end":        {*end},
	}
	var payload struct {
		Count    int             `json:"count"`
		Granules []model.Granule `json:"granules"`
	}
	if err := c.get("/api/v1/search?"+values.Encode(), &payload); err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(payload.Granules)
	}
	for _, granule := range payload.Granules {
		fmt.Printf("%s  assets=%d  %s\n", granule.Start.Format(time.RFC3339), len(granule.Assets), granule.ID)
	}
	return nil
}

func (c *client) get(path string, target any) error {
	return c.do(http.MethodGet, path, nil, target)
}

func (c *client) post(path string, body any, target any) error {
	return c.do(http.MethodPost, path, body, target)
}

func (c *client) do(method, path string, body any, target any) error {
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(payload))
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&problem)
		if problem.Error == "" {
			problem.Error = resp.Status
		}
		return errors.New(problem.Error)
	}
	if target == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage:
  satellite source list
  satellite catalog list --source goes18
  satellite query list --source goes18 --collection goes18:ABI-L1b-RadF --start 2026-09-22T09:00:00Z --end 2026-09-22T10:00:00Z [--json]
  satellite download create --source goes18 --collection goes18:ABI-L1b-RadF --start 2026-09-22 --end 2026-09-23 [--json]
  satellite download submit --source goes18 --collection goes18:ABI-L1b-RadF --start 2023-06-01 --end 2023-06-30 --band 6 --hourly [--json]
  satellite download list [--limit 50] [--json]
  satellite download status --id 1 [--json]
  satellite download providers

Environment:
  SATELLITE_SERVER=http://127.0.0.1:8080`)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
