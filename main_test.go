package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunUsageErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(bad, []byte("example.com\nnot a domain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, []byte("# only comments\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir) // no user config interferes

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"no input", nil, "no domain given"},
		{"invalid domain", []string{"http://example.com"}, "not a domain name"},
		{"public suffix", []string{"co.uk"}, "public suffix"},
		{"invalid line in file", []string{"-file", bad}, "line 2"},
		{"empty file", []string{"-file", empty}, "no domains to check"},
		{"missing file", []string{"-file", filepath.Join(dir, "nope.txt")}, "no such file"},
		{"unknown flag", []string{"-bogus", "example.com"}, "flag provided but not defined"},
		{"bad concurrency", []string{"-concurrency", "0", "example.com"}, "concurrency"},
		{"bad resolver", []string{"-resolver", "a:b:c", "example.com"}, "invalid resolver"},
		{"missing config", []string{"-config", filepath.Join(dir, "none.yml"), "example.com"}, "config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != exitUsage {
				t.Errorf("exit = %d, want %d (stderr: %s)", code, exitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantErr)
			}
		})
	}
}

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer
	if code := run([]string{"-version"}, &stdout, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.HasPrefix(stdout.String(), "domping ") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestParseArgsPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(cfg, []byte("resolver: 9.9.9.9\nresolver2: 149.112.112.112\nconcurrency: 4\ntimeout: 3s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Flags after the positional argument are still parsed.
	o, err := parseArgs([]string{"-config", cfg, "example.com", "-concurrency", "7", "-json", "other.com"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(o.args, ",") != "example.com,other.com" || !o.json {
		t.Errorf("args = %v, json = %v", o.args, o.json)
	}
	c := o.cfg
	if c.Resolver != "9.9.9.9:53" || c.Resolver2 != "149.112.112.112:53" {
		t.Errorf("resolvers from config not applied: %+v", c)
	}
	if c.Concurrency != 7 {
		t.Errorf("flag should override config: concurrency = %d", c.Concurrency)
	}
	if c.Timeout != 3*time.Second {
		t.Errorf("timeout = %s, want 3s from config", c.Timeout)
	}
}
