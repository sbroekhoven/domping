// Package input reads, normalizes and validates domain names.
package input

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// Domain is a normalized, registrable domain name together with the
// input it was derived from.
type Domain struct {
	// Name is the registrable domain in ASCII (punycode) form.
	Name string
	// Input is the cleaned original input (trimmed, lowercased, no trailing dot).
	Input string
	// PublicSuffix is set when the input is itself a public suffix (e.g.
	// co.uk, sandwell.sch.uk). Such names can't be registered; Name is then
	// the suffix itself.
	PublicSuffix bool
}

// Reduced reports whether Name differs from what the user typed, for example
// because a subdomain was reduced or an IDN was converted to punycode.
func (d Domain) Reduced() bool { return d.Name != d.Input }

// LineError is an invalid line in an input file.
type LineError struct {
	Line  int
	Input string
	Err   error
}

func (e *LineError) Error() string {
	return fmt.Sprintf("line %d: %q: %v", e.Line, e.Input, e.Err)
}

func (e *LineError) Unwrap() error { return e.Err }

// errPublicSuffix is returned by registrable for names that are themselves
// public suffixes.
var errPublicSuffix = errors.New("is a public suffix, not a registrable domain")

// Normalize converts raw user input into a registrable ASCII domain name.
// Subdomains are reduced to the registrable domain (eTLD+1, using ICANN
// suffixes only, so e.g. foo.blogspot.com becomes blogspot.com). Public
// suffixes are not an error: they are returned with PublicSuffix set.
func Normalize(raw string) (Domain, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimRight(s, ".")
	if s == "" {
		return Domain{}, errors.New("empty domain name")
	}
	if strings.Contains(s, "://") || strings.ContainsAny(s, "/?#@: \t") {
		return Domain{}, errors.New("not a domain name (looks like a URL, address or contains spaces)")
	}

	ascii, err := idna.Lookup.ToASCII(s)
	if err != nil {
		return Domain{}, fmt.Errorf("invalid domain name: %v", err)
	}
	if err := validateASCII(ascii); err != nil {
		return Domain{}, err
	}

	name, err := registrable(ascii)
	if errors.Is(err, errPublicSuffix) {
		return Domain{Name: ascii, Input: s, PublicSuffix: true}, nil
	}
	if err != nil {
		return Domain{}, err
	}
	return Domain{Name: name, Input: s}, nil
}

func validateASCII(s string) error {
	if len(s) > 253 {
		return errors.New("domain name longer than 253 characters")
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return errors.New("not a registrable domain (needs at least a name and a TLD)")
	}
	for _, l := range labels {
		if l == "" {
			return errors.New("empty label (consecutive dots)")
		}
		if len(l) > 63 {
			return fmt.Errorf("label %q longer than 63 characters", l)
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return fmt.Errorf("label %q starts or ends with a hyphen", l)
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return fmt.Errorf("label %q contains invalid character %q", l, c)
			}
		}
	}
	return nil
}

// registrable returns the registrable domain for name, based on the ICANN
// section of the public suffix list. Unknown TLDs are treated as single-label
// suffixes.
func registrable(name string) (string, error) {
	labels := strings.Split(name, ".")
	suffixLen := 1
	for i := range labels {
		cand := strings.Join(labels[i:], ".")
		if ps, icann := publicsuffix.PublicSuffix(cand); icann && ps == cand {
			suffixLen = len(labels) - i
			break
		}
	}
	if len(labels) <= suffixLen {
		return "", errPublicSuffix
	}
	return strings.Join(labels[len(labels)-suffixLen-1:], "."), nil
}

// Parse reads one domain per line. Blank lines and lines starting with # are
// ignored. Invalid lines are returned as *LineError values; valid domains are
// deduplicated (first occurrence wins).
func Parse(r io.Reader) ([]Domain, []error) {
	var (
		out  []Domain
		errs []error
		seen = map[string]bool{}
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		d, err := Normalize(text)
		if err != nil {
			errs = append(errs, &LineError{Line: line, Input: text, Err: err})
			continue
		}
		if seen[d.Name] {
			continue
		}
		seen[d.Name] = true
		out = append(out, d)
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, fmt.Errorf("reading input: %w", err))
	}
	return out, errs
}

// Dedupe removes domains whose Name was already seen, keeping order.
func Dedupe(ds []Domain) []Domain {
	seen := make(map[string]bool, len(ds))
	out := ds[:0:0]
	for _, d := range ds {
		if seen[d.Name] {
			continue
		}
		seen[d.Name] = true
		out = append(out, d)
	}
	return out
}
