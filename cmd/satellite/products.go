package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

// Split only the source prefix: upstream IDs can themselves contain colons.
func resolveProduct(source, collection, product string) (string, string, error) {
	source, collection, product = strings.TrimSpace(source), strings.TrimSpace(collection), strings.TrimSpace(product)
	if product != "" {
		if collection != "" && collection != product {
			return "", "", errors.New("use --product or --collection, not conflicting values")
		}
		collection = product
	}
	if collection == "" {
		return "", "", errors.New("--product is required (or --collection with --source); use `satellite product list` to find product keys")
	}
	if prefix, suffix, ok := strings.Cut(collection, ":"); ok {
		if prefix == "" || suffix == "" {
			return "", "", errors.New("invalid product key")
		}
		if source != "" && source != prefix {
			return "", "", errors.New("product does not belong to --source")
		}
		source = prefix
	} else {
		if source == "" {
			return "", "", errors.New("use a complete source:product key or specify --source")
		}
		collection = source + ":" + collection
	}
	return source, collection, nil
}

type productOption struct {
	model.Product
	Bands     []downloadfilter.Band `json:"bands"`
	OrderMode string                `json:"order_mode"`
}

func (c *client) productOptions(source string) ([]productOption, error) {
	var payload struct {
		Products []productOption `json:"products"`
	}
	if err := c.get("/api/v1/downloads/catalog?source="+url.QueryEscape(source), &payload); err != nil {
		return nil, err
	}
	if payload.Products == nil {
		payload.Products = []productOption{}
	}
	return payload.Products, nil
}

func (c *client) productList(args []string) error {
	set := flag.NewFlagSet("product list", flag.ContinueOnError)
	source := set.String("source", "", "optional source ID; default all sources")
	search := set.String("search", "", "case-insensitive product search")
	instrument := set.String("instrument", "", "instrument")
	level := set.String("level", "", "processing level")
	jsonOutput := set.Bool("json", false, "print product metadata as JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	items, err := c.productOptions(*source)
	if err != nil {
		return err
	}
	filtered := make([]productOption, 0, len(items))
	for _, item := range items {
		text := strings.ToLower(strings.Join([]string{item.ProductKey, item.DisplayName, item.DataType, item.Satellite, item.Instrument, item.Level, item.Version}, " "))
		if !strings.Contains(text, strings.ToLower(*search)) || (*instrument != "" && !strings.EqualFold(item.Instrument, *instrument)) || (*level != "" && !strings.EqualFold(item.Level, *level)) {
			continue
		}
		filtered = append(filtered, item)
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(filtered)
	}
	for _, item := range filtered {
		fmt.Printf("%-48s %-10s %-7s %-8s %-10s %s\n", item.ProductKey, item.Instrument, item.Level, item.Cadence, item.OrderMode, item.DisplayName)
	}
	if len(filtered) == 0 {
		fmt.Fprintln(os.Stderr, "No matching products in the centre catalogue.")
	}
	return nil
}

func (c *client) productShow(args []string) error {
	set := flag.NewFlagSet("product show", flag.ContinueOnError)
	product := set.String("product", "", "complete product key")
	source := set.String("source", "", "source ID for an unqualified product")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	sourceID, key, err := resolveProduct(*source, "", *product)
	if err != nil {
		return err
	}
	items, err := c.productOptions(sourceID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ProductKey == key {
			return json.NewEncoder(os.Stdout).Encode(item)
		}
	}
	return errors.New("unknown product; use `satellite product list` for the current catalogue")
}
