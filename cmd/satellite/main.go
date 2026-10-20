package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

type client struct {
	base   string
	apiKey string
	http   *http.Client
}

func main() {
	args := os.Args[1:]
	if len(args) < 2 {
		usage()
		os.Exit(2)
	}
	if args[0] == "config" && args[1] == "set" {
		if err := runConfigSet(args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "satellite:", err)
			os.Exit(1)
		}
		return
	}
	cfg, err := loadCLIConfig(configPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "satellite:", err)
		os.Exit(1)
	}
	c := newClient(cfg)
	switch args[0] + " " + args[1] {
	case "source list":
		err = c.sourceList(args[2:]...)
	case "catalog list":
		err = c.catalogList(args[2:])
	case "product list":
		err = c.productList(args[2:])
	case "product show":
		err = c.productShow(args[2:])
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
	case "download batch-status":
		err = c.downloadBatchStatus(args[2:])
	case "download providers":
		err = c.downloadProviders()
	case "era5 schema":
		err = c.era5Schema(args[2:])
	case "era5 submit":
		err = c.era5Submit(args[2:])
	case "era5 status":
		err = c.era5Status(args[2:])
	case "era5 result":
		err = c.era5Result(args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "satellite:", err)
		os.Exit(1)
	}
}

func newClient(cfg cliConfig) *client {
	return &client{base: cfg.Server, apiKey: cfg.APIKey, http: &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
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
	FailureCount       int       `json:"failure_count"`
	RequestedAt        time.Time `json:"requested_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	CompletedAt        time.Time `json:"completed_at,omitempty"`
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
	return c.dataOrder(args, true)
}

func (c *client) downloadCreate(args []string) error {
	return c.dataOrder(args, false)
}

func (c *client) downloadList(args []string) error {
	set := flag.NewFlagSet("download list", flag.ContinueOnError)
	limit := set.Int("limit", 50, "maximum requests to return")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 || *limit < 1 || *limit > 500 {
		return errors.New("--limit must be 1-500; no positional arguments")
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
	if *id <= 0 || set.NArg() != 0 {
		return errors.New("positive --id required; no positional arguments")
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

func (c *client) downloadBatchStatus(args []string) error {
	set := flag.NewFlagSet("download batch-status", flag.ContinueOnError)
	id := set.Int64("id", 0, "parent batch order ID")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *id <= 0 || set.NArg() != 0 {
		return errors.New("positive --id required")
	}
	var response struct {
		Group struct {
			ID           int64             `json:"id"`
			State        string            `json:"state"`
			Products     int               `json:"products"`
			AssetsQueued int               `json:"assets_queued"`
			FilesDone    int               `json:"files_done"`
			FilesFailed  int               `json:"files_failed"`
			Downloads    []downloadRequest `json:"downloads"`
		} `json:"group"`
	}
	if err := c.get("/api/v1/downloads/batch/"+strconv.FormatInt(*id, 10), &response); err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(response.Group)
	}
	fmt.Printf("批量订单 #%d · %d 产品 · %s · 已完成 %d / 已发现 %d · 失败 %d\n",
		response.Group.ID, response.Group.Products, response.Group.State,
		response.Group.FilesDone, response.Group.AssetsQueued, response.Group.FilesFailed)
	for _, item := range response.Group.Downloads {
		printDownload(item)
	}
	return nil
}

func (c *client) downloadProviders() error {
	var payload struct {
		Providers []struct {
			SourceID       string         `json:"source_id"`
			Enabled        bool           `json:"enabled"`
			AuthConfigured bool           `json:"auth_configured"`
			States         map[string]int `json:"states"`
		} `json:"providers"`
	}
	if err := c.get("/api/v1/downloader/providers", &payload); err != nil {
		return err
	}
	for _, item := range payload.Providers {
		fmt.Printf("%-12s enabled=%-5t configured=%-5t states=%v\n",
			item.SourceID, item.Enabled, item.AuthConfigured, item.States)
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

func (c *client) sourceList(args ...string) error {
	set := flag.NewFlagSet("source list", flag.ContinueOnError)
	jsonOutput := set.Bool("json", false, "print source capabilities as JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	var payload struct {
		Sources []model.Source `json:"sources"`
	}
	if err := c.get("/api/v1/sources", &payload); err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(payload.Sources)
	}
	for _, source := range payload.Sources {
		fmt.Printf("%-12s %-14s %-16s %s\n", source.ID, source.Tier, source.Name, source.Method)
	}
	return nil
}

func (c *client) catalogList(args []string) error {
	return c.productList(args)
}

func (c *client) query(args []string) error {
	return c.queryProducts(args)
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
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
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
		if c.apiKey != "" {
			problem.Error = strings.ReplaceAll(problem.Error, c.apiKey, "[REDACTED]")
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
  satellite config set
  satellite source list [--json]
  satellite product list [--source SOURCE] [--search TEXT] [--instrument NAME] [--level LEVEL] [--json]
  satellite product show --product SOURCE:PRODUCT
  satellite catalog list [--source SOURCE] [--search TEXT] [--json]
  satellite query list --product goes18:ABI-L1b-RadF --day 2026-09-22 [--band 6] [--json]
  satellite query list --source goes18 --collection goes18:ABI-L1b-RadF --start 2026-09-22T09:00:00Z --end 2026-09-22T10:00:00Z [--json]
  satellite download create --source goes18 --collection goes18:ABI-L1b-RadF --start 2026-09-22 --end 2026-09-23 [--json]
  satellite download submit --source goes18 --collection goes18:ABI-L1b-RadF --start 2023-06-01 --end 2023-06-30 --band 6 --hourly [--json]
  satellite download submit --product SOURCE:PRODUCT --product SOURCE:OTHER --day YYYY-MM-DD [--json]
  satellite download submit --product modis:COLLECTION --product modis:OTHER --start YYYY-MM-DD --end YYYY-MM-DD --scenes-file scenes.json [--json]
  satellite download batch-status --id 1 [--json]
  satellite download submit --product era5:reanalysis-era5-single-levels --start 2024-01-01 --end 2024-01-02 --variable 2m_temperature --area 32.25,-118.75,32,-118.5 [--json]
  satellite download list [--limit 50] [--json]
  satellite download status --id 1 [--json]
  satellite download providers
  satellite era5 schema --collection era5:reanalysis-era5-single-levels
  satellite era5 submit --collection era5:reanalysis-era5-single-levels --variable 2m_temperature --product-type reanalysis --year 2024 --month 01 --day 01 --time 00:00 --area 32.25,-118.75,32,-118.5 --data-format netcdf --download-format unarchived [--json]
  satellite era5 submit --collection era5:reanalysis-era5-single-levels --inputs-file request.json [--json]
  satellite era5 status --id JOB_ID [--json]
  satellite era5 result --id JOB_ID [--json]

Environment:
  SATELLITE_CONFIG selects the private CLI config file.
  SATELLITE_SERVER and SATELLITE_API_KEY override the saved centre configuration.`)
}
