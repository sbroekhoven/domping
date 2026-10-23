// Package output renders check results as aligned text or JSON lines, to
// the console and optionally to a file.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sbroekhoven/domping/internal/dnscheck"
	"github.com/sbroekhoven/domping/internal/rdap"
	"github.com/sbroekhoven/domping/internal/verdict"
)

// Record is the full result for one domain.
type Record struct {
	Domain   string          `json:"domain"`
	Input    string          `json:"input,omitempty"`
	Verdict  verdict.Verdict `json:"verdict"`
	Inactive bool            `json:"inactive"`
	Expiry   *time.Time      `json:"expiry,omitempty"`
	Notes    []string        `json:"notes,omitempty"`
	RDAP     rdap.Result     `json:"rdap"`
	DNS      dnscheck.Result `json:"dns"`
}

// Options control rendering.
type Options struct {
	JSON         bool
	InactiveOnly bool
	Color        bool // colorize console output (never the file)
	DomainWidth  int  // width of the domain column
}

// Writer writes records to the console (stdout/stderr) and an optional file.
type Writer struct {
	Stdout, Stderr io.Writer
	File           io.Writer // nil if no -out
	Opts           Options
}

const (
	verdictWidth = len(verdict.RegisteredNoDNS)
	rdapWidth    = len("not-registered")
	dnsWidth     = len("NS,A,AAAA")
	expiryWidth  = len("2006-01-02")
)

// Header writes the column header (table mode only).
func (w *Writer) Header() error {
	if w.Opts.JSON {
		return nil
	}
	h := w.columns("DOMAIN", "VERDICT", "RDAP", "DNS", "EXPIRES", "NOTES")
	if _, err := fmt.Fprintln(w.Stdout, w.paint(h, ansiBold)); err != nil {
		return err
	}
	if w.File != nil && !w.Opts.InactiveOnly {
		_, err := fmt.Fprintln(w.File, h)
		return err
	}
	return nil
}

// Write renders one record.
func (w *Writer) Write(r Record) error {
	if w.Opts.InactiveOnly {
		switch {
		case r.Verdict.Inactive():
			if err := w.console(r); err != nil {
				return err
			}
			if w.File != nil {
				_, err := fmt.Fprintln(w.File, r.Domain)
				return err
			}
		case r.Verdict == verdict.Unknown || r.Verdict == verdict.Inconsistent:
			return w.warn(r)
		}
		return nil
	}

	if err := w.console(r); err != nil {
		return err
	}
	if w.File == nil {
		return nil
	}
	if w.Opts.JSON {
		return writeJSON(w.File, r)
	}
	_, err := io.WriteString(w.File, w.text(r, false))
	return err
}

func (w *Writer) console(r Record) error {
	if w.Opts.JSON {
		return writeJSON(w.Stdout, r)
	}
	_, err := io.WriteString(w.Stdout, w.text(r, w.Opts.Color))
	return err
}

// warn reports an UNKNOWN/INCONSISTENT domain on stderr so it is not lost
// when only inactive domains are reported.
func (w *Writer) warn(r Record) error {
	msg := fmt.Sprintf("warning: %s %s (rdap: %s, dns: %s)", r.Domain, r.Verdict, rdapShort(r.RDAP), r.DNS.Short())
	if len(r.Notes) > 0 {
		msg += ": " + strings.Join(r.Notes, "; ")
	}
	_, err := fmt.Fprintln(w.Stderr, msg)
	if err == nil && r.Verdict == verdict.Inconsistent {
		_, err = io.WriteString(w.Stderr, rawLines(r, "  "))
	}
	return err
}

func (w *Writer) text(r Record, color bool) string {
	exp := "-"
	if r.Expiry != nil {
		exp = r.Expiry.Format("2006-01-02")
	}
	v := pad(string(r.Verdict), verdictWidth)
	if color {
		v = colorize(v, verdictColor(r.Verdict))
	}
	line := strings.Join([]string{
		pad(r.Domain, w.Opts.DomainWidth),
		v,
		pad(rdapShort(r.RDAP), rdapWidth),
		pad(r.DNS.Short(), dnsWidth),
		pad(exp, expiryWidth),
		strings.Join(r.Notes, "; "),
	}, "  ")
	line = strings.TrimRight(line, " ") + "\n"
	if r.Verdict == verdict.Inconsistent {
		line += rawLines(r, strings.Repeat(" ", 4))
	}
	return line
}

// rawLines prints both raw results for manual review.
func rawLines(r Record, indent string) string {
	rd := fmt.Sprintf("%srdap: state=%s http=%d url=%s", indent, r.RDAP.State, r.RDAP.HTTPStatus, r.RDAP.URL)
	if len(r.RDAP.Statuses) > 0 {
		rd += " status=" + strings.Join(r.RDAP.Statuses, ",")
	}
	if r.RDAP.Err != "" {
		rd += " error=" + r.RDAP.Err
	}
	return rd + "\n" + indent + "dns:  " + r.DNS.Raw() + "\n"
}

func (w *Writer) columns(cols ...string) string {
	widths := []int{w.Opts.DomainWidth, verdictWidth, rdapWidth, dnsWidth, expiryWidth, 0}
	for i := range cols {
		cols[i] = pad(cols[i], widths[i])
	}
	return strings.TrimRight(strings.Join(cols, "  "), " ")
}

func rdapShort(r rdap.Result) string {
	if r.State == rdap.Unknown && r.HTTPStatus != 0 && r.HTTPStatus != 200 {
		return fmt.Sprintf("unknown(%d)", r.HTTPStatus)
	}
	return r.State.String()
}

func writeJSON(w io.Writer, r Record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiMagenta = "\x1b[35m"
	ansiGray    = "\x1b[90m"
)

func verdictColor(v verdict.Verdict) string {
	switch v {
	case verdict.Active:
		return ansiGreen
	case verdict.RegisteredNoDNS:
		return ansiYellow
	case verdict.Unregistered:
		return ansiRed
	case verdict.Inconsistent:
		return ansiMagenta
	default:
		return ansiGray
	}
}

func colorize(s, code string) string { return code + s + ansiReset }

func (w *Writer) paint(s, code string) string {
	if !w.Opts.Color {
		return s
	}
	return colorize(s, code)
}

// IsTerminal reports whether f is a character device (a TTY).
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
