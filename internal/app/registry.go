package app

import (
	"fmt"

	"github.com/nuist-fy-lab/satellite-data/internal/objectstore"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/eumetsat"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/goes"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/himawari"
)

func NewRegistry() (*provider.Registry, error) {
	objects := objectstore.NewClient(nil)
	providers := make([]provider.Provider, 0, 7)
	for _, satellite := range []int{16, 17, 18, 19} {
		p, err := goes.New(satellite, objects)
		if err != nil {
			return nil, fmt.Errorf("create GOES-%d provider: %w", satellite, err)
		}
		providers = append(providers, p)
	}
	for _, satellite := range []int{8, 9} {
		p, err := himawari.New(satellite, objects)
		if err != nil {
			return nil, fmt.Errorf("create Himawari-%d provider: %w", satellite, err)
		}
		providers = append(providers, p)
	}
	providers = append(providers, eumetsat.New(nil))
	return provider.NewRegistry(providers...), nil
}
