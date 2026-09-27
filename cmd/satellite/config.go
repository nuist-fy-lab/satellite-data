package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

type cliConfig struct {
	Server string `json:"server"`
	APIKey string `json:"api_key"`
}

func configPath() string {
	if value := os.Getenv("SATELLITE_CONFIG"); value != "" {
		return value
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "satellite", "config.json")
}

func (cfg *cliConfig) validate() error {
	cfg.Server = strings.TrimRight(strings.TrimSpace(cfg.Server), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.Server == "" || cfg.APIKey == "" {
		return errors.New("centre URL and CLI API key required; run `satellite config set`")
	}
	u, err := url.Parse(cfg.Server)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid centre URL")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return errors.New("centre URL must use HTTPS (HTTP allowed only for loopback)")
	}
	if !strings.HasPrefix(cfg.APIKey, "satellite_v1_") || strings.ContainsAny(cfg.APIKey, "\r\n\t ") {
		return errors.New("use a CLI API key created on the Satellite Data web page")
	}
	return nil
}

func loadCLIConfig(path string) (cliConfig, error) {
	var cfg cliConfig
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return cfg, errors.New("CLI config must be a private regular file (mode 0600)")
		}
		if json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(&cfg) != nil {
			return cfg, errors.New("invalid CLI config JSON")
		}
	} else if !os.IsNotExist(err) {
		return cfg, errors.New("cannot open CLI config")
	}
	if value := os.Getenv("SATELLITE_SERVER"); value != "" {
		cfg.Server = value
	}
	if value := os.Getenv("SATELLITE_API_KEY"); value != "" {
		cfg.APIKey = value
	}
	err = cfg.validate()
	return cfg, err
}

func saveCLIConfig(path string, cfg cliConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("cannot create CLI config directory")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return errors.New("cannot create private CLI config")
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(cfg); err != nil {
		file.Close()
		return errors.New("cannot write CLI config")
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot close CLI config")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return errors.New("cannot save CLI config")
	}
	return nil
}

func runConfigSet(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: satellite config set (API keys are never command-line arguments)")
	}
	reader := bufio.NewReader(os.Stdin)
	server := strings.TrimSpace(os.Getenv("SATELLITE_SERVER"))
	if server == "" {
		fmt.Fprint(os.Stderr, "Centre URL: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return errors.New("cannot read centre URL")
		}
		server = strings.TrimSpace(line)
	}
	var key string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "CLI API key: ")
		secret, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return errors.New("cannot read CLI API key")
		}
		key = string(secret)
	} else {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return errors.New("cannot read CLI API key")
		}
		key = line
	}
	if err := saveCLIConfig(configPath(), cliConfig{Server: server, APIKey: key}); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "CLI configuration saved")
	return nil
}
