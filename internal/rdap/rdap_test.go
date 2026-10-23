package rdap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const registeredBody = `{
  "objectClassName": "domain",
  "ldhName": "example.test",
  "status": ["client transfer prohibited", "pending delete", "serverHold"],
  "events": [
    {"eventAction": "registration", "eventDate": "2001-01-01T00:00:00Z"},
    {"eventAction": "expiration", "eventDate": "2027-08-13T04:00:00.123Z"}
  ]
}`

// newTestEnv starts an RDAP server for the "test" TLD and a bootstrap
// server pointing at it. handler serves /domain/<name> requests.
func newTestEnv(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	bs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"services": [[["test", "multi.label"], [%q]]]}`, srv.URL+"/")
	}))
	t.Cleanup(bs.Close)
	c := New(Options{
		BootstrapURL: bs.URL,
		FallbackURL:  srv.URL + "/fallback/",
		Timeout:      2 * time.Second,
		Backoff:      time.Millisecond,
	})
	return c, srv
}

func TestLookupStatuses(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantState    State
		wantAttempts int
		wantErr      string
	}{
		{"registered", 200, registeredBody, Registered, 1, ""},
		{"not found", 404, `{"errorCode":404}`, NotRegistered, 1, ""},
		{"rate limited", 429, "", Unknown, 3, "HTTP 429"},
		{"server error", 503, "", Unknown, 3, "HTTP 503"},
		{"forbidden is not retried", 403, "", Unknown, 1, "HTTP 403"},
		{"bad json", 200, "{", Unknown, 1, "invalid RDAP JSON"},
		{"wrong object class", 200, `{"objectClassName":"entity"}`, Unknown, 1, "objectClassName"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			c, _ := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/domain/example.test" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			})
			res := c.Lookup(context.Background(), "example.test")
			if res.State != tt.wantState {
				t.Errorf("state = %s, want %s (err %q)", res.State, tt.wantState, res.Err)
			}
			if res.Attempts != tt.wantAttempts || int(calls.Load()) != tt.wantAttempts {
				t.Errorf("attempts = %d, calls = %d, want %d", res.Attempts, calls.Load(), tt.wantAttempts)
			}
			if !strings.Contains(res.Err, tt.wantErr) {
				t.Errorf("err = %q, want it to contain %q", res.Err, tt.wantErr)
			}
			if res.HTTPStatus != tt.status {
				t.Errorf("http status = %d, want %d", res.HTTPStatus, tt.status)
			}
		})
	}
}

func TestLookupParsesStatusAndExpiry(t *testing.T) {
	c, _ := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, registeredBody)
	})
	res := c.Lookup(context.Background(), "example.test")
	want := []string{"clientTransferProhibited", "pendingDelete", "serverHold"}
	if strings.Join(res.Statuses, ",") != strings.Join(want, ",") {
		t.Errorf("statuses = %v, want %v", res.Statuses, want)
	}
	if !res.HasStatus("serverhold") {
		t.Error("HasStatus should be case-insensitive")
	}
	if res.Expiry == nil || res.Expiry.Format("2006-01-02") != "2027-08-13" {
		t.Errorf("expiry = %v, want 2027-08-13", res.Expiry)
	}
}

func TestLookupRetryThenSuccess(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, registeredBody)
	})
	res := c.Lookup(context.Background(), "example.test")
	if res.State != Registered || res.Attempts != 3 || res.Err != "" {
		t.Errorf("got state=%s attempts=%d err=%q, want registered after 3 attempts", res.State, res.Attempts, res.Err)
	}
}

func TestLookupTimeout(t *testing.T) {
	c, _ := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	})
	c.opts.Timeout = 20 * time.Millisecond
	res := c.Lookup(context.Background(), "example.test")
	if res.State != Unknown || res.Attempts != 3 || !strings.Contains(res.Err, "timeout") {
		t.Errorf("got state=%s attempts=%d err=%q, want unknown timeout after 3 attempts", res.State, res.Attempts, res.Err)
	}
}

func TestLookupCanceled(t *testing.T) {
	c, _ := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c.opts.Backoff = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	res := c.Lookup(ctx, "example.test")
	if res.State != Unknown || time.Since(start) > 5*time.Second {
		t.Errorf("cancellation not honored: state=%s after %s", res.State, time.Since(start))
	}
}

func TestLookupFallback(t *testing.T) {
	tests := []struct {
		name      string
		domain    string
		handler   func(w http.ResponseWriter, r *http.Request)
		wantState State
		wantPath  string
	}{
		{
			name:   "multi-label bootstrap entry",
			domain: "example.multi.label",
			handler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, registeredBody)
			},
			wantState: Registered,
			wantPath:  "/domain/example.multi.label",
		},
		{
			name:   "TLD not in bootstrap uses fallback",
			domain: "example.other",
			handler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, registeredBody)
			},
			wantState: Registered,
			wantPath:  "/fallback/example.other",
		},
		{
			name:   "fallback 404 without redirect means no RDAP service",
			domain: "example.other",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantState: NoService,
			wantPath:  "/fallback/example.other",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			c, _ := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				tt.handler(w, r)
			})
			res := c.Lookup(context.Background(), tt.domain)
			if res.State != tt.wantState {
				t.Errorf("state = %s, want %s (err %q)", res.State, tt.wantState, res.Err)
			}
			if gotPath != tt.wantPath {
				t.Errorf("path = %s, want %s", gotPath, tt.wantPath)
			}
		})
	}
}

func TestFallbackRedirect404IsNotRegistered(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer registry.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, registry.URL+"/domain/example.other", http.StatusFound)
	}))
	defer redirector.Close()
	bs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"services":[[["test"],["http://unused.invalid/"]]]}`)
	}))
	defer bs.Close()

	c := New(Options{BootstrapURL: bs.URL, FallbackURL: redirector.URL + "/domain/", Backoff: time.Millisecond})
	res := c.Lookup(context.Background(), "example.other")
	if res.State != NotRegistered {
		t.Errorf("state = %s, want not-registered (err %q)", res.State, res.Err)
	}
}

func TestBootstrapFailureFallsBack(t *testing.T) {
	var bsCalls atomic.Int32
	bs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bsCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bs.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, registeredBody)
	}))
	defer srv.Close()

	c := New(Options{BootstrapURL: bs.URL, FallbackURL: srv.URL + "/fallback/", Backoff: time.Millisecond})
	for range 3 {
		if res := c.Lookup(context.Background(), "example.test"); res.State != Registered {
			t.Fatalf("state = %s, want registered via fallback (err %q)", res.State, res.Err)
		}
	}
	if c.BootstrapError() == nil {
		t.Error("BootstrapError() = nil, want error")
	}
	if n := bsCalls.Load(); n != 3 {
		t.Errorf("bootstrap fetched %d times, want 3 (one load with retries, then cached)", n)
	}
}

func TestBootstrapCached(t *testing.T) {
	var bsCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, registeredBody)
	}))
	defer srv.Close()
	bs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bsCalls.Add(1)
		fmt.Fprintf(w, `{"services":[[["test"],[%q]]]}`, srv.URL+"/")
	}))
	defer bs.Close()

	c := New(Options{BootstrapURL: bs.URL})
	for range 5 {
		c.Lookup(context.Background(), "example.test")
	}
	if n := bsCalls.Load(); n != 1 {
		t.Errorf("bootstrap fetched %d times, want 1", n)
	}
}

func TestHostLimiterSpacing(t *testing.T) {
	l := newHostLimiter(30 * time.Millisecond)
	start := time.Now()
	for range 4 {
		if err := l.Wait(context.Background(), "a"); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 85*time.Millisecond {
		t.Errorf("4 waits on one host took %s, want >= 90ms", el)
	}
	start = time.Now()
	if err := l.Wait(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 20*time.Millisecond {
		t.Errorf("other host was delayed by %s", el)
	}
}

func TestEPPStatus(t *testing.T) {
	tests := map[string]string{
		"client hold":                "clientHold",
		"redemption period":          "redemptionPeriod",
		"client transfer prohibited": "clientTransferProhibited",
		"serverHold":                 "serverHold",
		"active":                     "active",
	}
	for in, want := range tests {
		if got := eppStatus(in); got != want {
			t.Errorf("eppStatus(%q) = %q, want %q", in, got, want)
		}
	}
}
