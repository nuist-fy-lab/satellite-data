package app

import "testing"

func TestRegistrySourcesExposeDownloadRoutes(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range registry.Sources() {
		access := source.Access
		if access == nil || access.Catalog == "" || len(access.Downloads) == 0 {
			t.Fatalf("source %s is missing download access metadata", source.ID)
		}
		switch source.ID {
		case "goes16", "goes17", "goes18", "goes19":
			if access.ID != "goes" || access.DefaultCredentialKind() != "" {
				t.Fatalf("source %s did not share the public GOES route", source.ID)
			}
		case "modis", "viirs":
			if len(access.Downloads) != 2 || access.Downloads[0].CredentialKind != "laads" || access.Downloads[1].CredentialKind != "earthdata" {
				t.Fatalf("source %s is missing the native and cloud credential preferences", source.ID)
			}
		}
	}
}

func TestRegistryRegistersIASIMetop(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := registry.Provider("iasi")
	if err != nil {
		t.Fatal(err)
	}
	source := provider.Source()
	if source.ID != "iasi" || source.Name != "IASI / Metop" {
		t.Fatalf("unexpected IASI source: %+v", source)
	}
}

func TestRegistryRegistersIASINGMetopSG(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registry.Provider("iasi-ng")
	if err != nil {
		t.Fatal(err)
	}
	source := registered.Source()
	if source.ID != "iasi-ng" || source.Name != "IASI-NG / Metop-SG" {
		t.Fatalf("unexpected IASI-NG source: %+v", source)
	}
}
