package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "satellite:", err)
		os.Exit(1)
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
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
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
	return json.NewDecoder(resp.Body).Decode(target)
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage:
  satellite source list
  satellite catalog list --source goes18
  satellite query list --source goes18 --collection goes18:ABI-L1b-RadF --start 2026-09-22T09:00:00Z --end 2026-09-22T10:00:00Z [--json]

Environment:
  SATELLITE_SERVER=http://127.0.0.1:8080`)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
