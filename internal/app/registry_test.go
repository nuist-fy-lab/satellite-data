package app

import "testing"

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
