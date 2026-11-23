// Package sourceaccess describes download routes and their credential contracts.
package sourceaccess

import "slices"

const (
	LAADSArchiveHost = "ladsweb.modaps.eosdis.nasa.gov"
	LAADSCloudHost   = "data.laadsdaac.earthdatacloud.nasa.gov"
)

type Endpoint struct {
	Name           string `json:"name"`
	Host           string `json:"host"`
	CredentialKind string `json:"credential_kind,omitempty"`
}

type Route struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Catalog         string     `json:"catalog"`
	CredentialKinds []string   `json:"credential_kinds,omitempty"`
	Downloads       []Endpoint `json:"downloads"`
}

var laadsDownloads = []Endpoint{
	{Name: "LAADS 原生归档", Host: LAADSArchiveHost, CredentialKind: "laads"},
	{Name: "LAADS Earthdata Cloud", Host: LAADSCloudHost, CredentialKind: "earthdata"},
}

var routes = []Route{
	singleRoute("goes", "GOES", "NOAA NODD · GCS", "NOAA NODD · GCS", "storage.googleapis.com", ""),
	singleRoute("himawari8", "Himawari-8", "NOAA NODD · S3", "NOAA NODD · S3", "noaa-himawari8.s3.amazonaws.com", ""),
	singleRoute("himawari9", "Himawari-9", "NOAA NODD · S3", "NOAA NODD · S3", "noaa-himawari9.s3.amazonaws.com", ""),
	{ID: "modis", Name: "MODIS", Catalog: "NASA CMR · LAADS", CredentialKinds: []string{"laads", "earthdata"}, Downloads: laadsDownloads},
	{ID: "viirs", Name: "VIIRS", Catalog: "NASA CMR · LAADS", CredentialKinds: []string{"laads", "earthdata"}, Downloads: laadsDownloads},
	singleRoute("mtg", "MTG", "EUMETSAT Data Store", "EUMETSAT Data Store", "api.eumetsat.int", "eumetsat"),
	singleRoute("iasi", "IASI / Metop", "EUMETSAT Data Store", "EUMETSAT Data Store", "api.eumetsat.int", "eumetsat"),
	singleRoute("iasi-ng", "IASI-NG / Metop-SG", "EUMETSAT Data Store", "EUMETSAT Data Store", "api.eumetsat.int", "eumetsat"),
	singleRoute("earthcare", "EarthCARE", "ESA MAAP · STAC", "ESA MAAP", "catalog.maap.eo.esa.int", "earthcare"),
	singleRoute("calipso", "CALIPSO", "NASA CMR · ASDC", "NASA ASDC", "data.asdc.earthdata.nasa.gov", "earthdata"),
	singleRoute("cloudsat", "CloudSat", "CloudSat DPC · THREDDS", "CloudSat DPC · HTTPServer", "dpc-thredds.cira.colostate.edu", "cloudsat"),
	singleRoute("era5", "ERA5 / ERA5-Land", "Copernicus CDS · STAC", "Copernicus CDS · 按需生成", "cds.climate.copernicus.eu", "cds"),
}

func singleRoute(id, name, catalog, download, host, kind string) Route {
	var kinds []string
	if kind != "" {
		kinds = []string{kind}
	}
	return Route{ID: id, Name: name, Catalog: catalog, CredentialKinds: kinds,
		Downloads: []Endpoint{{Name: download, Host: host, CredentialKind: kind}}}
}

func IDs() []string {
	ids := make([]string, 0, len(routes))
	for _, route := range routes {
		ids = append(ids, route.ID)
	}
	return ids
}

func ForSource(id string) (Route, bool) {
	if slices.Contains([]string{"goes16", "goes17", "goes18", "goes19"}, id) {
		id = "goes"
	}
	return ForRoute(id)
}

func ForRoute(id string) (Route, bool) {
	for _, route := range routes {
		if route.ID == id {
			route.CredentialKinds = slices.Clone(route.CredentialKinds)
			route.Downloads = slices.Clone(route.Downloads)
			return route, true
		}
	}
	return Route{}, false
}

func (r Route) DefaultCredentialKind() string {
	if len(r.CredentialKinds) > 0 {
		return r.CredentialKinds[0]
	}
	return ""
}

func (r Route) AcceptsCredential(kind string) bool {
	return slices.Contains(r.CredentialKinds, kind)
}

func (r Route) PreferredCredentialKind(host string) string {
	for _, endpoint := range r.Downloads {
		if endpoint.Host == host {
			return endpoint.CredentialKind
		}
	}
	return r.DefaultCredentialKind()
}

func AllowsHost(id, host string) bool {
	for _, route := range routes {
		if route.ID == id {
			return slices.ContainsFunc(route.Downloads, func(endpoint Endpoint) bool { return endpoint.Host == host })
		}
	}
	return false
}
