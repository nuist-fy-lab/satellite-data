package provider

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

var processingLevel = regexp.MustCompile(`(?i)^(?:L|LEVEL\s*)?([0-4](?:\.[0-9]+)?[A-Z]*)$`)
var spatialResolution = regexp.MustCompile(`(?i)\b[0-9]+(?:\.[0-9]+)?(?:\s*[x\x{00d7}]\s*[0-9]+(?:\.[0-9]+)?)?\s*(?:km|m|degrees?|deg\.?|arc[- ]seconds?)\b`)

// NormalizeLevel leaves non-processing classifications and unknown values intact.
func NormalizeLevel(value string) string {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "/")
	for i, part := range parts {
		match := processingLevel.FindStringSubmatch(strings.TrimSpace(part))
		if match == nil {
			if strings.EqualFold(value, "aux") || strings.EqualFold(value, "orbit") {
				return strings.ToUpper(value)
			}
			return value
		}
		parts[i] = "L" + strings.ToUpper(match[1])
	}
	return strings.Join(parts, "/")
}

func NormalizeCollection(item model.Collection) model.Collection {
	item.Metadata = copyMetadata(item.Metadata)
	if item.Metadata == nil {
		item.Metadata = make(map[string]string)
	}
	item.Satellite = strings.TrimSpace(item.Satellite)
	item.Instrument = strings.TrimSpace(item.Instrument)
	item.Level = NormalizeLevel(item.Level)
	if strings.HasPrefix(item.SourceID, "goes") && item.Instrument == "SEIS" {
		item.Instrument = "SEISS"
	}
	switch strings.ToLower(item.Instrument) {
	case "auxiliary":
		item.Instrument = "Auxiliary"
	case "derived":
		item.Instrument = "Derived"
	}
	return item
}

// ResolutionFromTitle recognizes only explicit spatial units, not times or versions.
func ResolutionFromTitle(title string) string {
	return spatialResolution.FindString(title)
}

// NormalizeProduct also covers persisted catalogs without rewriting their keys.
func NormalizeProduct(item model.Product) model.Product {
	item.Metadata = copyMetadata(item.Metadata)
	if item.Metadata == nil {
		item.Metadata = make(map[string]string)
	}
	originalLevel := item.Level
	item.Level = NormalizeLevel(item.Level)
	item.Instrument = strings.TrimSpace(item.Instrument)
	switch strings.ToLower(item.Instrument) {
	case "auxiliary":
		item.Instrument = "Auxiliary"
	case "derived":
		item.Instrument = "Derived"
	}
	item.Satellite = strings.TrimSpace(item.Satellite)
	title := item.DisplayName
	if title == "" {
		title = firstMetadata(item.Metadata, "title")
		item.DisplayName = title
	}
	if item.DisplayName == "" {
		item.DisplayName = item.DataType
	}
	evidence := catalogEvidence(item)
	switch {
	case item.SourceID == "calipso":
		if strings.HasPrefix(item.DataType, "CAL_WFC_") {
			item.Instrument = "WFC"
		}
		if strings.HasPrefix(item.DataType, "CAL_LID_L15-") || strings.HasPrefix(item.DataType, "CAL_LID_L15_") {
			item.Level = "L1.5"
			item.Metadata["level_evidence"] = "https://asdc.larc.nasa.gov/documents/calipso/CALIPSO_DPC_Rev5x00.pdf"
		}
		if item.AreaType == "" && !strings.HasPrefix(item.Level, "L0") {
			item.AreaType = "SWATH"
		}
		if item.Level == "L0" {
			item.Metadata["resolution_status"] = "not_applicable"
			item.Metadata["area_type_status"] = "not_applicable"
		}
	case item.SourceID == "cloudsat":
		if item.Version == "" {
			_, item.Version, _ = strings.Cut(item.DataType, ".")
		}
		if item.AreaType == "" && !strings.HasPrefix(item.Level, "L3") {
			item.AreaType = "SWATH"
		}
	case item.SourceID == "earthcare":
		if strings.HasPrefix(item.DataType, "AUX_") {
			item.Instrument = "Auxiliary"
			if item.Level == "" {
				item.Level = "AUX"
			}
		}
		if item.DataType == "MPL_ORBSCT" {
			item.Instrument = "Orbit"
			item.Level = "ORBIT"
		}
		if item.Level == "AUX" || item.Level == "ORBIT" {
			item.Metadata["processing_level_status"] = "not_applicable"
			item.Metadata["resolution_status"] = "not_applicable"
			item.Metadata["area_type_status"] = "not_applicable"
		}
	case item.SourceID == "mtg":
		instrument, level, kind, resolution := MTGMetadata(title)
		if instrument != "" {
			item.Instrument = instrument
		}
		if item.Level == "" {
			item.Level = level
		}
		if item.Metadata["product_type"] == "" {
			item.Metadata["product_type"] = kind
		}
		if item.Resolution == "" {
			item.Resolution = resolution
		}
		if item.Level != "" {
			item.Metadata["level_evidence"] = "https://user.eumetsat.int/s3/eup-strapi-media/MTG_Generic_Format_Specification_GFS_V4_A_0_01dcde093e.pdf"
		}
	case strings.HasPrefix(item.SourceID, "goes"):
		if item.Instrument == "SEIS" {
			item.Instrument = "SEISS"
		}
		if strings.HasPrefix(item.DataType, "ABI-L1b-Rad") && item.Resolution == "" {
			item.Resolution = "0.5 / 1 / 2 km"
		}
		if item.Instrument == "EXIS" || item.Instrument == "SEISS" {
			item.Metadata["resolution_status"] = "not_applicable"
			item.Metadata["area_type_status"] = "not_applicable"
		}
		if item.Instrument == "SUVI" {
			if item.Resolution == "" {
				item.Resolution = "2.5 arcsec/pixel"
			}
			if item.AreaType == "" {
				item.AreaType = "SOLAR_DISK"
			}
			item.Metadata["resolution_evidence"] = "https://www.noaasis.noaa.gov/pdf/ps-pvr/goes17/SUVI/Full/GOES-17_SUVI_Full_Validation_README.pdf"
		}
		if item.Version == "" {
			item.Metadata["version_status"] = "per_file"
		}
		if strings.HasPrefix(item.DataType, "ABI-Flood-") {
			item.Metadata["level_evidence"] = "https://www.ospo.noaa.gov/products/land/efm/index.html"
		}
	case strings.HasPrefix(item.SourceID, "himawari"):
		if strings.HasPrefix(item.DataType, "AHI-L1b-") && item.Resolution == "" {
			item.Resolution = "0.5 / 1 / 2 km"
		}
		if item.AreaType == "" && strings.Contains(item.DataType, "FLDK") {
			item.AreaType = "DISK"
		}
		if item.AreaType == "" && strings.Contains(item.DataType, "Japan") {
			item.AreaType = "JAPAN"
		}
		if item.AreaType == "" && strings.Contains(item.DataType, "Target") {
			item.AreaType = "TARGET"
		}
		if item.Version == "" {
			item.Metadata["version_status"] = "per_file"
		}
	}
	if item.Resolution == "" {
		item.Resolution = ResolutionFromTitle(title)
	}
	if item.SourceID == "modis" || item.SourceID == "viirs" {
		if item.AreaType == "" {
			upper := strings.ToUpper(title)
			switch {
			case strings.Contains(upper, "SUBSETTING"):
				item.AreaType = "SUBSET"
			case strings.Contains(upper, "GRID"), strings.Contains(upper, "GLOBAL"):
				item.AreaType = "GLOBAL"
			case strings.Contains(upper, "SWATH"):
				item.AreaType = "SWATH"
			}
		}
		if strings.Contains(strings.ToUpper(title), "SUBSETTING") && item.Resolution == "" {
			item.Metadata["resolution_status"] = "subsetting"
		}
	}
	if originalLevel != item.Level && originalLevel != "" {
		item.Metadata["original_level"] = originalLevel
	}
	item.Metadata["metadata_scope"] = "collection"
	item.Metadata["resolution_scope"] = "nominal"
	item.Metadata["version_scope"] = "collection"
	if item.Metadata["version_status"] == "per_file" {
		item.Metadata["version_scope"] = "file"
	}
	item.Metadata["cadence_scope"] = "nominal"
	item.Metadata["official_product_url"] = ProductCatalogURL(item)
	for field, value := range map[string]string{"level": item.Level, "resolution": item.Resolution, "version": item.Version, "area_type": item.AreaType, "instrument": item.Instrument, "satellite": item.Satellite} {
		if value != "" {
			item.Metadata[field+"_status"] = "provided"
		} else if item.Metadata[field+"_status"] == "" {
			item.Metadata[field+"_status"] = "catalog_not_provided"
		}
		if item.Metadata[field+"_evidence"] == "" {
			item.Metadata[field+"_evidence"] = evidence
		}
	}
	return item
}

// ProductCatalogURL points to the specific official collection, not a mission homepage.
func ProductCatalogURL(item model.Product) string {
	switch {
	case item.SourceID == "modis" || item.SourceID == "viirs" || item.SourceID == "calipso":
		return "https://cmr.earthdata.nasa.gov/search/concepts/" + url.PathEscape(item.OfficialID) + ".html"
	case item.SourceID == "cloudsat":
		return "https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/" + url.PathEscape(item.DataType) + "/catalog.html"
	case item.SourceID == "earthcare":
		collection := firstMetadata(item.Metadata, "stac_collection")
		if collection == "" {
			collection, _, _ = strings.Cut(strings.TrimPrefix(item.ProductKey, "earthcare:"), ":")
		}
		if collection == "" {
			collection = item.OfficialID
		}
		return "https://catalog.maap.eo.esa.int/catalogue/collections/" + url.PathEscape(collection) + "?httpAccept=text%2Fhtml"
	case item.SourceID == "mtg":
		return "https://data.eumetsat.int/product/" + url.PathEscape(item.OfficialID)
	case strings.HasPrefix(item.SourceID, "goes"):
		bucket := "gcp-public-data-goes-" + strings.TrimPrefix(item.SourceID, "goes")
		return "https://storage.googleapis.com/storage/v1/b/" + bucket + "/o?" + url.Values{"prefix": {item.DataType + "/"}, "delimiter": {"/"}}.Encode()
	case strings.HasPrefix(item.SourceID, "himawari"):
		return "https://noaa-" + item.SourceID + ".s3.amazonaws.com/?" + url.Values{"list-type": {"2"}, "prefix": {item.DataType + "/"}, "delimiter": {"/"}}.Encode()
	case item.SourceID == "era5":
		return "https://cds.climate.copernicus.eu/datasets/" + url.PathEscape(item.DataType)
	default:
		return ""
	}
}

// MTGMetadata uses the mission's GFS product table, not arbitrary title tokens.
func MTGMetadata(title string) (instrument, level, kind, resolution string) {
	upper := strings.ToUpper(title)
	switch {
	case strings.Contains(upper, "IRS"):
		instrument = "IRS"
	case strings.HasPrefix(upper, "LI ") || strings.Contains(upper, "LIGHTNING"):
		instrument = "LI"
		level = "L2"
	case strings.Contains(upper, "BLENDED") && strings.Contains(upper, "PRECIPITATION"):
		instrument = "FCI + LEO MW"
		level = "L2"
	case strings.Contains(upper, "FCI"):
		instrument = "FCI"
	}
	for _, token := range []string{"1C", "1B", "1", "2", "3"} {
		if strings.Contains(upper, "LEVEL "+token) {
			level = "L" + token
			break
		}
	}
	for _, name := range []string{"ATMOSPHERIC MOTION VECTORS", "ALL SKY RADIANCE", "CLOUD MASK", "CLEAR SKY REFLECTANCE MAP", "CLOUD TYPE", "CLOUD TOP TEMPERATURE AND HEIGHT", "ACTIVE FIRE MONITORING", "GLOBAL INSTABILITY INDICES", "OPTIMAL CLOUD ANALYSIS", "OUTGOING LW RADIATION", "LAND SURFACE TEMPERATURE", "FCI SNOW DETECTION"} {
		if strings.HasPrefix(upper, name) && strings.Contains(upper, "MTG") {
			instrument = "FCI"
			level = "L2"
			break
		}
	}
	switch {
	case strings.Contains(upper, "FCI LEVEL 1C") && strings.Contains(upper, "NORMAL RESOLUTION"):
		kind = "FCI-1C-RRAD-FDHSI-FD"
		resolution = "1 / 2 km"
	case strings.Contains(upper, "FCI LEVEL 1C") && strings.Contains(upper, "HIGH RESOLUTION"):
		kind = "FCI-1C-RRAD-HRFI-FD"
		resolution = "0.5 / 1 km"
	case strings.Contains(upper, "IRS LEVEL 1") && strings.Contains(upper, "PRINCIPAL COMPONENTS"):
		kind = "IRS L1 - 300 principal components"
	case strings.Contains(upper, "IRS LEVEL 1") && strings.Contains(upper, "SPECTRAL SOUNDINGS"):
		kind = "IRS L1 - spectral soundings"
	}
	return
}

func catalogEvidence(item model.Product) string {
	switch {
	case item.SourceID == "modis" || item.SourceID == "viirs" || item.SourceID == "calipso":
		return "https://cmr.earthdata.nasa.gov/search/collections.json?concept_id=" + item.OfficialID
	case item.SourceID == "cloudsat":
		return "https://www.cloudsat.cira.colostate.edu/data-products"
	case item.SourceID == "earthcare":
		return "https://portal.maap.eo.esa.int/earthcare/earthcare-data-offer/"
	case item.SourceID == "mtg":
		return "https://api.eumetsat.int/data/browse/1.0.0/collections/" + item.OfficialID
	case strings.HasPrefix(item.SourceID, "goes"):
		return "https://www.ncei.noaa.gov/products/goes-terrestrial-weather-abi-glm"
	case strings.HasPrefix(item.SourceID, "himawari"):
		return "https://www.data.jma.go.jp/mscweb/en/himawari89/space_segment/spsg_ahi.html"
	case item.SourceID == "era5":
		return "https://cds.climate.copernicus.eu/datasets/" + item.DataType
	default:
		return catalogSource(item.SourceID)
	}
}
