package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
)

var era5JobID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (c *client) era5Schema(args []string) error {
	set := flag.NewFlagSet("era5 schema", flag.ContinueOnError)
	collection := set.String("collection", "", "ERA5 collection")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 || *collection == "" {
		return errors.New("--collection is required; no positional arguments")
	}
	var schema era5.Process
	if err := c.get("/api/v1/era5/schema?collection="+url.QueryEscape(*collection), &schema); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(schema)
}

func (c *client) era5Submit(args []string) error {
	set := flag.NewFlagSet("era5 submit", flag.ContinueOnError)
	collection := set.String("collection", "", "ERA5 collection")
	file := set.String("inputs-file", "", "JSON object of CDS input fields")
	variable := set.String("variable", "", "CDS variable")
	productType := set.String("product-type", "", "CDS product type")
	pressure := set.String("pressure-level", "", "CDS pressure level")
	year := set.String("year", "", "UTC year")
	month := set.String("month", "", "UTC month")
	day := set.String("day", "", "UTC day")
	hour := set.String("time", "", "UTC time HH:MM")
	area := set.String("area", "", "north,west,south,east")
	dataFormat := set.String("data-format", "", "data format")
	downloadFormat := set.String("download-format", "", "download format")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 || *collection == "" {
		return errors.New("--collection is required; no positional arguments")
	}
	fields := []struct{ name, value string }{
		{"variable", *variable}, {"product_type", *productType}, {"pressure_level", *pressure},
		{"year", *year}, {"month", *month}, {"day", *day}, {"time", *hour},
		{"data_format", *dataFormat}, {"download_format", *downloadFormat},
	}
	inputs := make(map[string]json.RawMessage)
	if *file != "" {
		if *area != "" {
			return errors.New("--inputs-file cannot be combined with input flags")
		}
		for _, field := range fields {
			if field.value != "" {
				return errors.New("--inputs-file cannot be combined with input flags")
			}
		}
		f, err := os.Open(*file)
		if err != nil {
			return errors.New("cannot open inputs file")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &inputs) != nil || len(inputs) == 0 {
			return errors.New("inputs file must contain one nonempty JSON object (max 1 MiB)")
		}
	} else {
		for _, field := range fields {
			if field.value != "" {
				var input any = []string{field.value}
				if field.name == "data_format" || field.name == "download_format" {
					input = field.value
				}
				encoded, _ := json.Marshal(input)
				inputs[field.name] = encoded
			}
		}
		if *area != "" {
			parts := strings.Split(*area, ",")
			if len(parts) != 4 {
				return errors.New("--area must be north,west,south,east")
			}
			coords := make([]float64, 4)
			for i, part := range parts {
				value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
				if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < -180 || value > 180 {
					return errors.New("invalid --area coordinate")
				}
				coords[i] = value
			}
			if coords[0] <= coords[2] || coords[1] >= coords[3] || coords[0] > 90 || coords[2] < -90 {
				return errors.New("--area must be north>south and west<east")
			}
			encoded, _ := json.Marshal(coords)
			inputs["area"] = encoded
		}
	}
	if len(inputs) == 0 {
		return errors.New("specify CDS inputs with flags or --inputs-file")
	}
	var job era5.Job
	if err := c.post("/api/v1/era5/requests", era5.Submission{CollectionID: *collection, Inputs: inputs}, &job); err != nil {
		return err
	}
	if !era5JobID.MatchString(job.ID) {
		return errors.New("centre returned an invalid CDS job ID; inspect CDS before resubmitting")
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(job)
	}
	fmt.Printf("CDS job %s: %s\n", job.ID, job.Status)
	return nil
}

func era5IDArgs(command string, args []string) (string, bool, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	id := set.String("id", "", "CDS job ID")
	jsonOutput := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return "", false, err
	}
	if set.NArg() != 0 || !era5JobID.MatchString(*id) {
		return "", false, errors.New("valid --id required; no positional arguments")
	}
	return *id, *jsonOutput, nil
}

func (c *client) era5Status(args []string) error {
	id, jsonOutput, err := era5IDArgs("era5 status", args)
	if err != nil {
		return err
	}
	var job era5.Job
	if err := c.get("/api/v1/era5/requests/"+id, &job); err != nil {
		return err
	}
	if job.ID != id {
		return errors.New("centre returned a different CDS job")
	}
	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(job)
	}
	fmt.Printf("CDS job %s: %s\n", job.ID, job.Status)
	return nil
}

func (c *client) era5Result(args []string) error {
	id, jsonOutput, err := era5IDArgs("era5 result", args)
	if err != nil {
		return err
	}
	var asset struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		MediaType string `json:"media_type"`
	}
	if err := c.get("/api/v1/era5/requests/"+id+"/result", &asset); err != nil {
		return err
	}
	if asset.ID != "era5:"+id || asset.Size <= 0 {
		return errors.New("centre returned an invalid CDS result")
	}
	// Signed result URLs are not printed or stored in terminal output.
	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(asset)
	}
	fmt.Printf("CDS result %s: %s (%d bytes, %s)\n", id, asset.Name, asset.Size, asset.MediaType)
	return nil
}
