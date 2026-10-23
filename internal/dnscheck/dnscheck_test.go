package dnscheck

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// zone describes how a fake resolver answers for one name.
type zone struct {
	rcode int
	types map[uint16]int // record count per type
	drop  bool           // never answer (forces a timeout)
}

// startServer runs an in-process DNS server on 127.0.0.1 and returns its address.
func startServer(t *testing.T, zones map[string]zone) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	srv := &dns.Server{
		PacketConn:        pc,
		NotifyStartedFunc: func() { close(started) },
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
			q := r.Question[0]
			z, ok := zones[q.Name]
			if !ok {
				z = zone{rcode: dns.RcodeNameError}
			}
			if z.drop {
				return
			}
			m := new(dns.Msg)
			m.SetRcode(r, z.rcode)
			for i := 0; i < z.types[q.Qtype]; i++ {
				hdr := dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: 60}
				switch q.Qtype {
				case dns.TypeNS:
					m.Answer = append(m.Answer, &dns.NS{Hdr: hdr, Ns: "ns.example.net."})
				case dns.TypeA:
					m.Answer = append(m.Answer, &dns.A{Hdr: hdr, A: net.IPv4(192, 0, 2, byte(i+1))})
				case dns.TypeAAAA:
					m.Answer = append(m.Answer, &dns.AAAA{Hdr: hdr, AAAA: net.ParseIP("2001:db8::1")})
				}
			}
			_ = w.WriteMsg(m)
		}),
	}
	go func() { _ = srv.ActivateAndServe() }()
	<-started
	t.Cleanup(func() { _ = srv.Shutdown() })
	return pc.LocalAddr().String()
}

var testZones = map[string]zone{
	"full.test.":     {rcode: dns.RcodeSuccess, types: map[uint16]int{dns.TypeNS: 2, dns.TypeA: 1, dns.TypeAAAA: 1}},
	"nsonly.test.":   {rcode: dns.RcodeSuccess, types: map[uint16]int{dns.TypeNS: 2}},
	"aonly.test.":    {rcode: dns.RcodeSuccess, types: map[uint16]int{dns.TypeA: 1}},
	"nodata.test.":   {rcode: dns.RcodeSuccess},
	"servfail.test.": {rcode: dns.RcodeServerFailure},
	"refused.test.":  {rcode: dns.RcodeRefused},
	"timeout.test.":  {drop: true},
	// nxdomain.test. is absent, so it gets NXDOMAIN.
}

func TestCheckSingleResolver(t *testing.T) {
	addr := startServer(t, testZones)
	c := &Checker{Primary: addr, Timeout: 200 * time.Millisecond}

	tests := []struct {
		domain    string
		want      Outcome
		wantShort string
	}{
		{"full.test", Records, "NS,A,AAAA"},
		{"nsonly.test", Records, "NS"},
		{"aonly.test", Records, "A"},
		{"nodata.test", NoData, "NODATA"},
		{"nxdomain.test", NXDomain, "NXDOMAIN"},
		{"servfail.test", Error, "SERVFAIL"},
		{"refused.test", Error, "REFUSED"},
		{"timeout.test", Error, "TIMEOUT"},
	}
	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			res := c.Check(context.Background(), tt.domain)
			if res.Outcome != tt.want {
				t.Errorf("outcome = %s, want %s (raw: %s)", res.Outcome, tt.want, res.Raw())
			}
			if got := res.Short(); got != tt.wantShort {
				t.Errorf("Short() = %q, want %q", got, tt.wantShort)
			}
			if res.Secondary != nil {
				t.Error("secondary consulted although none configured")
			}
		})
	}
}

func TestCheckSecondaryResolver(t *testing.T) {
	// The primary is broken or stale for some names; the secondary is correct.
	primary := startServer(t, map[string]zone{
		"full.test.":     testZones["full.test."],
		"servfail.test.": {rcode: dns.RcodeServerFailure},
		"timeout.test.":  {drop: true},
		"nodata.test.":   {rcode: dns.RcodeSuccess},
		// stale.test. and gone.test. are NXDOMAIN at the primary.
	})
	secondary := startServer(t, map[string]zone{
		"full.test.":     testZones["full.test."],
		"servfail.test.": testZones["full.test."],
		"timeout.test.":  {rcode: dns.RcodeSuccess},
		"stale.test.":    testZones["nsonly.test."],
		"nodata.test.":   testZones["full.test."],
	})
	c := &Checker{Primary: primary, Secondary: secondary, Timeout: 200 * time.Millisecond}

	tests := []struct {
		domain        string
		want          Outcome
		wantSecondary bool
	}{
		{"full.test", Records, false},
		{"nodata.test", NoData, false}, // NODATA is not retried at the secondary
		{"servfail.test", Records, true},
		{"timeout.test", NoData, true},
		{"stale.test", Records, true},
		{"gone.test", NXDomain, true},
	}
	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			res := c.Check(context.Background(), tt.domain)
			if res.Outcome != tt.want {
				t.Errorf("outcome = %s, want %s (raw: %s)", res.Outcome, tt.want, res.Raw())
			}
			if (res.Secondary != nil) != tt.wantSecondary {
				t.Errorf("secondary consulted = %v, want %v", res.Secondary != nil, tt.wantSecondary)
			}
		})
	}
}

func TestCheckSecondaryUnreachableKeepsPrimaryNXDOMAIN(t *testing.T) {
	primary := startServer(t, nil)
	dead := startServer(t, map[string]zone{"gone.test.": {drop: true}})
	c := &Checker{Primary: primary, Secondary: dead, Timeout: 100 * time.Millisecond}
	res := c.Check(context.Background(), "gone.test")
	if res.Outcome != NXDomain {
		t.Errorf("outcome = %s, want nxdomain", res.Outcome)
	}
}

func TestCombine(t *testing.T) {
	q := func(rcode string, count int, err string) Query { return Query{Rcode: rcode, Count: count, Err: err} }
	tests := []struct {
		name string
		qs   []Query
		want Outcome
	}{
		{"records win over errors", []Query{q("NOERROR", 1, ""), q("", 0, "timeout"), q("SERVFAIL", 0, "")}, Records},
		{"error beats nxdomain", []Query{q("NXDOMAIN", 0, ""), q("", 0, "timeout"), q("NXDOMAIN", 0, "")}, Error},
		{"all nxdomain", []Query{q("NXDOMAIN", 0, ""), q("NXDOMAIN", 0, ""), q("NXDOMAIN", 0, "")}, NXDomain},
		{"all nodata", []Query{q("NOERROR", 0, ""), q("NOERROR", 0, ""), q("NOERROR", 0, "")}, NoData},
		{"servfail", []Query{q("SERVFAIL", 0, ""), q("SERVFAIL", 0, ""), q("SERVFAIL", 0, "")}, Error},
	}
	for _, tt := range tests {
		if got := combine(tt.qs); got != tt.want {
			t.Errorf("%s: combine = %s, want %s", tt.name, got, tt.want)
		}
	}
}
