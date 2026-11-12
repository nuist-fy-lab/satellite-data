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
