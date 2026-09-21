# Operating soiree

What somebody running this needs that is not obvious from the configuration
table: how to get into a fresh instance, how to generate the one credential the
application cannot get for itself, what each optional subsystem does when it is
not configured, and how to tell from the outside that each one is working.

For what the variables *are*, see the README. For why any of it is shaped the
way it is, see [architecture.md](architecture.md).

## The shape of a running instance

One process, two listeners. The public one carries the site, the probes and —
when a DSN is configured — the API. The second carries `/metrics` and nothing
else, on a port the public ingress does not route to. The two must differ or the
process refuses to start, which is deliberate: the alternative is a bind failure
that names a port without saying why.

Migrations run at startup, inside a Postgres advisory lock, so several replicas
starting at once queue rather than race. The application holds no state of its
own beyond that, so replicas scale freely.

Three probes matter:

| | |
|---|---|
| `GET /healthz` | Liveness. Checks nothing downstream on purpose — a liveness probe that fails when the database is down converts an outage into a restart loop. |
| `GET /readyz` | Readiness. Pings the database when one is configured, bounded at two seconds; `503` and `database unreachable` otherwise. With no DSN it is identical to liveness, or a frontend-only deployment would never become ready. |
| `GET /metrics` | On the metrics listener only. A request for it on the public port is a 404, still counted under `route="metrics"`, which is what makes a stale scrape target or a path scanner visible rather than silent. |

## Reading the startup log

Logs are JSON on stdout via `log/slog`, one object per line, so a healthy start
looks like this:

```json
{"time":"…","level":"INFO","msg":"database ready","migrationsApplied":12}
{"time":"…","level":"INFO","msg":"accounts enabled","mail":true,"passkeys":true,
 "baseURL":"https://soiree.example.test"}
```

The rest are quoted below as their `msg` plus the fields that matter, rather
than repeating the envelope. On a fresh deploy these four say which subsystems
came up:

| `msg` | Fields worth reading |
|---|---|
| `soiree listening` | `addr`, `metricsAddr`, `version`, `commit`, `api` |
| `database ready` | `migrationsApplied` |
| `accounts enabled` | `mail`, `passkeys`, `baseURL` |
| `deadline reminders on` | `schedule`, `windowDays`, `recipients`, `timezone`, `mail`, `push` |

And these say something is off:

| `msg` | Means |
|---|---|
| `no DATABASE_URL set, serving the frontend only and leaving the API unmounted` | No API, no accounts, no live sync, no reminders |
| `deadline reminders are off (SOIREE_REMINDER_ENABLED is not true)` | The scheduler never starts |
| `SMTP is not configured; the deadline digest goes out as a notification only` | Reminders are on with push as the only channel |
| `Web Push is half-configured, so notifications are off` | One or two of the three VAPID variables are set. Logged by the reminder scheduler, so it appears only with reminders on and mail configured |
| `passkeys unavailable, leaving them off` | Logged with `err`; password login is untouched |
| `database schema is ahead of this binary` | A newer release migrated this database and this process is a rollback. It serves, but writes made here skip whatever that release added; `unknownVersions`, `unknownNames` and `earliestAppliedAt` say which release and when. Roll forward, or restore to before `earliestAppliedAt` |

`api` on the listening line is the single quickest check that the browser will
get a shared planner rather than a local one. `migrationsApplied` is the number
applied *by this process*, so on a fresh database it is one per `.sql` file in
`migrations/`, twelve of them today, and 0 on a restart against one that is
already current. `recipients` counts `SOIREE_REMINDER_TO` and nothing else: the
active admins who also receive the digest are resolved at each send, so
`recipients=0` is the ordinary reading on a deployment where the admins *are*
the recipients.

## Getting into a fresh instance

Accounts are created by admins, and a fresh database has no admins. Breaking
that circle is what the two bootstrap variables are for.

```
SOIREE_BOOTSTRAP_ADMIN=ada@example.test
SOIREE_BOOTSTRAP_PASSWORD=<at least 12 characters>
```

Both are consumed only while no admin exists, which is what makes them safe to
leave set: neither can resurrect a disabled account nor overwrite a password
that has since been changed. Setting the password without the address is
refused at startup, because it means somebody believes they have configured a
way in and has not.

On first start you get:

```
bootstrap admin created  email=ada@example.test  next="log in with SOIREE_BOOTSTRAP_PASSWORD, then change it"
```

Without `SOIREE_BOOTSTRAP_PASSWORD` the account is created `invited` with no
password and no link, and the `next` hint instead reads `request a set-password
link from POST /api/v1/auth/password-reset` — which needs working mail. That is
the better shape when mail works, because no credential is written down
anywhere. It is a dead end when mail does not: every route that hands back a
set-password link is admin-only, and `password-reset` gives its link to the
mailer and discards it. That is the whole reason `SOIREE_BOOTSTRAP_PASSWORD`
exists.

Then sign in on the page: *Sign in* in the bar at the top, or `/#/login`
directly, with the bootstrap address and password. The screens live in the URL
fragment, so they can be linked to — `/#/login`, `/#/account` for your own
password and passkeys, and `/#/admin` for everybody's accounts.

*Password* on that screen is where the `then change it` above is done, and it
needs nothing but the password you have: `POST /api/v1/auth/password` with
`currentPassword` and `newPassword`, which is the one way to a new password
that needs no mail at all. It ends every session the account had, because they
were all made with the password that is ending; the answer carries a new
cookie, so the browser doing it stays signed in and every other device is
signed out. Any set-password link still outstanding is spent with them.

The development stack in `compose.yaml` sets both variables itself, so
`docker compose up --build` comes up with an admin already made: sign in at
`http://localhost:8080/#/login` as `admin@example.test` with the password
`local-development-only`. Writing a password into a public file is safe for
that stack alone, whose database is a tmpfs that `docker compose down` throws
away. Nothing that outlives an afternoon should use a credential every reader
of the repository already knows.

Be clear about what the page keeps, because this is a ledger of names against
money. While somebody is signed in, the browser holds a copy of the plan in
`localStorage`; that copy is what lets the page paint at once and work offline.

- **Signing out removes it**, in every tab of that browser, and leaves the
  sign-in screen. If something had not reached the server yet the page tries to
  send it first, and asks before discarding what it could not. Signing out
  needs the server: offline, it says so and leaves the person signed in, rather
  than claiming a sign-out over a session that still works.
- **A session that merely ends does not.** Seven idle days, or an admin
  disabling the account: the copy stays, because that person's unsent edits are
  in it and they are coming back. Whoever opens that browser next sees it. On a
  computer other people use, sign out rather than closing the tab.
- A browser that has never signed in has nothing to show: the server refuses
  the plan without a session, and the page draws an empty planner.

**Replace the bootstrap password once you are in.** It has been sitting in an
environment variable. On the accounts screen, *Send a password link* on your
own row issues a fresh single-use link: mailed to you when SMTP is configured,
shown once on the screen when it is not. Follow it and choose a new password.
The variable can stay set afterwards — it is consumed only while no admin
exists, so it cannot put the old password back.

Creating everybody else is the same screen: an address, a role (`admin`,
`editor` or `viewer`) and the language of the invitation. There is no
self-service sign-up and no open invitation link; an admin creates each
account.

The language is a choice about that one mail. *Same as the planner* means
`SOIREE_LOCALE`. Choosing one — for the Dutch half of a family on a deployment
set to `id-ID`, say — writes the invitation in it and makes the link open in
it. It is not stored. After that first screen the planner follows the reader's
own browser, and there are three flags at the top of every screen for anybody
it guessed wrong about; their choice is remembered on their device. Sending a
link again offers the same choice beside the button, and a reset somebody asks
for themselves is written in the language they were reading the sign-in screen
in.

With SMTP configured the link goes to the person it belongs to and nowhere else
— an admin who never sees it cannot use it — unless an admin asks for it
instead of the mail, which is `"deliver": "link"` on a create or an invite, is
accepted only for an account that has not set a password yet, and is logged as
`account link issued to admin`. A link that goes out by mail is logged too, as
`account link issued by mail`, with the account it is for, whether it is an
`invite` or a `reset`, and the admin who asked. A reset somebody asked for on
the sign-in screen names the account itself, since nobody signed in asked for
it. Neither line ever carries the link. Without SMTP the screen shows the link once,
for the admin to pass on by another route. *Send the link again*
issues a fresh one when the first expires or goes to a mailbox nobody reads;
issuing supersedes whatever was outstanding, so it is also how a leaked link is
revoked.

The link points at `/#/set-password?token=…`. The page takes the token out of
the address bar before it does anything else, asks for a password of at least
twelve characters, and signs the person in. It works once and expires in 24
hours.

All of it is also plain HTTP, described in
[`api/openapi.yaml`](../api/openapi.yaml), for the day the page is not an
option:

```bash
curl -c cookies.txt -X POST https://soiree.example.test/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"ada@example.test","password":"…"}'

curl -b cookies.txt -X POST https://soiree.example.test/api/v1/users \
  -H 'Content-Type: application/json' \
  -d '{"email":"grace@example.test","role":"editor"}'
```

`"mailSent": true` in that response means the link was handed to the mailer;
`false` means the response carries `setPasswordUrl` instead, because there is
no mailer or because the request asked for the link.

### Who may do what

The server decides, and the page only reflects it.

| | reads the plan and its files | writes the plan, adds and removes files | manages accounts, reads the activity |
|---|---|---|---|
| no session | no — `401` | no | no |
| `viewer` | yes | no — `403 read_only` | no |
| `editor` | yes | yes | no — `403 forbidden` |
| `admin` | yes | yes | yes |

The activity screen — every change, who made it, the value before and after —
sits with account management rather than with the plan, deliberately. Each
entry names an account, and the list of accounts is an admin's to read; open to
editors, the feed would hand that list out through a second door, annotated
with what each person did and when.

The guard wraps the whole `/api/v1` subtree rather than each route, so the
event stream and any route added later are covered by being there. A role is
read from the database on every request, not from the session, so demoting or
disabling somebody takes effect on their next click rather than at their next
sign-in.

Every write records who made it: `updatedBy` on the row, and the actor in the
append-only change history, which is what the activity screen reads. Deleting an
account blanks that id everywhere it appears — the history keeps *what* changed
and loses *who*, and the activity screen then says "a deleted account" — so
prefer the status `disabled`, which keeps both.

A session lasts seven days idle and thirty days at most. When one ends under an
open page, the page stops asking, keeps the person's edits in the browser, shows
the sign-in screen with a line saying why, and sends those edits once they are
back.

A refused password login is `login refused` in the log, the way a refused
passkey is, with `method=password` and, when the address named an account,
that account and why it was refused (`wrong password`, `no password set`, or
the status it has instead of active). An address with no account here leaves
the line with nothing on it: the address itself is never written down, and
neither is the password. The two login buckets bound how many of these one
caller can produce, so a run against the sign-in screen shows up as a burst of
them rather than as a flood.

## Generating a VAPID key pair

Web Push has no API key and no account. The server proves who it is by signing
each request with a P-256 key pair it generated itself, and there is nowhere to
get one from — no push service issues credentials. So the operator makes one.

There is no CLI for this, and no subcommand on the binary.
`internal/push.GenerateKeys` is exported so that doing it needs nothing beyond
a checkout of this repository — the dependency is already in `go.mod`, so the
following works offline:

```bash
mkdir -p tmp/vapid && cat > tmp/vapid/main.go <<'EOF'
package main

import (
	"fmt"

	"github.com/Yornik/soiree/internal/push"
)

func main() {
	pub, priv, err := push.GenerateKeys()
	if err != nil {
		panic(err)
	}
	fmt.Println("SOIREE_VAPID_PUBLIC_KEY=" + pub)
	fmt.Println("SOIREE_VAPID_PRIVATE_KEY=" + priv)
}
EOF
go run ./tmp/vapid && rm -rf tmp
```

`GenerateKeys` returns the public half first, matching the order the variables
are documented in. The underlying `webpush.GenerateVAPIDKeys` returns them the
other way round — exactly the sort of thing that gets transposed once and not
noticed — so if you call the library directly, the *first* value is the private
key. Setting all three variables to values that are not two halves of one
P-256 pair is a refusal to start, and a transposed pair is named as such in the
error; a half-configured set is still only a warning.

Then set all three:

```
SOIREE_VAPID_PUBLIC_KEY=…
SOIREE_VAPID_PRIVATE_KEY=…
SOIREE_VAPID_SUBJECT=ada@example.test
```

The subject is where a push service complains to when this deployment
misbehaves: an address or an `https` URL. `ada@example.test` and
`mailto:ada@example.test` are both accepted and mean the same thing.

**Generate the pair once and keep it.** Every subscription a browser has made is
bound to the public key it was made against, so a new pair silently invalidates
all of them: the sends keep being accepted, the logs keep looking healthy, and
not one notification arrives. Nothing in the application can detect this,
because from the server's side a subscription made against an old key is
indistinguishable from one made against the current one until the push service
refuses it — and some will simply accept and drop. If a pair must be rotated,
treat every existing row in `push_subscriptions` as dead and have everybody
subscribe again.

The private key is a secret and belongs wherever the rest of this deployment's
secrets live. It is deliberately absent from the struct that is marshalled into
the page; the public half is published there, and has to be, because a browser
that has not seen it cannot subscribe at all.

A half-configured set is a warning at startup and nothing more, never a refusal
to boot. That is on purpose — but it also means an operator who set one variable
and stopped has a deployment that looks configured and sends nothing. The
warning comes from the reminder scheduler rather than from the configuration,
so it is printed only when reminders are on and mail is configured; with
reminders on and no relay, `deadline reminders are on but neither mail nor push
is configured` names the same three variables instead. With reminders off
nothing reports a partial set at all, so there the three are worth reading back
by hand after a change.

## What degrades, and what refuses to start

The pattern is "optional subsystems degrade to off, never to a startup
failure", with a short list of exceptions where being half-configured is worse
than being absent.

### Refuses to start

| Condition | Why it is fatal |
|---|---|
| `SOIREE_METRICS_ADDR` equals `SOIREE_LISTEN_ADDR` | `/metrics` would be served on the public port after all. Exact string comparison only, so `:8080` and `0.0.0.0:8080` are not caught — the case worth catching is the operator who set one and forgot the other. |
| `SOIREE_SMTP_FROM`, `SOIREE_SMTP_USER` or `SOIREE_SMTP_PASSWORD` set without `SOIREE_SMTP_HOST` | Half a relay looks configured and silently sends nothing. |
| `SOIREE_SMTP_HOST` set without `SOIREE_SMTP_FROM`, or user without password | The failure would otherwise be an authentication error against the relay at the worst possible moment. |
| `SOIREE_SMTP_HOST` set without `SOIREE_BASE_URL` | A mail whose link is relative is a mail that cannot be clicked. |
| `SOIREE_BOOTSTRAP_PASSWORD` without `SOIREE_BOOTSTRAP_ADMIN`, or shorter than 12 characters | There is no account for it to belong to, or the app would refuse the same password from a form. |
| Malformed `SOIREE_EVENT_DATE`, `SOIREE_CURRENCY`, `SOIREE_BASE_URL`, `SOIREE_BUDGET_CEILING`, `SOIREE_ALLOW_INDEXING`, `SOIREE_PUBLIC_EVENT_DETAILS`, `SOIREE_DEMO_DATA` or `SOIREE_TRUST_PROXY_HEADERS` | A value nobody can parse is a typo, and every one of these fails quietly at runtime instead. `SOIREE_PASSKEYS_ENABLED` is the deliberate exception: a value it cannot parse reads as off, because off loses nothing and refusing to start would turn a declined convenience into an outage. |
| `SOIREE_PUBLIC_EVENT_DETAILS` set to `false` without `DATABASE_URL` | The name, the tagline and the date it takes out of the page are sent on the session instead, and with no database nobody signs in. The page would have no heading and no countdown for ever, so the request cannot be carried out. Refusing says so; ignoring it would publish what the operator has just asked to keep back. |
| `SOIREE_CSP` set to anything but `on` or `off` | The Content-Security-Policy the binary sends is what enforces "no inline script, no inline style" in the browser. Read as off, a typo would drop that defence and nothing would say so until something took advantage of it. `off` is for a deployment whose proxy sends a policy instead. |
| Any malformed `SOIREE_REMINDER_*` | A digest that silently never arrives is the same outcome as having no reminders at all, which is the thing the feature exists to prevent. |
| All three `SOIREE_VAPID_*` variables set, but the two keys are not halves of one P-256 pair | The public half is published into a page anyone can fetch, so a transposed pair puts the signing key there — and nothing reports it afterwards: a browser refuses to subscribe against it, so no notification is ever sent and nothing ever fails. A pair that is merely wrong fails at the first weekly digest instead, once the permission prompt has been spent. |
| Some but not all of the five `SOIREE_S3_*` variables | A bucket with no secret starts cleanly, draws the upload control, and fails every upload in somebody's hand. The error names what is set and what is missing. |
| `SOIREE_ATTACHMENT_MAX_MB` or `SOIREE_ATTACHMENTS_TOTAL_MB` not a whole number above zero, or the first larger than the second | No file could ever be that large; it is a typo. |
| The database is unreachable, or a migration fails | There is nothing to serve the API from. A migration error reading `canceling statement due to lock timeout` means another session holds a long lock on a table it alters — an open `psql` transaction, a `pg_dump` in progress — and the migration gave up after ten seconds rather than make the release that is still serving queue behind it. `SELECT pid, state, xact_start, query FROM pg_stat_activity WHERE state <> 'idle' ORDER BY xact_start` finds it; the previous release keeps serving meanwhile. |
| Either listener cannot bind | A pod that looks healthy while every scrape fails is the failure nobody notices until they need the graph. |

### Degrades to off

| Not configured | What happens | How you can tell |
|---|---|---|
| `DATABASE_URL` | No `/api/v1` routes at all — they are never registered, so the paths 404 and fall through to the frontend. The browser probes once, gets the 404, and keeps the planner in `localStorage`. No accounts, no live sync, no reminders. | `no DATABASE_URL set, serving the frontend only…` at startup; `api=false` on the listening line. |
| `SOIREE_BASE_URL` | Passkeys are off — the Relying Party ID cannot be derived from anything else, and a request's `Host` header is not an alternative. Mailed links would be relative, which is why SMTP refuses to start without it. | `accounts enabled … passkeys=false baseURL=`. |
| `SOIREE_SMTP_*` | No mail anywhere. Account creation still succeeds and hands the set-password link back to the admin instead. The deadline digest goes out over push alone. | `accounts enabled … mail=false`; `SMTP is not configured; the deadline digest goes out as a notification only`. |
| `SOIREE_VAPID_*` | No push. The public key is omitted from the page entirely, so the client never offers to turn notifications on. The digest goes out by mail alone. | The config block in the page has no `vapidPublicKey`; `deadline reminders on … push=false`. |
| `SOIREE_S3_*` (all five) | No attachments. The four routes are not mounted and answer the API's ordinary 404; the page's config block has no `attachments`, so no paperclip is drawn; `GET /plan` still carries an empty `attachments` list. The same happens with a bucket and no `DATABASE_URL`. | No `attachments enabled` line at startup. |
| `SOIREE_REMINDER_ENABLED` | No scheduler at all. | `deadline reminders are off (SOIREE_REMINDER_ENABLED is not true)`. |
| Reminders on, but neither mail nor push | Nothing is scheduled. One channel is enough; refusing to run because the *other* is missing would be one channel suppressing the one that works. | `deadline reminders are on but neither mail nor push is configured; no digest will be sent`. |
| Passkeys asked for but unusable | Password login is untouched; the routes are not mounted and the browser is told not to offer the button. Never fatal: an account reachable by a passkey is always also reachable by its password, so refusing to start would turn a missing convenience into an outage. | `passkeys unavailable, leaving them off`. |

A bare IP in `SOIREE_BASE_URL` also turns passkeys off, without complaint: a
credential is scoped to a domain and an address is not one, so a deployment
reached by address gets password login and nothing else.

## Attachments: setting up the bucket

The browser uploads to the bucket and downloads from it directly. soiree only
signs the addresses. Four things follow: the first two and the last are yours
to do, and the third only if a proxy in front of soiree sends a policy of its
own.

**1. A private bucket, and preferably a key of its own.** Nothing in the bucket
is ever public; every read goes through an address that lives for a minute.
The secret signs addresses that are handed to browsers, so if your provider
allows it, give this bucket its own credential rather than reusing the one
that writes your database backups.

**2. A CORS rule that lets your pages `PUT`.** An upload is a cross-origin
request from your site to the bucket, carrying a `Content-Type`, so the browser
asks permission first and the bucket has to grant it. Downloads are ordinary
links and need no rule.

```json
{
  "CORSRules": [
    {
      "AllowedOrigins": ["https://soiree.example.test"],
      "AllowedMethods": ["PUT"],
      "AllowedHeaders": ["Content-Type"],
      "MaxAgeSeconds": 3000
    }
  ]
}
```

```bash
aws s3api put-bucket-cors --endpoint-url "$SOIREE_S3_ENDPOINT" \
  --bucket "$SOIREE_S3_BUCKET" --cors-configuration file://cors.json
```

Without it every upload fails in the browser with a network error, and nothing
appears in soiree's log, because the request never reaches soiree.

**3. Only if a proxy sets a Content-Security-Policy of its own: allow the
bucket in its `connect-src` too, or drop it.** soiree's policy already names
the bucket: it asks the bucket that signs the uploads where it is, so the
policy and the address a browser is handed cannot come to name different
origins. A second policy can still undo that: where both headers reach the
browser it takes the intersection, and a proxy that replaces the header rather
than adding to it leaves only its own policy in force. Either way a proxy
saying `connect-src 'self'` blocks the upload exactly as a missing CORS rule
does, and as silently. The startup log names the origin soiree allows:

```
attachments enabled  browsers_connect_to=https://nbg1.your-objectstorage.com max_file_mb=25 total_mb=2048
```

**4. Check that your provider behaves.** "S3-compatible" is a claim, and the
design leans on three behaviours a provider is free to get wrong: refusing an
upload whose length differs from the one signed into its address (this is what
makes the quota real), honouring a signed `Content-Disposition` and
`Content-Type` on download (this is what stops an uploaded web page being served
as one), and refusing an address after it expires. The same tests the project
runs against a throwaway bucket run against yours:

```bash
SOIREE_TEST_S3_ENDPOINT=https://nbg1.your-objectstorage.com \
SOIREE_TEST_S3_REGION=nbg1 SOIREE_TEST_S3_BUCKET=your-bucket \
SOIREE_TEST_S3_ACCESS_KEY_ID=… SOIREE_TEST_S3_SECRET_ACCESS_KEY=… \
  go test ./internal/objstore -run Bucket -v
```

They write only under `attachments/`, with random names, and delete what they
wrote. If `TestBucketRefusesAnotherLength` fails, do not enable attachments on
that provider: the per-file and total limits would be advice rather than limits.

What soiree does by itself: an upload that is started and never confirmed is
removed after an hour, and its reserved space given back. When a budget line or
a task is deleted its files' records go with it inside the database, which also
writes each object's key into a queue; soiree works through that queue at start
and then hourly, and a key leaves the queue only once its object is confirmed
gone. A bucket that is unreachable for a day loses nothing but time.

## Confirming each subsystem works

### The API and the shared planner

```bash
curl -s https://soiree.example.test/api/v1/plan | head -c 200
```

That carries no session, so a healthy deployment answers `401` with
`{"error":"unauthenticated"}`, which is itself the check: the API is mounted
and guarded, so the DSN reached the process. A `404` means it did not, and the
routes were never registered. A `503` with `{"error":"internal"}` is the third
answer, and the only one that is a fault rather than a deployment shape: a
database was configured and authentication was not, which the API refuses to
serve unguarded.

The plan itself needs a session, so reuse the cookie jar from the login shown
earlier:

```bash
curl -s -b cookies.txt https://soiree.example.test/api/v1/plan | head -c 200
```

A JSON object with `settings`, `phases`, `sponsors`, `budgetItems`,
`programme`, `tasks`, `notes` and `attachments` means the browser will get a
shared planner. A `503` with `{"error":"unavailable"}` and a `Retry-After` means
the database did not answer the session lookup; the page waits that out and asks
again rather than signing everybody out. Watch
`soiree_http_requests_total{route="api-plan"}` climb as people open the page.

Responses under `/api/v1` always carry `Cache-Control: no-store`. If something
in front of the deployment is adding its own caching there, a budget two people
are editing will be served stale from a proxy.

A write that answers `403` with the code `cross_origin` was refused because a
browser said it came from another origin: another site, a sibling subdomain, or
another port of the same host. `curl` and scripts send no such statement and
are never refused. One honest setup can trip it, which is a proxy that rewrites
`Host` on the way in. A current browser is unaffected, because its
`Sec-Fetch-Site` header is believed before anything else, but a browser from
before 2023 sends only `Origin`, that is compared with `Host`, and every write
it makes is then refused. Traefik, Caddy and HAProxy pass `Host` through as it
arrived; nginx's `proxy_pass` replaces it unless told
`proxy_set_header Host $host;`.

### Live sync

```bash
curl -N -b cookies.txt -H 'Accept: text/event-stream' \
  https://soiree.example.test/api/v1/events
```

The stream is under the same guard, so without the cookie jar this is a `401`
and nothing is opened. With it, a working stream answers immediately with a
retry interval, a `hello` and a `resync`, then a `: ping` comment every twenty
seconds:

```
retry: 3000

event: hello
data: {"version":"1.2.1","build":"/assets/app.7d3801be.js"}

event: resync
data: {}

: ping
```

The `hello` names the build answering this connection: the release, and the
script the page it serves would load. Two pods of the same build are
indistinguishable in it, so it says which build a client reached during a
rollout rather than which pod.

Change something through the API from another terminal and an `event: change`
frame should appear within the same second. The server logs
`live sync is listening for database changes` with a `backendPID` naming the
database session carrying it. That connection is opened on the first subscriber
rather than at startup, so the line appears the first time anybody connects. `soiree_sse_subscribers` is the gauge
of connected streams; `soiree_http_requests_total{route="api-events"}` counts
connections rather than events.

Two failure modes are worth recognising. If the heartbeats arrive but changes
never do, the `LISTEN` connection is the suspect — look for `live sync lost its
connection to the database` or `live sync could not reach the database`, which
retry with backoff and never wedge the process. If nothing arrives at all but
the request does not close, something between the client and the server is
buffering the response; the application sets `X-Accel-Buffering: no` for exactly
that, but not every proxy honours it.

`soiree_sse_subscribers` is the number of open, signed-in tabs: every page
holds one `EventSource` for as long as it has a session. It drops when a tab
closes and when a session ends, because the page closes its stream rather than
retrying into a refusal. A number that only ever climbs means disconnects are
not being noticed, which is the proxy's idle timeout more often than it is this
application.

### Mail

The set-password flow is the cheapest end-to-end test: create an account for an
address you can read and see whether the invite arrives. `"mailSent": true` in
the response means delivery was attempted, not that it succeeded — a relay
failure is logged, not returned, because the account exists either way and
implying the whole thing failed would have the admin create it twice.

Nothing about a link is ever logged. If an invite does not arrive, the evidence
is in the relay's logs, not this application's.

### Push

Push works end to end from the page. Knowing how the offer is made saves a
support conversation, because it is deliberately quiet:

- **It goes to admins, and only they are offered it.** The server pushes the
  digest to the devices of active admins and nobody else, so an editor or a
  viewer is shown no switch for it. The mail goes to every active admin
  regardless; push is per device and opt-in.
- **The offer is never made on load.** A denied notification permission is
  sticky in Chrome — undoing it means a trip into the site settings — so the
  page spends its one prompt at the moment the value is obvious: the first time
  an admin gives a task a due date, a line appears under the task list with
  *Turn on* and *Not now*.
- **The switch is on the account screen** (`/#/account`, *Reminders*), for
  everything the offer cannot do: turning them on without setting a due date,
  changing one's mind after *Not now*, and turning them off again. It reads the
  browser's own subscription, so it is right on each device separately. If the
  browser's prompt was refused it says so and offers no button, because only
  the browser's site settings can undo that.
- **On Android, Chrome is enough.** On an iPhone the site has to be added to
  the Home Screen first; Safari does not offer web push to a tab. The switch
  says so, with the steps, and says it only to an iPhone or iPad.
- **When there is no button, the line above where it would be says why**, and
  two of its sentences are about the site rather than the browser. *Still being
  set up* is a service worker that has not finished installing; it replaces
  itself. *Could not start on this device … the fault is with this site* means
  `navigator.serviceWorker.register('/sw.js')` was rejected for a reason other
  than the browser's own settings: open `/sw.js` and the browser's console,
  because that is a broken worker and nobody on that release has reminders or
  the offline copy. A top-level domain has nothing to do with any of it — Web
  Push needs https, a service worker and the browser's push service, and none
  of those looks at the name.

To check that it is working:

- The page's config block contains `vapidPublicKey`. If it does not, the
  server does not consider push configured — check all three variables.
- After *Turn on* the page says reminders are on for this device, and
  `push_subscriptions` has a row for that account.
- With a session, `POST /api/v1/push/subscriptions` accepts a `PushSubscription`
  object in the exact shape `JSON.stringify()` produces it (`endpoint`, and
  `keys.p256dh` plus `keys.auth`), and answers `204`. It is idempotent on the
  endpoint, so posting the same subscription on every page load is correct
  behaviour rather than a leak.
- After a digest, `deadline digest pushed` reports `devices`, `delivered` and
  `gone`.

`gone` is the count of subscriptions the push service said no longer exist —
`404` or `410`, which are final answers — and those rows are deleted. Anything
else is transient and the row is kept, and shows up as
`could not notify a device; keeping the subscription` with the account id and
the service's host. Note that a rotated VAPID pair does *not* appear here: the
package's own reading is that sends keep being accepted and nothing arrives, so
`delivered` stays healthy and `gone` stays at zero. There is no log line for
that failure, which is the whole reason the key is worth keeping.

### Attachments

On a budget line that has been saved, press the paperclip beside its name, add
a small file, and watch it appear. Then open the planner in a second browser:
the count beside the paperclip is there without a reload, and the file
downloads under its own name.

| Symptom | Cause |
|---|---|
| No paperclip anywhere | Attachments are off: look for `attachments enabled` in the startup log. Also absent for a row that has not reached the server yet, and on a deployment with no database. |
| "The upload did not get through", instantly, and nothing in soiree's log | The browser was not allowed to reach the bucket: the CORS rule or `connect-src`. The browser's console says which. |
| "The upload failed" after the bar reaches 100% | The bucket refused the body, or the server could not confirm it. `could not check an uploaded file` in the log is the bucket not answering; the page retries that by itself. |
| `an uploaded file is not the size that was declared` in the log | Your provider stored a body of a length other than the one signed. Run the provider check above. |
| `could not delete a file yet; it stays queued` | The bucket was unreachable. It will be retried within the hour. |

### Reminders

The scheduler runs once at startup and then once per schedule, which is
deliberate: pods restart far more often than a weekly ticker fires, so a ticker
that only fired after 168 uninterrupted hours would never fire at all. It is
safe because the period ledger makes a second run within the same period a
no-op.

So the quickest test is a restart with something due inside the window. Every
outcome is a log line, and a sample on the counter below:

| `msg` | Means |
|---|---|
| `reminder digest sent` | It went out. Carries `recipients`, `devicesReached`, `items` and `overdue`. |
| `nothing due; no reminder sent` | Nothing fell inside `windowDays`, so nothing was claimed either |
| `reminder digest skipped: another replica is sending it` | A second replica lost the advisory lock, which is the intended outcome |
| `reminder digest skipped: one went out too recently` | A period boundary was crossed within half a period of the last send. Carries `lastSent` and `minimumGap` — 84h on a weekly schedule. |
| `deadline digest has nothing due to nobody` | There is something to say and no active admin and no configured recipient |

An empty digest is never sent and never claimed, so "nothing due" on a plan with
deadlines means the window or the timezone is not what you think — or that the
lines in question have something paid against them, which is read as the
decision having been made and takes them out of the digest.

The one that should never be ignored is
`reminder digest for this period was claimed but never confirmed sent; not
retrying`. That is the single case where a digest is lost: something died
between handing the message to the relay and hearing back. It is not retried on
purpose, because the alternative risks sending it twice.

The ledger is readable directly if you need to know what a period did:

```sql
SELECT period_key, claimed_at, sent_at, recipients, item_count
  FROM reminders_sent ORDER BY claimed_at DESC LIMIT 5;
```

A row with `sent_at` null is a claimed-but-unconfirmed period.

Every outcome is also a sample on `soiree_reminder_runs_total{outcome}`, which
is what an alert can be written against. `sent`, `nothing_due`,
`already_sent`, `cooloff`, `not_leader` and `no_recipients` are the runs
nobody has to act on. The four that somebody does are `unrecorded` (it went
out, and the ledger write afterwards failed), `uncertain` (the relay never
acknowledged it), `lost` (a period claimed by an earlier run that never
confirmed it) and `failed`. `soiree_reminder_last_run_ok` is 1 after one of the
first six and 0 after one of those four. Both series are declared by the
scheduler, so a deployment with reminders off exports neither, and all ten
outcomes exist at zero from startup so that a failure on the very first run
still reads as an increase.

`no_recipients` is counted and left out of the gauge on purpose. It means
there is something due, and no active admin and no configured address to tell
about it, which no later run resolves and no restart clears, so it would hold
the gauge at 0 until somebody edits the configuration. Alert on
`increase(soiree_reminder_runs_total{outcome="no_recipients"}[1d]) > 0` if you
want to hear about it.

Alert on `soiree_reminder_last_run_ok == 0` for ten minutes. The delay is for
the pod that has only just started and whose first run is still in flight,
which is bounded at two minutes. To respond, read the outcome off the log line
and the period off the ledger: `failed` may have released the claim, in which
case the next run or a restart sends it, while `uncertain` and `lost` keep it
on purpose and the period is gone.

Do not alert on the time since the last digest went out. A plan with nothing
due legitimately goes weeks without one, so a quiet fortnight and a dead relay
look the same.

### Passkeys

`accounts enabled … passkeys=true` at startup means the routes are mounted and
the browser is told to offer them. The Relying Party ID is derived from
`SOIREE_BASE_URL` with only a leading `www.` stripped, so a deployment reached
at a host that is not a registrable-domain suffix of that value will register
credentials nobody can use again — and the browser's only report will be "no
credentials available". If passkeys stop matching after a domain change, that is
the first thing to check.

A person's own credentials are at `GET /api/v1/auth/passkeys` with their
session. There is no admin view of anybody else's, by design.

When a passkey fails on somebody's device, there are two places to look, and
which one has something in it says where the failure was. Both failures are in
the log, under different names:

- **The page** names the kind of refusal the browser gave, in brackets after a
  sentence about it — `(NotAllowedError)`, `(SecurityError)`,
  `(InvalidStateError)` and so on. A message like that means the browser never
  produced a credential and the server was never asked. `NotAllowedError` within
  a second of the tap is the browser declining to open a prompt at all; later
  than that, it is somebody closing one, or a timeout.
- **The log** has `passkey login refused` or `passkey registration refused`
  whenever the server was asked and said no, with `step` (`parse`, `challenge`,
  `challenge-owner`, `challenge-ceremony`, `verify`, `counter`) and, for
  `verify`, the library's `kind`, `err` and `detail`. The person sees only "that
  passkey did not sign you in"; the reason is here and nowhere else. No line
  carries an address.
- **The log also has `passkey refused by the browser`** for the first kind, the
  one the server is never asked about. The page reports it
  (`POST /api/v1/auth/passkeys/report`): `ceremony`, the error's `kind`, and the
  browser's own `message` - which the page does not show, and which is what
  tells one `NotAllowedError` from another ("the page does not have focus", "a
  request is already pending", or the catch-all for a closed prompt).
  `elapsedMs` is how long the browser took to refuse; `prepared` is whether the
  page had the options before the tap; `focused` is whether the page had the
  focus when it asked; `agent` is the user agent. The route is public, so
  everything in the line is cut to length and to printable ASCII, and it has an
  allowance of ten a minute for an address. A reverse-proxy rule that counts
  401-403 on the login routes never sees it: it answers 204, always.

## Upgrading and rolling back

Upgrading is deploying the newer tag. The new process applies the migrations it
carries before it listens, so for the length of a rollout the release before it
is serving against the new schema. That works because every migration so far
only adds: new tables, and in `0009_phase_revision.sql` new columns with
defaults. Nothing in CI enforces that, so the rule that keeps it true lives in
CONTRIBUTING, under "Migrations are append-only".

Rolling back is deploying the older tag, and the schema does not come back with
it. The older binary applies nothing, ignores the `schema_migrations` rows it
has no file for, and warns at every start with `database schema is ahead of
this binary` (above, under "Reading the startup log", with the fields that say
which release ran and when). Then it serves. What the newer release added is
dormant rather than lost, and writes made while rolled back are recorded
without whatever that release would have added to them.

Deletion is the exception. An attachment row cascades from the budget item or
task it hangs off, so deleting one of those while rolled back takes its files
with it, and the sweeper takes their objects out of the bucket within the
hour. Rolling forward does not bring those bytes back.

Two rules on top of that:

- Never go below v1.0.0. Those images mount `/api/v1` with no authorisation at
  all, which [SECURITY.md](../SECURITY.md) states as plainly as it can.
- Rolling the image back does not undo what a release *did*. For that, restore
  the database to before that migration's `applied_at` in `schema_migrations`,
  which is the `earliestAppliedAt` the warning above prints, and read the next
  section first.

A start that refuses with `migration NNNN was modified after it was applied`
means this image and this database disagree about the history. Deploy a
published tag rather than something built locally from a branch, and never edit
a `schema_migrations` row to quiet the message.

## Backup and restore

The container holds nothing. State is in PostgreSQL — and, if attachments are
on, in the bucket. Back up the database the way the rest of your platform does.

**The database backup does not contain the files.** It contains their records:
which row each belongs to, its name, its size. The bytes are in the bucket, and
nothing here copies them anywhere. If losing them matters, turn on versioning
or replication at your storage provider; that is the bucket's backup, and it is
separate from the database's.

Restoring the two to different moments is safe but not seamless. A database
older than a deletion lists a file whose object is gone: its download answers
404 and everything else works. A database older than an upload does not know
the object exists: it stays in the bucket, unreferenced, until somebody removes
it by hand — soiree only deletes objects it has a record of deleting.

Two things are worth knowing when restoring. The migration runner is
idempotent and advisory-locked, so bringing replicas up against a restored
database is safe. And `SOIREE_BOOTSTRAP_ADMIN` will not recreate an admin that
exists in the restored data, which means a restore of a database whose only
admin was disabled leaves no way in — re-enable the account in SQL, or restore
to a point where one was active.

A restore rewinds the accounts along with the plan, which is the part to settle
before anybody is let back in. Sessions are ordinary rows, so a sign-out, a
disabled account, a demotion or a password change made after the restore point
is undone, and the cookie each of those invalidated works again for whatever it
has left of its week idle and its thirty days in all. Run `DELETE FROM
sessions;`, which costs everybody one sign-in, and re-apply by hand every
disable, demotion and password reset made since. Set-password links redeemed
since then are usable again until their 24 hours run out, which is also the way
back in for somebody whose new password the restore has just discarded.

Three smaller ones:

- **An upload in flight at the restore point.** Its row is `uploading`,
  invisible in the plan, and the sweeper runs at startup as well as hourly, so
  the row and its object are gone within seconds of the process coming up. To
  keep such a file, confirm the object is in the bucket at the size the row
  claims and run `UPDATE attachments SET status = 'ready' WHERE id = '…';`
  before soiree starts, not after.
- **A passkey registered after the restore point.** The device still offers it,
  the restored database has no record of it, and the answer is the same 401 as
  a wrong password. Sign in with a password and register it again; the device
  normally overwrites the old entry. No restore can refuse a passkey over its
  signature counter: a rewind only ever moves the stored count backwards, and
  the check refuses the opposite.
- **The deadline digest.** If this period's `reminders_sent` row is one of the
  things the restore lost, the digest can go out a second time. Push
  subscriptions need no attention: each browser re-posts what it holds on its
  next visit.

The restore drill itself has not been done. A backup that has never been
restored is not a backup, and that item is still open on the roadmap.
