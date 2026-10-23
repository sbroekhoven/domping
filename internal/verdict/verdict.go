// Package verdict cross-checks RDAP and DNS results into a single verdict.
package verdict

import (
	"fmt"
	"strings"

	"github.com/sbroekhoven/domping/internal/dnscheck"
	"github.com/sbroekhoven/domping/internal/rdap"
)

// Verdict is the overall classification of a domain.
type Verdict string

const (
	Active          Verdict = "ACTIVE"
	RegisteredNoDNS Verdict = "REGISTERED_NO_DNS"
	Unregistered    Verdict = "UNREGISTERED"
	Inconsistent    Verdict = "INCONSISTENT"
	Unknown         Verdict = "UNKNOWN"
)

// Inactive reports whether v counts as inactive. UNKNOWN and INCONSISTENT
// are never inactive.
func (v Verdict) Inactive() bool {
	return v == Unregistered || v == RegisteredNoDNS
}

// holdStatuses take a domain out of the zone even if resolvers still
// return cached or glue data.
var holdStatuses = []string{"clientHold", "serverHold"}

// noteStatuses are worth calling out when present.
var noteStatuses = []string{"pendingDelete", "redemptionPeriod", "pendingRestore", "clientHold", "serverHold", "inactive"}

// Decide combines the RDAP and DNS results.
func Decide(r rdap.Result, d dnscheck.Result) (Verdict, []string) {
	var notes []string
	if s := flagged(r); len(s) > 0 {
		notes = append(notes, "status: "+strings.Join(s, ","))
	}

	var v Verdict
	switch r.State {
	case rdap.Registered:
		switch {
		case onHold(r):
			v = RegisteredNoDNS
			if d.Outcome == dnscheck.Records {
				notes = append(notes, "on hold, DNS still answers ("+d.Short()+")")
			}
		case d.Outcome == dnscheck.Records:
			v = Active
		case d.Outcome == dnscheck.NoData || d.Outcome == dnscheck.NXDomain:
			v = RegisteredNoDNS
		default:
			v = Unknown
		}
	case rdap.NotRegistered:
		switch d.Outcome {
		case dnscheck.NXDomain:
			v = Unregistered
		case dnscheck.Records, dnscheck.NoData:
			v = Inconsistent
			notes = append(notes, fmt.Sprintf("RDAP 404 but DNS %s", d.Short()))
		default:
			v = Unknown
		}
	case rdap.NoService:
		// Without RDAP the registration can't be confirmed or denied, so
		// only positive DNS evidence counts; never report it as inactive.
		if d.Outcome == dnscheck.Records {
			v = Active
			notes = append(notes, "no RDAP for this TLD, verdict based on DNS only")
		} else {
			v = Unknown
			notes = append(notes, "no RDAP for this TLD and DNS "+d.Short())
		}
	default:
		v = Unknown
	}

	if r.State == rdap.Unknown && r.Err != "" {
		notes = append(notes, "rdap: "+r.Err)
	}
	if d.Outcome == dnscheck.Error {
		notes = append(notes, "dns: "+d.Short()+" from "+resolvers(d))
	}
	if d.Secondary != nil && d.Secondary.Outcome != d.Primary.Outcome {
		notes = append(notes, fmt.Sprintf("dns: %s said %s, %s said %s",
			d.Primary.Resolver, d.Primary.Outcome, d.Secondary.Resolver, d.Secondary.Outcome))
	}
	return v, notes
}

func onHold(r rdap.Result) bool {
	for _, s := range holdStatuses {
		if r.HasStatus(s) {
			return true
		}
	}
	return false
}

func flagged(r rdap.Result) []string {
	var out []string
	for _, s := range noteStatuses {
		if r.HasStatus(s) {
			out = append(out, s)
		}
	}
	return out
}

func resolvers(d dnscheck.Result) string {
	if d.Secondary != nil {
		return d.Primary.Resolver + " and " + d.Secondary.Resolver
	}
	return d.Primary.Resolver
}
