package rdap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type bootstrapFile struct {
	Services [][][]string `json:"services"`
}

// serverFor returns the RDAP base URL for domain and whether it came from
// the bootstrap registry. The bootstrap file is fetched once per Client.
func (c *Client) serverFor(ctx context.Context, domain string) (string, bool) {
	c.bsMu.Lock()
	if !c.bsLoaded {
		m, err := c.fetchBootstrap(ctx)
		if err != nil && ctx.Err() != nil {
			// Interrupted: don't cache the failure.
			c.bsMu.Unlock()
			return "", false
		}
		c.bs, c.bsErr, c.bsLoaded = m, err, true
	}
	bs := c.bs
	c.bsMu.Unlock()

	// Longest matching suffix wins (entries may in theory be multi-label).
	labels := strings.Split(domain, ".")
	for i := 1; i < len(labels); i++ {
		if base, ok := bs[strings.Join(labels[i:], ".")]; ok {
			return base, true
		}
	}
	return "", false
}

func (c *Client) fetchBootstrap(ctx context.Context) (map[string]string, error) {
	var lastErr error
	for attempt := 1; attempt <= c.opts.MaxAttempts; attempt++ {
		m, retry, err := c.fetchBootstrapOnce(ctx)
		if err == nil {
			return m, nil
		}
		lastErr = err
		if !retry || attempt == c.opts.MaxAttempts {
			break
		}
		if err := sleep(ctx, c.opts.Backoff<<(attempt-1)); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("loading RDAP bootstrap: %w", lastErr)
}

func (c *Client) fetchBootstrapOnce(ctx context.Context) (map[string]string, bool, error) {
	actx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodGet, c.opts.BootstrapURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode == 429 || resp.StatusCode >= 500, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var f bootstrapFile
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&f); err != nil {
		return nil, true, fmt.Errorf("invalid bootstrap JSON: %w", err)
	}
	m := map[string]string{}
	for _, svc := range f.Services {
		if len(svc) < 2 || len(svc[1]) == 0 {
			continue
		}
		base := pickURL(svc[1])
		for _, tld := range svc[0] {
			m[strings.ToLower(strings.TrimSuffix(tld, "."))] = base
		}
	}
	if len(m) == 0 {
		return nil, false, fmt.Errorf("bootstrap file contains no services")
	}
	return m, false, nil
}

// pickURL prefers an https base URL, as recommended by RFC 9224.
func pickURL(urls []string) string {
	for _, u := range urls {
		if strings.HasPrefix(strings.ToLower(u), "https://") {
			return u
		}
	}
	return urls[0]
}
