# Architecture

One Go binary serves the whole application. The frontend is embedded with
`//go:embed` and processed at startup; there is no separate asset build and no
runtime disk access.

```
cmd/soiree/main.go      wiring, signals, graceful shutdown
internal/config/        environment parsing and validation
internal/httpd/
  assets.go             hashing, compression, content addressing
  server.go             routing, cache headers, conditional requests
  metrics.go            Prometheus collectors and request instrumentation
web/embed.go            //go:embed of web/src
web/src/                index.html, styles.css, app.js, sw.js, fonts/
```

## Request paths

| Path | Caching | Notes |
|---|---|---|
| `/` | `no-cache` + ETag | Go template. Carries the hashed asset URLs and the config data block, so it must revalidate for a deploy to take effect. |
| `/assets/<name>.<hash>.<ext>` | `immutable`, 1 year | Content-addressed. The URL changes whenever the bytes do. |
| `/sw.js` | `no-cache` + ETag | Rendered with the precache list. Never cached hard, or a broken worker would pin itself. |
| `/healthz` | `no-store` | Liveness. |
| `/readyz` | `no-store` | Readiness. |
| `/metrics` | `no-store` | Prometheus exposition. |

## Startup pipeline

Order matters, because assets reference each other by name:

1. **Leaf assets** — the font and favicon are hashed first.
2. **Stylesheet** — its `url('fraunces-display.woff2')` is rewritten to the
   font's hashed filename, *then* the stylesheet itself is hashed. Doing it in
   this order is what keeps the font reference from 404ing.
3. **Application script** — hashed.
4. **Manifest** — templated (it references the hashed favicon), then hashed.
5. **HTML shell** — templated with the config and the hashed asset URLs.
6. **Service worker** — templated with the shell's hash as a cache version, so
   a new build retires the previous cache automatically.

Every text asset is gzip- and brotli-compressed once at startup, and a
compressed variant is kept only when it is actually smaller. `woff2` is skipped
because it is already compressed.

## Configuration as the de-personalisation boundary

Nothing event-specific exists in the source. The event name, tagline, date,
currency and locale all arrive as `SOIREE_*` environment variables and are
marshalled into a `<script type="application/json">` data block in the page.

Two consequences worth stating explicitly:

- The same public image serves any event. A deployment's specifics live in its
  own deployment config, never in this repository.
- The block is `application/json`, not executable script, so a strict
  `script-src 'self'` CSP permits it. Inlining it also avoids a round trip that
  a separate `/api/v1/config` fetch would cost — which matters, because the
  round trip is the expensive part for most users.

`SOIREE_EVENT_DATE` is rejected unless it carries a timezone. A bare
`2027-06-12T00:00:00` is parsed in the *viewer's* timezone, so the countdown
reads differently depending on where someone is — the exact failure mode this
application is most exposed to.

## The `Store` seam

All persistence in `web/src/app.js` goes through one object:

```js
Store.read()        // -> state object, or null
Store.write(state)  // persist the whole state object
```

Nothing else touches `localStorage`. `save()` wraps `Store.write` and is
debounced by 500 ms, flushed on `pagehide` and on `visibilitychange`. That
matters for more than typing latency: because every one of the ~24 mutation
sites already goes through a deferred write, making `Store` asynchronous does
not require touching any of them.

### State shape

One plain JSON object. Money values are integers in the primary currency.

```
{
  ceiling, inflationPct, fxRate, splitEvenly,
  colWidths: [9 numbers], rowHeights: { itemId: px },
  sponsors:    [ { id, code, name } ],
  budgetItems: [ { id, item, unit, qty, paid, sponsors: [sponsorId], note } ],
  tasks:       [ { id, name, owner, due, status } ],
  notes:       [ { id, text } ]
}
```

`status` is one of `not-started` | `in-progress` | `done`. Line total is
`unit * qty`; outstanding is `total - paid`. A budget line with more than one
entry in `sponsors` is a shared cost, and `splitEvenly` decides whether the
breakdown divides it between them or reports it as a shared bucket.

## Latency strategy

The origin is in one place, users are not, and there is no CDN in front. That
constraint drives the design:

1. **Zero third-party origins.** Two fonts from an external CDN cost two extra
   DNS + TCP + TLS handshakes before first paint — about a second at 300 ms
   RTT, more than everything else combined. The display font is self-hosted and
   body text uses the system stack.
2. **The service worker is the CDN.** Repeat visits are served from local
   cache, so load time stops scaling with distance. The shell uses
   stale-while-revalidate: the cached copy paints immediately and the update
   lands on the next visit. Because asset URLs are content-addressed, an older
   shell still references assets that are still cached, so the pairing is never
   inconsistent.
3. **Local-first writes.** Edits apply to local state and paint immediately.
   The write is deferred. When the API lands, the round trip stays off the
   interaction path rather than being eliminated — which is the only option
   available, since the distance is real.
4. **Compression and cache headers in the binary.** No proxy configuration is
   required for either.

Measured, brotli: shell 1.7 kB, stylesheet 2.8 kB, application 8.4 kB, font
33 kB. First paint needs the shell and stylesheet only — about 4.6 kB.

### Deliberately excluded

- **HTTP/3.** Real benefit on lossy, high-RTT mobile links, but it needs QUIC
  over UDP end to end — an infrastructure change, not an application one.
- **Edge PoPs.** Adding a server geographically closer does not help while the
  proxy in front is a layer-4 TCP passthrough: TLS still terminates at the
  origin, so the client's handshake round-trips the full distance anyway. It
  would only pay if the edge terminated TLS and cached.

## Observability

The application is built to be operated, not just run.

| Endpoint | Purpose |
|---|---|
| `/healthz` | Liveness. Checks nothing downstream on purpose — a liveness probe that fails when the database is down converts an outage into a restart loop. |
| `/readyz` | Readiness. Identical to liveness while everything is in memory; this is where the database check goes once it lands. |
| `/metrics` | Prometheus exposition, on a private registry. |

Exported series:

- `soiree_http_requests_total{route,method,status}`
- `soiree_http_request_duration_seconds{route,method}` — buckets start at
  100 µs, because everything is served from memory and the default buckets are
  far too coarse to show anything here
- `soiree_http_requests_in_flight`
- `soiree_build_info{version,commit}` — stamped at link time, so a running pod
  can be tied back to a commit
- Standard Go runtime and process collectors

**`route` is a small fixed set** (`shell`, `asset`, `service-worker`,
`healthz`, `readyz`, `metrics`, `other`), never the raw path. Asset URLs
contain a content hash, so labelling by path would mint a fresh time series on
every single deploy — a textbook cardinality leak that eventually takes
Prometheus down with it.

Logs are JSON on stdout via `log/slog`, which is what the cluster's log
pipeline expects.

## Roadmap

State is currently per-browser. Shared state is the point of the project.

1. ~~Configurable single binary, no third-party requests~~ — done
2. **Data schema and store layer.** Migrations, the tables below, and a typed
   data-access layer with its own tests against a real Postgres. No HTTP
   surface yet — this milestone is finished when the schema is right and
   round-tripping is proven. See *Data storage*.
3. **REST API.** The store layer exposed over HTTP, with `Store` in the browser
   becoming async. See *API shape* for the worked request/response examples.
4. **Accounts and roles.** Session cookie, Argon2id hashes, roles enforced
   server-side. Accounts are created by an admin — see *Accounts*.
5. **Live sync.** SSE over one long-lived connection, so updates cost no extra
   handshakes. Writes go per-field with a revision check; a stale revision
   returns 409 and the client refetches.

Two tracks run alongside those and do not block them:

6. **Build and supply chain.** See below.
7. **Design pass.** See below.

### Data storage (steps 2 and 3)

PostgreSQL via `pgx`, run as a CloudNativePG cluster. The data is small — a
planner holds tens of rows, not millions — so this is chosen for operational
consistency with the rest of the platform (HA, barman backups, PITR) rather
than for scale.

One deployment serves one event: the event's identity comes from the
environment, so there is no tenant or event table and no row-level scoping.

#### Schema

`users` is defined under *Accounts* below and is created by the first
migration, since the tables here carry `updated_by` foreign keys into it.

```sql
-- Singleton. The one row is created by the first migration.
CREATE TABLE settings (
  id             boolean PRIMARY KEY DEFAULT true CHECK (id),
  ceiling        bigint  NOT NULL DEFAULT 0,
  inflation_pct  numeric(5,2) NOT NULL DEFAULT 0,
  fx_rate        numeric(18,6) NOT NULL DEFAULT 0,
  split_evenly   boolean NOT NULL DEFAULT false,
  revision       bigint  NOT NULL DEFAULT 1,
  updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sponsors (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code        text NOT NULL,
  name        text NOT NULL DEFAULT '',
  position    integer NOT NULL,
  revision    bigint NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid REFERENCES users(id) ON DELETE SET NULL
);

-- Named stages of the event that items group under ("guests arrive",
-- "dinner", "speeches"). Real planning spreadsheets organise costs by the
-- run of the evening, not as a flat list.
CREATE TABLE phases (
  id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name     text NOT NULL,
  position integer NOT NULL
);

CREATE TABLE budget_items (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  phase_id    uuid REFERENCES phases(id) ON DELETE SET NULL,
  -- Self-reference for quotes that break down into components: a catering
  -- line is one number in the budget but a dozen rows in the caterer's
  -- quote. Children roll up into the parent rather than being counted
  -- separately.
  parent_id   uuid REFERENCES budget_items(id) ON DELETE CASCADE,
  item        text   NOT NULL DEFAULT '',
  vendor      text   NOT NULL DEFAULT '',
  unit        bigint NOT NULL DEFAULT 0,   -- minor units
  qty         numeric(12,3) NOT NULL DEFAULT 1,
  paid        bigint NOT NULL DEFAULT 0,   -- minor units, deposits included
  -- A decision deadline, distinct from a task due date: "this vendor has to
  -- be committed to by then or the price or the slot is gone". This is the
  -- thing planners actually track, and hand-maintaining it in prose is what
  -- the old dashboard's watch list was doing.
  lock_by     date,
  note        text   NOT NULL DEFAULT '',
  position    integer NOT NULL,
  revision    bigint NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid REFERENCES users(id) ON DELETE SET NULL,
  CONSTRAINT no_self_parent CHECK (parent_id IS DISTINCT FROM id)
);

CREATE INDEX ON budget_items (phase_id, position);
CREATE INDEX ON budget_items (parent_id);
CREATE INDEX ON budget_items (lock_by) WHERE lock_by IS NOT NULL;

-- The relational win over the current JSON array: a sponsor reference
-- cannot dangle, because deleting a sponsor cascades here.
CREATE TABLE budget_item_sponsors (
  budget_item_id uuid NOT NULL REFERENCES budget_items(id) ON DELETE CASCADE,
  sponsor_id     uuid NOT NULL REFERENCES sponsors(id)     ON DELETE CASCADE,
  PRIMARY KEY (budget_item_id, sponsor_id)
);

CREATE TABLE tasks (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name        text NOT NULL DEFAULT '',
  owner       text NOT NULL DEFAULT '',
  due         date,
  status      text NOT NULL DEFAULT 'not-started'
                CHECK (status IN ('not-started','in-progress','done')),
  position    integer NOT NULL,
  revision    bigint NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid REFERENCES users(id) ON DELETE SET NULL
);

CREATE TABLE notes (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  text        text NOT NULL DEFAULT '',
  position    integer NOT NULL,
  revision    bigint NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Grid column widths and row heights are per-person preferences, not shared
-- data. They live in the state blob today, which would mean one person
-- dragging a column resizes it for everyone.
CREATE TABLE user_ui_prefs (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  prefs   jsonb NOT NULL DEFAULT '{}'::jsonb
);
```

Three decisions worth stating explicitly:

- **Money is `bigint` in minor units.** The browser currently works in whole
  units, which is lossless for IDR (zero-decimal) but silently drops cents for
  EUR. Storing minor units and converting at the API boundary fixes that. The
  exponent is derived from the ISO 4217 code, so the conversion is not a
  per-deployment setting.
- **`position` replaces array order.** Order is meaningful in the UI and JSON
  array order does not survive a relational round trip.
- **`revision` is per row**, bumped on every write. That is what makes step 4's
  conflict detection possible.

#### Writes and conflicts

Per-field updates carry the revision the client last saw:

```sql
UPDATE budget_items
   SET unit = $1, revision = revision + 1, updated_at = now(), updated_by = $2
 WHERE id = $3 AND revision = $4;
```

Zero rows affected means someone else got there first: the API returns `409`
with the current row, and the client reconciles rather than overwriting. This
is what replaces the current last-write-wins behaviour.

#### Change fan-out

After a successful write the server issues `NOTIFY soiree_changes` with the
table and row id. One `LISTEN` connection per process fans events out to the
connected SSE clients, so a second person's edit appears without polling and
without a new connection per update.

#### Migrations

Plain `.sql` files embedded with `//go:embed`, applied in order at startup and
recorded in a `schema_migrations` table. The whole run is wrapped in
`pg_advisory_lock`, because the Deployment can have more than one replica and
they will start simultaneously — without the lock they race and two of them try
to apply the same migration.

The app connects to the cluster's `-rw` service. It holds no state of its own,
so replicas scale freely.

#### Where this schema came from

It is modelled on real planning spreadsheets rather than invented. Three of the
columns above exist because actual planners keep them and a flat item list
cannot express them:

- **Phases.** Costs get organised by the run of the evening — arrival, dinner,
  speeches — not as one undifferentiated list.
- **Vendor as its own column.** It is the thing people chase, filter by, and
  chase again. Buried in a free-text note it is unusable.
- **`lock_by`.** Distinct from a task due date: the date a decision has to be
  made or the price or the slot is lost. The previous dashboard tracked these
  by hand-writing paragraphs into a "watch list", which is exactly the sort of
  thing that goes stale. As a column it can be queried and surfaced.
- **Parent/child items.** A caterer quotes a per-dish breakdown that is one
  line in the budget. Counting those rows separately would double the total.

#### Importing existing data

Two paths in, both tolerant by design.

The current **Export JSON** maps directly onto these tables, allocating
`position` from array order and converting money to minor units.

**Spreadsheets** (`.ods`, `.csv`) are the messier case, and the importer is
built for real ones rather than idealised ones. Sheets made by people for
people routinely have header rows repeated partway down, numbers stored as
formatted strings (`"22,500,000"`), totals embedded in the grid alongside
data, several columns sharing one header, and a second sheet holding a
breakdown that belongs to a single line on the first. The importer therefore
takes an explicit column mapping rather than guessing, parses grouped numbers
by stripping separators, skips rows that re-declare headers, and puts a
breakdown sheet in as child rows under one parent item.

#### API shape

Read the whole plan in one request — one round trip matters more than
granularity when the origin is far away:

```http
GET /api/v1/plan
```

```json
{
  "settings": { "ceiling": 7000000, "inflationPct": 4, "revision": 3 },
  "phases":   [ { "id": "3f1c…", "name": "Arrival", "position": 0 } ],
  "sponsors": [ { "id": "9a2e…", "code": "Rose", "name": "Ada", "revision": 1 } ],
  "budgetItems": [
    {
      "id": "7b4d…", "phaseId": "3f1c…", "parentId": null,
      "item": "Welcome signage", "vendor": "Local print shop",
      "unit": 500, "qty": 3, "paid": 0,
      "lockBy": "2026-10-31", "note": "A4, mounted",
      "sponsors": ["9a2e…"], "revision": 2
    }
  ],
  "tasks": [], "notes": []
}
```

Writes are per field and carry the revision the client last saw:

```http
PATCH /api/v1/budget-items/7b4d…
{ "revision": 2, "unit": 550 }
```

```json
{ "id": "7b4d…", "unit": 550, "revision": 3 }
```

If someone else changed that row first, the write is refused rather than
silently clobbering them:

```http
HTTP/1.1 409 Conflict
{ "error": "stale_revision", "current": { "id": "7b4d…", "unit": 600, "revision": 3 } }
```

The client reconciles against `current` instead of refetching the whole plan.
Creates are `POST /api/v1/budget-items`, deletes are
`DELETE /api/v1/budget-items/{id}` carrying the same revision check, and
`GET /api/v1/events` is the SSE stream that announces which rows changed.

### Accounts (step 4)

There is no self-service sign-up and no open invite link. An **admin creates
each account** by entering an email address and a role. The server mails that
person a single-use link to set their own password.

```
admin ──► email + role ──► account created (no password yet)
                                  │
                                  ▼
                    mail: "set your password" link
                                  │
              recipient sets a password ──► account active
```

The password is never chosen by the admin and never travels by email. Only a
one-time link does, and it is useless once used or expired.

| Field | Purpose |
|---|---|
| `email` | Identity and delivery address, unique |
| `role` | `admin` \| `editor` \| `viewer`, chosen by the admin at creation |
| `password_hash` | Argon2id (see below). Null until the person sets one. |
| `status` | `invited` \| `active` \| `disabled` |
| `created_by` | Attribution |

Set-password and reset both use the same short-lived token: ≥128 bits from a
CSPRNG, stored only as a hash, single-use, expiring in 24 hours, consumed
inside a transaction so it cannot be redeemed twice, and rate-limited per IP.
Requesting a reset returns the same response whether or not the address exists,
so the endpoint cannot be used to enumerate accounts.

Mail goes out over SMTP (`SOIREE_SMTP_*`). If SMTP is not configured, account
creation still succeeds and the admin is shown the set-password link to pass on
directly — so a deployment without mail is degraded, not broken.

### Password storage

Hashed with **Argon2id**, the current password-hashing standard and the winner
of the Password Hashing Competition. Never encrypted — encryption is reversible
and that is the wrong property for a password.

- Per-password salt, 16 bytes from `crypto/rand`. Never reused, stored
  alongside the hash in the standard encoded form.
- Parameters at least the OWASP-recommended floor: 19 MiB memory, 2 iterations,
  1 degree of parallelism, 32-byte output. Memory cost is what makes GPU and
  ASIC attacks expensive, which is the property that matters.
- Parameters are stored in the encoded hash, so they can be raised later and
  existing passwords are transparently re-hashed on next successful login.
- Verification is constant-time.
- Login is rate-limited per account and per IP.

The implementation uses `golang.org/x/crypto/argon2` — no hand-rolled
cryptography.

### Build and supply chain (track 6)

Partly in place; the rest is the work.

Done: reproducible static build with `-trimpath`, version and commit stamped at
link time, `scratch` base with no shell or package manager, unprivileged UID,
`govulncheck` on every PR, SBOM and `mode=max` provenance attached to released
images, Renovate on every dependency including a custom manager keeping the CI
toolchain in step with the Dockerfile.

Still to do:

- **Sign releases** with cosign, keyless via the GitHub OIDC identity, so the
  signature proves which workflow in which repository built the image.
- **Verify in cluster.** A signature nothing checks is decoration; the point is
  an admission policy that refuses unsigned images.
- **Pin actions by digest** rather than tag. A moving tag is a supply-chain
  hole, and Renovate can bump digests just as well as tags.
- **Publish the SBOM** as a release asset, not only as an image attestation.
- **Decide on multi-arch.** The cluster is amd64, so `linux/arm64` currently
  buys nothing and doubles release build time. Worth adding only if someone
  actually wants to run this on a Pi.

### Design pass (track 7)

The interface inherited its look from a single-purpose dashboard. It is
decent — warm paper tones, a serif display face against a system sans, real
data density — and the point of this track is to make it feel deliberate
rather than to restyle it into something generic.

Principles, in rough priority:

- **Keep it a tool, not a landing page.** People come here to reconcile
  numbers. Density, scannability and alignment of figures matter more than
  hero space. Tabular figures, right-aligned money, columns that line up.
- **Typography carries it.** The display/body pairing is already the strongest
  thing on the page. Lean on scale and weight for hierarchy instead of adding
  borders, cards and shadows around everything.
- **Earn every decoration.** A rule, a shadow or a background tint has to do a
  job. Uniform rounding and a drop shadow on every surface is the house style
  of software nobody chose.
- **Colour means something.** Gold, jade and rust already encode committed,
  settled and outstanding. Keep that mapping honest and do not spend those
  colours on decoration.
- **Light and dark both first-class.** Both are defined now and both must stay
  legible; dark mode is not an inverted afterthought.
- **The empty state is the first impression.** A fresh instance shows no data
  at all, and that screen currently does nothing to explain what to do next.

Explicitly avoided: the generated-template look — a centred hero over three
equal feature cards, a purple-to-indigo gradient, emoji standing in for icons,
uniform 8px rounding everywhere, and stock illustrations. None of that suits a
budget ledger, and all of it reads as unconsidered.

Accessibility is part of this, not a follow-up: contrast that holds in both
themes, visible focus rings, full keyboard operation of the grid, and hit
targets that work on a phone — which is where a good share of this audience
will open it.

Step 4 is where the current last-write-wins behaviour gets fixed. It is
acceptable for a single-user browser store and is not acceptable once two
people edit at once.
