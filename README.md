# soiree

A shared budget and task planner for a group event, in a single small
container.

Planning a wedding, a milestone birthday or a reunion usually collapses into
one spreadsheet that one person owns and everyone else guesses at. soiree keeps
the same numbers, but adds the part a spreadsheet is bad at: who agreed to
cover which line, what is actually paid, and what is still open — visible to
everyone at once.

- **Budget** — line items with unit price, quantity, what has been paid, and
  what is still outstanding, against an agreed ceiling with an inflation buffer.
- **Who's covering what** — attribute any line to one person or split it
  between several, and see each person's total.
- **Tasks** — owner, due date, status, filtered to what is still open.
- **Any currency** — primary currency plus an optional second readout, so people
  in different countries can each see a number that means something to them.

Configured entirely by environment variables. No build step, no third-party
requests, and nothing to run alongside it yet — the database layer exists but
is not wired in, so today it is a single binary and a browser.

```bash
docker run --rm -p 8080:8080 \
  -e SOIREE_EVENT_NAME="Ada's Retirement" \
  -e SOIREE_EVENT_TAGLINE="Dinner and speeches" \
  -e SOIREE_EVENT_DATE="2027-06-12T00:00:00Z" \
  -e SOIREE_CURRENCY=SEK \
  -e SOIREE_DEMO_DATA=true \
  ghcr.io/yornik/soiree:latest
```

Then open <http://localhost:8080>.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `SOIREE_EVENT_NAME` | `A Celebration` | Page title and hero heading |
| `SOIREE_EVENT_TAGLINE` | *(empty)* | Subtitle under the heading |
| `SOIREE_EVENT_DATE` | *(empty)* | RFC3339 **with a timezone**, e.g. `2027-06-12T00:00:00Z`. Drives the countdown. Omit for no countdown. |
| `SOIREE_CURRENCY` | `EUR` | Primary currency, ISO 4217 |
| `SOIREE_LOCALE` | `en-US` | Number and date formatting |
| `SOIREE_SECONDARY_CURRENCY` | *(unset)* | Optional second readout. Unset hides the column and the rate field. |
| `SOIREE_SECONDARY_LOCALE` | *primary locale* | Formatting for the second currency |
| `SOIREE_BUDGET_CEILING` | `0` | Starting spending ceiling |
| `SOIREE_DEMO_DATA` | `false` | Seed obviously-fake sample data |
| `SOIREE_LISTEN_ADDR` | `:8080` | Bind address |

A date without a timezone is rejected at startup rather than accepted. It is a
real bug, not pedantry: `2027-06-12T00:00:00` is interpreted in the *viewer's*
timezone, so the countdown silently reads a day differently depending on where
someone is sitting — which is precisely the situation this app is built for.

## Design notes

The people using this are spread across the world; the server is in one place.
There is no CDN in front of it. Everything below follows from that.

**No third-party requests, ever.** The display font is self-hosted and is the
only font downloaded. Pulling two fonts from an external CDN costs two extra
DNS + TCP + TLS handshakes before first paint — roughly a second on a 300 ms
connection, which is more than the entire rest of the page. Body text uses the
system UI stack, so it paints immediately with no font swap.

**The service worker is the CDN.** After the first visit the app shell is served
from the local cache, so load time stops depending on distance. It also means
the planner keeps working with no connection.

**Everything is prepared at startup.** Assets are hashed, pre-compressed with
both gzip and brotli, and held in memory. No request compresses anything or
touches a disk. Hashed URLs are served `immutable` with a one-year lifetime;
only the HTML shell is revalidated, which is what makes a deploy land.

Measured transfer, brotli:

| | |
|---|---|
| HTML shell | 1.7 kB |
| Stylesheet | 2.8 kB |
| Application | 8.4 kB |
| Display font | 33 kB (`font-display: swap`, does not block paint) |
| **First paint** | **~4.6 kB** |

**Writes are debounced.** Edits apply to local state instantly and persist
500 ms later, flushed on page hide. That keeps typing smooth now, and means the
write path is already async-shaped for the API that replaces it.

## Status

Usable today, with one significant limitation: **the running app still keeps
state in the browser's `localStorage`**, so it is per-browser and not yet shared
between people. Use Export / Import JSON to move data around in the meantime.

Shared state is the point of the project. The database layer exists —
`migrations/`, `internal/migrate` and `internal/store` are in the tree and
tested against a real PostgreSQL — but the binary does not read `DATABASE_URL`
yet, so nothing is wired through to it.

1. ~~Configurable, self-hostable single binary~~ — done
2. ~~Schema and store layer.~~ Migrations applied at startup under an advisory
   lock; per-row revisions so concurrent edits are detected rather than
   silently overwritten — done, not yet wired in
3. REST API over that store, and the browser's `Store` becoming async
4. Accounts with roles (admin / editor / viewer). An admin creates each account
   from an email address and a role; the person receives a single-use link to
   set their own password. Passwords are stored as Argon2id hashes with a
   per-password salt — never encrypted, never emailed.
5. Live sync over SSE, with per-field writes and conflict detection

Browser-side persistence goes through a single `Store` object in
`web/src/app.js` — that is the seam the API replaces. See
[docs/architecture.md](docs/architecture.md).

## Development

The Go compiler is enough to build and run. The frontend is embedded and
processed at startup, so there is no asset build and no Node toolchain.

```bash
go test -short ./...           # no Docker needed
go run ./cmd/soiree            # http://localhost:8080
SOIREE_DEMO_DATA=true go run ./cmd/soiree
```

The full suite additionally exercises the store and migrations against a real
PostgreSQL via testcontainers, so it needs Docker and will pull
`postgres:18-alpine`:

```bash
go test ./...
```

`-short` skips exactly those tests, which is why it is the default suggestion
above.

For work that needs a database, `compose.yaml` brings up a throwaway Postgres
alongside the app:

```bash
docker compose up --build      # http://localhost:8080
docker compose down            # leaves nothing behind
```

Its data directory is a tmpfs, so every run starts from the migrations instead
of from whatever a previous branch left behind. It matches the major version of
the production cluster, and it runs with durability off because the data is
disposable — that is a development-only setting.

Layout:

```
cmd/soiree/          entrypoint
cmd/soiree-import/   spreadsheet importer CLI
internal/config/     environment parsing and validation
internal/httpd/      asset pipeline, HTTP handlers, metrics
internal/migrate/    migration runner, advisory-locked
internal/store/      typed data access (pgx)
internal/sheetimport/ .ods / .csv reader
migrations/          numbered SQL, embedded and append-only
web/src/             frontend sources, embedded via //go:embed
```

### Importing a spreadsheet

`soiree-import` converts a planning spreadsheet into the JSON the app's
**Import data** button accepts. It takes an explicit column mapping rather than
guessing, and reports every row it skipped and why:

```bash
go run ./cmd/soiree-import -list-sheets plan.ods
go run ./cmd/soiree-import -detect plan.ods > mapping.json   # proposes only
go run ./cmd/soiree-import -mapping mapping.json -o plan.json plan.ods
```

Real planning spreadsheets are written for people, not parsers — two unrelated
tables stacked in one sheet, headers repeating mid-data, `"22,500,000"` as text,
totals inline with the rows they total, and sub-items marked with a leading
dash. The importer handles those and refuses loudly rather than guessing when
it cannot.

### Regenerating the font subset

The shipped `web/src/fonts/fraunces-display.woff2` is [Fraunces][fraunces]
(SIL OFL 1.1), reduced from 67 kB to 33 kB by pinning the optical-size axis and
capping weight to the range actually used:

```bash
python3 -m venv /tmp/fontenv && /tmp/fontenv/bin/pip install fonttools brotli
/tmp/fontenv/bin/python -c "
from fontTools.ttLib import TTFont
from fontTools.varLib import instancer
f = TTFont('fraunces-latin-var.woff2')
inst = instancer.instantiateVariableFont(f, {'opsz': 72, 'wght': (400, 700)})
inst.flavor = 'woff2'
inst.save('web/src/fonts/fraunces-display.woff2')
"
```

## Contributing and security

How to build and test it, what CI enforces, and the two house rules that are
not obvious from the code: [CONTRIBUTING.md](CONTRIBUTING.md). What is expected
of everyone taking part: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

Security flaws go to the repository's Security tab —
[Report a vulnerability](https://github.com/Yornik/soiree/security/advisories/new)
— and not into a public issue. [SECURITY.md](SECURITY.md) covers what is in
scope and what to expect. To check a release you already have, see
[docs/verifying-releases.md](docs/verifying-releases.md).

## License

MIT — see [LICENSE](LICENSE). The bundled Fraunces font is licensed separately
under the [SIL Open Font License 1.1][ofl].

[fraunces]: https://github.com/undercasetype/Fraunces
[ofl]: https://openfontlicense.org/
