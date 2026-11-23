// Command domping checks whether domain names are alive by cross-checking
// RDAP registration data with DNS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/sbroekhoven/domping/internal/config"
	"github.com/sbroekhoven/domping/internal/dnscheck"
	"github.com/sbroekhoven/domping/internal/input"
	"github.com/sbroekhoven/domping/internal/output"
	"github.com/sbroekhoven/domping/internal/verdict"
	"github.com/sbroekhoven/domping/rdap"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	exitActive       = 0
	exitInactive     = 1
	exitUsage        = 2
	exitInconclusive = 3
	exitInterrupted  = 130
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	file, out, configPath string
	inactiveOnly, json    bool
	noColor, showVersion  bool
	cfg                   config.Config
	args                  []string
}

func parseArgs(args []string, stderr io.Writer) (*options, error) {
	o := &options{}
	def := config.Default()
	fs := flag.NewFlagSet("domping", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `Usage:
  domping [flags] <domain> [domain...]
  domping [flags] -file list.txt [-out results.txt] [-inactive-only]

Checks whether domains are registered (RDAP) and resolve (DNS), and
cross-checks both into a verdict: ACTIVE, REGISTERED_NO_DNS, UNREGISTERED,
INCONSISTENT or UNKNOWN.

Exit codes: 0 all active, 1 inactive found, 2 usage/input error,
3 only unknown/inconsistent (nothing definitively inactive), 130 interrupted.

Flags:
`)
		fs.PrintDefaults()
	}

	var (
		resolver, resolver2   string
		concurrency           int
		timeout, rdapInterval time.Duration
	)
	fs.StringVar(&o.file, "file", "", "read domains from `path` (one per line, # comments allowed; - for stdin)")
	fs.StringVar(&o.out, "out", "", "also write results to `path`")
	fs.BoolVar(&o.inactiveOnly, "inactive-only", false, "report only inactive domains; -out file gets bare domain names")
	fs.BoolVar(&o.json, "json", false, "output one JSON object per domain")
	fs.BoolVar(&o.noColor, "no-color", false, "disable colored output")
	fs.BoolVar(&o.showVersion, "version", false, "print version and exit")
	fs.StringVar(&o.configPath, "config", "", "config `file` (default "+displayPath(config.DefaultPath())+" if it exists)")
	fs.StringVar(&resolver, "resolver", def.Resolver, "primary DNS resolver `host:port`")
	fs.StringVar(&resolver2, "resolver2", def.Resolver2, "secondary DNS resolver `host:port`, consulted on NXDOMAIN/SERVFAIL (empty disables)")
	fs.IntVar(&concurrency, "concurrency", def.Concurrency, "number of domains checked in parallel")
	fs.DurationVar(&timeout, "timeout", def.Timeout, "timeout per RDAP request / DNS query")
	fs.DurationVar(&rdapInterval, "rdap-interval", def.RDAPInterval, "minimum time between requests to the same RDAP server")

	// Allow flags after positional arguments (domping example.com -json).
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		o.args = append(o.args, args[0])
		args = args[1:]
	}
	if o.showVersion {
		return o, nil
	}

	cfg, _, err := config.Load(o.configPath)
	if err != nil {
		return nil, err
	}
	// Explicit flags override the config file.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "resolver":
			cfg.Resolver = resolver
		case "resolver2":
			cfg.Resolver2 = resolver2
		case "concurrency":
			cfg.Concurrency = concurrency
		case "timeout":
			cfg.Timeout = timeout
		case "rdap-interval":
			cfg.RDAPInterval = rdapInterval
		}
	})
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	o.cfg = cfg

	if o.file == "" && len(o.args) == 0 {
		return nil, errors.New("no domain given: pass a domain or -file")
	}
	return o, nil
}

func displayPath(p string) string {
	if p == "" {
		return "none"
	}
	return p
}

func run(args []string, stdout, stderr io.Writer) int {
	o, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitActive
		}
		fmt.Fprintln(stderr, "domping:", err)
		return exitUsage
	}
	if o.showVersion {
		fmt.Fprintln(stdout, "domping", version)
		return exitActive
	}

	domains, errs := loadDomains(o)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(stderr, "domping:", e)
		}
		return exitUsage
	}
	if len(domains) == 0 {
		fmt.Fprintln(stderr, "domping: no domains to check")
		return exitUsage
	}

	var outFile *os.File
	if o.out != "" {
		outFile, err = os.Create(o.out)
		if err != nil {
			fmt.Fprintln(stderr, "domping:", err)
			return exitUsage
		}
		defer outFile.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // a second Ctrl+C terminates immediately
	}()

	width := len("DOMAIN")
	for _, d := range domains {
		width = max(width, len(d.Name))
	}
	w := &output.Writer{
		Stdout: stdout,
		Stderr: stderr,
		Opts: output.Options{
			JSON:         o.json,
			InactiveOnly: o.inactiveOnly,
			Color:        !o.noColor && os.Getenv("NO_COLOR") == "" && stdout == os.Stdout && output.IsTerminal(os.Stdout),
			DomainWidth:  width,
		},
	}
	if outFile != nil {
		w.File = outFile
	}

	checker := &checker{
		rdap: rdap.New(rdap.Options{
			BootstrapURL: o.cfg.RDAPBootstrap,
			FallbackURL:  o.cfg.RDAPFallback,
			Timeout:      o.cfg.Timeout,
			HostInterval: o.cfg.RDAPInterval,
			UserAgent:    "domping/" + version,
		}),
		dns: &dnscheck.Checker{Primary: o.cfg.Resolver, Secondary: o.cfg.Resolver2, Timeout: o.cfg.Timeout},
	}

	if err := w.Header(); err != nil {
		fmt.Fprintln(stderr, "domping:", err)
		return exitUsage
	}
	counts, writeErr := checkAll(ctx, checker, domains, o.cfg.Concurrency, w)
	if err := checker.rdap.BootstrapError(); err != nil {
		fmt.Fprintln(stderr, "domping: warning:", err, "(used", o.cfg.RDAPFallback, "for all lookups)")
	}
	if writeErr != nil {
		fmt.Fprintln(stderr, "domping:", writeErr)
	}
	if outFile != nil {
		if err := outFile.Close(); err != nil && writeErr == nil {
			fmt.Fprintln(stderr, "domping:", err)
			writeErr = err
		}
	}

	total := 0
	for _, n := range counts {
		total += n
	}
	if len(domains) > 1 || ctx.Err() != nil {
		fmt.Fprintf(stderr, "checked %d/%d: %d active, %d registered_no_dns, %d unregistered, %d public_suffix, %d inconsistent, %d unknown\n",
			total, len(domains), counts[verdict.Active], counts[verdict.RegisteredNoDNS],
			counts[verdict.Unregistered], counts[verdict.PublicSuffix], counts[verdict.Inconsistent], counts[verdict.Unknown])
	}
	if outFile != nil && writeErr == nil {
		fmt.Fprintln(stderr, outSummary(o, w.FileRecords))
	}

	switch {
	case ctx.Err() != nil:
		fmt.Fprintln(stderr, "domping: interrupted")
		return exitInterrupted
	case writeErr != nil:
		return exitUsage
	case counts[verdict.Unregistered]+counts[verdict.RegisteredNoDNS]+counts[verdict.PublicSuffix] > 0:
		return exitInactive
	case counts[verdict.Unknown]+counts[verdict.Inconsistent] > 0:
		return exitInconclusive
	default:
		return exitActive
	}
}

// outSummary tells where the -out file is and what was stored in it.
func outSummary(o *options, n int) string {
	path, err := filepath.Abs(o.out)
	if err != nil {
		path = o.out
	}
	switch {
	case o.inactiveOnly && n == 0:
		return fmt.Sprintf("wrote 0 inactive domains to %s (none found, file is empty)", path)
	case o.inactiveOnly:
		return fmt.Sprintf("wrote %d inactive %s to %s (domain names only, usable with -file)", n, plural(n, "domain"), path)
	case o.json:
		return fmt.Sprintf("wrote %d %s to %s (JSON lines)", n, plural(n, "result"), path)
	default:
		return fmt.Sprintf("wrote %d %s to %s (table)", n, plural(n, "result"), path)
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func loadDomains(o *options) ([]input.Domain, []error) {
	var (
		domains []input.Domain
		errs    []error
	)
	for _, a := range o.args {
		d, err := input.Normalize(a)
		if err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", a, err))
			continue
		}
		domains = append(domains, d)
	}
	if o.file != "" {
		var r io.Reader = os.Stdin
		if o.file != "-" {
			f, err := os.Open(o.file)
			if err != nil {
				return nil, append(errs, err)
			}
			defer f.Close()
			r = f
		}
		ds, ferrs := input.Parse(r)
		for _, e := range ferrs {
			errs = append(errs, fmt.Errorf("%s: %w", o.file, e))
		}
		domains = append(domains, ds...)
	}
	return input.Dedupe(domains), errs
}

type checker struct {
	rdap *rdap.Client
	dns  *dnscheck.Checker
}

func (c *checker) check(ctx context.Context, d input.Domain) output.Record {
	if d.PublicSuffix {
		return output.Record{
			Domain:   d.Name,
			Verdict:  verdict.PublicSuffix,
			Inactive: true,
			Notes:    []string{"public suffix, not a registrable domain"},
		}
	}
	var (
		rr rdap.Result
		dr dnscheck.Result
		wg sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); rr = c.rdap.Lookup(ctx, d.Name) }()
	go func() { defer wg.Done(); dr = c.dns.Check(ctx, d.Name) }()
	wg.Wait()

	v, notes := verdict.Decide(rr, dr)
	rec := output.Record{
		Domain:   d.Name,
		Verdict:  v,
		Inactive: v.Inactive(),
		Expiry:   rr.Expiry,
		Notes:    notes,
		RDAP:     rr,
		DNS:      dr,
	}
	if d.Reduced() {
		rec.Input = d.Input
		rec.Notes = append([]string{"input: " + d.Input}, rec.Notes...)
	}
	return rec
}

// checkAll runs a worker pool over domains and writes results in input
// order as soon as they are available. Domains interrupted by cancellation
// are skipped rather than reported as UNKNOWN.
func checkAll(ctx context.Context, c *checker, domains []input.Domain, workers int, w *output.Writer) (map[verdict.Verdict]int, error) {
	type result struct {
		idx int
		rec output.Record
		ok  bool
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	results := make(chan result)

	var wg sync.WaitGroup
	for range min(workers, len(domains)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				rec := c.check(ctx, domains[i])
				results <- result{idx: i, rec: rec, ok: ctx.Err() == nil}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range domains {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	counts := map[verdict.Verdict]int{}
	pending := map[int]result{}
	next := 0
	var writeErr error
	for res := range results {
		pending[res.idx] = res
		for {
			r, ok := pending[next]
			if !ok {
				break
			}
			delete(pending, next)
			next++
			if !r.ok {
				continue
			}
			counts[r.rec.Verdict]++
			if writeErr == nil {
				if writeErr = w.Write(r.rec); writeErr != nil {
					cancel() // e.g. broken pipe: stop checking
				}
			}
		}
	}
	return counts, writeErr
}
