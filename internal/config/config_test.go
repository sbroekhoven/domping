package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("overrides only given keys", func(t *testing.T) {
		p := write("partial.yml", "resolver: 9.9.9.9\ntimeout: 3s\n")
		cfg, used, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if used != p || cfg.Resolver != "9.9.9.9" || cfg.Timeout != 3*time.Second {
			t.Errorf("got %+v (used %q)", cfg, used)
		}
		if cfg.Resolver2 != Default().Resolver2 || cfg.Concurrency != Default().Concurrency {
			t.Errorf("defaults not kept: %+v", cfg)
		}
	})
	t.Run("empty resolver2 disables fallback", func(t *testing.T) {
		cfg, _, err := Load(write("nores2.yml", "resolver2: \"\"\n"))
		if err != nil || cfg.Resolver2 != "" {
			t.Errorf("resolver2 = %q, err = %v", cfg.Resolver2, err)
		}
	})
	t.Run("empty file", func(t *testing.T) {
		cfg, _, err := Load(write("empty.yml", ""))
		if err != nil || cfg != Default() {
			t.Errorf("got %+v, %v", cfg, err)
		}
	})
	t.Run("unknown key", func(t *testing.T) {
		if _, _, err := Load(write("typo.yml", "resolvr: 1.1.1.1\n")); err == nil {
			t.Error("want error for unknown key")
		}
	})
	t.Run("bad duration", func(t *testing.T) {
		if _, _, err := Load(write("dur.yml", "timeout: soon\n")); err == nil {
			t.Error("want error for invalid duration")
		}
	})
	t.Run("explicit missing file", func(t *testing.T) {
		if _, _, err := Load(filepath.Join(dir, "missing.yml")); err == nil {
			t.Error("want error for missing explicit file")
		}
	})
	t.Run("missing default file is fine", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "nothing-here"))
		t.Setenv("HOME", filepath.Join(dir, "nothing-here"))
		cfg, used, err := Load("")
		if err != nil || used != "" || cfg != Default() {
			t.Errorf("got %+v, %q, %v", cfg, used, err)
		}
	})
}

func TestNormalizeResolver(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "1.1.1.1", want: "1.1.1.1:53"},
		{in: "1.1.1.1:5353", want: "1.1.1.1:5353"},
		{in: "2606:4700:4700::1111", want: "[2606:4700:4700::1111]:53"},
		{in: "[2606:4700:4700::1111]:53", want: "[2606:4700:4700::1111]:53"},
		{in: "dns.example", want: "dns.example:53"},
		{in: ":53", wantErr: true},
		{in: "1.1.1.1:", wantErr: true},
		{in: "a:b:c", wantErr: true},
	}
	for _, tt := range tests {
		got, err := NormalizeResolver(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("NormalizeResolver(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		ok     bool
	}{
		{"defaults", func(*Config) {}, true},
		{"no resolver", func(c *Config) { c.Resolver = "" }, false},
		{"zero concurrency", func(c *Config) { c.Concurrency = 0 }, false},
		{"zero timeout", func(c *Config) { c.Timeout = 0 }, false},
		{"bad resolver2", func(c *Config) { c.Resolver2 = ":" }, false},
	}
	for _, tt := range tests {
		c := Default()
		tt.mutate(&c)
		if err := c.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}
