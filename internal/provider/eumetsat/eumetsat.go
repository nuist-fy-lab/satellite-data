package eumetsat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

const (
	apiBase        = "https://api.eumetsat.int"
	searchEndpoint = apiBase + "/data/search-products/1.0.0/os"
	browseCatalog  = apiBase + "/data/browse/1.0.0/collections"
)

type Provider struct {
	client   *http.Client
	sourceID string

	catalogMu   sync.Mutex
	catalog     []model.Collection
	catalogTime time.Time
}

func New(client *http.Client) *Provider {
	return newProvider("mtg", client)
}

func NewIASI(client *http.Client) *Provider {
	return newProvider("iasi", client)
}

func NewIASING(client *http.Client) *Provider {
	return newProvider("iasi-ng", client)
}

func newProvider(sourceID string, client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	return &Provider{client: client, sourceID: sourceID}
}

func (p *Provider) Source() model.Source {
	if p.sourceID == "iasi" {
		return model.Source{
			ID:     "iasi",
			Name:   "IASI / Metop",
			Tier:   model.TierToken,
			Method: "EUMETSAT Data Store OpenSearch + OAuth2 download",
			Capabilities: []model.Capability{
				model.CapabilityCatalog,
				model.CapabilitySearch,
				model.CapabilityDownload,
			},
			Description: "First-generation IASI products from Metop-A, Metop-B and Metop-C; catalog/search are anonymous and product download requires EUMETSAT API credentials.",
		}
	}
	if p.sourceID == "iasi-ng" {
		return model.Source{
			ID:     "iasi-ng",
			Name:   "IASI-NG / Metop-SG",
			Tier:   model.TierToken,
			Method: "EUMETSAT Data Store Browse / OpenSearch + OAuth2 download",
			Capabilities: []model.Capability{
				model.CapabilityCatalog,
				model.CapabilitySearch,
				model.CapabilityDownload,
			},
			Description: "Next-generation IASI products from Metop-SG; the currently published global Level 1C collection is pre-operational. Catalog/search are anonymous and product download requires EUMETSAT API credentials.",
		}
	}
	return model.Source{
		ID:     "mtg",
		Name:   "Meteosat Third Generation",
		Tier:   model.TierToken,
		Method: "EUMETSAT Data Store OpenSearch + OAuth2 download; MQTT publication feed / EUMETCast Terrestrial for low-latency operation",
		Capabilities: []model.Capability{
			model.CapabilityCatalog,
			model.CapabilitySearch,
			model.CapabilityDownload,
		},
		Description: "Catalog/search are anonymously accessible; product download requires an EUMETSAT API token.",
	}
}

type browseCollectionList struct {
	Links []struct {
		Href             string  `json:"href"`
		Title            string  `json:"title"`
		DatasetTitle     string  `json:"datasetTitle"`
		Rel              string  `json:"rel"`
		NumberOfProducts float64 `json:"numberOfProducts"`
	} `json:"links"`
}

func (p *Provider) Collections(ctx context.Context) ([]model.Collection, error) {
	p.catalogMu.Lock()
	defer p.catalogMu.Unlock()
	if len(p.catalog) > 0 && time.Since(p.catalogTime) < 6*time.Hour {
		return append([]model.Collection(nil), p.catalog...), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, browseCatalog, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("EUMETSAT Browse API returned HTTP %d", resp.StatusCode)
	}
	var payload browseCollectionList
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode EUMETSAT collection catalogue: %w", err)
	}
	out := make([]model.Collection, 0, 32)
	for _, item := range payload.Links {
		title := strings.TrimSpace(item.DatasetTitle)
		officialID := strings.TrimSpace(item.Title)
		if item.Rel != "collection/details" || officialID == "" {
			continue
		}
		if p.sourceID == "mtg" && !isMTGCollection(title) {
			continue
		}
		// The Browse API collection index is authoritative for searchable and downloadable products.
		// Portal-only IASI records are not synthesized when OpenSearch cannot resolve their collection.
		if p.sourceID == "iasi" && !isIASICollection(title) {
			continue
		}
		if p.sourceID == "iasi-ng" && !isIASINGCollection(title) {
			continue
		}
		if p.sourceID != "mtg" && p.sourceID != "iasi" && p.sourceID != "iasi-ng" {
			continue
		}
		if p.sourceID == "iasi" {
			out = append(out, iasiCollection(item.Title, title, item.Href, item.NumberOfProducts))
			continue
		}
		if p.sourceID == "iasi-ng" {
			out = append(out, iasiNGCollection(item.Title, title, item.Href, item.NumberOfProducts))
			continue
		}
		out = append(out, model.Collection{
			ID:         p.sourceID + ":" + officialID,
			SourceID:   p.sourceID,
			Satellite:  "MTG",
			Instrument: mtgInstrument(title),
			Level:      mtgLevel(title),
			Product:    officialID,
			Cadence:    mtgCadence(title),
			Metadata: map[string]string{
				"official_id":    officialID,
				"collection_ref": officialID,
				"title":          title,
				"product_count":  strconv.FormatInt(int64(item.NumberOfProducts), 10),
				"catalog_href":   item.Href,
				"area_type":      mtgArea(title),
				"product_type":   mtgProductType(title),
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Product < out[j].Product })
	p.catalog = out
	p.catalogTime = time.Now()
	return append([]model.Collection(nil), out...), nil
}

type searchPage struct {
	TotalResults int `json:"totalResults"`
	Features     []struct {
		ID         string         `json:"id"`
		Properties map[string]any `json:"properties"`
	} `json:"features"`
}

func (p *Provider) Search(ctx context.Context, q model.Query) ([]model.Granule, error) {
	if err := provider.ValidateQuery(q); err != nil {
		return nil, err
	}
	collectionID, ok := strings.CutPrefix(q.CollectionID, p.sourceID+":")
	if !ok || !p.knownCollection(ctx, collectionID) {
		return nil, provider.ErrUnsupportedCollection
	}

	const pageSize = 500
	out := make([]model.Granule, 0, 256)
	for startIndex := 0; ; startIndex += pageSize {
		values := url.Values{
			"format":  {"json"},
			"pi":      {collectionID},
			"si":      {strconv.Itoa(startIndex)},
			"c":       {strconv.Itoa(pageSize)},
			"set":     {"full"},
			"dtstart": {q.Start.UTC().Format(time.RFC3339)},
			"dtend":   {q.End.UTC().Format(time.RFC3339)},
			"sort":    {"start,time,1"},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchEndpoint+"?"+values.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}
		var page searchPage
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("EUMETSAT OpenSearch returned HTTP %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode EUMETSAT OpenSearch: %w", decodeErr)
		}

		for _, feature := range page.Features {
			start, end, ok := parseDateProperty(feature.Properties["date"])
			if !ok {
				continue
			}
			productID := feature.ID
			if p.sourceID == "iasi" && collectionID == "EO:EUM:DAT:1099" {
				start, end, ok = parseIASINoiseCovarianceTime(productID)
				if !ok {
					continue
				}
				if start.Before(q.Start) || !start.Before(q.End) {
					continue
				}
			}
			downloadURL, mediaType, ok := productDownloadLink(feature.Properties)
			if !ok {
				continue
			}
			if mediaType == "" {
				mediaType = "application/zip"
			}
			granuleMetadata := map[string]string{
				"collection_ref": collectionID,
				"publication":    stringProperty(feature.Properties["updated"]),
			}
			if p.sourceID == "iasi" {
				granuleMetadata = iasiGranuleMetadata(productID, feature.Properties, collectionID)
			} else if p.sourceID == "iasi-ng" {
				granuleMetadata = iasiNGGranuleMetadata(productID, feature.Properties, collectionID)
			}
			out = append(out, model.Granule{
				ID:           p.sourceID + ":" + productID,
				SourceID:     p.sourceID,
				CollectionID: q.CollectionID,
				Start:        start,
				End:          end,
				Assets: []model.Asset{
					{
						ID:        productID,
						Name:      productAssetName(productID, mediaType),
						URL:       downloadURL,
						AuthRef:   "eumetsat",
						MediaType: mediaType,
						Metadata: map[string]string{
							"container": "EUMETSAT Data Store SIP",
						},
					},
				},
				Metadata: granuleMetadata,
			})
		}

		if startIndex+len(page.Features) >= page.TotalResults || len(page.Features) == 0 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

func productDownloadLink(properties map[string]any) (string, string, bool) {
	links, ok := properties["links"].(map[string]any)
	if !ok {
		return "", "", false
	}
	data, ok := links["data"].([]any)
	if !ok {
		return "", "", false
	}
	for _, raw := range data {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		href, _ := item["href"].(string)
		if !strings.HasPrefix(href, apiBase+"/data/download/") {
			continue
		}
		mediaType, _ := item["mediaType"].(string)
		return href, mediaType, true
	}
	return "", "", false
}

func productAssetName(productID, mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "application/zip", "application/x-zip-compressed":
		return productID + ".zip"
	default:
		return productID
	}
}

func (p *Provider) knownCollection(ctx context.Context, id string) bool {
	collections, err := p.Collections(ctx)
	if err != nil {
		return false
	}
	for _, collection := range collections {
		if collection.Metadata["collection_ref"] == id {
			return true
		}
	}
	return false
}

func isMTGCollection(title string) bool {
	return strings.Contains(strings.ToUpper(title), "MTG")
}

func isIASICollection(title string) bool {
	upper := strings.ToUpper(title)
	return strings.Contains(upper, "IASI") && !strings.Contains(upper, "IASI-NG") && !strings.Contains(upper, "IASI NG")
}

func isIASINGCollection(title string) bool {
	upper := strings.ToUpper(title)
	return strings.Contains(upper, "IASI-NG") || strings.Contains(upper, "IASI NG")
}

func iasiCollection(officialID, title, href string, productCount float64) model.Collection {
	level := iasiLevel(officialID, title)
	area := iasiArea(officialID)
	satellite := iasiSatelliteGroup(title)
	platforms := iasiPlatforms(title)
	if officialID == "EO:EUM:DAT:METOP:IASIL2COX" {
		satellite = "Metop-B / Metop-C"
		platforms = "Metop-B, Metop-C"
	}
	return model.Collection{
		ID:         "iasi:" + officialID,
		SourceID:   "iasi",
		Satellite:  satellite,
		Instrument: iasiInstrument(officialID),
		Level:      level,
		Product:    officialID,
		Cadence:    "event",
		Metadata: map[string]string{
			"official_id":    officialID,
			"collection_ref": officialID,
			"title":          title,
			"product_count":  strconv.FormatInt(int64(productCount), 10),
			"catalog_href":   href,
			"area_type":      area,
			"product_type":   iasiProductType(officialID),
			"platforms":      platforms,
			"version":        iasiVersion(title),
		},
	}
}

func iasiNGCollection(officialID, title, href string, productCount float64) model.Collection {
	level := iasiNGLevel(title)
	area := "GLOBAL"
	if strings.Contains(strings.ToUpper(title), "REGIONAL") {
		area = "SWATH"
	}
	satellite := "Metop-SG"
	platforms := "Metop-SG-A1"
	metadata := map[string]string{
		"official_id":                officialID,
		"collection_ref":             officialID,
		"title":                      title,
		"product_count":              strconv.FormatInt(int64(productCount), 10),
		"catalog_href":               href,
		"area_type":                  area,
		"platforms":                  platforms,
		"official_documentation_url": "https://user.eumetsat.int/resources/user-guides/metop-sg-iasi-ng-l1c-and-l1d-data-guide",
		"official_product_url":       "https://user.eumetsat.int/catalogue/" + url.PathEscape(officialID),
		"level_evidence":             apiBase + "/data/browse/1.0.0/collections/" + url.PathEscape(officialID) + "?format=json",
	}
	if level == "L1c" || level == "L1d" {
		metadata["spatial_resolution"] = "12 km"
		metadata["resolution_evidence"] = "https://user.eumetsat.int/catalogue/" + url.PathEscape(officialID)
	}
	if officialID == "EO:EUM:DAT:0442" && level == "L1c" {
		metadata["product_type"] = "IASRAD1C"
		metadata["spectral_samples"] = "16921"
		metadata["spectral_range"] = "645–2760 cm⁻¹"
		metadata["processing_status"] = "Pre-operational"
		metadata["processing_status_evidence"] = "https://user.eumetsat.int/catalogue/" + url.PathEscape(officialID)
		metadata["time_start"] = "2026-10-07"
		metadata["rights"] = "NoConditions"
		metadata["description"] = "IASI-NG Level 1C calibrated and geolocated infrared spectral radiances. Each scan line contains 14 fields of regard with 4×4 fields of view; the product contains 16,921 spectral samples from 645 to 2,760 cm⁻¹."
	}
	return model.Collection{
		ID:         "iasi-ng:" + officialID,
		SourceID:   "iasi-ng",
		Satellite:  satellite,
		Instrument: "IASI-NG",
		Level:      level,
		Product:    officialID,
		Cadence:    "event",
		Metadata:   metadata,
	}
}

func iasiNGLevel(title string) string {
	upper := strings.ToUpper(title)
	switch {
	case strings.Contains(upper, "LEVEL 1C"):
		return "L1c"
	case strings.Contains(upper, "LEVEL 1D"):
		return "L1d"
	case strings.Contains(upper, "LEVEL 2"):
		return "L2"
	default:
		return ""
	}
}

func iasiInstrument(officialID string) string {
	if officialID == "EO:EUM:DAT:1099" {
		return "Auxiliary"
	}
	return "IASI"
}

func iasiLevel(officialID, title string) string {
	upper := strings.ToUpper(title)
	switch {
	case officialID == "EO:EUM:DAT:1099":
		return "AUX"
	case officialID == "EO:EUM:DAT:METOP:IASPCS01":
		return "L1"
	case officialID == "EO:EUM:DAT:METOP:IASIL1C-ALL", officialID == "EO:EUM:DAT:0590", strings.Contains(upper, "LEVEL 1C"):
		return "L1c"
	case officialID == "EO:EUM:DAT:0758", strings.Contains(upper, "LEVEL 1"):
		return "L1"
	case strings.Contains(upper, "LEVEL 2"), strings.Contains(upper, "PROFILE"), strings.Contains(upper, "SOUNDING"), strings.Contains(upper, "CLIMATE DATA RECORD"):
		return "L2"
	default:
		return ""
	}
}

func iasiArea(officialID string) string {
	switch officialID {
	case "EO:EUM:DAT:1099":
		return "AUX"
	case "EO:EUM:DAT:METOP:IASIL2COX":
		return "SWATH"
	default:
		return "ORBIT"
	}
}

func iasiSatelliteGroup(title string) string {
	upper := strings.ToUpper(title)
	if strings.Contains(upper, "METOP-A AND -B") || strings.Contains(upper, "METOP-A AND METOP-B") {
		return "Metop-A / Metop-B"
	}
	return "Metop"
}

func iasiPlatforms(title string) string {
	if iasiSatelliteGroup(title) == "Metop-A / Metop-B" {
		return "Metop-A, Metop-B"
	}
	return "Metop-A, Metop-B, Metop-C"
}

func iasiProductType(officialID string) string {
	switch officialID {
	case "EO:EUM:DAT:METOP:IASIL1C-ALL":
		return "All spectral samples"
	case "EO:EUM:DAT:METOP:IASPCS01", "EO:EUM:DAT:0758":
		return "Principal component scores"
	case "EO:EUM:DAT:METOP:IASIL2COX", "EO:EUM:DAT:0959":
		return "Carbon monoxide profiles"
	case "EO:EUM:DAT:0960":
		return "Sulphur dioxide profiles"
	case "EO:EUM:DAT:0576":
		return "All-sky temperature and humidity profiles"
	case "EO:EUM:DAT:METOP:IASSND02":
		return "Combined sounding products"
	case "EO:EUM:DAT:1027":
		return "Ozone profiles"
	case "EO:EUM:DAT:0590":
		return "Level 1C climate data record"
	case "EO:EUM:DAT:1099":
		return "Noise covariance matrix"
	default:
		return "IASI product"
	}
}

func iasiVersion(title string) string {
	fields := strings.Fields(title)
	for i, field := range fields {
		if strings.EqualFold(field, "release") && i+1 < len(fields) {
			return "R" + strings.TrimRight(fields[i+1], ".,;:")
		}
	}
	return ""
}

func parseIASINoiseCovarianceTime(productID string) (time.Time, time.Time, bool) {
	for i := 0; i+15 <= len(productID); i++ {
		candidate := productID[i : i+14]
		if productID[i+14] != 'Z' {
			continue
		}
		if _, err := strconv.ParseInt(candidate, 10, 64); err != nil {
			continue
		}
		at, err := time.Parse("20060102150405Z", candidate+"Z")
		if err == nil {
			return at.UTC(), at.UTC(), true
		}
	}
	return time.Time{}, time.Time{}, false
}

func iasiGranuleMetadata(productID string, properties map[string]any, collectionID string) map[string]string {
	metadata := map[string]string{
		"collection_ref": collectionID,
		"publication":    stringProperty(properties["updated"]),
	}
	if acquisitions, ok := properties["acquisitionInformation"].([]any); ok {
		for _, raw := range acquisitions {
			acquisition, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if platform, ok := acquisition["platform"].(map[string]any); ok {
				if name := stringProperty(platform["platformShortName"]); name != "" {
					metadata["satellite"] = name
				}
			}
			if instrument, ok := acquisition["instrument"].(map[string]any); ok {
				if name := stringProperty(instrument["instrumentShortName"]); name != "" {
					metadata["instrument"] = name
				}
			}
			if parameters, ok := acquisition["acquisitionParameters"].(map[string]any); ok {
				if orbit, ok := parameters["orbitNumber"].(float64); ok && orbit > 0 {
					metadata["orbit_number"] = strconv.FormatInt(int64(orbit), 10)
				}
			}
		}
	}
	if metadata["satellite"] == "" {
		metadata["satellite"] = iasiSatelliteFromProductID(productID)
	}
	if product, ok := properties["productInformation"].(map[string]any); ok {
		if productType := stringProperty(product["productType"]); productType != "" {
			metadata["product_type"] = productType
		}
	}
	return metadata
}

func iasiNGGranuleMetadata(productID string, properties map[string]any, collectionID string) map[string]string {
	metadata := iasiGranuleMetadata(productID, properties, collectionID)
	metadata["instrument"] = "IASI-NG"
	switch strings.ToUpper(metadata["satellite"]) {
	case "SGA1", "METOP-SGA1", "METOP-SG-A1":
		metadata["satellite"] = "Metop-SG-A1"
	case "SGB1", "METOP-SGB1", "METOP-SG-B1":
		metadata["satellite"] = "Metop-SG-B1"
	case "SGA2", "METOP-SGA2", "METOP-SG-A2":
		metadata["satellite"] = "Metop-SG-A2"
	case "SGB2", "METOP-SGB2", "METOP-SG-B2":
		metadata["satellite"] = "Metop-SG-B2"
	case "SGA3", "METOP-SGA3", "METOP-SG-A3":
		metadata["satellite"] = "Metop-SG-A3"
	case "SGB3", "METOP-SGB3", "METOP-SG-B3":
		metadata["satellite"] = "Metop-SG-B3"
	}
	return metadata
}

func iasiSatelliteFromProductID(productID string) string {
	upper := strings.ToUpper(productID)
	switch {
	case strings.Contains(upper, "_M01_") || strings.Contains(upper, "METOPB+"):
		return "Metop-B"
	case strings.Contains(upper, "_M02_") || strings.Contains(upper, "METOPA+"):
		return "Metop-A"
	case strings.Contains(upper, "_M03_") || strings.Contains(upper, "METOPC+"):
		return "Metop-C"
	default:
		return "Metop"
	}
}

func mtgInstrument(title string) string {
	upper := strings.ToUpper(title)
	switch {
	case strings.Contains(upper, "FCI"):
		return "FCI"
	case strings.Contains(upper, "IRS"):
		return "IRS"
	case strings.Contains(upper, "LIGHTNING"), strings.Contains(upper, "LI "):
		return "LI"
	case mtgFCILevelTwo(title):
		return "FCI"
	default:
		return "MTG"
	}
}

func mtgLevel(title string) string {
	upper := strings.ToUpper(title)
	switch {
	case strings.Contains(upper, "LEVEL 1C"):
		return "L1c"
	case strings.Contains(upper, "LEVEL 1B"):
		return "L1b"
	case strings.Contains(upper, "LEVEL 1"):
		return "L1"
	case strings.Contains(upper, "LEVEL 2"):
		return "L2"
	case strings.Contains(upper, "LEVEL 3"):
		return "L3"
	case strings.HasPrefix(upper, "LI "), mtgFCILevelTwo(title):
		return "L2"
	default:
		return ""
	}
}

func mtgFCILevelTwo(title string) bool {
	upper := strings.ToUpper(title)
	for _, name := range []string{
		"ATMOSPHERIC MOTION VECTORS", "ALL SKY RADIANCE", "CLOUD MASK",
		"CLEAR SKY REFLECTANCE MAP", "CLOUD TYPE", "CLOUD TOP TEMPERATURE AND HEIGHT",
		"ACTIVE FIRE MONITORING", "GLOBAL INSTABILITY INDICES", "OPTIMAL CLOUD ANALYSIS",
		"OUTGOING LW RADIATION",
	} {
		if strings.HasPrefix(upper, name) && isMTGCollection(title) {
			return true
		}
	}
	return false
}

func mtgCadence(title string) string {
	upper := strings.ToUpper(title)
	if strings.Contains(upper, "FCI LEVEL 1C") {
		return "10m"
	}
	return "event"
}

func mtgProductType(title string) string {
	upper := strings.ToUpper(title)
	switch {
	case strings.Contains(upper, "FCI LEVEL 1C") && strings.Contains(upper, "NORMAL RESOLUTION"):
		return "FCI-1C-RRAD-FDHSI-FD (normal resolution)"
	case strings.Contains(upper, "FCI LEVEL 1C") && strings.Contains(upper, "HIGH RESOLUTION"):
		return "FCI-1C-RRAD-HRFI-FD (high resolution)"
	case strings.Contains(upper, "IRS LEVEL 1") && strings.Contains(upper, "PRINCIPAL COMPONENTS"):
		return "IRS L1 - 300 principal components"
	case strings.Contains(upper, "IRS LEVEL 1") && strings.Contains(upper, "SPECTRAL SOUNDINGS"):
		return "IRS L1 - spectral soundings"
	default:
		return ""
	}
}

func mtgArea(title string) string {
	upper := strings.ToUpper(title)
	if strings.Contains(upper, "0 DEGREE") {
		return "DISK"
	}
	return ""
}

func parseDateProperty(value any) (time.Time, time.Time, bool) {
	raw, ok := value.(string)
	if !ok || raw == "" {
		return time.Time{}, time.Time{}, false
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	parts := strings.Split(raw, "/")
	if len(parts) < 2 {
		return time.Time{}, time.Time{}, false
	}
	start, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	end, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return start.UTC(), end.UTC(), true
}

func stringProperty(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
