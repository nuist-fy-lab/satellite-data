package main

import (
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const accessPath = "/api/v1/cli/access"

func (c *client) accessList(args []string) error {
	set := flag.NewFlagSet("access list", flag.ContinueOnError)
	jsonOutput := set.Bool("json", false, "print compact JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("access list takes no positional arguments")
	}
	var result json.RawMessage
	if err := c.get(accessPath, &result); err != nil {
		return err
	}
	return printAccessJSON(result, *jsonOutput)
}

func (c *client) accessSave(args []string) error {
	set := flag.NewFlagSet("access save", flag.ContinueOnError)
	id := set.Int64("id", 0, "existing access profile ID")
	alias := set.String("alias", "", "access profile name")
	source := set.String("source", "", "data source ID")
	mode := set.String("proxy-mode", "direct", "direct, auto or fixed")
	enabled := set.Bool("enabled", true, "enable the access profile")
	priority := set.Int("priority", 0, "access profile priority")
	credential := set.Int64("credential", 0, "saved credential ID")
	proxy := set.Int64("proxy", 0, "saved proxy ID for fixed mode")
	jsonOutput := set.Bool("json", false, "print compact JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 || *id < 0 || *credential < 0 || *proxy < 0 || strings.TrimSpace(*alias) == "" || strings.TrimSpace(*source) == "" {
		return errors.New("--source and --alias are required, and IDs must be positive when specified")
	}
	switch *mode {
	case "direct", "auto":
		if *proxy != 0 {
			return errors.New("--proxy requires --proxy-mode fixed")
		}
	case "fixed":
		if *proxy == 0 {
			return errors.New("--proxy-mode fixed requires --proxy")
		}
	default:
		return errors.New("--proxy-mode must be direct, auto or fixed")
	}
	payload := map[string]any{
		"alias": *alias, "source_id": *source, "proxy_mode": *mode, "enabled": *enabled, "priority": *priority,
	}
	if *id != 0 {
		payload["id"] = *id
	}
	if *credential != 0 {
		payload["credential_id"] = *credential
	}
	if *proxy != 0 {
		payload["proxy_id"] = *proxy
	}
	var result json.RawMessage
	if err := c.post(accessPath, payload, &result); err != nil {
		return err
	}
	return printAccessJSON(result, *jsonOutput)
}

func (c *client) accessDelete(args []string) error {
	set := flag.NewFlagSet("access delete", flag.ContinueOnError)
	id := set.Int64("id", 0, "access profile ID")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 || *id <= 0 {
		return errors.New("--id must be a positive access profile ID")
	}
	return c.do(http.MethodDelete, accessPath+"?id="+strconv.FormatInt(*id, 10), nil, nil)
}

func printAccessJSON(result json.RawMessage, compact bool) error {
	encoder := json.NewEncoder(os.Stdout)
	if !compact {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(result)
}
