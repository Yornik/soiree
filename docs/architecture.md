# Architecture

One Go binary serves the whole application. The frontend is embedded with
`//go:embed` and processed at startup; there is no separate asset build and no
runtime disk access.

The main pieces:

```
cmd/soiree/main.go      wiring, signals, graceful shutdown
cmd/soiree-import/      the spreadsheet importer, a separate binary
internal/config/        environment parsing and validation
internal/httpd/
  assets.go             hashing, compression, content addressing
  server.go             routing, cache headers, conditional requests
  api.go                the REST API's generic verbs and error shape
  api_entities.go       one descriptor per table
  api_json.go           wire types; money, dates, partial updates
  api_settings.go       the singleton's own PATCH
  activity.go           the admin's read of the change history
  attachments.go        the signed upload and download surface
  auth.go               login, set-password, the admin's view of accounts
  authmw.go             session resolution, roles, per-IP limits
  ratelimit.go          the buckets those limits are kept in, and their keys
  passkeys.go           the WebAuthn surface
  push.go               storing and removing a browser's push subscription
  sse.go                the change fan-out behind GET /api/v1/events
  metrics.go            Prometheus collectors and request instrumentation
internal/store/         typed data access, change history, LISTEN/NOTIFY
internal/reminders/     the deadline digest and its scheduler
internal/push/          the Web Push transport
internal/mailer/        the SMTP transport
internal/objstore/      presigned S3 addresses, no SDK
internal/sheetimport/   the .ods / .csv reader behind cmd/soiree-import
internal/auth/          Argon2id hashing, one-time token minting
internal/migrate/       advisory-locked migration runner
internal/pgtest/        a throwaway Postgres for the tests
internal/s3test/        a throwaway MinIO for the tests
web/embed.go            //go:embed of web/src
web/src/                index.html, styles.css, app.js, auth.js, sw.js,
                        the manifest, fonts/ and icons/
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

1. **Leaf assets** — the font, the favicon and the four PNG icons are hashed
   first.
2. **Stylesheet** — its `url('bricolage-display.woff2')` is rewritten to the
   font's hashed filename, *then* the stylesheet itself is hashed. Doing it in
   this order is what keeps the font reference from 404ing.
3. **Application scripts** — `app.js` and `auth.js`, each hashed.
4. **Manifest** — templated (it names the event and references the hashed
   icons), then hashed.
5. **HTML shell** — templated with the config and the hashed asset URLs.
6. **Service worker** — templated with the shell's hash as a cache version, so
   a new build retires the previous cache automatically.

The shell is HTML and goes through `html/template`. The manifest is JSON and
the worker is a script, so they go through `text/template`, and every value
they take is written with the `json` template function. Do not move them to
`html/template`: it escapes a template's own text as the text of a page, and a
`<` in the worker reaches the browser as `&lt;`, which is a syntax error that
no server-side check reports. Only a browser that runs the worker finds it:
`e2e/tests/service-worker.spec.js` does, and so does the test of the reminders
switch in `api.spec.js`; every other spec blocks service workers.

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
own details is capability signals, one per subsystem the page can only draw a
control for when the deployment has it: the VAPID **public** key, and only when
the server could actually send with it; a boolean saying whether to offer a
passkey button; and, when files can be attached, the per-file size limit. The
private key is deliberately absent from the type, the passkey flag names no
domain and carries no key, and the attachment entry names no bucket and no
endpoint. None of them reveals anything a request to the login page would not.
The passkey flag also has to agree with whether the routes are actually
mounted, which is why `cmd/soiree` clears it when there is no database: a
button whose route is a 404 is worse than no button.

The database DSN is `DATABASE_URL` and not `SOIREE_DATABASE_URL`, deliberately.
It is the name every Postgres tool and the CloudNativePG connection secret
already use, and it is not part of the event's identity, so it does not belong
inside the `SOIREE_*` de-personalisation boundary.

`SOIREE_EVENT_DATE` is rejected unless it carries a timezone. A bare
`2027-06-12T00:00:00` is parsed in the *viewer's* timezone, so the countdown
reads differently depending on where someone is — the exact failure mode this
application is most exposed to.

The offset is kept, not normalised away. Until 1.1.1 the loader converted the
value to UTC, which preserves the instant and loses the day: midnight on the
12th at +09:00 became 15:00 on the 11th, the page announced the 11th to every
reader, and the archive would have closed the planner on the morning of the
event. The event is on a calendar day in a place; the page takes the day from
the text and reckons "today" at the event's offset, so neither depends on where
the reader is or on UTC.

### The shell is a page anybody can fetch

Inlining that block also puts the event's name, tagline and date into a page
served to whoever has the address. For most deployments that is what a masthead
is for. For a deployment that treats the event's identity as personal it is
not: such a deployment keeps the name, the day and the place out of everything
it can, and its hostname is then usually the one thing about it that is public.
One visit to that hostname hands all three back. `SOIREE_ALLOW_INDEXING` does
not help, because `X-Robots-Tag` says nothing to somebody who already has the
URL.

So the decision above was revisited and this switch approved, with the default
left exactly where it was: `SOIREE_PUBLIC_EVENT_DETAILS`, on unless an operator
turns it off. Off:

- The shell, the manifest and the config block are rendered with the product's
  name and nothing else, and `ClientConfig` carries no event name, tagline,
  date or ceiling seed. The capability signals stay, because they say what this
  deployment can do rather than whose evening it is.
- `GET /api/v1/auth/session` and the two logins carry an `event` object
  instead. Those three because the page is waiting for one of them anyway
  before it knows who is reading, so the round trip the inline block exists to
  save is still saved. It is a different audience, not a different cost: the
  shell answers a stranger, this answers a session.
- The page keeps those fields beside the plan in `localStorage` and reads them
  before the first render, because it paints the cached copy first and asks
  afterwards. Without that, a repeat visit flashes a nameless heading; worse,
  `isPast()` is false while the date is missing, so a settled planner would
  offer itself as editable to a browser that is offline. They are cleared with
  the plan when somebody signs out.
- It needs `DATABASE_URL`. With no accounts nothing could ever deliver them, so
  the process refuses to start rather than quietly publishing what an operator
  has just asked to keep back.

Three things the switch cannot do, and all of them are worth knowing before
turning it on. The manifest is one rendering for everybody, so an installed app
is named "soiree". Somebody following a set-password link is not told which
event they are joining until they are signed in. And the account mails go on
naming the event, in the subject and in the first line, because they are
written that way deliberately: see the mail section below. So an invitation to
a mistyped address still tells a stranger whose planner this is. Of the two
reasons that naming was judged to cost nothing, this switch takes one away, in
that the page behind the link no longer names the event to an anonymous
visitor. The other stands: a stranger holding a mistyped invitation is holding
a working credential besides.

## The `Store` seam

Everything in this section describes `web/src/app.js` as it stands at the 1.0.0
tag. The frontend is the half still moving, so where it and the code disagree,
the code is right.

All persistence in `web/src/app.js` goes through one object:

```js
Store.read()        // -> what was saved, or null if nothing was yet
Store.write(state)  // persist the whole state object; false if it was refused
Store.keep()        // persist it again, because the merge base moved
```

Nothing else touches `localStorage`. `save()` wraps `Store.write` and is
debounced by 500 ms, flushed on `pagehide` and on `visibilitychange` — but only
where a debounce is actually pending, because the write is the whole state and
a tab that has typed nothing has nothing to add to what is already there. That
seam is what let the network be added behind it without touching any of the ~28
mutation sites: they call `save()` and know nothing about where the state goes.

A `setItem` the browser refuses is answered rather than swallowed. Site data
blocked for the origin throws on every write, and in the deployment with no
database that write is the planner: the page used to carry on looking saved and
keep nothing at all. Once the origin's `404` says there is no server either,
the status line says so and asks for an export, and takes it back when a write
gets through again.

### Two modes, decided once at startup

The page asks the origin for `GET /api/v1/plan` exactly once on load. A `404` is
the final answer for a deployment with no database — those paths are never
registered, so asking again would only be a second `404`. Anything else that is
not an answer is asked again for as long as the page is open, doubling from a
second to the thirty-second cap the write loop uses, and at once when the
browser reports `online` or `auth.js` reports a sign-in. `200`, `401` and `404`
end the asking, exactly as they do when they arrive first, and a `200` merges
what was typed meanwhile the way a sign-in does.

It used to be four retries and then silence. That was written for a browser
offline on a first visit, but an origin that is away at load is the ordinary
start for an installed planner, because the service worker paints the shell
with no network at all. A page that gave up stayed a local-only planner for the
rest of its life: the write loop does nothing until a plan has arrived, so
every edit went to `localStorage` only and nothing said so. Now the status line
says, after the second failure, that the changes are not reaching the server,
as it does for a write that cannot get out. Only when the cached copy holds
rows under server ids, though. A planner that has only ever lived in this
browser may belong to a deployment with no database, and is told nothing about
a server it may not have.

There is one connection at a time. Two things want one on an ordinary signed-in
load — the page starting, and `auth.js` reporting the session a moment later,
while the first request is still in flight — and letting both through fetched
the whole plan twice on every load, the largest request the page makes, from a
server that may be 300 ms away. It was also a race: both answers reached
`adopt()`, the second reset the shadow while the first one's `POST`s were
landing, and a planner being carried up to an empty database was carried up
twice.

A `401` is a third answer and must not be read as either of the others: it is a
deployment that *has* an API, which wants a session. Falling back to
`localStorage` on it would be the worst of both — edits would look saved, live
in one browser, and never reach the plan everybody else is reading. The page
holds, and connects properly when `auth.js` announces a sign-in on
`soiree:session`.

The other question asked at load, `GET /api/v1/auth/session`, is held to the
same rule. `200`, `401` and `404` are answers. Anything else draws the sign-in
door, which is the right thing to offer meanwhile, and is asked again on the
same backoff, and at once on `online` or when `soiree:api` says the planner has
just heard from the origin. It used to be drawn as signed out for the life of
the page, on the grounds that the planner works either way. It did not: the
planner retried, got its plan and ran fully synced beside an account bar that
said "Sign in", with no admin links and no lock on a viewer's ledger, which is
only applied on a session; and when the plan had arrived first, "signed out"
stopped a running planner and told somebody with a good session to sign in
again. An outage is not a sign-out at load either. A `401` is an answer and is
never asked about twice.

- **No API.** `localStorage` is the planner. One browser, one copy, no network
  after the probe. A self-hoster without Postgres, and `docker run` with no
  arguments, both land here and both work. One copy means one copy between the
  tabs too: each holds the whole state and each save writes the whole of it, so
  a tab that hears another one save reads what is now there rather than going
  on drawing what it replaced — see the `storage` event below.
- **API.** The server is the planner. `localStorage` stays as the cached copy
  that paints before the plan arrives, plus the handful of fields that have no
  column behind them, plus the shadow that copy was last agreed against — see
  below.

`localStorage` is written synchronously and *first* in both modes. `pagehide`
has no time to wait on a promise, and the whole point of a deferred write is
that the round trip is not on the interaction path.

### A tab that was left open

The page asks to be installed, and a reminder tapped on the home screen focuses
the window that is already there rather than loading a new one, so a planner
open for days is the ordinary case. Nothing between renders reads the clock: a
tab open across midnight kept yesterday's "days to go", and the day after the
event it did not close the ledger. So `visibilitychange` to visible, a
`pageshow` from the back/forward cache and the browser's `online` all call
`applyMode()`, which re-reckons the day, the run-up and the archive lock
together and is the same call every plan merge already makes.

Where there is a database they also catch the page up. A pending write retry is
brought forward — cleared first, because the resync defers itself for as long
as that timer is booked, and without resetting the failure count, so the wait
resumes if the origin is still away — and after an absence of more than 45
seconds the plan is re-read. Most sleeps and network changes end with the
browser noticing the dead socket and reconnecting, and every connection opens
with a `resync`; the case this covers is the socket that is half-open, where
`onerror` never fires and the heartbeat is a comment no script can see.

### Signing out forgets the plan

The copy in `localStorage` is a ledger of people's names against money, on
whatever computer somebody used. A sign-out somebody asked for removes it —
`forgetPlan()` clears the key, empties `state`, and puts the page back to one
that has never met the server, so the next sign-in takes `adopt()` and the
server's plan rather than a merge against a shadow of something it no longer
holds. Other tabs of the same browser hear about it through the `storage` event
and let go as well; otherwise the first one to save would write it straight
back.

The same listener answers the other thing that event reports, a tab that
*wrote* the key. With a database that settles itself and is ignored here: the
server is the planner and the stream says what changed. With none it is adopted
— parsed, normalised and drawn — because the key is the planner, and the tab
that wrote it last is simply what there now is. On the latched `404` only, not
on `apiMode`, which is also false before the first plan arrives and during a
`401` hold; and not while this tab has a keystroke of its own inside the
debounce, since that write is the newer one.

It is the one action in the page that can destroy an edit, so it goes in a fixed
order. `auth.js` asks `window.soiree.beforeSignOut()`, which flushes the
debounce, gives the write loop a few seconds, and answers with the number of
changes that still exist nowhere else — waiting to be sent, or refused and
parked. Above zero, the person is asked. Then the server is told, and only a
`204` counts: wiping the page while saying "signed out" over a cookie that still
works would be a lie on exactly the computer where it matters.

A session that merely *ends* is the opposite case and leaves everything alone —
see below. The two are told apart by the `reason` on `soiree:session`.

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
- **A `401` is the answer; nothing asks twice.** The planner tells `auth.js` on
  `soiree:session-check`, marked *definitive*, and `auth.js` signs the person
  out and opens the sign-in screen at once. It used to re-probe
  `GET /auth/session` first, which was a round trip spent hearing the same
  answer again — 300 ms from the far side of the world — and a second request
  that could be lost: when it was, the status line said "sign in again" and no
  sign-in screen ever opened. Only a real doubt is asked about. A refused
  `EventSource` reports no status at all, so it raises the event unmarked,
  `auth.js` probes, and only a `401` signs anybody out — an outage is not a
  sign-out — while the stream keeps its reopen booked in case the cause was the
  subscriber cap. A doubt raised while one is already being asked about is
  asked again afterwards rather than dropped.
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

Five consequences worth stating, because each is easy to undo:

- **The shadow outlives the page.** It is written to `localStorage` with the
  state, inside the one `soiree.v1` value and in the same `setItem` — never
  under a key of its own, because two writes are two moments and a state paired
  on the next load with a base another tab wrote differs from it in ways
  neither of them edited. It is written again whenever it moves without
  anybody typing: a confirmed write, a merged plan. Without it the next load
  has no way to tell an unsent edit from a copy that is merely old, so
  `adopt()` replaces the copy with the plan and the edit goes with it — under a
  status line that has just said it is safe in this browser. With it, the next
  load computes the same difference the last page was holding and sends it.
- **A `409` is a three-way merge, not a refetch.** The shadow is the version
  *both* edits started from. A field this browser did not touch takes theirs; a
  field it did keeps ours and goes again on the next pass. The obvious
  alternative — set the shadow to `current` and re-diff — sends the *old* value
  of the field they changed straight back and quietly undoes them. A field
  *both* sides changed is the case the rule above cannot settle: a list of ids
  is merged as the set it is, and anything else keeps ours and says so, because
  the other value is gone and "both were kept" would not be true.
- **A `4xx` that is not a `409` parks the row** rather than retrying it. The
  server understood and said no; an identical body would only earn an identical
  refusal. The edit is not lost — it is in `state`, on the screen and in
  `localStorage` — and the person is told it has not left the browser, for as
  long as it has not: a parked row is a condition rather than news, so it holds
  the status line the way "the server is away" does instead of passing in a
  six-second flash. Which is why what is parked is read back out of the
  differences the page is holding rather than out of the list of refusals: the
  person changes the row, the write is no longer the one that was refused, it
  goes and is taken, and the line has to have nothing left to say. A `404` is
  the exception, because it is not about the edit at all: somebody else removed
  the line, and the re-read that the same removal announces takes the row off
  this screen too. There is nowhere left to keep the work, because the row has
  no server side to write it to and posting it back would undo a removal
  somebody meant, so the person is told the changes went with the line rather
  than promised a copy the next read will drop.
- **Ids are reconciled, not assumed.** The page mints an optimistic id the
  moment a row appears, because the row has to be addressable before any round
  trip could have answered. The `POST` also carries a uuid the page chose for
  the row, and the server stores the row under it: nothing in the browser can
  tell an answer that never arrived from a request that never went, so a create
  whose answer is lost is sent again, and a create this server has already done
  then comes back as the row it stored, with a `200`, rather than as a second
  line that every total counts. The optimistic id keeps its own shape and is
  not that uuid, because the shape is what tells a reload which rows have ever
  been sent. Adopting the id the answer carries is a rename everywhere the old
  id was referred to — otherwise a budget line keeps pointing at a sponsor id
  that only ever existed in this browser. The rename also lets go of a copy of
  the row that a plan read brought in under that uuid while the answer was
  missing, because one id held by two rows is a difference no later pass can
  settle.
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
what a typed field gives back; every sum accumulates whole minor units as
integers and converts once at the end. The fields are `type="text"` read by
`parseAmount`, not `<input type="number">`: a number field reads a comma or a
point by the language of the browser's own menus, which the page cannot see,
so "45,50" arrives as 4550 on English menus and "1.500.000" as 1.5 on any of
them.

```
{
  ceiling, inflationPct, fxRate, splitEvenly, reopened,
  colWidths: [9 numbers], rowHeights: { itemId: px },
  sponsors:    [ { id, code, name } ],
  budgetItems: [ { id, item, vendor, unit, qty, paid,
                   sponsors: [sponsorId], note, lockBy } ],
  tasks:       [ { id, name, owner, due, status } ],
  notes:       [ { id, text } ]
}
```

`status` is one of `not-started` | `in-progress` | `done`. Line total is
`unit * qty`; outstanding is `total - paid` **per line, floored at zero**, and
anything paid above a line's total is reported separately as overpaid rather
than netted against a line nobody has paid. A budget line with more than one
entry in `sponsors` is a shared cost, and `splitEvenly` decides whether the
breakdown divides it between them or reports it as a shared bucket. A share of
a shared line is allocated in whole minor units, with the remainder going to
the first names on the line, and the whole-unit figures a list shows are
allocated against the total above it, so the rows add up to it and the
percentages to 100. The allocation runs in one fixed order, the sponsors as
they are listed and then whatever is unassigned, and a list sorts for display
only once its figures are decided: a leftover unit goes to a row by its
position, so a list that sorted first would give it to a different name than
the figure beside that name in the sponsor grid. In the final reckoning the
paid column is allocated against its own total and then held to the share it
stands against, so it can add to a whole unit less than the Paid headline.
That is the lesser of the two, because the alternative reads as a person
owing less than nothing.

`colWidths`, `rowHeights` and `reopened` have no column behind them and survive
an adopted plan untouched. For the first two that is a decision: one person
dragging a column must not resize it for everybody. `reopened` is there for a
smaller reason, which is that there is nowhere else to put it. Lifting the
archive lock is therefore a per-browser guard against editing the record by
accident, not a decision the event carries, and the banner says as much in all
three languages.

`vendor` and `lockBy` are fields of the line with no column of their own: the
nine widths above are measured against the width of the page, so two more can
only be paid for out of the remark and the money. They sit behind a button in
the first cell instead, which also carries the mark for a line whose decide-by
date has gone by with nothing paid against it, which is the same rule the
reminder digest reads the column by.

`position` is a field of a budget line like any other, and of no other
collection: this is the one table with an interface for it, which is a pair of
arrows in the last cell of every row. A move renumbers the whole list from zero
rather than swapping two numbers with each other, because two rows may hold the
same one (the column has no unique constraint, and a row the API created
without a position sits at 0), so a swap of a pair of equal numbers would move
nothing. Only the rows whose number really changed become a patch, which for a
move of one line is two of them. The grid draws by `position, id`, the order the
server reads every collection in, so a duplicate between two browsers is still
drawn the same way in both. A row saved by a release that had no such field
takes the number the merge base holds for it rather than the place it sits in
the list: the two part company the moment a line is deleted, and numbering by
the list would be a move of everybody's rows, made by a page nobody had
touched.

The search above the grid is in none of that. It is a variable beside `state`
and deliberately not in it, because everything in `state` is saved, sent and
merged, and one person looking for the caterer must not narrow the grid for
everybody else. It reads the item, the remark, the vendor and the callsigns
covering the line, which is what makes "everything I am paying for" a name
typed into one box; the names already on the lines are offered as a
`<datalist>` so that a vendor written once is not written a second, slightly
different way. The totals are the plan's throughout: a search is a way of
reading the ledger, never a way of changing what it comes to. Moving a line
while one is on moves it past the neighbour that can be seen, and the lines it
is hiding keep their places.

### The one case where the browser wins

Normally the server is simply right and what is on screen is a cached copy. Two
cases are not normal, and in both the browser keeps what it is holding and sends
it up instead: something was typed between the cached copy painting and the plan
arriving, or this browser holds a planner somebody actually built and the server
has never held one. The second is the database being added to a deployment that
was running without one, and replacing that planner with an empty plan would
destroy the only copy of it, on the first load, with no warning.

It is deliberately narrow — only a planner that was genuinely saved, never a
blank one or generated demo data, only against a plan with nothing in it, and
only where those rows may be the only copy of themselves.

That last test is not the same as the plan being empty, which is where this
rule was too wide for its own argument. A plan somebody emptied looks on the
wire exactly like one nobody has filled in yet, and so does a plan the
retention purge has just emptied; a browser holding a cached copy of either
one used to put every row back: every name, vendor and amount, minutes after
they were deleted on purpose, recorded against whoever happened to open the
page. For the purge that undoes the decision the purge exists to carry out.

So `GET /plan` carries `pristine`, which says whether the plan has ever been
written to. It is read from the change log rather than from the rows, because
the log is append-only and outlives what it describes, and it counts the plan's
own tables only: settings and accounts are both written before any plan is. A
browser seeds when the plan is pristine, or when it has no base of its own.
Having no base means it has never held a row the server confirmed, so what it
is holding cannot be a copy of rows somebody deleted. Either is enough, because
the costs are still lopsided: seeding where it was not wanted produces every row
twice, which is visible, fixable by hand, and in the change log besides, while
not seeding where the browser held the only copy destroys it. When the browser
does stand down, it says so in the status line rather than quietly emptying the
screen.

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
   required for either. Assets are encoded once at startup; `GET /plan` and the
   activity page are gzipped per request, because the plan is re-read whole by
   every other open browser after anybody's edit and again on every reconnect.
   Nothing else is: an error is too small to pay for the encoder's header, and
   a compressed `/events` would be a buffered one.

What each of those costs in bytes is measured and tabulated in the README,
under *Design notes*, with the command to check the figures against the build
in hand. Both scripts are `defer`, so first paint is the shell and the
stylesheet and nothing else. The figures live in one place because they were
written down in three and the three disagreed.

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

### The one stall that is not about distance

Everything above is about the distance to the origin. The one measured stall in
the planner owes nothing to it, and it is written down here so it is not looked
for on the network. `fitBudgetText()` in `web/src/app.js` sizes every textarea
in the budget table one field at a time, and `fitText` reads `offsetParent`,
writes `height: auto`, reads `offsetHeight`, `clientHeight` and `scrollHeight`,
then writes the height again. Every read follows a write, so the browser has to
lay the table out again before it can answer, and `table-layout: fixed` lays
out the whole table. That is two forced layouts per field, so 4n of them for n
budget lines, each one O(n).

Measured in the suite's Chromium against the real binary, on a seeded plan
whose names and notes wrap, medians of seven runs at 1400x900. The layout
counts are `LayoutCount` from the CDP performance metrics; the absolute
milliseconds are from a busy development machine, so the shape is the finding
and not the figures.

| Lines | Forced layouts | `fitBudgetText` | The three-pass form below |
|---|---|---|---|
| 25 | 100 | 51 ms | 5.3 ms |
| 50 | 200 | 136 ms | 11.9 ms |
| 100 | 400 | 427 ms | 21.4 ms |
| 200 | 800 | 1632 ms | 44.7 ms |

Doubling the lines costs between 2.7 and 3.8 times as much, which is the
quadratic term showing. The fit is three quarters or more of the handler it
sits in, so switching to the Budget tab, typing one character into a sponsor's
callsign and each frame of a column drag all cost within about a fifth of each
other. The table is also rebuilt whenever somebody else's edit arrives, so on a
shared plan this is charged to every browser showing the Budget tab, which is
the most frequent way to meet it. At 390 px the table is a card stack with
`table-layout: auto`, where the same work costs about a third and grows closer
to linearly, but a tab switch at 100 lines is still 159 ms and the drag grips
that a desktop has are hidden there anyway.

The form to write instead: check once that the table is on screen, then set
`height: auto` on every field, then read every height into an array, then write
them all. Measured at two forced layouts whatever the size, and byte-identical
`style.height` on every field at both widths. Hoisting that visibility check
out of the loop is the half of it that is easy to miss: left per field it is
itself a read after a write, which still costs a layout per field plus one and
gives back only half the time. `fitText(ta)` stays as it is for the one field
being typed into.

`renderAll()` pays for that pass twice. It calls `applyColWidths()`, which ends
in a fit, and then `renderBudgetTable()`, which empties `#budgetBody`, builds
the rows again and fits those, so the first pass sizes fields about to be
thrown away. With the Budget tab on screen at 100 lines, a language switch,
which is `renderAll()`, forces 827 layouts where a sponsor keystroke, which is
`renderBudgetTable()` alone, forces 401. It is free only while that tab is
hidden, which is where every page load starts: the panel is `display: none`, so
`offsetParent` is null and the same switch forces 28. What is left to pay it is
a language switch, an import, a sign-out and a plan arriving from a retried
`connect()`, each with the Budget tab showing. Once the fit is O(n) the wasted
pass is the table's 21.4 ms at 100 lines, which is not worth a flag on
`applyColWidths()` to skip.

Set aside on purpose: `field-sizing: content` behind `@supports` would be a
second sizing path to keep in step with the `height: 100%` rule and with a row
somebody has dragged taller, for no gain once the fit is O(n); rebuilding
`<colgroup>` on every drag frame is too small to measure beside this; and
browsers already coalesce `pointermove` to about one event per frame, so a
`requestAnimationFrame` guard on the drag saves little.

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
- `soiree_reminder_runs_total{outcome}` and `soiree_reminder_last_run_ok`, from
  the deadline digest: how each run ended, and whether the last one ended in
  something somebody has to act on. Registered by the scheduler itself, so a
  deployment with reminders off declares neither, and "nothing is scheduled
  here" cannot be read as "the digest went out". Every outcome exists at zero
  from startup, because the scheduler's first act is a run and a counter first
  seen at 1 has no `increase()` for an alert to read
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
`api-phases`, `api-programme-entries`, `api-auth`, `api-users`,
`api-attachments`, `api-push`, `api-activity`, and `api-other` for everything
else under the prefix, `/api/v1/version` included. The collection is matched
against that map rather than taken from the URL, because the segment is
caller-controlled and an unknown one must never become a label.

One second segment is split out by a map of the same kind: `/api/v1/auth/login`
is `api-auth-login`, while every other `/api/v1/auth/...` path, the passkey
routes included, is `api-auth`. Login is the route that is rate limited, that
costs an Argon2id evaluation and whose refusals a ban is built from, and
`/auth/session` runs on every page load, so one shared label leaves login's
latency and its 401 and 429 rate unreadable under traffic a hundred times its
size. A second segment nobody registered falls back to `api-auth`, for the
reason an unknown collection falls back to `api-other`.

Logs are JSON on stdout via `log/slog`, which is what the cluster's log
pipeline expects.

A `500` names the request it failed: the method, the matched route pattern and
the account id. There is no access log here to join a bare "api request failed"
against, and the metrics carry no id to match a line to. A caller that hung up
mid-read is answered `499` instead: nobody receives it, and it is what keeps a
phone that locked its screen out of the 5xx rate it would otherwise be counted
in. The session middleware answers the same event the same way. It meets that
event a query earlier, and a bare return there left the request counted as one
this server served, because a response nothing wrote a status on is recorded as
`200`. Both are read as a cancelled query and a request context that is done,
so that a database failing while somebody happens to close a tab is still a
failure.

A value the database refused as a data exception (SQLSTATE class 22) is
answered `400`, which is the right answer to a figure past what its column
holds, and logged at WARN with the SQLSTATE and the constraint. The same class
covers a bound in this application that stopped agreeing with the column behind
it, and nothing else would show that: the caller is refused like any other
caller and a `400` is not in the 5xx rate. It is logged at WARN rather than
ERROR, so a saved `level=ERROR` query does not show it. Beginning an attachment
answers the same class the same way without the line: it calls
`constraintError` directly rather than going through `writeStoreError`.

A handler panic is recovered where the request is counted, so it arrives as one
ERROR line carrying the route and the stack, and as a `status="500"` sample.
Left to net/http it would be neither: its own "panic serving" line goes through
the standard log bridge, which emits at INFO, and a request that never returned
was never counted. Both servers are given an `ErrorLog` of their own for the
same reason — the metrics listener is not behind the instrumentation.

## Roadmap

State is shared, the API is guarded, and the browser uses all of it. What is
left is listed under *Open*.

Done:

1. ~~Configurable single binary, no third-party requests.~~
2. ~~Data schema and store layer.~~ Migrations through 0012, a typed data-access
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
   the change it records, and the screen an admin reads it on. See *Activity*.
9. ~~Deadline reminders~~, by mail and by web push. See *Web push*.
10. **Data protection** — half done. `internal/store/privacy.go` implements
    subject export, erasure and a retention purge, with their own tests. Nothing
    invokes them: there is no route, no subcommand and no caller outside that
    package, so honouring a request today means writing Go or SQL against a
    production database. The way in is not the only thing missing, either. That
    file was written against the schema as it stood at migration 0005 and has
    since been taught about one of the tables added after it, `change_log`
    (0007), which held every name, owner cell, sentence and address the rows
    ever carried: erasure strikes the person out of those entries and the
    purge strikes every name a person can be known by out of them, both
    through the one exception migration 0013 makes to the append-only
    trigger. Those names, and not figures. The redaction reaches the text
    fields and leaves the rest of the entry as it was written, so every
    recorded amount, quantity, date, position and row id survives a purge,
    and the shape of a purged plan is still readable in its history with
    nobody in it named. Whether those go too is a retention decision nobody
    has taken. The tables `privacy.go` still does not know about: `sessions`
    and `password_tokens` (0008), `push_subscriptions` (0010),
    `passkey_credentials` (0011) and `attachments` (0012). So the export is
    short by all of them and says so in its caveats, an erasure that
    anonymises rather than deletes leaves the account's session, token,
    passkey and push rows behind, and a file keeps the name it was uploaded
    under.
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

- **What an expired session leaves behind.** Signing out removes this browser's
  copy of the plan. A session that ends by itself does not, because the unsent
  edits are in that copy — so a tab abandoned on a shared computer still shows
  the ledger a week later. Clearing it after the absolute session lifetime,
  when there is provably nobody coming back to it, would close that.
- **A way to invoke the data-protection functions.** Export, erasure and purge
  exist and nothing calls them. An admin-only route or a subcommand, either
  would do for the way in; what there must not be is a documented obligation
  that can only be met by hand-written SQL. The hardest half is done: an
  erasure now reaches `change_log`, which refused every UPDATE and DELETE by
  trigger, because migration 0013 made the decision that trigger's own comment
  asked to be made deliberately. What is left before a route is worth wiring
  is the rest of the list under item 10, none of which needs a decision: the
  export has to read the tables it admits it does not, and an anonymising
  erasure has to take the account's sessions, passkeys and push subscriptions
  with it rather than leaving them disabled in place. One question there does
  need a decision, and it is not in the way of a route: a purge strikes the
  names out of the history and leaves every amount, quantity and date
  standing, so what a purged plan cost is still readable even though nobody
  in it is named. That is either retention working as intended or a second
  thing to strike out, and it is a policy call rather than a defect.
- **Restore drill.** Backups that have never been restored are not backups.
  Restore into a scratch namespace, confirm the data, write down the steps.
- **The budget table's sizing pass.** `fitBudgetText()` forces two layouts per
  field on a fixed-layout table, so a large plan stalls on a tab switch, on a
  keystroke and on every frame of a column drag. Measured, with the form to
  write instead, under *The one stall that is not about distance*.

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

Twelve migrations, applied in order at startup. The plan's own tables are
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
-- would be a migration per stylistic change. Nothing writes a row yet: the
-- widths live in the browser, as State shape above says, and this is what a
-- layout that follows somebody between devices would be built on.
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
| 0012 | `attachments`, `attachment_garbage` | One row per file on a budget line or a task: which row it belongs to, its name and size, and whether the upload was ever confirmed. The bytes are in the bucket. `attachment_garbage` collects the keys of rows a cascade removed, so their objects can be deleted afterwards. See *Attachments*. |
| 0013 | (no table) | The second mutation `change_log` allows by name: a redaction, where every column but `changes` is unchanged, the same keys are there afterwards, and a recorded value may only become the tombstone. It is what lets an erasure strike a person out of the history, and it is shaped so that the trigger can tell a redaction from an edit without being told. Deleting entries stays refused. Roadmap item 10 has the rest. |

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
  array order does not survive a relational round trip. The page writes it on
  budget lines as an ordinary field, so moving one merges and carries a
  revision exactly as a price does; see *State shape*.
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
| `POST /api/v1/attachments`, `POST /{id}/complete`, `GET /{id}/content`, `DELETE /{id}` | Files on a line or a task. See *Attachments*. |
| `GET /api/v1/activity` | The change history, read. Admin only. See *Activity*. |
| `GET /api/v1/version` | Which release is running, to anybody signed in. Behind the guard on purpose: the public page does not name its build. |
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
`read_only` and `forbidden` (`403`) from the guard in front of it, and
`cross_origin` (`403`) from the origin check in front of everything. The
accounts surface names its own refusals — `invalid_email`, `revision_required`,
`self_change`, `rate_limited` and the rest; the full list is in
`api/openapi.yaml`. `current` appears only on a `409`. The database's own
rejections are translated rather than surfaced as a `500`: a foreign key
violation is a `400` saying the request referred to something that is not there,
and a unique violation is a `409`. A data exception, SQLSTATE class `22`, is a
`400` too: a figure past what its column holds, or a NUL byte in a text field.
By class rather than by code, because a list of codes is what lets the next
column added bring the `500` back. `qty` and the two settings rates are bounded
and scale-checked before they get that far, so the limit is named rather than
merely refused: `numeric(12,3)` rounds a fourth decimal away silently, as
`numeric(18,6)` does a seventh, and a client that then compares what it sent
with what it holds rewrites the row on every pass. A `500` never carries
the error — that goes to the log, because it contains SQL and column names —
but it always goes *somewhere*, since a 500 whose cause was dropped cannot be
operated on.

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
A reset is limited per account as well, so that knowing somebody's address is
not a way to fill their inbox. Requesting a reset returns the same response
whether or not the address exists, so the endpoint cannot be used to enumerate
accounts.

Mail goes out over SMTP (`SOIREE_SMTP_*`). If SMTP is not configured, account
creation still succeeds and the admin is shown the set-password link to pass on
directly — so a deployment without mail is degraded, not broken. Asking for a
reset there issues nothing at all: a link nobody can be sent would still
supersede the one the admin is passing on by hand.

An admin who needs the link itself can ask for it. `deliver: link` on a create
or an invite holds the mail back and returns `setPasswordUrl`. Where a relay is
configured it is accepted only for an account that has not set a password yet,
which is where a mail the relay drops actually strands somebody; an account in
use keeps its link to its owner, so a live account cannot be taken over in a
click. Without a relay there is nothing left to weigh, since every invitation
comes back as a link anyway, for an account in use as much as for a new one.
Either way, every answer that carries a link logs `account link issued to
admin` with who asked and for whom, and never the link. An admin could already
reach the same link by pointing an account at a mailbox of their own and
re-inviting, which records nothing about how it was got, so this is that route
asked for out loud.

Both mails name the event, in the subject and in the first line, and the
invitation says where to go once its link has expired: the sign-in screen's
*Email me a link to set a new password*, which reissues an invitation to an
account that has not set a password yet. They named nothing until that was
reversed. The omission was meant to keep a mail to a mistyped address from
telling a stranger whose planner this is, and it protected nothing, because the
link's hostname is in the mail and the page behind it gives the event's name and
date to any anonymous visitor. On a deployment with
`SOIREE_PUBLIC_EVENT_DETAILS` off that page gives neither, and what is left of
the argument is the credential in the stranger's hands. The mails name the
event there too; the switch is about the page. Who sent the invitation is still
unnamed.

**The first account is the exception, and needs its own way in.** Every route
that hands back a set-password link is admin-only, and `password-reset` issues
nothing without a mailer — so on an empty database with no working SMTP there
is no path to the first admin at all. `SOIREE_BOOTSTRAP_ADMIN`
creates that account; `SOIREE_BOOTSTRAP_PASSWORD` optionally gives it a
password, hashed with the same Argon2id parameters as any other, so it can log
in immediately. Both are consumed only while no admin exists, which is what
makes them safe to leave set: neither can resurrect a disabled account nor
overwrite a password that has since been changed. Without the password the
account stays `invited` and the mailed link is the only way in — the better
shape when mail works, since no credential is written down.

The routes are `POST /api/v1/auth/login`, `logout`, `password-reset` and
`set-password`; `GET /api/v1/auth/session` for "who am I";
`POST /api/v1/auth/password` to change the password you already know, which is
the only way that needs no mail and which ends every session the account had;
and
`GET|POST /api/v1/users`, `GET|PATCH|DELETE /api/v1/users/{id}`,
`POST /api/v1/users/{id}/invite` and
`POST /api/v1/users/{id}/revoke-credentials` for administration. Every one of
the administration routes is checked per request against the role as it stands
in the database, not as it stood when the session was created.

The browser's half is `web/src/auth.js`, a second script beside the planner
rather than part of it: a deployment with no database has no accounts at all,
and `auth.js` is then a script that finds nothing and draws nothing. It draws
five screens, routed in the URL fragment so they can be linked to — sign in,
set a password (where an invitation link lands, with the token taken out of the
address bar before anything else happens), your own account with its password
and its passkeys,
and, for an admin, the accounts screen and the activity screen. It enforces
nothing; the server does.

It is translated like the planner, and deliberately not *by* the planner. The
table is `auth.js`'s own — a deployment with no database never draws these
screens and should not carry their strings — and its static markup is keyed
with `data-i18n-auth`, because `app.js` walks the whole document for
`data-i18n` and answers a key it does not know with the key itself. What the
two share is the choice of language: `app.js` resolves it and writes it to
`<html lang>`, and `auth.js`, a deferred script after it, reads it from there
rather than resolving it a second time.

**Nothing about language is stored against an account.** A deployment has one
locale and the people using it do not have one language, and the first version
of this answered that with a `language` column an admin filled in. It was taken
out again before it shipped: an admin's guess, made once when inviting somebody,
is right for the one mail it was made for and wrong to pin anything else to —
the person it is about cannot change it, and can change their own browser.

So the interface asks the reader. In order: `?lang=` on the link; the flag they
clicked in the switcher, remembered on that device (`soiree.lang`, the third and
last `localStorage` key); `navigator.languages`, in their own order of
preference, first one there is a translation for; then `SOIREE_LOCALE`; then
English. The switcher sits above both the planner and the accounts screens,
because the first screen an invited person sees is the one for choosing a
password and that is where being in the wrong language matters most. It
switches in place — `app.js` re-applies its strings, re-renders, and tells
`auth.js` on `soiree:language` to do the same — and never by reloading. A page
that has a shadow behind it survives a reload with its unsent edits; a page
that has never reached the server has no shadow to merge against, so a reload
throws away whatever was typed into it, and it costs the screen somebody is on
either way.

And a mail is written in a language chosen *for that mail*, by whoever causes it
to be sent. `POST /api/v1/users` and `POST /api/v1/users/{id}/invite` take an
optional `language` from the admin, who is the one person who knows what the
person they are inviting reads. `POST /api/v1/auth/password-reset` takes it from
the sign-in screen, which sends the language it is being read in — nobody else
is involved in a reset, and the server has never seen that browser. The link in
the mail then carries `?lang=`, so the screen it opens matches the mail; that is
a query string, which is sent to the server, and harmless there — it is a
language tag, and the token is still behind the `#`. With no language chosen the
mail is in the deployment's and the link is bare, so the page decides for
itself. The deadline digest has nobody to ask: it is composed once for every
recipient, so it is always in the deployment's. Refusals are worded from the
server's error *code*; its English `message` is shown only to somebody reading
English.

#### Sessions

A session is a server-side row; the cookie carries a token whose SHA-256 is
what the row stores, so a copy of the database contains nothing replayable.

- `HttpOnly`, so script cannot read it and an XSS anywhere on the origin does
  not become a stolen session. `Secure`, so it never crosses plain HTTP —
  browsers treat `localhost` as a secure context, so `docker run` still works.
  `SameSite=Lax`, so a form on another site cannot post here with the session
  attached while an ordinary link from a mail still arrives logged in.
- `SameSite` is a *site* boundary, and an origin is narrower than a site. A
  page on a sibling subdomain, or on another port of `localhost`, is same-site,
  so a form there posts with the session attached, and a `text/plain` form
  whose bytes happen to be JSON is a body the API accepts. The origin boundary
  is `net/http`'s `CrossOriginProtection`, around the whole mux: a request that
  is not a GET, HEAD or OPTIONS is refused with 403 `cross_origin` when the
  browser's `Sec-Fetch-Site` says it came from anywhere but this origin, or,
  from a browser too old to send that, when `Origin` does not match `Host`. A
  caller that sends neither is not a browser and has no ambient session to
  lend, so curl and scripts are untouched. It sits inside the metrics wrapper,
  so refusals are counted.
- No `__Host-` prefix, deliberately. It is stricter, and it would also require
  HTTPS outright, which breaks a bare `docker run` entirely.
- Seven days idle, thirty days absolute. The idle window is slid in the database
  *and* in the browser, at most once an hour — doing only the first would leave
  the cookie expiring at the moment it was issued, so somebody using this daily
  would still be logged out on the seventh day.
- A cookie that no longer resolves is cleared on the way past, so a browser
  holding a revoked session stops re-presenting it on every request for a month.
- Only a lookup that answers "no such session" clears it. One that fails
  because the database is restarting or failing over has said nothing about the
  session, so the cookie stays and the guarded route answers `503` with
  `Retry-After` instead of `401`. The page takes a `401` as final and cannot
  put back a cookie it is not allowed to read, so an outage reported as one
  would sign out everybody who had the page open, for sessions that were all
  still valid.

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
required), `POST /api/v1/auth/passkeys/login/{begin,finish}` (public; `finish`
is the attempt and sits behind the same per-IP bucket as the password login, so
alternating between the two does not buy twice the allowance, while `begin`
checks nothing and has a bucket of its own), `POST /api/v1/auth/passkeys/report`
(public, its own bucket: the page tells the server what the browser said when
it refused, because a refusal in the browser asks the server nothing and would
otherwise leave no trace anywhere an operator can look; the line it makes names
no account), and `GET /api/v1/auth/passkeys` plus
`DELETE /api/v1/auth/passkeys/{id}` for managing one's own credentials. Never
anybody else's: there is no admin view of somebody's passkeys, because an admin
has no use for the list and the person who does is the one holding the devices.

**A credential somebody else added is taken away, not waited out.**
Registering needs a live session and nothing else, so a minute at an
unattended browser leaves a passkey behind, and that one outlives every other
remedy here: a new password ends the sessions and does not touch it, each
login with it mints a fresh session so the absolute cap never reaches it, and
disabling the account only parks it until somebody enables the account again.
Its owner removes it from their own account screen, where the list says which
device is which. An admin, who has no such list, removes all of them at once
with `POST /api/v1/users/{id}/revoke-credentials`: sessions, passkeys,
registrations in flight and any outstanding link, in one transaction, leaving
the password alone so the account is still its owner's to come back to. That
route reports nothing about what it found, which is what leaves the decision
above standing: there is still no admin view of anybody's passkeys, because
taking them all away needs no list.

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

Two accommodations for browsers that are not Chromium, both learned from a
sign-in that worked with Windows Hello and not on an Apple device:

- **The page asks for the challenge before anybody taps.** Safari wants
  `navigator.credentials` called from inside the tap, and how much of a fetch
  between the two WebKit forgives has changed from version to version. So the
  options are fetched when the sign-in or account screen is
  drawn, refreshed before the challenge's five minutes are up, and the tap calls
  `navigator.credentials` in the same turn. That is why `login/begin` is not
  charged to the login bucket: drawing the screen would otherwise spend the
  allowance for using it.
- **An extension output nobody requested is ignored, not refused.** The library
  defaults to failing the ceremony over one, and WebKit reports `appid: false`
  on every assertion from a security key while several password managers attach
  `credProps` to every registration. This server requests no extensions and
  reads no outputs, so there is nothing an unrequested one can change.

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
- Login is rate-limited per account and per IP, and the per-account bucket is
  keyed on the client's network as well as on the address.

The implementation uses `golang.org/x/crypto/argon2` — no hand-rolled
cryptography.

**Why the network is in that key.** Keyed on the submitted address alone, the
per-account bucket is one anybody who knows an address can hold at zero. A
refused attempt costs its sender nothing, so ten wrong guesses and then a
request every few seconds answer the owner's own sign-in with a `429`, from
one client, with no credentials, and from inside the per-address allowance,
which never comes near firing at that rate. A looser ceiling for the account
across every network sits behind the tight bucket, and is what a run spread
over many addresses meets instead. An attempt the tight bucket refused is never
charged to that ceiling: one client at the per-address rate would otherwise
empty it by itself and the lockout would be back wearing a bigger number. What
is left is deliberate and worth writing down: somebody with addresses in
enough networks can still hold the ceiling at zero. Putting the network in the
key raises the price of a lockout from one client to many rather than removing
it, and a passkey, charged to the per-address bucket alone, is the way in that
remains when somebody pays it.

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
  behaviour. One account may hold 16 of those, which is several tabs, a phone
  and a laptop and still nowhere near it. Reaching *that* one is a `429`,
  because a caller's own share running out is about the caller — and without a
  second number one account can hold every slot, which is a state disabling
  that account would not clear.
- **Degrading.** With no `DATABASE_URL` the route is never mounted. With a
  database that goes away, the listener retries with jittered backoff from
  200 ms to 30 s and tells every client to refetch once it is back. It never
  wedges the process and never takes the site down with it.
- **Honest.** No event ids and no replay. A client that was disconnected missed
  changes and is told to refetch on connect, rather than being handed a cursor
  that implies the gap can be filled.

Every connection therefore opens with three frames: the reconnect interval, a
`hello` naming the build that is answering, and a `resync` telling the client
its copy of the plan is stale. It usually is, and that one rule is the whole of
what a client has to do about missed events. A `resync` is also broadcast
whenever the listening connection is re-established — without it, a client that
stayed connected through a failover would keep a stale plan indefinitely, which
is the precise failure this feature exists to prevent.

`hello` carries `version` and `build`: the release, and the content-hashed URL
of the page's script. A planner is opened once and then left open for days, so
nothing else in the page ever learns that the deployment changed under it. The
shell is revalidated on the next visit and a tab nobody revisits keeps running
the JavaScript it started with, which is how a fix that shipped a week ago has
still not reached somebody. The stream is the one thing that notices a deploy
without being asked, because it drops with the old process and is reopened
against the new one. The build id is the script URL rather than a hash of its
own, because that is the only identifier a page can compare against itself: it
is written into the shell it loaded. Both values are already readable by a
signed-in caller, through `GET /version` and the shell itself, and the stream
is behind the same session guard, so the frame discloses nothing new. The page
does not read the frame yet; a notice saying the site was updated, which leaves
the reload to the reader, is the other half of it. A notice rather than a
reload for the reason the `notificationclick` handler in `sw.js` gives: nothing
here may take away the screen somebody is on, and whatever they were half way
through typing with it.

Events are named (`change`, `resync`, `hello`) rather than default, so a client
registers for each separately and an unknown future event name is ignored by an
old client instead of being mistaken for a change. A `change` frame carries the
notice described under *Change fan-out*.

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

A stream is authorised once, when it is opened, and then lives for hours. So
every fifteenth heartbeat — about five minutes — it reads its own session
again, and ends when that lookup comes back empty: revoked by a sign-out, past
its idle window, past its absolute lifetime, or belonging to an account
somebody has disabled, which are the same four reasons every other route
refuses the same cookie. Any other error leaves the stream open, because a
database that is not answering says nothing about the session — the distinction
`Authenticate` draws for the same lookup. Revocation therefore reaches an open
stream within about five minutes rather than whenever its socket happens to
drop. It is not instantaneous: an admin disabling an account waits that long
for the feed to stop and for the slots it holds to come back.

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
URL whose host is not `localhost` and not a loopback, private, link-local or
unspecified address: the digest run posts to whatever is stored, from inside
the network this server runs in, so the account that stores a row must not
choose a target on that network. Written addresses only — a name that resolves
to a private address still passes, and `internal/push` follows redirects — so
it is a fence rather than a wall, and what it leaves is a push service reached
over the public internet.

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
because a denied permission is sticky, but the first time a signed-in admin
gives a task a due date — and posts the subscription, re-posting whatever the
device already holds on every load so that a restored database gets its devices
back. `web/src/sw.js` handles `push` and `notificationclick`: it always shows a
notification, because the subscription is `userVisibleOnly`, replaces the
previous digest rather than stacking on it by reusing the `tag`, and brings an
open planner to the front rather than opening a second one — without navigating
it, when it is already the planner: every screen is a fragment of one page, the
digest opens `/`, and navigating from a fragment to `/` is a reload that takes
somebody's half-typed form with it. The replacement alerts again rather than
landing in silence: reusing a tag replaces the notification on screen with no
sound and nothing on the lock screen unless `renotify` says otherwise, and the
phone still holding last week's digest is exactly the one this week's is for.
The payload's four
fields (`title`, `body`, `url`, `tag`) are a contract between the server and
that handler: adding a field is safe, renaming one is not.

The offer is a good moment to ask and a bad thing to depend on, so the account
screen has the standing switch: on, off, and an honest line when neither is
possible. `app.js` owns what the switch does (`window.soiree.push`: `state`,
`enable`, `disable`, `why`) and `auth.js` only draws it, for admins alone,
because `NotifiablePushSubscriptions` sends to active admins alone and a switch
connected to nothing is worse than none. Turning off tells the server first and
the browser second: the other order leaves a row the digest keeps sending to
until the push service reports it gone.

"Honest" is one sentence per situation, and that was learnt the hard way. There
used to be one sentence — this browser cannot receive notifications, and on an
iPhone add the page to the Home Screen — for everything that was not on, off or
blocked, including a wait on `navigator.serviceWorker.ready` that ran out. For
three releases the worker did not parse, so that wait always ran out, and Chrome
on a Windows PC was sent to look for a Home Screen. The states now keep apart
what the browser lacks from what this site failed to do:

| state | what is true | what is said |
|---|---|---|
| `unsupported` | no `serviceWorker`, `PushManager` or `Notification` | by `why`: Safari tab on iOS (Home Screen, step by step), another browser or an in-app one on iOS (open in Safari first), a Home Screen app that still has none (iOS older than 16.4), plain http, or any other browser (say so; a private window is the usual reason) |
| `starting` | the browser can; no worker is active yet | still being set up — and `soiree:push` redraws the switch when `ready` settles, however long that takes |
| `noworker` | the browser can; `register()` rejected | a security error is the browser blocking site data; anything else is this site's fault and is said to be |
| `blocked` | permission is `denied` | how to allow it again: the address bar, Safari's settings, or the phone's Settings for a Home Screen app |
| `refused` | permission given, `subscribe()` rejected | push is switched off in the browser — Brave until its setting is on |

`why` reads the user-agent string for one thing, whether this is an iPhone or
iPad (an iPad says it is a Mac; a Mac has no touch screen), because a Safari tab
there and an old desktop browser lack exactly the same things and need opposite
advice. Everything else is a feature check.

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
anything `SOIREE_REMINDER_TO` names, with the two lists matched on the parsed
address so that somebody in both is one recipient. One message **each** rather
than one addressed to everyone: a shared `To` header hands every recipient the
others' addresses and carries them along wherever the mail is forwarded, and a
single address the relay refuses at RCPT ends the transaction before the body
is offered, which loses that period's digest for everybody on the envelope.
The ledger still records a single claim per period, because the claim is taken
before the first copy goes out. A crash half-way through the list loses the
copies still to send rather than offering anybody a second one, which is the
at-most-once stance already chosen. A copy the relay refuses is logged by its
position in the list rather than by the address the scheduler holds, though the
relay's own answer is carried through as it came and can quote the mailbox back
inside it. That copy leaves the claim standing: the others are out, so a retry
would send them the same digest twice. Push goes to the devices of active
admins by the same rule.

Both channels are written in the deployment's language: the primary subtag of
`SOIREE_LOCALE`, and English for a locale this binary has no words for, which
is the rule an invitation follows when nobody chose a language for it. It is
also the only rule available here. Nothing about language is stored against an
account, and a digest is composed once for everybody it goes to, so there is
nobody to ask. On a deployment whose readers do not all share that language it
is still the one they all get, which is the price of the weekly mail matching
the screens it talks about.

The footer carries the planner's own address, as text rather than as an anchor.
A mail that lists decisions has to say where they are made, and this is the
address the notification's click already opens. Written as text, a mail client
turns it into something clickable itself, while a relay's click tracking
rewrites `<a href>` and nothing else, so there is nothing for it to turn into
an address that would report who followed it. That is the same reason there is
no image and no stylesheet in there either. The footer also says where an
answer goes: one copy each means a reply reaches the address in
`SOIREE_SMTP_FROM` and none of the other readers.

The two channels get separate time budgets inside the run's two minutes. With
one shared deadline a relay that stalls would spend the whole run before push
was reached, so a broken relay would switch off notifications too; neither
channel is allowed to do that to the other. A push failure never fails the run —
it has already been mailed — and mail failing does not release the claim if any
device was reached, because releasing it asserts that this period reached
nobody.

### Attachments

Files on budget lines and tasks. The bytes are in an S3 bucket and never pass
through this process; what soiree holds is the record, and what it does is sign.

```
browser                         soiree                          bucket
   │  POST /attachments            │                               │
   │  {parent, name, size, type} ─▶│ quota under an advisory lock  │
   │                               │ row: 'uploading'              │
   │◀─ 201 {attachment, upload} ───│ sign PUT for exactly `size`   │
   │                                                               │
   │  PUT <signed address>  ──────────────────────────────────────▶│ refuses any
   │◀─ 200 ────────────────────────────────────────────────────────│ other length
   │                               │                               │
   │  POST …/{id}/complete ───────▶│ HEAD ────────────────────────▶│
   │                               │◀─ size ───────────────────────│
   │◀─ 200 ────────────────────────│ row: 'ready'; change_log;     │
   │                               │ NOTIFY → every open page      │
   │  GET …/{id}/content ─────────▶│ session? allow-list?          │
   │◀─ 303 Location: <signed GET> ─│ sign GET, 60 s, disposition   │
   │  GET <signed address> ───────────────────────────────────────▶│
```

**Why direct.** The likely origin is a small machine behind a home connection,
and the people using it may be on the far side of the world. Proxying would
carry every photograph across that uplink twice, and would not survive it
anyway: `ReadTimeout` is 30 s and a reverse proxy in front typically allows 60,
neither of which is a 25 MB upload from a phone. A direct upload passes through
neither, so there is no chunking protocol to get wrong. It also puts downloads
on the bucket's origin, where a hostile file cannot reach this site's session
cookie at all.

**Why no SDK.** All four operations are one algorithm — Signature Version 4 in
its query-string form. The browser is given the PUT and GET addresses; the
server calls the HEAD and DELETE addresses itself over `net/http`. That is one
signing routine (`internal/objstore`, about a hundred lines of `crypto/hmac`),
checked against the signature AWS publishes for its own example, against ~15
modules of SDK for four calls.

**What is signed, and why each.** `Content-Length`, so the bucket refuses a body
of any other size and the quota is decided before the upload rather than
discovered after it — the integration test shows a bucket storing eleven bytes
to an address meant for ten the moment the length is left out of the signature.
`Content-Type`, so the stored object carries what was declared. On download,
`response-content-disposition` and `response-content-type`, so whoever holds the
address cannot turn "save this" into "render this". How a file is served comes
from a five-entry allow-list in the handler (JPEG, PNG, WebP, GIF, PDF may open
inline); the type a file was stored with never picks its own treatment, and SVG
is absent because it is a document format that runs script.

**Two systems that do not commit together.** A row can exist with no object (an
upload somebody abandoned) and an object can outlive its row (a delete that
reached Postgres and not the bucket). Neither can be prevented, so both are made
harmless:

- `status` is `uploading` until the server has seen the object at its declared
  size. Only `ready` rows are in the plan. `uploading` rows still count toward
  the quota — otherwise the cap is a race anybody wins by starting ten uploads
  before finishing one — and the sweeper removes them after an hour.
- An `AFTER DELETE` trigger writes each removed row's object key into
  `attachment_garbage` in the same transaction. Deleting a budget line cascades
  to its attachments inside Postgres, where no application code runs and the
  server never learns which rows went; the trigger is how the objects are still
  found. The server drains the queue at start and hourly, and a key leaves only
  once its object is confirmed gone. `ObjectKey` therefore exists twice, in Go
  and in the trigger, and a test holds the two together.

**Not knowing is not the same as knowing it failed.** When the bucket does not
answer, `complete` says 503 and keeps the row, so the same request works once it
is back and the page retries by itself. When the bucket says there is nothing
there, or something of another size, the row and the object both go.

**History.** A file is recorded when it is confirmed and when it is removed —
including when its parent is removed, which both delete paths record in Go
because a cascade is invisible to the change log. The uploader is *not* written
into the entry: its actor already says who, and a second copy of their id inside
the JSON would be the one that outlives the deletion of their account.

**In the page** attachments are deliberately not part of `state`. A file exists
on the server or it does not exist: there is nothing to edit offline, nothing to
merge, nothing to cache. The list arrives with every plan read and is replaced,
and a change notice for `attachments` — which has no revision to compare — means
"read the plan again". They are a collection of their own in `GET /plan` rather
than a list on each row, because a row is sent back whole on an edit and the API
refuses fields it does not know.

**Not done.** Nothing scans a file. There is no resumable upload: a dropped
connection starts that file again, which the 25 MB default keeps tolerable.
Export and the database backup carry records, never bytes. The subject-access
tooling in `privacy.go` does not look at file names.

### Activity

What everybody has been doing, newest first, for an admin. It is a read of
`change_log`, the table the audit trail has written to since before there was
anything to show it with, translated on the way out into the language the rest
of the API speaks: camelCase fields, and money as decimal strings in major
units, because a client that learned `"500.00"` from `GET /plan` must not be
handed `50000` here.

**Why admin-only.** Every entry names an account, and the list of accounts is
something only an admin can read. A feed open to every editor would hand that
list out through a second door, annotated with what each person did and when.

**Why an exact path.** `GET /api/v1/activity` is mounted on the main mux rather
than inside the `/api/v1` subtree, because its rule is stricter than the
subtree's: `RequireRole(admin)` rather than `RequireWrite`. Go's mux prefers the
more specific pattern, so this is what answers a `GET`, and any other method
falls through to the subtree, which has no such route. Moving it inside would
silently widen it to every signed-in reader.

**Who and what it was called are joined at read time**, not recorded. An address
written into an append-only table would outlive the erasure meant to remove it,
so the actor's address comes from `users` on each read and is absent once that
account is gone; the page shows those entries as a deleted account. The row's
label does come from the log, which is what lets an entry about a line that has
since been deleted still say which line it was. Where that label was somebody's
name and they have been erased, it reads as `(erased)`: the erasure strikes the
name out of the entries themselves, which is what migration 0013 exists for.

### Build and supply chain (track 6)

Mostly in place.

Done: reproducible static build with `-trimpath`, version and commit stamped at
link time, `scratch` base with no shell or package manager, unprivileged UID,
`govulncheck` on every PR, SBOM and `mode=max` provenance attached to released
images, Renovate on every dependency including custom managers for the tool
versions the workflows pin inline and for the CI toolchain, which it keeps in
step with the Dockerfile.

Also done: **releases are signed** with cosign, keyless via the GitHub OIDC
identity, so a signature proves which workflow in which repository built the
image. **Every action is pinned to a commit digest** with the readable version
kept in a trailing comment, since a moving tag is a supply-chain hole. **The
SBOM is published as a release asset**, not only as an image attestation, so it
can be read without pulling the image. **The builder image is pinned the same
way**, by digest as well as by tag, so the toolchain that compiled a release is
the one that tag's `Dockerfile` names rather than whichever Go patch shipped
last. A CI job builds the binary twice on independent builders with the cache
off and fails if the bytes differ.

**A release waits for the checks.** The jobs every change passes live in their
own workflow, `checks.yaml`, and both jobs that publish an image list it in
`needs`, so a release is built only once that commit's own tests are green.
Both paths needed it: v1.1.0 and v1.2.1 were pushed as `:latest` and signed
from commits whose run was red, and a `vX.Y.Z` tag pushed by hand skipped every
check outright. A red run withholds the image and nothing else, since the tag
and the GitHub Release are made by an earlier job, so re-run the failed jobs
for a flake and fix forward for anything worse. Merging into `main` is still
unenforced; that needs a repository ruleset requiring the `checks / ...`
contexts, which is a setting rather than a file here. **The signed image is
built cold**, without the shared Actions cache every job on `main` can write,
for the reason the reproducibility job turns it off.

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

The interface inherited its look from a single-purpose dashboard: warm paper
tones, a serif display face against a system sans, a big countdown number in
the corner. It was decent, and it was also the look every generated page
arrives at by default.

Since 1.2.0 the look comes from the subject instead. A soiree is an evening, so
the palette is one: dusk for ink and for the dark page, porcelain for the light
one, a lamp for the one warm point, which is always "today". The display face
is a condensed, heavy grotesque, the lettering of tickets and marquees. And the
first thing on the page is the thing every line of an event plan is measured
against: **the run-up**, a scale from today to the day with what is due pinned
along it. The count of days is still there, as the scale's left end rather than
a number by itself. Underneath, money and jobs sit side by side, and one bar
says what four figures in a row could only list: paid and owed are the two
halves of what is committed, and the ceiling is a mark that total has or has
not reached.

The rules that follow still hold, and the redesign was held to them.

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
