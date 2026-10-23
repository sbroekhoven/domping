// Package config loads domping settings from a YAML file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all tunable settings. Command-line flags override values
// loaded from a file, which override the defaults.
type Config struct {
	Resolver      string        `yaml:"resolver"`
	Resolver2     string        `yaml:"resolver2"`
	Concurrency   int           `yaml:"concurrency"`
	Timeout       time.Duration `yaml:"timeout"`
	RDAPInterval  time.Duration `yaml:"rdap_interval"`
	RDAPBootstrap string        `yaml:"rdap_bootstrap"`
	RDAPFallback  string        `yaml:"rdap_fallback"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Resolver:      "1.1.1.1:53",
		Resolver2:     "8.8.8.8:53",
		Concurrency:   10,
		Timeout:       10 * time.Second,
		RDAPInterval:  200 * time.Millisecond,
		RDAPBootstrap: "https://data.iana.org/rdap/dns.json",
		RDAPFallback:  "https://rdap.org/domain/",
	}
}

// DefaultPath is where Load looks when no explicit path is given:
// $XDG_CONFIG_HOME/domping/config.yml (or the OS equivalent).
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "domping", "config.yml")
}

// Load reads path on top of the defaults. If path is empty, DefaultPath is
// used and a missing file is not an error. It returns the path actually
// loaded ("" if none).
func Load(path string) (Config, string, error) {
	cfg := Default()
	explicit := path != ""
	if !explicit {
		path = DefaultPath()
		if path == "" {
			return cfg, "", nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			return cfg, "", nil
		}
		return cfg, "", fmt.Errorf("config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return cfg, "", fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, path, nil
}

// Validate checks values and normalizes resolver addresses (adding :53).
func (c *Config) Validate() error {
	var err error
	if c.Resolver == "" {
		return errors.New("resolver must not be empty")
	}
	if c.Resolver, err = NormalizeResolver(c.Resolver); err != nil {
		return err
	}
	if c.Resolver2 != "" {
		if c.Resolver2, err = NormalizeResolver(c.Resolver2); err != nil {
			return err
		}
	}
	if c.Concurrency < 1 {
		return errors.New("concurrency must be at least 1")
	}
	if c.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if c.RDAPInterval < 0 {
		return errors.New("rdap_interval must not be negative")
	}
	return nil
}

// NormalizeResolver accepts "host", "host:port", "ipv6" or "[ipv6]:port"
// and returns "host:port", defaulting to port 53.
func NormalizeResolver(s string) (string, error) {
	if host, port, err := net.SplitHostPort(s); err == nil {
		if host == "" || port == "" {
			return "", fmt.Errorf("invalid resolver address %q", s)
		}
		return s, nil
	}
	if ip := net.ParseIP(s); ip != nil || !strings.Contains(s, ":") {
		return net.JoinHostPort(s, "53"), nil
	}
	return "", fmt.Errorf("invalid resolver address %q", s)
}
