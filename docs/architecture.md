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
  api.go                the REST API's generic verbs and error shape
  api_entities.go       one descriptor per table
  api_json.go           wire types; money, dates, partial updates
  api_settings.go       the singleton's own PATCH
  auth.go               login, set-password, the admin's view of accounts
  authmw.go             session resolution, roles, per-IP limits
  passkeys.go           the WebAuthn surface
  push.go               storing and removing a browser's push subscription
  sse.go                the change fan-out behind GET /api/v1/events
  metrics.go            Prometheus collectors and request instrumentation
internal/store/         typed data access, change history, LISTEN/NOTIFY
internal/reminders/     the deadline digest and its scheduler
internal/push/          the Web Push transport
internal/mailer/        the SMTP transport
internal/auth/          Argon2id hashing, one-time token minting
internal/migrate/       advisory-locked migration runner
web/embed.go            //go:embed of web/src
web/src/                index.html, styles.css, app.js, sw.js, fonts/
```

Three things are attached rather than built in, and each is nil when the
deployment does not have it: the store (no `DATABASE_URL`), the accounts surface
(no store), and the passkey Relying Party (no `SOIREE_BASE_URL`). A nil one
means the corresponding routes are never registered, so "off" is the absence of
a path rather than a handler that refuses. There is nothing to get wrong in a
handler that was never mounted.

## Request paths

| Path | Caching | Notes |
|---|---|---|
| `/` | `no-cache` + ETag | Go template. Carries the hashed asset URLs and the config data block, so it must revalidate for a deploy to take effect. |
| `/assets/<name>.<hash>.<ext>` | `immutable`, 1 year | Content-addressed. The URL changes whenever the bytes do. |
| `/sw.js` | `no-cache` + ETag | Rendered with the precache list. Never cached hard, or a broken worker would pin itself. |
| `/robots.txt` | `public, max-age=3600` | Rendered from `SOIREE_ALLOW_INDEXING`. Short rather than immutable, so flipping the flag takes effect within the hour. At a fixed path, because a content-addressed `robots.<hash>.txt` is not a place any crawler looks. |
| `/healthz` | `no-store` | Liveness. Checks nothing downstream. |
| `/readyz` | `no-store` | Readiness. Pings the database when one is configured, with a 2 s bound; `503` and `database unreachable` otherwise. |
| `/api/v1/...` | `no-store` | Mounted only when a DSN is set, as a subtree mux of its own. `no-store` is applied on the way *in*, so the mux's own 404 and 405 carry it too. |
| `/api/v1/events` | `no-store` | The SSE stream. Also `X-Accel-Buffering: no`, because a buffered event stream is an event stream that never arrives. |
| `/metrics` | — | **Not on this listener.** Served on `SOIREE_METRICS_ADDR` instead; a request here is a 404, still counted under `route="metrics"` so a stale scrape target or a path scanner is visible rather than silent. |

The API gets a mux of its own, mounted as a subtree, rather than registering on
the main one. That is what makes a wrong method on a real collection a `405`
with an `Allow` header: on the main mux the frontend's catch-all `/` matches
every path and every method, which counts as a full match and stops Go's mux
ever reaching its method-not-allowed branch.

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

The block is a purpose-built struct rather than the whole configuration, so a
variable cannot be published by being added. What it carries beyond the event's
own details is exactly two capability signals: the VAPID **public** key, and
only when the server could actually send with it, and a boolean saying whether
to offer a passkey button. The private key is deliberately absent from the type,
and the passkey flag names no domain and carries no key — it reveals nothing a
request to the login page would not. That flag also has to agree with whether
the routes are actually mounted, which is why `cmd/soiree` clears it when there
is no database: a button whose route is a 404 is worse than no button.

The database DSN is `DATABASE_URL` and not `SOIREE_DATABASE_URL`, deliberately.
It is the name every Postgres tool and the CloudNativePG connection secret
already use, and it is not part of the event's identity, so it does not belong
inside the `SOIREE_*` de-personalisation boundary.

`SOIREE_EVENT_DATE` is rejected unless it carries a timezone. A bare
`2027-06-12T00:00:00` is parsed in the *viewer's* timezone, so the countdown
reads differently depending on where someone is — the exact failure mode this
application is most exposed to.

## The `Store` seam

Everything in this section describes `web/src/app.js` as it stands at the 1.0.0
tag. The frontend is the half still moving, so where it and the code disagree,
the code is right.

All persistence in `web/src/app.js` goes through one object:

```js
Store.read()        // -> state object, or null
Store.write(state)  // persist the whole state object
```

Nothing else touches `localStorage`. `save()` wraps `Store.write` and is
debounced by 500 ms, flushed on `pagehide` and on `visibilitychange`. That seam
is what let the network be added behind it without touching any of the ~28
mutation sites: they call `save()` and know nothing about where the state goes.

### Two modes, decided once at startup

The page asks the origin for `GET /api/v1/plan` exactly once on load. A `404` is
the final answer for a deployment with no database — those paths are never
registered, so asking again would only be a second `404` — and anything else
that is not an answer gets a few retries, because it might be a browser offline
on a first visit.

A `401` is a third answer and must not be read as either of the others: it is a
deployment that *has* an API, which wants a session. Falling back to
`localStorage` on it would be the worst of both — edits would look saved, live
in one browser, and never reach the plan everybody else is reading. The page
holds, and connects properly when `auth.js` announces a sign-in on
`soiree:session`.

- **No API.** `localStorage` is the planner. One browser, one copy, no network
  after the probe. A self-hoster without Postgres, and `docker run` with no
  arguments, both land here and both work.
- **API.** The server is the planner. `localStorage` stays as the cached copy
  that paints before the plan arrives, plus the handful of fields that have no
  column behind them.

`localStorage` is written synchronously and *first* in both modes. `pagehide`
has no time to wait on a promise, and the whole point of a deferred write is
that the round trip is not on the interaction path.

### A session that ends while the page is open

The first request is not the only one that can be refused. A session lasts
seven idle days; an admin can disable an account; somebody signs out in another
tab. The page is mid-use when that happens, with three things running that each
ask again on a timer — the write loop, the resync and the event stream.

- **A `401` on a write is not a refusal of the row.** It is not parked the way
  other `4xx` answers are: nothing about the edit needs to change for it to be
  accepted, only who is asking, and a parked row stays parked until it is
  edited again. The pass stops, the shadow does not advance, and the same
  difference is still there to send.
- **All three loops stand down.** Asking into a `401` every thirty seconds for
  as long as a tab stays open is what a proxy's ban rule reads as an attack,
  and one forgotten tab is most of a household's allowance.
- **`auth.js` owns the question.** The planner raises `soiree:session-check`;
  `auth.js` re-probes once, and only a `401` signs the person out — an outage
  is not a sign-out. A refused `EventSource` reports no status at all, so it
  raises the same doubt and keeps its reopen booked, in case the cause was the
  subscriber cap rather than the session.
- **Signing back in is a merge, never `adopt()`.** `dirty` is set by the first
  keystroke of a page's life and never cleared, so `adopt()` would let this
  browser's copy win: diff a state that may be a week stale against a fresh
  shadow, `PATCH` it over everybody at the current revision — so without a
  `409` — and `POST` back every row somebody deleted in the meantime. The
  shadow the page still holds is the version both sides started from, which is
  what `applyPlan()`'s three-way merge is for.

### The shadow

With an API, the page keeps a private copy of every row as the server last
confirmed it, revision included. A write is the difference between `state` and
that shadow, which is how 28 mutation sites that say nothing about *what* they
changed still turn into per-field `PATCH`es carrying a revision. It also makes
the retry free: a write that fails simply does not advance the shadow, so the
next pass computes the same difference again and nothing is lost.

Four consequences worth stating, because each is easy to undo:

- **A `409` is a three-way merge, not a refetch.** The shadow is the version
  *both* edits started from. A field this browser did not touch takes theirs; a
  field it did keeps ours and goes again on the next pass. The obvious
  alternative — set the shadow to `current` and re-diff — sends the *old* value
  of the field they changed straight back and quietly undoes them.
- **A `4xx` that is not a `409` parks the row** rather than retrying it. The
  server understood and said no; an identical body would only earn an identical
  refusal. The edit is not lost — it is in `state`, on the screen and in
  `localStorage` — and the person is told it has not left the browser. A `404`
  in particular does not delete the row: somebody else removed what this person
  is editing, and throwing their work away to agree is the one outcome worse
  than being out of step.
- **Ids are reconciled, not assumed.** The page mints an optimistic id the
  moment a row appears, because the row has to be addressable before any round
  trip could have answered. `POST` returns a uuid, and adopting it is a rename
  everywhere the old id was referred to — otherwise a budget line keeps pointing
  at a sponsor id that only ever existed in this browser.
- **Collections are sent in dependency order.** Sponsors before budget items,
  because a line tagged with a sponsor created in the same debounce window has
  to reach a server that already knows that sponsor, or the attribution is a
  foreign-key violation and the whole line is a `400`.

`phases` and `programme` are in the plan and deliberately not in the page's
collection list: there is no interface for either, and a client must not delete
rows it cannot draw. The same reasoning applies to parent/child budget rows —
the page has no notion of a breakdown, so an imported plan reads its headline
figures high, and the fix is to teach the page about parents rather than to
filter children out of the sync layer, which would delete them.

### State shape

One plain JSON object. Money sits in `state` as major units, because that is
what an `<input type="number">` gives back; every sum accumulates whole minor
units as integers and converts once at the end.

```
{
  ceiling, inflationPct, fxRate, splitEvenly, reopened,
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
`colWidths`, `rowHeights` and `reopened` have no column behind them and survive
an adopted plan untouched — one person dragging a column must not resize it for
everybody.

### The one case where the browser wins

Normally the server is simply right and what is on screen is a cached copy. Two
cases are not normal, and in both the browser keeps what it is holding and sends
it up instead: something was typed between the cached copy painting and the plan
arriving, or this browser holds a planner somebody actually built and the server
has none at all. The second is the database being added to a deployment that was
running without one, and replacing that planner with an empty plan would destroy
the only copy of it, on the first load, with no warning.

It is deliberately narrow — only a planner that was genuinely saved, never a
blank one or generated demo data, and only against a plan with nothing in it.
Getting it wrong costs two browsers each seeding the same empty database and
producing every row twice, which is visible and fixable by hand. Not doing it
costs somebody their planner, which is not.

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
   The write is deferred and batched into a difference against the shadow, so
   the round trip stays off the interaction path rather than being eliminated —
   which is the only option available, since the distance is real.
4. **Compression and cache headers in the binary.** No proxy configuration is
   required for either.

Measured at 1.0.0, brotli: shell 3.6 kB, stylesheet 8.4 kB, planner script
35 kB, accounts script 13 kB, font 33 kB. Both scripts are `defer`, so first
paint needs the shell and stylesheet only — about 12 kB.

### Deliberately excluded

- **HTTP/3.** Decided against, and worth recording why, because the naive
  reading says it should help. QUIC completes a handshake in one round trip
  where TCP + TLS 1.3 needs two, so on a 300 ms link it saves roughly 300 ms —
  genuinely significant, not a rounding error.

  It is still not worth it here, because the items above already removed the
  cost it would address. The service worker means repeat visits make no network
  request at all; reads are a single request; writes are deferred off the
  interaction path; updates arrive over one long-lived SSE connection instead
  of repeated handshakes. What is left for QUIC to improve is *connection
  establishment*, which this design has deliberately made rare — a one-off on a
  visitor's first load.

  The price is not small: QUIC cannot be passed through at layer 4 the way TCP
  is, so terminating it at the edge would put TLS private keys on the most
  exposed hosts in the deployment. Paying that to speed up a once-per-device
  event is the wrong trade. Browsers fall back via `Alt-Svc` with no
  user-visible effect, so declining costs nothing.
- **Edge PoPs.** Adding a server geographically closer does not help while the
  proxy in front is a layer-4 TCP passthrough: TLS still terminates at the
  origin, so the client's handshake round-trips the full distance anyway. It
  would only pay if the edge terminated TLS and cached.

## Observability

The application is built to be operated, not just run.

| Endpoint | Purpose |
|---|---|
| `/healthz` | Liveness. Checks nothing downstream on purpose — a liveness probe that fails when the database is down converts an outage into a restart loop. |
| `/readyz` | Readiness. With a DSN configured it pings the database, bounded at two seconds, and answers `503` if it cannot be reached — the point of readiness is to take such an instance out of rotation without restarting it. With no DSN it is identical to liveness, or a frontend-only deployment would never become ready. |
| `/metrics` | Prometheus exposition, on a private registry — and on a **separate listener** (`SOIREE_METRICS_ADDR`, default `:9090`). The ingress route in front of the site carries no path constraint, so anything on the main listener is world-readable, and `soiree_build_info` would name the running version and commit to anyone who asked. A second port is also the shape a ServiceMonitor expects. The probes stay on the main port, because that is the one kubelet reaches. |

Exported series:

- `soiree_http_requests_total{route,method,status}`
- `soiree_http_request_duration_seconds{route,method}` — buckets start at
  100 µs, because everything is served from memory and the default buckets are
  far too coarse to show anything here
- `soiree_http_requests_in_flight`
- `soiree_sse_subscribers` — live-sync clients currently connected to
  `/api/v1/events`. Registered on the same private registry by the hub itself,
  so a deployment with no database never declares it
- `soiree_build_info{version,commit}` — stamped at link time, so a running pod
  can be tied back to a commit
- Standard Go runtime and process collectors

**`route` is a small fixed set**, never the raw path. Asset URLs contain a
content hash and API paths after the collection are row ids, so labelling by
path would mint a fresh time series on every deploy and on every budget line —
a textbook cardinality leak that eventually takes Prometheus down with it.

The set is `shell`, `asset`, `service-worker`, `healthz`, `readyz`, `metrics`,
`other`, plus one label per API collection: `api-plan`, `api-settings`,
`api-events`, `api-budget-items`, `api-sponsors`, `api-tasks`, `api-notes`,
`api-phases`, `api-programme-entries`, and `api-other` for everything else
under the prefix. The collection is matched against that map rather than taken
from the URL, because the segment is caller-controlled and an unknown one must
never become a label. `/api/v1/auth/...` and `/api/v1/users/...` therefore land
in `api-other`.

Logs are JSON on stdout via `log/slog`, which is what the cluster's log
pipeline expects.

## Roadmap

State is shared, the API is guarded, and the browser uses all of it. What is
left is listed under *Open*.

Done:

1. ~~Configurable single binary, no third-party requests.~~
2. ~~Data schema and store layer.~~ Migrations through 0011, a typed data-access
   layer, and its own tests against a real Postgres. See *Data storage*.
3. ~~REST API~~, including `PATCH /api/v1/settings` and the 409-on-stale-revision
   path. `Store` in the browser is async and writes through it. See *API shape*.
4. ~~Accounts and roles.~~ Session cookie, Argon2id hashes, admin-created
   accounts, single-use set-password links, and passkeys alongside the password.
   See *Accounts*.
5. ~~Live sync.~~ One `LISTEN` connection per process fanning out over SSE at
   `GET /api/v1/events`, and a page that holds the stream and merges what it
   announces. See *Live sync*.
6. ~~Build and supply chain.~~ See below; two items there belong elsewhere.
7. ~~Design pass.~~ See below.
8. ~~Audit trail.~~ Append-only `change_log`, written in the same transaction as
   the change it records.
9. ~~Deadline reminders~~, by mail and by web push. See *Web push*.
10. **Data protection** — half done. `internal/store/privacy.go` implements
    subject export, erasure and a retention purge, with their own tests. Nothing
    invokes them: there is no route, no subcommand and no caller outside that
    package, so honouring a request today means writing Go or SQL against a
    production database. The hard part is built and the way in is not.
11. ~~Vulnerability disclosure.~~ `SECURITY.md`, and a documented verification
    command that the release workflow itself re-runs.
13. ~~Mobile budget grid.~~
14. ~~Interface language.~~ English, Dutch and Indonesian, as a table of strings
    rather than a framework.
15. ~~After the event.~~ The planner becomes an archive on the day, and can be
    reopened deliberately.
16. ~~A browser-level test.~~ Playwright specs in `e2e/`, driving the real
    binary, including one instance with a database behind it.
17. ~~Authorise the plan API.~~ The whole `/api/v1` subtree is behind
    `RequireWrite`, and writes carry their actor. See *API shape*.
18. ~~A login and account interface.~~ `web/src/auth.js`. See *Accounts*.
19. ~~An API description.~~ `api/openapi.yaml`, checked against the running
    server by `internal/httpd/openapi_test.go`.

Number 12, the restore drill, is the one missing from that list; number 10 is
struck only halfway.

Open, in the order they matter:

- **Translate the accounts screens.** The planner speaks English, Dutch and
  Indonesian; `auth.js` speaks English, whatever `SOIREE_LOCALE` says. The
  first screen an invited person sees is the one that is not in their language.
- **A standing control for reminders.** See *Web push*: the offer is the only
  way in and there is no way out.
- **What a signed-out browser shows.** The page draws its cached copy of the
  plan for whoever opens it, and signing out does not clear that copy. That is
  what working offline means, and it is also a ledger of names against money
  left on a shared computer. It wants a decision rather than a default.
- **A way to invoke the data-protection functions.** Export, erasure and purge
  exist and nothing calls them. An admin-only route or a subcommand, either
  would do; what there must not be is a documented obligation that can only be
  met by hand-written SQL.
- **Restore drill.** Backups that have never been restored are not backups.
  Restore into a scratch namespace, confirm the data, write down the steps.

Explicitly out of scope: multi-event tenancy, a plugin system, analytics, and a
marketing site. This is a tool a dozen people use for one evening.

The sections that follow are the reference material for what each of those
items actually delivered, and are where the decisions worth not undoing are
written down.

### Data storage

PostgreSQL via `pgx`, run as a CloudNativePG cluster. The data is small — a
planner holds tens of rows, not millions — so this is chosen for operational
consistency with the rest of the platform (HA, barman backups, PITR) rather
than for scale.

One deployment serves one event: the event's identity comes from the
environment, so there is no tenant or event table and no row-level scoping.

#### Schema

Eleven migrations, applied in order at startup. The plan's own tables are
below; the rest are named at the end of this section.

`users` is defined under *Accounts* and is created by migration 0001, before
anything else, since every shared table carries an `updated_by` foreign key
into it.

```sql
-- Singleton. The one row is inserted by the migration that creates it, so
-- callers only ever update. No second row can exist: that is what the boolean
-- primary key and its CHECK are for.
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
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name        text NOT NULL,
  position    integer NOT NULL,
  revision    bigint NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid REFERENCES users(id) ON DELETE SET NULL
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

-- The run of show: guests arrive, speeches, cake, dinner, karaoke. Not a
-- budget with a timestamp bolted on — most of these lines cost nothing, and
-- several that do share one budget line. Ordering is `position`, not a clock
-- time: evenings run late, and the order survives that where "20:15" written
-- down three weeks earlier does not.
CREATE TABLE programme_entries (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  title          text NOT NULL DEFAULT '',
  note           text NOT NULL DEFAULT '',
  position       integer NOT NULL,
  -- Optional in both directions, and SET NULL rather than CASCADE: dropping
  -- the cake from the budget does not drop the cake from the evening.
  budget_item_id uuid REFERENCES budget_items(id) ON DELETE SET NULL,
  revision       bigint NOT NULL DEFAULT 1,
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- One budget line belongs to at most one moment in the evening, or a "cost of
-- the evening so far" roll-up double-counts it. Partial, to keep the index off
-- the many entries with no cost attached.
CREATE UNIQUE INDEX programme_entries_budget_item_key
  ON programme_entries (budget_item_id) WHERE budget_item_id IS NOT NULL;

-- Grid column widths and row heights are per-person preferences, not shared
-- data. Deliberately jsonb and deliberately unvalidated: the shape is owned by
-- the frontend and nothing on the server reads it, so a column per preference
-- would be a migration per stylistic change.
CREATE TABLE user_ui_prefs (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  prefs   jsonb NOT NULL DEFAULT '{}'::jsonb
);
```

The remaining tables belong to features documented in their own sections:

| Migration | Table | What it holds |
|---|---|---|
| 0006 | `reminders_sent` | One row per digest period, so a restart never says it twice. `period_key` is the schedule width plus the calendar day the period started on, in the event's timezone, so every replica computes the same string for the same week. |
| 0007 | `change_log` | Append-only history. One table rather than nine, because "what happened to this plan last week" would otherwise be a nine-way `UNION`. |
| 0008 | `password_tokens`, `sessions` | The two short-lived secrets logging in needs. Both store the SHA-256 of one and never the value, so a stray `pg_dump` contains nothing replayable. |
| 0010 | `push_subscriptions` | One row per device, not per person: a phone and a laptop are separate subscriptions with separate keys. |
| 0011 | `passkey_credentials`, `passkey_challenges` | One row per authenticator, plus the in-flight ceremonies. Public keys only — this database holds nothing that can log in. |

Three decisions worth stating explicitly:

- **Money is `bigint` in minor units in the database, and a decimal string in
  major units over the API** (`"250.50"`, `"750000"`). Not a JSON number: every
  browser parses those as IEEE-754 doubles, and while a single value survives
  the round trip, the client's own arithmetic does not — `45.33 × 40` is
  `1813.1999999999998`. A cent per line is exactly what a budget must not do.
  The string costs nothing, since the digits came from an integer and go back
  to one. Input must be a string too: accepting numbers would mean a client
  that computed a figure sends `0.30000000000000004` and is told it has 17
  decimal places, which is a worse error than a flat type mismatch.

  The exponent comes from the code, but note it is **this project's table, not
  ISO 4217's** — IDR is treated as zero-decimal, where ISO says 2 and `Intl`
  agrees with ISO. The sen has not circulated in decades and no Indonesian
  price, invoice or spreadsheet carries one, so treating rupiah as two-decimal
  would show every figure a hundred times too small.

  A client must therefore **mirror the table and must not derive the exponent
  from `Intl`**, or it will disagree with the server on precisely the currency
  the deployment is running. The table is in `internal/store/money.go` and
  again in `web/src/app.js`, and the two are meant to stay mirrored;
  `Intl.NumberFormat` is used in the page for *display* only, where the
  server's answer has already been parsed into integers.

  `qty` stays a JSON number, deliberately. It is not money — nothing is paid in
  it and it is never summed — and its column, `numeric(12,3)`, converts to a
  float64 and back exactly at every value it can hold. `inflationPct` and
  `fxRate` are numbers for the same reason.
- **`position` replaces array order.** Order is meaningful in the UI and JSON
  array order does not survive a relational round trip.
- **`revision` is per row**, bumped on every write. That is what makes conflict
  detection possible, and since migration 0009 every shared table has one —
  `phases` was the last exception, and two people renaming the same stage of
  the evening could not be told apart.

#### Writes and conflicts

Per-field updates carry the revision the client last saw:

```sql
UPDATE budget_items
   SET unit = $1, revision = revision + 1, updated_at = now(), updated_by = $2
 WHERE id = $3 AND revision = $4;
```

Zero rows affected means someone else got there first: the API returns `409`
with the current row, and the client reconciles rather than overwriting. A
`DELETE` carries the same check, because deleting a line somebody has just
edited would discard their edit with no more ceremony than deleting a stale one.

The read a `PATCH` performs in between is not a race. Whatever it returns, the
`UPDATE` still names the *caller's* revision, so a write that slipped in
between matches no rows and comes back as a conflict — which is the whole point.

#### Change fan-out

After a successful write the server issues `NOTIFY soiree_changes` carrying the
entity, the row id, the action and the row's revision as it now stands. One
`LISTEN` connection per process fans those out to the connected SSE clients, so
a second person's edit appears without polling and without a new connection per
update.

Three properties are load-bearing:

- **The announcement is issued from inside the transaction that made the
  change.** `NOTIFY` is transactional in PostgreSQL — the payload is queued and
  delivered only on commit — so a write that rolls back announces nothing, with
  no compensating logic to get wrong.
- **It is issued from the same funnel that records the history.** A change
  cannot be announced without also being recorded, and cannot be recorded
  without also being announced. There is no third path to keep in step.
- **What travels is identifiers, never the row.** PostgreSQL caps a payload at
  8000 bytes and a budget note alone can approach that, but the better reason
  is that a listener is not an authorisation boundary: the payload says *what*
  changed and the client re-reads it through the API, which is where the rules
  about who may see what belong.

The revision in the payload is what makes a notice actionable rather than
merely interesting: a client that already holds it — because it is the one that
just wrote it — can ignore its own echo without the server having to say who
caused the change.

`users` is recorded in the history and deliberately never announced.
`/api/v1/users` is admin-only while this stream is not, and "user *x* changed"
would hand out the existence and count of accounts through a door the users API
keeps shut.

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
  "settings": { "ceiling": "70000.00", "inflationPct": 4, "fxRate": 0,
                "splitEvenly": false, "revision": 3, "updatedAt": "…" },
  "phases":   [ { "id": "3f1c…", "name": "Arrival", "position": 0, "revision": 1 } ],
  "sponsors": [ { "id": "9a2e…", "code": "Rose", "name": "Ada", "revision": 1 } ],
  "budgetItems": [
    {
      "id": "7b4d…", "phaseId": "3f1c…", "parentId": null,
      "item": "Welcome signage", "vendor": "Local print shop",
      "unit": "5.00", "qty": 3, "paid": "0.00",
      "lockBy": "2026-10-31", "note": "A4, mounted",
      "sponsors": ["9a2e…"], "revision": 2
    }
  ],
  "programme": [], "tasks": [], "notes": []
}
```

Every list is present and non-null even when empty, so the client never has to
check which of the two it got. The keys are the lowerCamelCase of the store's
own field names without exception, so there is one rule to remember rather than
seven. A `date` column crosses as a plain `"2026-10-31"` and never as a
timestamp: `"2026-10-31T00:00:00Z"` renders as the 30th east of UTC, which is
the same failure the event date's timezone rule exists to prevent.

Writes are per field and carry the revision the client last saw:

```http
PATCH /api/v1/budget-items/7b4d…
{ "revision": 2, "unit": "5.50" }
```

```json
{ "id": "7b4d…", "unit": "5.50", "revision": 3 }
```

If someone else changed that row first, the write is refused rather than
silently clobbering them:

```http
HTTP/1.1 409 Conflict
{ "error": "stale_revision", "current": { "id": "7b4d…", "unit": "6.00", "revision": 3 } }
```

The client reconciles against `current` instead of refetching the whole plan.

The full surface:

| | |
|---|---|
| `GET /api/v1/plan` | The whole plan in one round trip |
| `GET /api/v1/events` | The SSE change stream. See *Live sync*. |
| `PATCH /api/v1/settings` | The plan-wide knobs: ceiling, inflation buffer, fx rate, split-evenly |
| `POST`, `PATCH /{id}`, `DELETE /{id}` on `/api/v1/budget-items`, `/sponsors`, `/tasks`, `/notes`, `/phases`, `/programme-entries` | The collections |
| `POST`, `DELETE /api/v1/push/subscriptions` | A device asking to be notified. Session required; mounted only when the deployment has accounts. |
| `/api/v1/auth/...`, `/api/v1/users/...` | See *Accounts*. |

`settings` has its own handler because it is the one table the collection
descriptors cannot express: a singleton behind a boolean primary key, so there
is no `{id}` to route on, nothing to `POST` and nothing to `DELETE`. Everything
a client can observe about it is the same as any other patch.

Four details a client has to get right:

- **A patch must carry `revision`.** It is read separately from every other
  field, because it is not one — it is the caller's claim about which version
  they were looking at. Merged in with the rest it would default to whatever was
  just read, and a patch that forgot it would quietly overwrite somebody's edit.
  Omitting it is a `400`.
- **A delete carries it as `?revision=N`**, not in a body. A body on `DELETE` is
  poorly supported by enough of the stack that it is not worth the argument.
- **Decoding is strict.** An unknown field is a `400` rather than a silent
  no-op, because the field being ignored is as likely to be `unit` misspelt as
  something harmless, and nobody notices a money column that did not change.
  The read-only fields a client legitimately echoes back when it patches a row
  it is holding — `id`, `revision`, `updatedAt`, `updatedBy` — are accepted and
  dropped, so strictness does not make the obvious client illegal.
- **An omitted field is left alone; an explicit `null` is not.** Those are
  different requests. `null` clears a nullable field (`phaseId`, `lockBy`) and
  is refused on a non-nullable one, because `{"unit": null}` is a client bug and
  silently writing `0` into a money column is the expensive way to find out.

Every non-2xx response has one shape — `{error, message?, current?}` — so a
client never has to guess. The codes are stable strings, because clients branch
on them: `bad_request`, `not_found`, `stale_revision`, `payload_too_large`,
`conflict`, `internal` from the plan itself, and `unauthenticated` (`401`),
`read_only` and `forbidden` (`403`) from the guard in front of it. The accounts
surface names its own refusals — `invalid_email`, `revision_required`,
`self_change`, `rate_limited` and the rest; the full list is in
`api/openapi.yaml`. `current` appears only on a `409`. The database's own
rejections are translated rather than surfaced as a `500`: a foreign key
violation is a `400` saying the request referred to something that is not there,
and a unique violation is a `409`. A `500` never carries the error — that goes
to the log, because it contains SQL and column names — but it always goes
*somewhere*, since a 500 whose cause was dropped cannot be operated on.

Nothing under `/api/v1` is cached. Every response carries `no-store`: a budget
two people are editing is the last thing that should come from a proxy, a
back/forward cache, or the service worker.

**Every route under `/api/v1` checks who is calling.** `routeAPI` wraps the
whole subtree in `RequireWrite` rather than guarding each route: anybody signed
in may read, only an editor or an admin may write, and that rule is a property
of the method, not of the route. Attaching it per route is how one route added
later ends up unguarded — which is how this subtree spent its first several
releases, with the middleware, the roles and the sessions all built and nothing
calling any of them. `withActor` then hands the store the caller's id, and only
the id: the change log outlives the account, and an address written into it
could never be erased. Push is a subtree of its own behind `RequireAuth`,
because a viewer may subscribe a device; `/api/v1/users` is admin-only.

### Accounts

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

**The first account is the exception, and needs its own way in.** Every route
that hands back a set-password link is admin-only, and `password-reset` gives
its link to the mailer and discards it — so on an empty database with no
working SMTP there is no path to the first admin at all. `SOIREE_BOOTSTRAP_ADMIN`
creates that account; `SOIREE_BOOTSTRAP_PASSWORD` optionally gives it a
password, hashed with the same Argon2id parameters as any other, so it can log
in immediately. Both are consumed only while no admin exists, which is what
makes them safe to leave set: neither can resurrect a disabled account nor
overwrite a password that has since been changed. Without the password the
account stays `invited` and the mailed link is the only way in — the better
shape when mail works, since no credential is written down.

The routes are `POST /api/v1/auth/login`, `logout`, `password-reset` and
`set-password`; `GET /api/v1/auth/session` for "who am I"; and
`GET|POST /api/v1/users`, `GET|PATCH|DELETE /api/v1/users/{id}` and
`POST /api/v1/users/{id}/invite` for administration. Every one of the
administration routes is checked per request against the role as it stands in
the database, not as it stood when the session was created.

The browser's half is `web/src/auth.js`, a second script beside the planner
rather than part of it: a deployment with no database has no accounts at all,
and `auth.js` is then a script that finds nothing and draws nothing. It draws
four screens, routed in the URL fragment so they can be linked to — sign in,
set a password (where an invitation link lands, with the token taken out of the
address bar before anything else happens), your own account and its passkeys,
and the accounts screen for an admin. It enforces nothing; the server does. It
is in English only, where the planner is translated, and that is a gap.

#### Sessions

A session is a server-side row; the cookie carries a token whose SHA-256 is
what the row stores, so a copy of the database contains nothing replayable.

- `HttpOnly`, so script cannot read it and an XSS anywhere on the origin does
  not become a stolen session. `Secure`, so it never crosses plain HTTP —
  browsers treat `localhost` as a secure context, so `docker run` still works.
  `SameSite=Lax`, so a form on another site cannot post here with the session
  attached while an ordinary link from a mail still arrives logged in.
- No `__Host-` prefix, deliberately. It is stricter, and it would also require
  HTTPS outright, which breaks a bare `docker run` entirely.
- Seven days idle, thirty days absolute. The idle window is slid in the database
  *and* in the browser, at most once an hour — doing only the first would leave
  the cookie expiring at the moment it was issued, so somebody using this daily
  would still be logged out on the seventh day.
- A cookie that no longer resolves is cleared on the way past, so a browser
  holding a revoked session stops re-presenting it on every request for a month.

Resolving the session and deciding whether the caller may do something are two
jobs, kept apart. That is what lets a public endpoint still know that an admin
is the one calling it.

#### Passkeys

A second way in, alongside the password and never instead of it. A passkey is a
key pair the authenticator holds — a phone's secure element, a laptop's TPM, a
USB key — and logging in is a server-minted challenge signed by a private key
that never leaves the device. It cannot be phished, reused across sites, or read
out of a database: the columns hold public keys, which verify signatures and
cannot produce them.

The endpoints are `POST /api/v1/auth/passkeys/register/{begin,finish}` (session
required), `POST /api/v1/auth/passkeys/login/{begin,finish}` (public, behind the
same per-IP bucket as the password login, so alternating between the two does
not buy twice the allowance), and `GET /api/v1/auth/passkeys` plus
`DELETE /api/v1/auth/passkeys/{id}` for managing one's own credentials. Never
anybody else's: there is no admin view of somebody's passkeys, because an admin
has no use for the list and the person who does is the one holding the devices.

**Lockout is impossible by construction.** Registering a passkey requires a
session; a session requires a password; so an account that can be reached only
by a passkey cannot exist. Losing a phone is losing a credential, not an
account — the password still works and an admin can still issue a fresh
set-password link. That is also why nothing here is ever a startup failure: a
deployment that cannot offer passkeys has one way in instead of two, and
refusing to boot over that would turn a missing convenience into an outage.

Three values do the security work, and each is somewhere specific:

- **The Relying Party ID** scopes a credential to a domain, and comes from
  `SOIREE_BASE_URL` and never from a request's `Host` header, which the client
  chooses. It is the field with the quietest failure mode in the protocol: a
  credential registered under the wrong RP ID simply never matches again, and
  the browser reports nothing more useful than "no credentials available". Only
  a leading `www.` is stripped; nothing further, because guessing at the public
  suffix boundary is how a deployment at `soiree.example.test` ends up
  registering credentials scoped to `example.test`. A bare IP cannot be an RP ID
  at all, so a deployment reached by address gets password login and nothing
  else.
- **The origin list has exactly one entry**, the origin of `SOIREE_BASE_URL`.
  Every additional entry is a host whose pages can mint assertions this server
  accepts.
- **The challenge is single-use and expires** in five minutes — generous next to
  the browser's own timeout, because the slow part is a person finding their
  phone. It is redeemed by a `DELETE`, so two requests presenting the same one
  cannot both win.

Two further choices worth recording. Credentials are **discoverable** (resident)
always: that is what makes "sign in" a single tap with no address typed first,
and it is also what lets `login/begin` answer identically for an address that
has an account and one that does not, because it never has to look. And
**attestation is not requested**, because it would tell this deployment which
make of authenticator somebody carries — a fact about a person that nothing here
would act on — and verifying it properly means the FIDO metadata service and a
trust store to keep current.

The user handle stored on the authenticator is the account's uuid and not its
email address. The handle travels with every assertion and appears in the
device's own passkey list; a uuid names the same account and says nothing about
who they are.

Every way a passkey login can fail answers with the same `401` and the same
code, for the same reason the rest of this surface answers identically to a
stranger and to a user: the difference between "no such credential" and "that
account is disabled" is an answer to a question nobody logged in should be able
to ask.

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

### Live sync

`GET /api/v1/events` is a Server-Sent Events stream. The API alone gets two
people a correct view of the plan each, right up until one of them changes
something — after which the other is silently looking at stale money. This is
the part that closes that window.

The shape is **one `LISTEN` connection per process, fanning out in memory**, not
one database connection per client. A twenty-person planning session is twenty
sockets and one database connection, cheap enough to leave open for the whole
evening. The connection is opened on the first subscriber and kept afterwards,
so a deployment nobody is watching holds none at all.

Three properties everything is arranged around:

- **Bounded.** A subscriber that falls more than 64 frames behind is dropped
  rather than queued for; the browser reconnects and refetches, which is cheaper
  for everyone than one stuck laptop growing a queue inside the server. There is
  a cap of 256 concurrent streams — far above any real session, and there so
  that a number exists at all. Reaching it is a `503` with `Retry-After`, not a
  `429`: the limit is about this instance's capacity, not the caller's
  behaviour.
- **Degrading.** With no `DATABASE_URL` the route is never mounted. With a
  database that goes away, the listener retries with jittered backoff from
  200 ms to 30 s and tells every client to refetch once it is back. It never
  wedges the process and never takes the site down with it.
- **Honest.** No event ids and no replay. A client that was disconnected missed
  changes and is told to refetch on connect, rather than being handed a cursor
  that implies the gap can be filled.

Every connection therefore opens with two frames: the reconnect interval, and a
`resync` telling the client its copy of the plan is stale. It usually is, and
that one rule is the whole of what a client has to do about missed events. A
`resync` is also broadcast whenever the listening connection is re-established —
without it, a client that stayed connected through a failover would keep a stale
plan indefinitely, which is the precise failure this feature exists to prevent.

Events are named (`change`, `resync`) rather than default, so a client registers
for each separately and an unknown future event name is ignored by an old client
instead of being mistaken for a change. A `change` frame carries the notice
described under *Change fan-out*.

Two mechanics exist for the network in between rather than for this
application. A comment is written into an idle stream every 20 seconds, because
an idle stream looks exactly like a dead one to anything counting seconds since
the last byte. And `X-Accel-Buffering: no` is set, because a buffered event
stream is an event stream that never arrives.

The stream also has to escape the server's own timeouts, which are armed for
ordinary requests. The read deadline is cleared once — net/http usually handles
this itself, but not for a request that carries a body, which never hits EOF
here. The write deadline is *rolled* per frame rather than cleared: cleared, a
client that stopped reading would block a goroutine inside `Write` forever;
rolled, it gets ten seconds to accept eight bytes and is otherwise disconnected,
which is what actually reclaims a stuck connection. Shutdown is registered
explicitly too, since `http.Server.Shutdown` waits for connections to go idle
and a stream blocked on its request context never does — without that, one
connected browser turns every `SIGTERM` into a hung shutdown.

#### The rule a client must implement

Announcements are enough to update a row in place *except after a delete of a
phase, a sponsor or a budget item*. Those three cascade in the database, and a
cascade is performed by the database on its own behalf:

- deleting a **phase** sets `budget_items.phase_id` to null on its items;
- deleting a **sponsor** removes their rows from `budget_item_sponsors`;
- deleting a **budget item** sets `programme_entries.budget_item_id` to null.

None of those bump the affected rows' revisions, and none of them are announced
— a write path in Go cannot record a change it never issued, and catching them
would mean database triggers, which cannot see who is making the request. So a
client that only applied the delete it was told about would be left holding
budget items that still name a dead phase and a revision the server agrees with.
Its next write would pass the revision check and put a dangling reference back.

**A delete of `phases`, `sponsors` or `budget_items` therefore requires a full
`GET /api/v1/plan`.** Deletes of `tasks`, `notes` and `programme_entries` cascade
to nothing and can be applied in place.

The one exception in the other direction: the child rows of a deleted budget
item *are* announced individually. `DeleteBudgetItem` reads and records the
whole tree in Go before issuing the statement, precisely because the cascade
would otherwise take a caterer's entire breakdown with no trace of what it said.
That is deliberate and worth not "simplifying" later.

The page takes the conservative reading of all of the above: it never updates a
row in place. Every `change` it does not already hold, and every `resync`,
ends in one coalesced `GET /api/v1/plan` merged three ways against the shadow —
which is always correct, costs one request however many events arrived in the
burst, and leaves the in-place optimisation to a client that needs it. It
ignores a `create` or `update` whose revision it already holds, which is what
suppresses the echo of its own writes, and never applies that test to a
`delete`, which announces the revision already held.

### Web push

The deadline digest's second channel. `internal/mailer` sends the whole digest
to a mailbox; `internal/push` sends a sentence to a lock screen and the click
opens the planner. That division is forced rather than chosen: a push payload is
a few kilobytes at most and the browser shows it in two lines, so trying to fit
the digest into one would produce something unreadable in a place nobody reads
carefully.

Web Push has no API key and no account. The server proves who it is by signing
each request with a P-256 pair it generated itself, and the browser pins the
public half at subscribe time — which is why the public key has to reach the
page, and why **rotating the pair silently invalidates every existing
subscription**. See [operating.md](operating.md) for how to generate one and
what rotation costs.

The public key is published into the page's config block only when the server
could actually send with it. Publishing the public half of a pair whose private
half is missing is worse than publishing nothing: the browser subscribes, the
permission prompt is spent, the UI reports success, and not one notification
ever arrives. An absent key is a feature that is visibly off, and "is there a
key here?" is the single question the client asks before offering to turn
notifications on.

A subscription is stored per **device**, not per person — a phone and a laptop
are separate subscriptions with separate keys, and one row per person would mean
the second device silently replaced the first. `POST /api/v1/push/subscriptions`
is idempotent on the endpoint, because the Push API hands a browser back
whatever subscription already exists rather than minting a new one, and the
correct client posts on every page load. Both endpoints require a session: a
subscription belongs to an account, and an unauthenticated `POST` here would be
an open invitation to fill the table. The endpoint must be an absolute `https`
URL, which is also the line that stops an authenticated account from pointing
the digest sender at something inside the network.

**`404` or `410` from a push service means permanently gone, and the row must be
deleted.** Storage was cleared, the app was uninstalled, permission was revoked,
the endpoint was retired — that answer is final and will not become "yes" again.
Every other failure (a `500`, a timeout, a refused connection) is the service or
the network having a bad minute and says nothing about the subscription, so
those rows stay. Getting this backwards is expensive in both directions: prune
on everything and one bad minute at the push service unsubscribes everybody;
prune on nothing and the table fills with endpoints that will never accept
another notification, each costing a round trip on every digest for the life of
the deployment.

Two smaller rules. A full endpoint URL is the capability to notify that device,
so logs and errors carry the push service's host and the account id, never the
endpoint — a log file is read by more people than a database is. And a
notification is given a 24-hour TTL and a collapsing tag, because a digest
describes the week it was sent in: a phone that was off for a fortnight should
show this week's, not both.

On iOS, Safari grants push only to a site added to the Home Screen. That is an
Apple platform decision, no amount of correctness here changes it, and some
recipients will therefore never receive a notification however well this works.
It is the reason mail remains the primary channel.

The browser half is two pieces. The page makes the offer — never on load,
because a denied permission is sticky, but the first time a signed-in editor
gives a task a due date — and posts the subscription, re-posting whatever the
device already holds on every load so that a restored database gets its devices
back. `web/src/sw.js` handles `push` and `notificationclick`: it always shows a
notification, because the subscription is `userVisibleOnly`, replaces the
previous digest rather than stacking on it by reusing the `tag`, and brings an
open planner to the front rather than opening a second one. The payload's four
fields (`title`, `body`, `url`, `tag`) are a contract between the server and
that handler: adding a field is safe, renaming one is not.

What is missing is a standing control. The offer is the only way in, so
somebody who never sets a due date is never asked, and there is nowhere to turn
reminders off again short of the browser's own site settings.

#### The reminder digest

The digest itself is `internal/reminders`, and it runs only when
`SOIREE_REMINDER_ENABLED` is true and at least one channel is configured. One of
the two is enough: a deployment that notifies and does not mail is as complete
as the other way round, and refusing to run because the *other* channel is
missing would be one channel suppressing the one that works.

Sending exactly once is layered, because three things would otherwise send the
same digest twice — a second replica, a redeploy, and a crash loop. A Postgres
advisory lock makes one replica the sender for the duration of a run, which
handles the simultaneous case and nothing else. The real guard is the
`reminders_sent` ledger, keyed by a period computed from the schedule rather
than from the clock the process started at, so every replica in every process
computes the same key for the same week. The row goes in *before* the mail goes
out, which makes this at-most-once on purpose: a crash between the claim and the
acknowledgement loses that period's digest rather than duplicating it. An
unresolved deadline is still unresolved next period and comes back marked
overdue; a digest that arrives twice is how a mail becomes noise. A run also
refuses to go out within half a period of the last one, which closes the
boundary case of a restart at one minute to midnight and the ticker at one
minute past.

Two more choices that are easy to mistake for oversights:

- **An empty digest is never claimed and never sent.** A weekly mail that
  usually says nothing is a weekly mail nobody opens, and the week it matters is
  the week it gets ignored.
- **A budget line with anything paid against it is skipped.** The schema has no
  "decided" flag, and `paid` is the only commitment signal it has: a deposit has
  gone to the vendor, so the decision this deadline is about has been made. That
  is an interpretation rather than something the schema states, which is why it
  is written down here, in the migration, and in a test named for it.

Recipients are every **active admin**, resolved at send time so an admin added
or disabled between digests is respected without restarting anything, plus
anything `SOIREE_REMINDER_TO` names. One message to everyone rather than one
each, which is what lets the ledger record a single claim per period. Push goes
to the devices of active admins by the same rule.

The two channels get separate time budgets inside the run's two minutes. With
one shared deadline a relay that stalls would spend the whole run before push
was reached, so a broken relay would switch off notifications too; neither
channel is allowed to do that to the other. A push failure never fails the run —
it has already been mailed — and mail failing does not release the claim if any
device was reached, because releasing it asserts that this period reached
nobody.

### Build and supply chain (track 6)

Mostly in place.

Done: reproducible static build with `-trimpath`, version and commit stamped at
link time, `scratch` base with no shell or package manager, unprivileged UID,
`govulncheck` on every PR, SBOM and `mode=max` provenance attached to released
images, Renovate on every dependency including a custom manager keeping the CI
toolchain in step with the Dockerfile.

Also done: **releases are signed** with cosign, keyless via the GitHub OIDC
identity, so a signature proves which workflow in which repository built the
image. **Every action is pinned to a commit digest** with the readable version
kept in a trailing comment, since a moving tag is a supply-chain hole. **The
SBOM is published as a release asset**, not only as an image attestation, so it
can be read without pulling the image. A CI job builds the binary twice on
independent builders with the cache off and fails if the bytes differ.

Both release workflows then re-run the exact verification command
[docs/verifying-releases.md](verifying-releases.md) gives third parties, against
the image they just pushed. If the documented command stops working, the
release fails rather than someone else's admission controller.

Still to do:

- **Verify in cluster.** A signature nothing checks is decoration; the point is
  an admission policy that refuses unsigned images. That belongs in the
  deployment repository, not here.
- **Decide on multi-arch.** The cluster is amd64, so `linux/arm64` currently
  buys nothing and doubles release build time. Worth adding only if someone
  actually wants to run this on a Pi.

Known limitation: the reproducibility check compares the **binary**, not the
image digest — BuildKit stamps a build timestamp into the image config, so
identical inputs still produce different image digests. The signature and
provenance are what tie an image back to its source, not digest equality.

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
  at all, and a screen that says nothing about what to do next is the one most
  people see first.

Explicitly avoided: the generated-template look — a centred hero over three
equal feature cards, a purple-to-indigo gradient, emoji standing in for icons,
uniform 8px rounding everywhere, and stock illustrations. None of that suits a
budget ledger, and all of it reads as unconsidered.

Accessibility is part of this, not a follow-up: contrast that holds in both
themes, visible focus rings, full keyboard operation of the grid, and hit
targets that work on a phone — which is where a good share of this audience
will open it.
