package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sbroekhoven/domping/internal/dnscheck"
	"github.com/sbroekhoven/domping/internal/rdap"
	"github.com/sbroekhoven/domping/internal/verdict"
)

func records() []Record {
	exp := time.Date(2027, 8, 13, 4, 0, 0, 0, time.UTC)
	rec := func(d string, v verdict.Verdict, rs rdap.State, o dnscheck.Outcome) Record {
		return Record{
			Domain: d, Verdict: v, Inactive: v.Inactive(),
			RDAP: rdap.Result{State: rs},
			DNS:  dnscheck.Result{Outcome: o, Primary: dnscheck.ResolverResult{Resolver: "1.1.1.1:53", Outcome: o}},
		}
	}
	active := rec("active.com", verdict.Active, rdap.Registered, dnscheck.Records)
	active.Expiry = &exp
	return []Record{
		active,
		rec("gone.com", verdict.Unregistered, rdap.NotRegistered, dnscheck.NXDomain),
		rec("parked.com", verdict.RegisteredNoDNS, rdap.Registered, dnscheck.NoData),
		rec("odd.com", verdict.Inconsistent, rdap.NotRegistered, dnscheck.NoData),
		rec("flaky.com", verdict.Unknown, rdap.Unknown, dnscheck.Error),
	}
}

func render(t *testing.T, opts Options) (stdout, stderr, file string) {
	t.Helper()
	var so, se, f bytes.Buffer
	opts.DomainWidth = len("parked.com")
	w := &Writer{Stdout: &so, Stderr: &se, File: &f, Opts: opts}
	if err := w.Header(); err != nil {
		t.Fatal(err)
	}
	for _, r := range records() {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	return so.String(), se.String(), f.String()
}

func TestTableOutput(t *testing.T) {
	stdout, stderr, file := render(t, Options{})
	if stdout != file {
		t.Errorf("file differs from console:\n%s\nvs\n%s", file, stdout)
	}
	if stderr != "" {
		t.Errorf("unexpected stderr: %q", stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if !strings.HasPrefix(lines[0], "DOMAIN      VERDICT") {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "active.com  ACTIVE             registered") || !strings.HasSuffix(lines[1], "2027-08-13") {
		t.Errorf("active line = %q", lines[1])
	}
	// Verdict column starts at the same offset on every line.
	col := strings.Index(lines[0], "VERDICT")
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, " ") {
			continue // raw detail line
		}
		if l[col-1] != ' ' || l[col] == ' ' {
			t.Errorf("verdict column not aligned in %q", l)
		}
	}
	if !strings.Contains(stdout, "    rdap: state=not-registered") || !strings.Contains(stdout, "    dns:  1.1.1.1:53") {
		t.Errorf("INCONSISTENT raw details missing:\n%s", stdout)
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Error("color codes without Color option")
	}
}

func TestColorOnlyOnConsole(t *testing.T) {
	stdout, _, file := render(t, Options{Color: true})
	if !strings.Contains(stdout, "\x1b[32mACTIVE") {
		t.Errorf("expected colored verdict on console:\n%q", stdout)
	}
	if strings.Contains(file, "\x1b[") {
		t.Error("file contains color codes")
	}
}

func TestInactiveOnly(t *testing.T) {
	stdout, stderr, file := render(t, Options{InactiveOnly: true})
	if file != "gone.com\nparked.com\n" {
		t.Errorf("file = %q, want bare inactive domains", file)
	}
	if strings.Contains(stdout, "active.com") || strings.Contains(stdout, "flaky.com") || strings.Contains(stdout, "odd.com") {
		t.Errorf("stdout contains non-inactive domains:\n%s", stdout)
	}
	if !strings.Contains(stdout, "gone.com") || !strings.Contains(stdout, "parked.com") {
		t.Errorf("stdout missing inactive domains:\n%s", stdout)
	}
	if !strings.Contains(stderr, "warning: odd.com INCONSISTENT") || !strings.Contains(stderr, "warning: flaky.com UNKNOWN") {
		t.Errorf("stderr missing warnings:\n%s", stderr)
	}
	if strings.Contains(stderr, "active.com") {
		t.Error("active domain warned about")
	}
}

func TestJSON(t *testing.T) {
	stdout, _, file := render(t, Options{JSON: true})
	if stdout != file {
		t.Error("JSON file differs from console")
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5", len(lines))
	}
	var r map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &r); err != nil {
		t.Fatal(err)
	}
	if r["domain"] != "active.com" || r["verdict"] != "ACTIVE" || r["expiry"] != "2027-08-13T04:00:00Z" {
		t.Errorf("unexpected JSON: %v", r)
	}
	if r["rdap"].(map[string]any)["state"] != "registered" || r["dns"].(map[string]any)["outcome"] != "records" {
		t.Errorf("states not rendered as text: %v", r)
	}
}

func TestJSONInactiveOnly(t *testing.T) {
	stdout, _, file := render(t, Options{JSON: true, InactiveOnly: true})
	if file != "gone.com\nparked.com\n" {
		t.Errorf("file = %q, want bare domains", file)
	}
	if n := strings.Count(stdout, "\n"); n != 2 {
		t.Errorf("stdout has %d JSON lines, want 2", n)
	}
}
