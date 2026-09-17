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

## Roadmap

State is currently per-browser. Shared state is the point of the project.

1. ~~Configurable single binary, no third-party requests~~ — done
2. **Postgres + REST API.** Schema and migrations embedded, applied at startup.
   `Store` becomes async.
3. **Accounts and roles.** Session cookie, argon2id hashes, roles enforced
   server-side. Sign-up is by **invite link**, not email — see below.
4. **Live sync.** SSE over one long-lived connection, so updates cost no extra
   handshakes. Writes go per-field with a revision check; a stale revision
   returns 409 and the client refetches.

Step 4 is where the current last-write-wins behaviour gets fixed. It is
acceptable for a single-user browser store and is not acceptable once two
people edit at once.

### Accounts (planned, step 3)

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
