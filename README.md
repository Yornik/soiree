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
- **Three interface languages** — English, Dutch and Indonesian. The page reads
  the visitor's own browser, falls back on the deployment's `SOIREE_LOCALE`, and
  has three flags at the top for anybody it guessed wrong about. A link can ask
  for one with `?lang=nl`. No framework; the strings are a table in the page.

Configured entirely by environment variables, and no third-party requests ever.
Give it a PostgreSQL DSN and the planner is shared: the browser reads and
writes the plan through the API, several people edit the same ledger, and the
server keeps the history. Leave the DSN out and the same image serves the
planner alone, keeping state in the browser — one copy, one device, no network
after the first probe. No build step either way.

The shared deployment is the one the project is for. To see it, use the
development stack, which is a throwaway Postgres alongside the app:

```bash
docker compose up --build      # http://localhost:8080
```

To see the planner on its own, with no database anywhere:

```bash
docker run --rm -p 8080:8080 \
  -e SOIREE_EVENT_NAME="Ada's Retirement" \
  -e SOIREE_EVENT_TAGLINE="Dinner and speeches" \
  -e SOIREE_EVENT_DATE="2027-06-12T00:00:00Z" \
  -e SOIREE_CURRENCY=SEK \
  -e SOIREE_DEMO_DATA=true \
  ghcr.io/yornik/soiree:latest
```

Then open <http://localhost:8080>. With no `DATABASE_URL` the `/api/v1` routes
are never registered at all, which is what makes this a supported way to run it
rather than a broken one.

Running it for real needs a little more than the above —
[docs/operating.md](docs/operating.md) covers the first admin, the optional
subsystems, and how to tell each one is working.

## Configuration

Everything is read from the environment at startup. Nothing event-specific
exists in the source, which is what lets one public image serve any event.

### The event

| Variable | Default | Purpose |
|---|---|---|
| `SOIREE_EVENT_NAME` | `A Celebration` | Page title and hero heading. Also the Relying Party display name shown in a passkey prompt. |
| `SOIREE_EVENT_TAGLINE` | *(empty)* | Subtitle under the heading |
| `SOIREE_EVENT_DATE` | *(empty)* | RFC3339 **with a timezone**, e.g. `2027-06-12T00:00:00Z`. Drives the countdown. Omit for no countdown. |
| `SOIREE_CURRENCY` | `EUR` | Primary currency, ISO 4217. Decides the minor-unit exponent the API speaks in. |
| `SOIREE_LOCALE` | `en-US` | Number and date formatting, and the *fallback* language when its primary subtag is one of `en`, `nl` or `id`: what the interface is in for a visitor whose browser asks for none of those, and what a mail is in when nobody chose. A visitor's own browser comes first, a flag they click comes before that, and `?lang=nl` on a link before everything. |
| `SOIREE_SECONDARY_CURRENCY` | *(unset)* | Optional second readout. Unset hides the column and the rate field. |
| `SOIREE_SECONDARY_LOCALE` | *primary locale* | Formatting for the second currency |
| `SOIREE_BUDGET_CEILING` | `0` | The ceiling a *fresh* browser starts with, in whole major units. Once a database is in play the stored `settings.ceiling` is authoritative and replaces it as soon as the plan arrives. |
| `SOIREE_DEMO_DATA` | `false` | Seed obviously-fake sample data in the browser |

### Serving

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | *(unset)* | PostgreSQL DSN. Unprefixed because it is the name every Postgres tool already uses, and because it is not part of the event's identity. Unset means no API, no accounts, no live sync and no reminders — the frontend alone. |
| `SOIREE_LISTEN_ADDR` | `:8080` | Bind address for the site |
| `SOIREE_METRICS_ADDR` | `:9090` | Bind address for the Prometheus exposition. A separate listener on purpose — a reverse proxy in front of the site usually has no path constraint, so `/metrics` on the main port would be world-readable. Must differ from the above, and the process refuses to start if the two strings are equal. |
| `SOIREE_BASE_URL` | *(unset)* | The origin this deployment is reached at, e.g. `https://soiree.example.test`. Mailed links and the passkey Relying Party are built from it and never from a request's `Host` header, which is attacker-supplied. Required when SMTP is configured. |
| `SOIREE_TRUST_PROXY_HEADERS` | `false` | Whether `X-Forwarded-For` may be believed. Off by default: with it on and no proxy in front, anyone can pick their own client address and the per-IP rate limits stop meaning anything. |
| `SOIREE_ALLOW_INDEXING` | `false` | Let search engines index the site. Off by default: a planner holds people's names against money they owe, and none of them chose to publish it. While off, `robots.txt` disallows everything **and** every response carries `X-Robots-Tag: noindex, nofollow` — the header matters because `robots.txt` only asks, and says nothing to a crawler that already has the URL from a link or a shared screenshot. |

### Accounts

| Variable | Default | Purpose |
|---|---|---|
| `SOIREE_BOOTSTRAP_ADMIN` | *(unset)* | Email address that becomes the first admin on an empty database. Consumed only while no admin exists, so it is safe to leave set. |
| `SOIREE_BOOTSTRAP_PASSWORD` | *(unset)* | Gives that first admin a password so they can log in without waiting for mail. At least 12 characters, and refused without `SOIREE_BOOTSTRAP_ADMIN`. Also consumed only while no admin exists. |
| `SOIREE_PASSKEYS_ENABLED` | *derived* | Whether to offer WebAuthn passkeys alongside the password. **Derived, not simply read**: on whenever `SOIREE_BASE_URL` is set, and setting this to `true` cannot turn it on without one — there would be no domain to scope a credential to. Set it to `false` to decline. A value that will not parse reads as off. |

### Mail

Mail carries set-password links and the deadline digest. The zero state is no
mail at all, which is supported; a *half*-configured relay is refused at
startup, because it looks configured and silently sends nothing.

| Variable | Default | Purpose |
|---|---|---|
| `SOIREE_SMTP_HOST` | *(unset)* | Relay hostname. Unset means no mail anywhere in the application. |
| `SOIREE_SMTP_PORT` | `465` | Implicit TLS. Read only when a host is set. |
| `SOIREE_SMTP_USER` | *(unset)* | Must be set together with the password, or neither |
| `SOIREE_SMTP_PASSWORD` | *(unset)* | See above |
| `SOIREE_SMTP_FROM` | *(unset)* | Sender address. Required once a host is set. |

### Reminders

A digest of approaching `lock_by` deadlines and task due dates. Off by default;
mail that starts sending itself because somebody deployed a new version is not
a feature. A malformed setting here is a startup failure rather than a warning,
because a digest that silently never arrives is the same outcome as having no
reminders at all.

| Variable | Default | Purpose |
|---|---|---|
| `SOIREE_REMINDER_ENABLED` | `false` | The master switch. Goes to every **active admin**, resolved at send time, so adding an admin adds a recipient without touching config. Nothing due means no mail. |
| `SOIREE_REMINDER_TO` | *(unset)* | Extra recipients beyond the admins — for a deployment with no accounts, or somebody who should read the digest without being given a login to the event's finances. Comma-, semicolon- or newline-separated; `Ada Lovelace <ada@example.test>` is accepted. |
| `SOIREE_REMINDER_SCHEDULE` | `weekly` | `daily`/`weekly`/`fortnightly`, or a Go duration. Minimum 24h — `lock_by` and `due` are calendar days, so two digests in one day carry the same words. |
| `SOIREE_REMINDER_WINDOW_DAYS` | `14` | How far ahead to look, in whole days |
| `SOIREE_REMINDER_TZ` | `UTC` | IANA zone deciding which calendar day "today" is |

### Web push

The digest's second channel: a sentence on a lock screen, with the detail
behind the click. Web Push has no API key and no account — the server signs
each request with a P-256 pair it generated itself, so the operator generates
one. See [docs/operating.md](docs/operating.md) for how, and for why rotating
the pair silently unsubscribes everybody.

| Variable | Default | Purpose |
|---|---|---|
| `SOIREE_VAPID_PUBLIC_KEY` | *(unset)* | The browser's half. Published into the page, and a subscription is made against it. |
| `SOIREE_VAPID_PRIVATE_KEY` | *(unset)* | The signing key. Never leaves the server and is deliberately absent from the page's config block. |
| `SOIREE_VAPID_SUBJECT` | *(unset)* | Where a push service complains to: an address or an `https` URL. `ada@example.test` and `mailto:ada@example.test` mean the same thing. |

All three or none. Push is off unless every one is present, because a push
service is entitled to refuse a request whose signature carries no subject, and
finding that out one notification at a time is worse than the feature being
visibly off. A partial set is a warning in the log, never a refusal to boot.

A date without a timezone is rejected at startup rather than accepted. It is a
real bug, not pedantry: `2027-06-12T00:00:00` is interpreted in the *viewer's*
timezone, so the countdown silently reads a day differently depending on where
someone is sitting — which is precisely the situation this app is built for.

## The API

[`api/openapi.yaml`](api/openapi.yaml) describes the whole HTTP surface, as
OpenAPI 3.1. Read it there, or render it with any viewer:

```bash
npx @redocly/cli preview-docs api/openapi.yaml
```

Three things in it are easy to get wrong and worth reading before writing a
client: money crosses the wire as a **decimal string in major units** and never
as a JSON number; every write carries the `revision` you read, and a stale one
comes back as 409 with the current row attached; and a `PATCH` body has three
states, where an omitted field is left alone and an explicit `null` clears it.

No Swagger UI is bundled. The strict CSP this app ships under forbids inline
script and style, which is what the split-asset build exists to satisfy —
vendoring a UI that needs both would mean weakening it.

The document is hand-written, so it is checked rather than trusted. `redocly
lint` in CI proves it is valid OpenAPI. `TestEveryDocumentedRouteExists` asks
the running server for every path and method it describes, so a renamed or
deleted route fails the build. `TestTheDocumentedErrorContractHolds` pins the
answers a client branches on — the codes behind 400, 401, 403 and 429.

That is narrower than "the document is correct", deliberately: nothing checks
the prose or the schemas against live responses. Writing the first version of
this specification produced two responses that were described from inference
rather than read from the source, and the tests above are what found them.

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

Measured transfer at 1.0.0, brotli:

| | |
|---|---|
| HTML shell | 4.2 kB (the three flags are inline SVG, so they cost no request) |
| Stylesheet | 8.8 kB |
| Planner script | 37 kB (`defer`, does not block paint) |
| Accounts script | 20 kB (`defer`, does not block paint; three languages) |
| Display font | 33 kB (`font-display: swap`, does not block paint) |
| **First paint** | **~13 kB** |

**Writes are debounced.** Edits apply to local state instantly and persist
500 ms later, flushed on page hide. That keeps typing smooth, and it is what
lets the network sit behind the same seam: the round trip is never on the
interaction path.

**The write is a difference, not a snapshot.** With a database configured, the
browser keeps a private copy of every row as the server last confirmed it and
sends only the fields that differ, carrying the revision they were read at. A
write somebody else got to first comes back `409` with the row as it now
stands, and the two are merged three ways against that copy: a field this
browser did not touch takes their value, a field it did keeps ours and goes
again. Nothing is silently overwritten in either direction.

## Status

Version 1.0.0 is a shared planner with a shared database behind it, accounts in
front of it, and a page that uses all of it.

What works end to end:

- **The planner, shared.** The browser reads `GET /api/v1/plan` on load and
  writes every subsequent edit through `/api/v1`. `localStorage` is still
  written first and synchronously in both modes, but with a database it is a
  cache that paints before the plan arrives, not the plan itself. Without a
  database it *is* the planner, and that deployment still works exactly as it
  did.
- **Signing in.** A sign-in screen, the set-password screen an invitation link
  lands on, an account screen for your own passkeys, and an accounts screen
  where an admin creates people and chooses their role, and the language of
  the invitation: that mail and the screen its link opens are written in it.
  It is a choice about one mail and is not stored. There is no self-service
  sign-up. **Passkeys** work alongside the password wherever
  `SOIREE_BASE_URL` gives them a domain to belong to.
- **Roles, enforced by the server.** Nobody reads the plan without a session; a
  viewer reads and writes nothing; an editor or an admin writes; only an admin
  reaches `/api/v1/users`. The guard wraps the whole `/api/v1` subtree rather
  than each route, so a route added later is guarded by being there. Every
  write records who made it, in `updatedBy` and in the append-only change
  history.
- **Live sync.** The page holds one `EventSource` on `GET /api/v1/events`. A
  second person's edit arrives on its own, and is merged three ways against the
  copy both sides started from, so nothing anybody is typing is overwritten.
- **A session that ends while the page is open** — a week idle, a disabled
  account — stops the page asking, keeps every edit in the browser, opens the
  sign-in screen with a line saying why, and sends those edits once the person
  is back. Signing back in is a merge, never this browser's copy winning.
- **Deadline reminders** by mail and by web push, with a control on the page
  that asks for permission and subscribes the device.
- **The API is described** in [`api/openapi.yaml`](api/openapi.yaml), and the
  description is tested against the running server.

What is not there, stated rather than left to be discovered:

- **The deadline digest is in English only.** Every screen is in three
  languages, and so are the invitation and the password-reset mail — in the
  language the admin chose for that mail, or the one the sign-in screen was
  being read in. The digest is one body sent to every admin at once, and is
  not.
- **Phases and the programme have an API and no interface.** Both are in the
  plan and in `/api/v1`; the page draws neither. For the same reason a budget
  row with a `parentId`, which only a client writing to the API directly can
  create, is drawn as an ordinary line and counted alongside its parent.
- **Subject export, erasure and the retention purge** are implemented and
  tested in `internal/store/privacy.go`, but nothing calls them: there is no
  route and no subcommand, so honouring a request today means writing Go or
  SQL.

Most of the rest of the original roadmap has landed, including the parts listed
as coming after a working demo: the audit trail, vulnerability disclosure, the
phone layout, three interface languages, the after-the-event archive, and
browser-level tests. The restore drill has not been done, and signature
verification in the cluster belongs to the deployment repository rather than
this one. The full list, with what each item means, is in
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

There is also a browser-level suite, which is opt-in and not part of
`go test ./...`. It starts the real binary three times — once plain, once with
a different locale and no event date, once with a throwaway Postgres behind it
— and drives them with Playwright:

```bash
cd e2e && npm install && npx playwright install --with-deps && npm test
```

The API specs skip themselves when Docker is unavailable, so the suite still
runs on a machine with no containers.

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
cmd/soiree/           entrypoint
cmd/soiree-import/    spreadsheet importer CLI
internal/auth/        Argon2id hashing, one-time token minting
internal/config/      environment parsing and validation
internal/httpd/       asset pipeline, HTTP handlers, accounts, SSE, metrics
internal/mailer/      SMTP transport
internal/migrate/     migration runner, advisory-locked
internal/pgtest/      throwaway Postgres for the tests
internal/push/        Web Push transport
internal/reminders/   the deadline digest and its scheduler
internal/sheetimport/ .ods / .csv reader
internal/store/       typed data access (pgx), history, LISTEN/NOTIFY
migrations/           numbered SQL, embedded and append-only, at 0011
web/src/              frontend sources, embedded via //go:embed
e2e/                  Playwright specs against the running binary
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
