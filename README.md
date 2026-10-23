# domping

`domping` checks whether domain names are actually alive by combining
**RDAP** (is it registered?) with **DNS** (does it resolve?) and cross-checking
the two.

## Install / build

```sh
make build          # produces bin/domping
go install github.com/sbroekhoven/domping@latest
```

## Usage

```sh
domping example.com
domping example.com example.org -json
domping -file list.txt
domping -file list.txt -out results.txt
domping -file list.txt -inactive-only -out dead.txt
cat list.txt | domping -file -
```

Flags may appear before or after domain arguments.

| Flag | Default | Description |
|------|---------|-------------|
| `-file path` | | Read domains from a file, one per line (`-` = stdin) |
| `-out path` | | Also write results to a file |
| `-inactive-only` | off | Report only inactive domains (see below) |
| `-json` | off | One JSON object per domain (JSON Lines) |
| `-no-color` | off | Disable color (color is only used when stdout is a TTY; `NO_COLOR` is honored too) |
| `-resolver host:port` | `1.1.1.1:53` | Primary DNS resolver |
| `-resolver2 host:port` | `8.8.8.8:53` | Secondary resolver, consulted on NXDOMAIN / SERVFAIL / timeout; empty disables |
| `-concurrency n` | `10` | Domains checked in parallel |
| `-timeout d` | `10s` | Timeout per RDAP request and per DNS query |
| `-rdap-interval d` | `200ms` | Minimum spacing between requests to the same RDAP server |
| `-config path` | `~/.config/domping/config.yml` | Config file (see [config.yml](config.yml)) |
| `-version` | | Print version |

### Input

- Blank lines and lines starting with `#` are ignored; whitespace is trimmed,
  names are lowercased, trailing dots stripped and duplicates removed.
- IDNs are converted to punycode (`bücher.de` → `xn--bcher-kva.de`).
- Subdomains are reduced to the registrable domain using the ICANN section of
  the Public Suffix List (`www.example.co.uk` → `example.co.uk`). The original
  input is shown in the notes.
- Invalid input (URLs, public suffixes such as `co.uk`, bad characters) is
  rejected with the line number; nothing is checked and the exit code is 2.

### Configuration

Settings can be stored in a YAML file. `domping` loads
`$XDG_CONFIG_HOME/domping/config.yml` (usually `~/.config/domping/config.yml`)
if it exists, or the file passed with `-config`. Flags override the file,
which overrides the defaults. See [config.yml](config.yml) for all keys.

```sh
mkdir -p ~/.config/domping && cp config.yml ~/.config/domping/
```

## How it works

**RDAP.** The RDAP server for the TLD is looked up in the IANA bootstrap file
(`https://data.iana.org/rdap/dns.json`, fetched once per run). TLDs not listed
there go to `https://rdap.org/domain/<domain>`.

- `200` → registered; status values and the expiration date are parsed.
- `404` → not registered. Exception: when rdap.org itself answers 404 without
  redirecting, the TLD has no RDAP service at all (e.g. `.de`) and the result
  is *unknown*, not *not registered*.
- `429`, `5xx`, timeouts → retried with exponential backoff (honoring
  `Retry-After`, max 3 attempts), then *unknown*.
- Requests are rate-limited per RDAP host; a 429 slows down all workers for
  that host.

**DNS.** `NS`, `A` and `AAAA` are queried at the primary resolver. Each
outcome is classified as *records*, *NODATA* (NOERROR, empty), *NXDOMAIN* or
*error* (SERVFAIL, REFUSED, timeout). If the primary says NXDOMAIN or fails,
the secondary resolver is asked too and the more positive answer wins, so one
flaky or stale resolver cannot produce a false "dead".

`github.com/miekg/dns` is used rather than Go's `net.Resolver`, because the
standard library cannot reliably distinguish NXDOMAIN, NODATA and SERVFAIL and
cannot portably query a specific resolver.

## Verdicts

| Verdict | RDAP | DNS | Inactive? |
|---------|------|-----|-----------|
| `ACTIVE` | registered | NS, A or AAAA found | no |
| `REGISTERED_NO_DNS` | registered | NODATA or NXDOMAIN, **or** status `clientHold`/`serverHold` | **yes** |
| `UNREGISTERED` | 404 | NXDOMAIN | **yes** |
| `INCONSISTENT` | 404 | records or NODATA | no (review manually) |
| `UNKNOWN` | error/timeout/no RDAP | anything, or any RDAP + DNS error | no |

Statuses such as `pendingDelete`, `redemptionPeriod`, `pendingRestore`,
`clientHold`, `serverHold` and `inactive` are always listed in the notes.
For `INCONSISTENT` results both raw RDAP and DNS results are printed below
the line so you can review them.

## Output

```
DOMAIN                  VERDICT            RDAP            DNS        EXPIRES     NOTES
example.com             ACTIVE             registered      NS,A,AAAA  2027-08-13
blogspot.com            ACTIVE             registered      NS,A,AAAA  2027-07-31  input: foo.blogspot.com
xn--bcher-kva.de        UNKNOWN            no-rdap         NS,A,AAAA  -           input: bücher.de; rdap: no RDAP service available for this TLD
not-a-real-domain.com   UNREGISTERED       not-registered  NXDOMAIN   -
checked 4/4: 2 active, 0 registered_no_dns, 1 unregistered, 0 inconsistent, 1 unknown
```

Results are printed in input order as soon as they are available. The final
summary line goes to stderr.

- `-out file` without `-inactive-only`: the file contains the same lines as
  the console (or the same JSON lines with `-json`), without color codes.
- `-inactive-only`: only `UNREGISTERED` and `REGISTERED_NO_DNS` domains are
  reported on stdout, and the `-out` file contains **just the domain names**,
  one per line, so it can be fed back with `-file`. `UNKNOWN` and
  `INCONSISTENT` domains are written to stderr as warnings so they are not
  lost.

```sh
domping -file list.txt -inactive-only -out dead.txt 2> review.txt
domping -file dead.txt            # re-check later
```

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | All checked domains are active |
| 1 | At least one inactive domain found |
| 2 | Usage or input error |
| 3 | Only unknown/inconsistent results, nothing definitively inactive |
| 130 | Interrupted (Ctrl+C); results so far are printed |

## Development

```sh
make test        # unit tests (RACE=1 make test for the race detector)
make lint        # gofmt, go vet, staticcheck if installed
make build
```

Tests need no network access: RDAP is mocked with `httptest` and DNS with an
in-process `miekg/dns` server.
