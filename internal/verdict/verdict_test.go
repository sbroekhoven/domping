package verdict

import (
	"strings"
	"testing"

	"github.com/sbroekhoven/domping/internal/dnscheck"
	"github.com/sbroekhoven/domping/internal/rdap"
)

func dnsResult(o dnscheck.Outcome) dnscheck.Result {
	return dnscheck.Result{Outcome: o, Primary: dnscheck.ResolverResult{Resolver: "1.1.1.1:53", Outcome: o}}
}

func TestDecide(t *testing.T) {
	registered := rdap.Result{State: rdap.Registered, HTTPStatus: 200}
	withStatus := func(s ...string) rdap.Result {
		r := registered
		r.Statuses = s
		return r
	}
	notFound := rdap.Result{State: rdap.NotRegistered, HTTPStatus: 404}
	unknown := rdap.Result{State: rdap.Unknown, HTTPStatus: 503, Err: "HTTP 503"}
	rateLimited := rdap.Result{State: rdap.Unknown, HTTPStatus: 429, Err: "HTTP 429"}
	noRDAP := rdap.Result{State: rdap.NoService, HTTPStatus: 404, Err: "no RDAP service available for this TLD"}

	tests := []struct {
		name      string
		rdap      rdap.Result
		dns       dnscheck.Outcome
		want      Verdict
		wantNotes []string // substrings expected in the notes
	}{
		{"registered with records", registered, dnscheck.Records, Active, nil},
		{"registered nodata", registered, dnscheck.NoData, RegisteredNoDNS, nil},
		{"registered nxdomain", registered, dnscheck.NXDomain, RegisteredNoDNS, nil},
		{"registered dns error", registered, dnscheck.Error, Unknown, []string{"dns:"}},
		{"pendingDelete no dns", withStatus("pendingDelete"), dnscheck.NXDomain, RegisteredNoDNS, []string{"pendingDelete"}},
		{"redemptionPeriod no dns", withStatus("redemptionPeriod", "serverHold"), dnscheck.NXDomain, RegisteredNoDNS, []string{"redemptionPeriod", "serverHold"}},
		{"clientHold but dns answers", withStatus("clientHold"), dnscheck.Records, RegisteredNoDNS, []string{"clientHold", "on hold"}},
		{"serverHold dns error", withStatus("serverHold"), dnscheck.Error, RegisteredNoDNS, []string{"serverHold"}},
		{"hold status case-insensitive", withStatus("ClientHold"), dnscheck.Records, RegisteredNoDNS, nil},
		{"pendingDelete still resolving", withStatus("pendingDelete"), dnscheck.Records, Active, []string{"pendingDelete"}},
		{"not found nxdomain", notFound, dnscheck.NXDomain, Unregistered, nil},
		{"not found but resolves", notFound, dnscheck.Records, Inconsistent, []string{"RDAP 404"}},
		{"not found but nodata", notFound, dnscheck.NoData, Inconsistent, []string{"RDAP 404"}},
		{"not found dns error", notFound, dnscheck.Error, Unknown, nil},
		{"rdap unknown records", unknown, dnscheck.Records, Unknown, []string{"rdap: HTTP 503"}},
		{"rdap unknown nxdomain", unknown, dnscheck.NXDomain, Unknown, nil},
		{"both unknown", unknown, dnscheck.Error, Unknown, []string{"rdap:", "dns:"}},
		{"rate limited with records", rateLimited, dnscheck.Records, Active, []string{"HTTP 429"}},
		{"rate limited nodata", rateLimited, dnscheck.NoData, Unknown, []string{"rdap: HTTP 429"}},
		{"rate limited nxdomain", rateLimited, dnscheck.NXDomain, Unknown, []string{"rdap: HTTP 429"}},
		{"rate limited dns error", rateLimited, dnscheck.Error, Unknown, []string{"rdap: HTTP 429", "dns:"}},
		{"no rdap service with records", noRDAP, dnscheck.Records, Active, []string{"DNS only"}},
		{"no rdap service nodata", noRDAP, dnscheck.NoData, Unknown, []string{"NODATA"}},
		{"no rdap service nxdomain", noRDAP, dnscheck.NXDomain, Unregistered, []string{"DNS NXDOMAIN only"}},
		{"no rdap service dns error", noRDAP, dnscheck.Error, Unknown, []string{"dns:"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, notes := Decide(tt.rdap, dnsResult(tt.dns))
			if got != tt.want {
				t.Errorf("verdict = %s, want %s (notes: %v)", got, tt.want, notes)
			}
			joined := strings.Join(notes, "; ")
			for _, n := range tt.wantNotes {
				if !strings.Contains(joined, n) {
					t.Errorf("notes %q missing %q", joined, n)
				}
			}
		})
	}
}

func TestDecideNotesResolverDisagreement(t *testing.T) {
	d := dnscheck.Result{
		Outcome:   dnscheck.Records,
		Primary:   dnscheck.ResolverResult{Resolver: "10.0.0.1:53", Outcome: dnscheck.Error},
		Secondary: &dnscheck.ResolverResult{Resolver: "8.8.8.8:53", Outcome: dnscheck.Records},
	}
	v, notes := Decide(rdap.Result{State: rdap.Registered}, d)
	if v != Active {
		t.Fatalf("verdict = %s, want ACTIVE", v)
	}
	if !strings.Contains(strings.Join(notes, ";"), "10.0.0.1:53 said error") {
		t.Errorf("notes %v do not mention resolver disagreement", notes)
	}
}

func TestInactive(t *testing.T) {
	tests := map[Verdict]bool{
		Active:          false,
		RegisteredNoDNS: true,
		Unregistered:    true,
		Inconsistent:    false,
		Unknown:         false,
		PublicSuffix:    true,
	}
	for v, want := range tests {
		if got := v.Inactive(); got != want {
			t.Errorf("%s.Inactive() = %v, want %v", v, got, want)
		}
	}
}
