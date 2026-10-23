// Package dnscheck queries NS, A and AAAA records for a domain and
// classifies the outcome (records, NODATA, NXDOMAIN or error).
//
// It uses github.com/miekg/dns rather than net.Resolver because the
// standard library folds NXDOMAIN, NOERROR/NODATA and SERVFAIL into
// generic "not found"/"temporary" errors and cannot portably be pointed at
// a specific resolver; the verdict depends on telling those apart.
package dnscheck

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// Outcome classifies a DNS check. Higher values are "more alive"; when two
// resolvers disagree the higher outcome wins, to avoid false negatives.
type Outcome int

const (
	Error    Outcome = iota // SERVFAIL, REFUSED, timeout, network error
	NXDomain                // name does not exist
	NoData                  // name exists, but no NS/A/AAAA
	Records                 // at least one NS, A or AAAA record
)

func (o Outcome) String() string {
	switch o {
	case NXDomain:
		return "nxdomain"
	case NoData:
		return "nodata"
	case Records:
		return "records"
	default:
		return "error"
	}
}

func (o Outcome) MarshalText() ([]byte, error) { return []byte(o.String()), nil }

// Query is the raw result of one question.
type Query struct {
	Type  string `json:"type"`
	Rcode string `json:"rcode,omitempty"`
	Count int    `json:"count"`
	Err   string `json:"error,omitempty"`
}

func (q Query) outcome() Outcome {
	switch {
	case q.Err != "":
		return Error
	case q.Count > 0:
		return Records
	case q.Rcode == "NXDOMAIN":
		return NXDomain
	case q.Rcode == "NOERROR":
		return NoData
	default:
		return Error
	}
}

// ResolverResult is the combined result from one resolver.
type ResolverResult struct {
	Resolver string  `json:"resolver"`
	Outcome  Outcome `json:"outcome"`
	Queries  []Query `json:"queries"`
}

// Result is the final DNS outcome for a domain.
type Result struct {
	Outcome   Outcome         `json:"outcome"`
	Primary   ResolverResult  `json:"primary"`
	Secondary *ResolverResult `json:"secondary,omitempty"`
}

// Found lists the record types that returned data, e.g. "NS,A".
func (r Result) Found() string {
	var types []string
	for _, rr := range []*ResolverResult{&r.Primary, r.Secondary} {
		if rr == nil || rr.Outcome != Records {
			continue
		}
		for _, q := range rr.Queries {
			if q.Count > 0 {
				types = append(types, q.Type)
			}
		}
		break
	}
	return strings.Join(types, ",")
}

// Short is a compact human-readable summary: "NS,A,AAAA", "NXDOMAIN",
// "NODATA", "SERVFAIL", "TIMEOUT" or "ERROR".
func (r Result) Short() string {
	switch r.Outcome {
	case Records:
		return r.Found()
	case NXDomain:
		return "NXDOMAIN"
	case NoData:
		return "NODATA"
	}
	last := r.Primary
	if r.Secondary != nil {
		last = *r.Secondary
	}
	for _, q := range last.Queries {
		if q.Rcode != "" && q.Rcode != "NOERROR" && q.Rcode != "NXDOMAIN" {
			return q.Rcode
		}
	}
	for _, q := range last.Queries {
		if strings.Contains(q.Err, "timeout") {
			return "TIMEOUT"
		}
	}
	return "ERROR"
}

// Raw describes every query, for reviewing inconsistent results.
func (r Result) Raw() string {
	parts := []string{r.Primary.raw()}
	if r.Secondary != nil {
		parts = append(parts, r.Secondary.raw())
	}
	return strings.Join(parts, " | ")
}

func (rr ResolverResult) raw() string {
	var b strings.Builder
	b.WriteString(rr.Resolver)
	b.WriteString(":")
	for _, q := range rr.Queries {
		b.WriteString(" ")
		b.WriteString(q.Type)
		b.WriteString("=")
		switch {
		case q.Err != "":
			b.WriteString("err(" + q.Err + ")")
		case q.Count > 0:
			b.WriteString(q.Rcode + "/" + strconv.Itoa(q.Count))
		default:
			b.WriteString(q.Rcode)
		}
	}
	return b.String()
}

// Checker runs DNS checks. It is safe for concurrent use.
type Checker struct {
	Primary   string        // host:port
	Secondary string        // host:port, consulted on NXDOMAIN or error; "" disables
	Timeout   time.Duration // per query
}

var qtypes = []uint16{dns.TypeNS, dns.TypeA, dns.TypeAAAA}

// Check queries domain at the primary resolver and, if the result is
// NXDOMAIN or an error, again at the secondary resolver.
func (c *Checker) Check(ctx context.Context, domain string) Result {
	res := Result{Primary: c.resolve(ctx, c.Primary, domain)}
	res.Outcome = res.Primary.Outcome
	if c.Secondary == "" || ctx.Err() != nil {
		return res
	}
	if res.Outcome == NXDomain || res.Outcome == Error {
		sec := c.resolve(ctx, c.Secondary, domain)
		res.Secondary = &sec
		if sec.Outcome > res.Outcome {
			res.Outcome = sec.Outcome
		}
	}
	return res
}

func (c *Checker) resolve(ctx context.Context, server, domain string) ResolverResult {
	rr := ResolverResult{Resolver: server, Queries: make([]Query, len(qtypes))}
	var wg sync.WaitGroup
	for i, t := range qtypes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr.Queries[i] = c.query(ctx, server, domain, t)
		}()
	}
	wg.Wait()
	rr.Outcome = combine(rr.Queries)
	return rr
}

// combine merges per-type outcomes: any data means Records; otherwise any
// error makes the result inconclusive; then NXDOMAIN; else NODATA.
func combine(qs []Query) Outcome {
	has := map[Outcome]bool{}
	for _, q := range qs {
		has[q.outcome()] = true
	}
	switch {
	case has[Records]:
		return Records
	case has[Error]:
		return Error
	case has[NXDomain]:
		return NXDomain
	default:
		return NoData
	}
}

func (c *Checker) query(ctx context.Context, server, domain string, qtype uint16) Query {
	q := Query{Type: dns.TypeToString[qtype]}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true
	m.SetEdns0(1232, false)

	cl := &dns.Client{Net: "udp", Timeout: timeout}
	resp, _, err := cl.ExchangeContext(qctx, m, server)
	if err == nil && resp.Truncated {
		cl.Net = "tcp"
		resp, _, err = cl.ExchangeContext(qctx, m, server)
	}
	if err != nil {
		q.Err = errString(ctx, err)
		return q
	}
	q.Rcode = dns.RcodeToString[resp.Rcode]
	for _, a := range resp.Answer {
		if a.Header().Rrtype == qtype {
			q.Count++
		}
	}
	return q
}

func errString(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return ctx.Err().Error()
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return err.Error()
}
