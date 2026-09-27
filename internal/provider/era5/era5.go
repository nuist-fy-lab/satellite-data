package era5

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

const apiBase = "https://cds.climate.copernicus.eu/api"

var datasets = []struct{ id, level, cadence, resolution string }{
	{"reanalysis-era5-single-levels", "single-levels", "1h", "0.25 degrees (reanalysis)"},
	{"reanalysis-era5-pressure-levels", "pressure-levels", "1h", "0.25 degrees (reanalysis)"},
	{"reanalysis-era5-single-levels-monthly-means", "single-levels", "monthly", "0.25 degrees (reanalysis)"},
	{"reanalysis-era5-pressure-levels-monthly-means", "pressure-levels", "monthly", "0.25 degrees (reanalysis)"},
	{"reanalysis-era5-land", "land", "1h", "0.1 degrees"},
	{"reanalysis-era5-land-monthly-means", "land", "monthly", "0.1 degrees"},
}

var ErrCredentialsRequired = errors.New("CDS Personal Access Token is not configured; add a central CDS credential and ERA5 account, and accept the dataset licence in CDS")
var ErrInvalidRequest = errors.New("invalid CDS request")

type Provider struct {
	client      *http.Client
	base        string
	token       func() (string, error)
	mu          sync.Mutex
	catalog     []model.Collection
	catalogTime time.Time
}

func New(client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Provider{client: client, base: apiBase, token: func() (string, error) { return "", ErrCredentialsRequired }}
}

// A centre-owned route keeps its credential in memory for this API operation.
func NewWithToken(client *http.Client, token string) *Provider {
	p := New(client)
	p.token = func() (string, error) {
		if token == "" {
			return "", ErrCredentialsRequired
		}
		return token, nil
	}
	return p
}

func (p *Provider) Source() model.Source {
	return model.Source{
		ID: "era5", Name: "ERA5 / ERA5-Land", Tier: model.TierToken,
		Method:       "CDS STAC catalogue + authenticated asynchronous subset requests",
		Capabilities: []model.Capability{model.CapabilityCatalog, model.CapabilityRetrieval},
		Description:  "ECMWF reanalysis, not satellite observations. Catalogue and request schemas are public; retrieval requires a CDS Personal Access Token and accepted licences. Files are generated on demand, not published granules. Downloader archive integration is not yet available.",
	}
}

type catalogCollection struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	License string `json:"license"`
	Extent  struct {
		Temporal struct {
			Interval [][]*string `json:"interval"`
		} `json:"temporal"`
	} `json:"extent"`
}

func (p *Provider) Collections(ctx context.Context) ([]model.Collection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.catalog) > 0 && time.Since(p.catalogTime) < 6*time.Hour {
		return cloneCatalog(p.catalog), nil
	}
	out := make([]model.Collection, len(datasets))
	errs := make([]error, len(datasets))
	var wg sync.WaitGroup
	for n, dataset := range datasets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var item catalogCollection
			if err := p.request(ctx, http.MethodGet, "/catalogue/v1/collections/"+dataset.id, nil, "", &item); err != nil {
				errs[n] = err
				return
			}
			if item.ID != dataset.id || item.Title == "" {
				errs[n] = errors.New("CDS catalogue returned an invalid collection")
				return
			}
			metadata := map[string]string{
				"official_id": item.ID, "title": item.Title, "licence": item.License,
				"area_type": "global", "spatial_resolution": dataset.resolution,
				"data_kind": "reanalysis", "availability_mode": "on-demand",
				"catalogue_url":      p.base + "/catalogue/v1/collections/" + item.ID,
				"request_schema_url": p.base + "/retrieve/v1/processes/" + item.ID,
			}
			if len(item.Extent.Temporal.Interval) > 0 {
				interval := item.Extent.Temporal.Interval[0]
				if len(interval) == 2 {
					for k, key := range []string{"temporal_start", "temporal_end"} {
						if interval[k] != nil {
							metadata[key] = *interval[k]
						}
					}
				}
			}
			out[n] = model.Collection{
				ID: "era5:" + item.ID, SourceID: "era5", Satellite: "ECMWF",
				Instrument: "ERA5", Level: dataset.level, Product: item.ID,
				Cadence: dataset.cadence, Metadata: metadata,
			}
			if strings.Contains(item.ID, "era5-land") {
				out[n].Instrument = "ERA5-Land"
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	p.catalog, p.catalogTime = out, time.Now()
	return cloneCatalog(out), nil
}

func cloneCatalog(in []model.Collection) []model.Collection {
	out := append([]model.Collection(nil), in...)
	for n := range out {
		out[n].Metadata = make(map[string]string, len(in[n].Metadata))
		for key, value := range in[n].Metadata {
			out[n].Metadata[key] = value
		}
	}
	return out
}

func datasetID(collectionID string) (string, error) {
	id := strings.TrimPrefix(collectionID, "era5:")
	for _, dataset := range datasets {
		if id == dataset.id {
			return id, nil
		}
	}
	return "", provider.ErrUnsupportedCollection
}

func (p *Provider) Search(_ context.Context, q model.Query) ([]model.Granule, error) {
	if err := provider.ValidateQuery(q); err != nil {
		return nil, err
	}
	if _, err := datasetID(q.CollectionID); err != nil {
		return nil, err
	}
	return nil, provider.ErrOnDemand
}

type Process struct {
	ID     string                     `json:"id"`
	Inputs map[string]json.RawMessage `json:"inputs"`
}

func (p *Provider) Process(ctx context.Context, collectionID string) (Process, error) {
	id, err := datasetID(collectionID)
	if err != nil {
		return Process{}, err
	}
	var process Process
	err = p.request(ctx, http.MethodGet, "/retrieve/v1/processes/"+id, nil, "", &process)
	if err == nil && (process.ID != id || len(process.Inputs) == 0) {
		err = errors.New("CDS returned an invalid request schema")
	}
	return process, err
}

type Submission struct {
	CollectionID string                     `json:"collection"`
	Inputs       map[string]json.RawMessage `json:"inputs"`
}

// Jobs live in CDS. Polling is explicit, so queued requests do not occupy a
// synchronous search worker and submission is never retried automatically.
type Job struct {
	ID        string `json:"jobID"`
	ProcessID string `json:"processID,omitempty"`
	Status    string `json:"status"`
	Created   string `json:"created,omitempty"`
	Updated   string `json:"updated,omitempty"`
}

func (p *Provider) Submit(ctx context.Context, submission Submission) (Job, error) {
	id, err := datasetID(submission.CollectionID)
	if err != nil {
		return Job{}, err
	}
	if len(submission.Inputs) == 0 {
		return Job{}, fmt.Errorf("%w: inputs are required", ErrInvalidRequest)
	}
	token, err := p.token()
	if err != nil {
		return Job{}, err
	}
	process, err := p.Process(ctx, id)
	if err != nil {
		return Job{}, err
	}
	for name := range submission.Inputs {
		if _, ok := process.Inputs[name]; !ok {
			return Job{}, fmt.Errorf("%w: unsupported input field", ErrInvalidRequest)
		}
	}
	var job Job
	err = p.request(ctx, http.MethodPost, "/retrieve/v1/processes/"+id+"/execution", struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}{submission.Inputs}, token, &job)
	if err == nil && (!validJobID.MatchString(job.ID) || job.Status == "") {
		err = errors.New("CDS returned an invalid job; check your CDS requests before resubmitting")
	}
	return job, err
}

var validJobID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (p *Provider) Job(ctx context.Context, id string) (Job, error) {
	if !validJobID.MatchString(id) {
		return Job{}, fmt.Errorf("%w: invalid job id", ErrInvalidRequest)
	}
	token, err := p.token()
	if err != nil {
		return Job{}, err
	}
	var job Job
	err = p.request(ctx, http.MethodGet, "/retrieve/v1/jobs/"+id, nil, token, &job)
	if err == nil && (job.ID != id || job.Status == "") {
		err = errors.New("CDS returned an invalid job")
	}
	return job, err
}

func (p *Provider) Result(ctx context.Context, id string) (model.Asset, error) {
	if !validJobID.MatchString(id) {
		return model.Asset{}, fmt.Errorf("%w: invalid job id", ErrInvalidRequest)
	}
	token, err := p.token()
	if err != nil {
		return model.Asset{}, err
	}
	var result struct {
		Asset struct {
			Value struct {
				URL  string `json:"href"`
				Size int64  `json:"file:size"`
				Type string `json:"type"`
			} `json:"value"`
		} `json:"asset"`
	}
	if err := p.request(ctx, http.MethodGet, "/retrieve/v1/jobs/"+id+"/results", nil, token, &result); err != nil {
		return model.Asset{}, err
	}
	value := result.Asset.Value
	u, err := url.Parse(value.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || value.Size <= 0 {
		return model.Asset{}, errors.New("CDS returned an invalid result asset")
	}
	return model.Asset{ID: "era5:" + id, Name: path.Base(u.Path), URL: value.URL, Size: value.Size, MediaType: value.Type}, nil
}

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string {
	return fmt.Sprintf("CDS returned HTTP %d (check token, accepted licences and request parameters in CDS)", e.Status)
}

func (p *Provider) request(ctx context.Context, method, endpoint string, body any, token string, target any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return errors.New("invalid CDS request JSON")
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+endpoint, reader)
	if err != nil {
		return errors.New("invalid CDS API endpoint")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	// PRIVATE-TOKEN is not one of net/http's protected redirect headers.
	client := *p.client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("CDS API network request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &HTTPError{Status: resp.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(target); err != nil {
		return errors.New("CDS API returned invalid JSON")
	}
	return nil
}
