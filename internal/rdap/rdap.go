// Package rdap looks up domain registration data via RDAP (RFC 9082/9083),
// locating the authoritative server through the IANA bootstrap registry
// (RFC 9224).
package rdap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// State is the outcome of an RDAP lookup.
type State int

const (
	Unknown State = iota
	Registered
	NotRegistered
	// NoService means the TLD has no RDAP service at all (e.g. .de), as
	// opposed to Unknown, where a lookup was possible but failed.
	NoService
)

func (s State) String() string {
	switch s {
	case Registered:
		return "registered"
	case NotRegistered:
		return "not-registered"
	case NoService:
		return "no-rdap"
	default:
		return "unknown"
	}
}

func (s State) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Result is the outcome of looking up one domain.
type Result struct {
	State      State      `json:"state"`
	HTTPStatus int        `json:"http_status,omitempty"`
	URL        string     `json:"url,omitempty"`
	Statuses   []string   `json:"statuses,omitempty"`
	Expiry     *time.Time `json:"expiry,omitempty"`
	Attempts   int        `json:"attempts"`
	Err        string     `json:"error,omitempty"`
}

// HasStatus reports whether the domain carries the given EPP-style status
// (case-insensitive, e.g. "clientHold").
func (r Result) HasStatus(status string) bool {
	for _, s := range r.Statuses {
		if strings.EqualFold(s, status) {
			return true
		}
	}
	return false
}

// Options configure a Client. Zero values select sensible defaults.
type Options struct {
	BootstrapURL string        // IANA dns.json
	FallbackURL  string        // base URL used when the TLD is not in the bootstrap; domain is appended
	Timeout      time.Duration // per HTTP attempt
	MaxAttempts  int           // total attempts for retryable failures
	Backoff      time.Duration // base backoff, doubled per retry
	HostInterval time.Duration // minimum spacing between requests to the same host
	HTTPClient   *http.Client
	UserAgent    string
}

// Client performs RDAP lookups. It is safe for concurrent use.
type Client struct {
	opts    Options
	http    *http.Client
	limiter *hostLimiter

	bsMu     sync.Mutex
	bsLoaded bool
	bsErr    error
	bs       map[string]string // TLD (or multi-label suffix) -> base URL
}

// New returns a Client.
func New(opts Options) *Client {
	if opts.BootstrapURL == "" {
		opts.BootstrapURL = "https://data.iana.org/rdap/dns.json"
	}
	if opts.FallbackURL == "" {
		opts.FallbackURL = "https://rdap.org/domain/"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	if opts.Backoff <= 0 {
		opts.Backoff = time.Second
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "domping"
	}
	c := &Client{opts: opts, limiter: newHostLimiter(opts.HostInterval)}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	cp := *hc
	// Redirects (e.g. rdap.org -> registry) count against the target host's rate limit.
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return c.limiter.Wait(req.Context(), req.URL.Host)
	}
	c.http = &cp
	return c
}

// BootstrapError returns the error from loading the IANA bootstrap, if any.
// Lookups still work through the fallback URL when it failed.
func (c *Client) BootstrapError() error {
	c.bsMu.Lock()
	defer c.bsMu.Unlock()
	return c.bsErr
}

// Lookup queries RDAP for domain (ASCII form).
func (c *Client) Lookup(ctx context.Context, domain string) Result {
	base, fromBootstrap := c.serverFor(ctx, domain)
	u := c.opts.FallbackURL + domain
	if fromBootstrap {
		u = strings.TrimRight(base, "/") + "/domain/" + domain
	}
	res := Result{URL: u}
	parsed, err := url.Parse(u)
	if err != nil {
		res.Err = err.Error()
		return res
	}

	for attempt := 1; attempt <= c.opts.MaxAttempts; attempt++ {
		res.Attempts = attempt
		if err := c.limiter.Wait(ctx, parsed.Host); err != nil {
			res.Err = err.Error()
			return res
		}
		retry, wait := c.attempt(ctx, u, &res, fromBootstrap)
		if !retry || attempt == c.opts.MaxAttempts {
			return res
		}
		if wait <= 0 {
			wait = c.opts.Backoff << (attempt - 1)
		}
		if res.HTTPStatus == http.StatusTooManyRequests {
			c.limiter.Delay(parsed.Host, wait)
		}
		if err := sleep(ctx, wait); err != nil {
			res.Err = err.Error()
			return res
		}
	}
	return res
}

// attempt performs one request and fills res. It reports whether the failure
// is retryable and, if the server said so, how long to wait.
func (c *Client) attempt(ctx context.Context, u string, res *Result, fromBootstrap bool) (retry bool, wait time.Duration) {
	actx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(actx, http.MethodGet, u, nil)
	if err != nil {
		res.Err = err.Error()
		return false, 0
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	req.Header.Set("User-Agent", c.opts.UserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		res.State, res.HTTPStatus = Unknown, 0
		if ctx.Err() != nil {
			res.Err = ctx.Err().Error()
			return false, 0
		}
		if errors.Is(err, context.DeadlineExceeded) {
			res.Err = fmt.Sprintf("timeout after %s", c.opts.Timeout)
		} else {
			res.Err = err.Error()
		}
		return true, 0
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	res.HTTPStatus = resp.StatusCode
	res.URL = resp.Request.URL.String()

	switch {
	case resp.StatusCode == http.StatusOK:
		if err != nil {
			res.State, res.Err = Unknown, "reading response: "+err.Error()
			return true, 0
		}
		if err := parseDomain(body, res); err != nil {
			res.State, res.Err = Unknown, err.Error()
			return false, 0
		}
		res.State, res.Err = Registered, ""
		return false, 0

	case resp.StatusCode == http.StatusNotFound:
		// rdap.org answers 404 itself (without redirecting) when no RDAP
		// service exists for the TLD; that says nothing about registration.
		if !fromBootstrap && sameHost(resp.Request.URL.String(), c.opts.FallbackURL) {
			res.State, res.Err = NoService, "no RDAP service available for this TLD"
			return false, 0
		}
		res.State, res.Err = NotRegistered, ""
		return false, 0

	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		res.State, res.Err = Unknown, "HTTP "+strconv.Itoa(resp.StatusCode)
		return true, retryAfter(resp.Header.Get("Retry-After"))

	default:
		res.State, res.Err = Unknown, "HTTP "+strconv.Itoa(resp.StatusCode)
		return false, 0
	}
}

type domainResponse struct {
	ObjectClassName string   `json:"objectClassName"`
	Status          []string `json:"status"`
	Events          []struct {
		Action string `json:"eventAction"`
		Date   string `json:"eventDate"`
	} `json:"events"`
}

func parseDomain(body []byte, res *Result) error {
	var d domainResponse
	if err := json.Unmarshal(body, &d); err != nil {
		return fmt.Errorf("invalid RDAP JSON: %v", err)
	}
	if d.ObjectClassName != "" && !strings.EqualFold(d.ObjectClassName, "domain") {
		return fmt.Errorf("unexpected RDAP objectClassName %q", d.ObjectClassName)
	}
	res.Statuses = res.Statuses[:0]
	for _, s := range d.Status {
		res.Statuses = append(res.Statuses, eppStatus(s))
	}
	for _, e := range d.Events {
		if strings.EqualFold(e.Action, "expiration") {
			if t, ok := parseTime(e.Date); ok {
				res.Expiry = &t
			}
		}
	}
	return nil
}

// eppStatus converts RDAP status values ("client hold") to the familiar
// EPP spelling ("clientHold"). Values without spaces are kept as-is.
func eppStatus(s string) string {
	words := strings.Fields(strings.ToLower(s))
	if len(words) <= 1 {
		return strings.TrimSpace(s)
	}
	for i := 1; i < len(words); i++ {
		words[i] = strings.ToUpper(words[i][:1]) + words[i][1:]
	}
	return strings.Join(words, "")
}

func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = time.Until(t)
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	if d < 0 {
		d = 0
	}
	return d
}

func sameHost(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && strings.EqualFold(ua.Host, ub.Host)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
