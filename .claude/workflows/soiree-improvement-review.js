// A full improvement review of soiree, run by Claude Code's Workflow tool.
//
// Shape: thirteen reviewers each read one dimension of the code and return
// findings with file:line evidence; every finding is then handed to two
// independent skeptics, one asked whether it is true of the code and one
// asked whether it is worth the maintainer's time given the decisions written
// down in docs/architecture.md; a critic looks at what the thirteen covered
// and names what they missed, which gets the same treatment; and an editor
// merges the survivors into a ranked report. Nothing here modifies the
// repository: reviewers may build, test, grep and start the binary on the
// ports their brief assigns, and write only under outDir.
//
// Run it from Claude Code at the repository root: ask for "the
// soiree-improvement-review workflow", or pass this file as scriptPath.
// The concurrency cap is min(16, CPUs - 2) per workflow; the whole thing is a
// few hundred agent runs, so give it a machine with cores to spare or split
// the dimensions across several workflows with the `dimensions` argument.
//
// args (all optional):
//   repo          absolute path of the checkout; default: the working directory
//   outDir        scratch files and the final synthesis.md; default /tmp/soiree-review
//   dimensions    subset of the keys in DIMENSIONS below; default all thirteen
//   lean          true: one combined skeptic per finding instead of two
//   skipCritique  true: no second round
//
// See docs/reviews/2026-09-19-review.md for the reasoning behind the design.

export const meta = {
  name: 'soiree-improvement-review',
  description: 'Review soiree from 13 angles, verify every finding with independent skeptics, find what was missed, and write a ranked improvement report',
  whenToUse: 'A full-repository improvement review of soiree. Read-only apart from outDir.',
  phases: [
    { title: 'Find', detail: 'one reviewer per dimension, reading the real code' },
    { title: 'Verify', detail: 'independent skeptics per finding: is it true, is it worth doing' },
    { title: 'Critique', detail: 'what the first round missed' },
    { title: 'Synthesize', detail: 'merge duplicates, rank, write the report' },
  ],
}

const A = args || {}
const OUT = A.outDir || '/tmp/soiree-review'
const WHERE = A.repo ? `the soiree checkout at ${A.repo}` : 'the soiree checkout in the current working directory'
const CD = A.repo ? `cd ${A.repo} && ` : ''

const COMMON = `You are reviewing the "soiree" application, ${WHERE}, to find concrete improvements. The owner asked: "look at the soiree app and tell me how to improve". Your report is one input among several; return raw findings, not a human-facing message. All paths below are relative to the repository root.

What soiree is: a shared budget and task planner for one group event (wedding, birthday, reunion) shipped as one small container. Go backend (internal/httpd, internal/store on pgx/PostgreSQL, SSE live sync via LISTEN/NOTIFY, WebAuthn passkeys, Argon2id passwords, S3 presigned attachments, SMTP + Web Push deadline digest, Prometheus metrics), a framework-free vanilla JS frontend embedded via go:embed (web/src/app.js ~4900 lines, web/src/auth.js ~2600 lines, index.html, styles.css, sw.js), OpenAPI 3.1 description in api/openapi.yaml, Playwright e2e in e2e/. Around 60k lines total.

Hard constraints the project has chosen (CONTRIBUTING.md "Two house rules", docs/architecture.md, README "Design notes"): no third-party origins in the frontend, no asset build step and no Node toolchain for the app itself, a strict CSP (no inline executable script, no inline style attributes), migrations are append-only, one deployment = one event (no multi-tenancy), explicitly out of scope: multi-event tenancy, plugin system, analytics, marketing site. Comments explain why, not what. Any suggestion that conflicts with one of these must say so and argue why it is still worth it; otherwise do not make it.

Environment: find out what is installed before relying on it (go, golangci-lint, staticcheck, node, npm, docker, a Playwright browser). Other reviewers run at the same time in the same checkout: bind only the ports your brief assigns, and only the "measurements" reviewer runs npm ci, the browser suite, or the database-backed Go tests. Before you start, run \`${CD}git log --oneline -30\` and \`${CD}git branch -r\` to see what is in flight; do not propose what an open branch already does.

Rules:
- READ-ONLY. Do not modify, create or delete any tracked file. If you need scratch space use ${OUT}/<your-dimension>/ (mkdir -p it). Running go build/test/vet, grep, and starting the binary on the port your brief assigns are fine.
- Read the actual code before claiming anything. Every finding must cite file and line numbers you opened and quote the evidence (a few lines). Do not infer behaviour from a function name or from the docs; the docs may be stale, and doc-vs-code disagreement is itself a finding.
- Before writing findings in your area, read the matching sections of docs/architecture.md (headings: Request paths, Startup pipeline, The Store seam, Latency strategy incl. "Deliberately excluded", Observability, Roadmap incl. "Open" and "Explicitly out of scope", and the feature sections) so you know which decisions were deliberate and which gaps are already acknowledged. Restating an acknowledged roadmap item is only useful if you add a concrete plan or new information.
- Distinguish: "bug" = behaves wrongly now (give the concrete input/state and wrong result); everything else is an improvement. Be specific and actionable: what to change, where, and roughly how.
- Return up to 12 findings ordered by value to the owner. Quality over quantity: a wrong or vague finding costs more than a missing one. In coverage_notes say what you inspected and what you did not get to.
- Severity: high = data loss, security, money wrong, or a broken user flow; medium = real degradation or a clearly missing piece a user would hit; low = polish, hygiene, drift. Effort: small = under half a day, medium = a day or two, large = more.`

const DIMENSIONS = [
  { key: 'security', prompt: `Dimension: SECURITY. Files: internal/httpd/auth.go, authmw.go, passkeys.go, server.go (headers, cookies, routing), sse.go, attachments.go, push.go, activity.go, api.go; internal/objstore/objstore.go; internal/store/passwordtokens.go, users.go, passkeys.go, attachments.go; internal/auth/password.go; internal/config/config.go; migrations/0008 and 0011. Look for: session cookie attributes and lifetime, CSRF posture (SameSite, Origin/Fetch-Metadata checks on state-changing requests, SSE), session fixation/rotation on login and password change, token TTL and single-use enforcement, user enumeration and timing differences in login/reset, rate limiting scope and bypass (X-Forwarded-For handling), authorization on every write path including attachments and push subscriptions (IDOR: can an editor touch another user's push subscription or passkey?), presigned URL scope/expiry/content-type/size enforcement, Content-Disposition/type allow-list, SSE data exposure to viewers, admin bootstrap safety, secrets in logs or metrics, WebAuthn verification details (RP ID/origin, counter, user verification), password policy, response headers (CSP is at ingress per docs: check what the binary itself sets and whether it should set more), robots/noindex. Read internal/httpd/*_test.go for what is already covered so you do not claim an unguarded path that has a test proving otherwise.` },
  { key: 'backend-correctness', prompt: `Dimension: BACKEND CORRECTNESS. Files: internal/httpd/api.go, api_json.go, api_entities.go, api_settings*.go, activity.go; internal/store/store.go, budgetitems.go, money.go, audit.go, notify.go, attachments.go, plus every other non-test file in internal/store; migrations/*.sql. Look for: PATCH tri-state (omitted vs null vs value) handled consistently for every field and entity; revision check on every UPDATE and DELETE; 409 body correctness; money parsing (decimal string in major units -> bigint minor units, per-currency exponent table, rounding, negative values, overflow, IDR zero-decimal rule) and formatting back; qty numeric(12,3) conversions; position handling on create/reorder/delete (gaps, duplicates, concurrent inserts); parent_id/phase_id validation; cascade behaviour vs what the history/notify records; transaction boundaries (is history + NOTIFY truly in the same tx for every write path including users, attachments, settings?); context cancellation and pool usage; error mapping to HTTP codes; JSON decoding strictness (unknown fields, duplicate keys, body size limits); date handling (lock_by, due as plain dates); export/import round trip if server-side. Also compare the schema in migrations/ against docs/architecture.md's "Schema" text (it says eleven migrations; there are twelve).` },
  { key: 'frontend-planner', prompt: `Dimension: FRONTEND PLANNER LOGIC. File: web/src/app.js (about 4900 lines; read it in chunks, all of it) plus web/src/index.html where it hooks in. Look for: the Store seam (local vs API mode), debounce/flush on pagehide and its failure modes (unload during a flush, multiple tabs, queue ordering), the shadow copy and the three-way merge on 409 and on SSE notices (fields touched by both sides, deletes vs edits, sponsors array merges), SSE reconnect and missed-event recovery, the money exponent table mirrored from internal/store/money.go (diff the two tables literally), number parsing of user input in different locales (comma decimals for nl/id), Intl formatting, ceiling/inflation/fx computations and totals (do sponsor splits sum exactly to the line, rounding), event date/timezone handling for the countdown and archive turnover, import/export JSON shape vs API shape, i18n: are all three language tables (en/nl/id) complete with identical keys, any hard-coded English left, pluralization; localStorage size and error handling (quota, private mode); error surfaces when the API fails; memory leaks from listeners on re-render; any places where a viewer role could trigger writes client-side. Also give an honest structural assessment: how the file is organized, how hard a change is, and whether splitting into ES modules (loaded with <script type=module>, no build step required) would be feasible given how internal/httpd/assets.go hashes and rewrites asset URLs (read assets.go for that).` },
  { key: 'frontend-shell-and-accounts', prompt: `Dimension: FRONTEND SHELL, ACCOUNTS UI, PWA, ACCESSIBILITY. Files: web/src/auth.js (all of it), web/src/index.html, web/src/styles.css, web/src/sw.js, web/src/manifest.webmanifest, and internal/httpd/assets.go + language*.go for how they are served. Look for: sign-in, set-password, account, accounts-admin, activity screens: flows, error messages, loading states, what happens on 401/403/429/network failure; passkey registration/sign-in UX; push opt-in prompt logic; keyboard operability, focus management when screens swap, focus-visible styles, form labels, aria-live for async results, colour contrast of the palette in styles.css (compute a few ratios), touch target sizes on mobile, reduced-motion, prefers-color-scheme; CSP compliance (any inline style attribute, any inline script that executes, any javascript: URL, any element.style assignments that would break under style-src without unsafe-inline? note that CSP is enforced at ingress not in the binary: check what it would need); service worker: cache versioning and update flow (does a deploy actually land without a second reload; is the HTML shell revalidated; what about the API responses and auth pages; offline behaviour when signed out), manifest correctness; i18n coverage of auth.js strings for en/nl/id and the language switcher; semantic HTML and heading order; print styles; the 30 kB index.html (the README says 4.5 kB brotli: verify what is in it).` },
  { key: 'operations-reliability', prompt: `Dimension: OPERATIONS AND RELIABILITY. Files: cmd/soiree/main.go, internal/config/config.go, internal/httpd/server.go, assets.go, metrics.go, sse.go; internal/migrate/migrate.go; internal/store/store.go and notify.go (LISTEN connection lifecycle and reconnect); internal/reminders/*.go (scheduler, ledger, period keys, multi-replica behaviour); internal/mailer/smtp.go; internal/push/push.go; Dockerfile; compose.yaml; docs/operating.md. Look for: graceful shutdown ordering (listeners, SSE clients, in-flight writes, scheduler), http.Server timeouts (ReadHeader/Read/Write/Idle; SSE needs WriteTimeout off for that route only), MaxHeaderBytes and body limits, pgx pool configuration and health, what /healthz actually checks vs a readiness probe (does it check the DB, and should it?), behaviour when the DB is down at startup vs mid-run, LISTEN reconnect with backoff and what clients miss during the gap, reminders sending twice across replicas or never after restart, SMTP timeouts and retries, push endpoint 404/410 cleanup, structured logging and log levels, secrets never logged, metrics: are the useful ones there (request latency by route, SSE clients, DB pool, reminder outcomes, push failures), metrics label cardinality, panics recovered, startup validation gaps in config.go (contradictory or half-configured settings that are accepted), signal handling, container: scratch base, non-root, healthcheck, image size, ca-certs; anything in docs/operating.md that the code does not actually do.` },
  { key: 'api-and-data-model', prompt: `Dimension: API DESIGN AND DATA MODEL. Files: api/openapi.yaml (all of it), internal/httpd/api*.go, activity.go, attachments.go, push.go, auth.go route table in server.go, internal/httpd/openapi_test.go, internal/store/*.go, migrations/*.sql. Look for: openapi.yaml vs handler disagreements (fields, nullability, enum values, status codes, error bodies, required vs optional, formats), routes present in code but undocumented or vice versa (the test checks documented routes exist, not the reverse), consistency of error contract, pagination and bounds on list endpoints (activity feed, plan size), idempotency of POST creates, ETag/If-None-Match or Last-Modified on GET /plan for the far-away-client case the docs care about, gzip/br on API responses, the users API shape, features with tables and endpoints but no UI (phases, programme, parent items) and whether the API around them is complete enough to build the UI, the unwired privacy functions in internal/store/privacy.go (what a minimal admin route or subcommand would need), schema issues: missing indexes for real queries (check every WHERE/ORDER BY in the store against the indexes), missing constraints (e.g. positive money, qty > 0, code uniqueness on sponsors, email uniqueness case-insensitivity), timestamptz vs date usage, change_log growth and retention, sessions table cleanup, whether revision + updated_by exist on every shared table (notes and programme_entries lack updated_by in the documented schema).` },
  { key: 'tests-and-ci', prompt: `Dimension: TESTS AND CI. Files: every *_test.go (skim all, read the important ones), e2e/tests/*.spec.js, e2e/playwright.config.js, e2e/scripts/*, .github/workflows/*.yaml, renovate.json, release-please-config.json. You may run \`${CD}go test -short -cover ./...\` for per-package coverage; do not run the database-backed or browser suites yourself, the measurements reviewer does. Look for: packages or critical paths with thin coverage (the auth middleware, the 3-way merge in app.js has no unit test at all because there is no JS test runner: assess whether a tiny node:test harness over pure functions extracted from app.js is feasible without adding a build step), what the e2e suite covers vs the user flows that matter (sign-in with password and passkey, invitation, conflict merge between two browsers, SSE arrival, attachment upload, archive turnover, offline), flaky patterns (sleeps, time-dependent tests, port collisions, testcontainers per test cost, the 522 s -race duration noted in ci.yaml: find the actual cause by reading how the httpd tests set up databases and whether they could share one container), CI gaps: no golangci config (which linters would add value), no coverage threshold, no OpenAPI vs live-response schema check, Node 24 vs lockfile, browser job doing playwright install every run, missing e2e run against the Docker image, no CodeQL/secret scanning, release-please config; test hygiene: shared state, t.Parallel use, helper duplication.` },
  { key: 'docs-drift', prompt: `Dimension: DOCUMENTATION DRIFT. Files: README.md, CONTRIBUTING.md, SECURITY.md, docs/architecture.md, docs/operating.md, docs/verifying-releases.md, CHANGELOG.md, api/openapi.yaml prose, compose.yaml and Dockerfile comments, go.mod, .github/workflows. A first pass is recorded in docs/reviews/2026-09-19-review.md and its items were fixed in the same change; confirm they stayed fixed, then go further. Systematically check every checkable statement against the code: counts (migrations, CI jobs, languages, tables), names (the font, packages, env vars, defaults), claims about behaviour (what /healthz does, what -short skips, Go version floor vs go.mod, byte sizes in the transfer table vs actual compressed sizes which you can measure by starting the binary with \`${CD}SOIREE_LISTEN_ADDR=:18083 SOIREE_METRICS_ADDR=:19093 go run ./cmd/soiree &\` and fetching with curl --compressed -w '%{size_download}'; kill it afterwards), the layout tree in README vs actual directories, env var tables vs config.go (every variable read in config.go documented, every documented one read, defaults equal), operating.md procedures vs actual flags/routes, SECURITY.md supported versions, CHANGELOG duplicates, CODE_OF_CONDUCT contact. Each finding: the exact sentence, the code that contradicts it, and the correct wording. Group many small drifts in one file into one finding with a list inside the problem field.` },
  { key: 'maintainability', prompt: `Dimension: MAINTAINABILITY AND CODE STRUCTURE. Read enough of every package to judge it: cmd/, internal/*, web/src/*.js. Look for: duplicated knowledge that must stay in sync by hand (the money exponent table in Go and JS, i18n string tables in app.js vs auth.js vs Go mail templates, the route table vs openapi.yaml, CSS custom properties vs JS colours) and how to make each drift fail loudly (a test that compares them, or one source generating the other at startup within the no-build-step rule); functions over ~150 lines; the size and organization of app.js and auth.js (sections, globals, how state flows, how a new field would be added end to end: count the places), config.go size and whether it is uniform, error handling patterns in Go (wrapping, sentinel errors, HTTP mapping in one place or scattered), naming consistency, dead code (\`${CD}go run golang.org/x/tools/cmd/deadcode@latest ./...\` if it installs; otherwise skip), test helper duplication, comments that explain "what" not "why" contrary to the house style, TODO/FIXME/XXX left in code, any generated or vendored blobs. For each, give the concrete refactor and its risk. Respect: no build step, no framework; ES modules via <script type=module> are allowed if assets.go can hash and rewrite them (read assets.go and say whether it can).` },
  { key: 'performance', prompt: `Dimension: PERFORMANCE. Files: internal/httpd/assets.go, server.go, api.go (GET /plan path), sse.go, internal/store/*.go queries, web/src/app.js render path, web/src/sw.js, styles.css size. Look for: how many SQL round trips GET /api/v1/plan makes and whether they run in one tx/snapshot; N+1 patterns (sponsors per item, attachments per item); missing indexes for actual query shapes; SSE fan-out cost per event and per client (per-client goroutine, buffer, slow-client handling, backpressure, dropped events); the frontend render strategy on each keystroke (full innerHTML rebuild of the table? per-row update? measure by reading the render functions and the debounce), reflow-heavy patterns, listener churn; localStorage write frequency and size; asset sizes: start the binary (\`${CD}SOIREE_LISTEN_ADDR=:18080 SOIREE_METRICS_ADDR=:19090 go run ./cmd/soiree &\`) and measure brotli/gzip sizes of /, app.js, auth.js, styles.css, the font (use the hashed names index.html references), with curl -sS -H 'Accept-Encoding: br' -o /dev/null -w '%{size_download}' and compare with the README transfer table; check cache headers per asset class; TTFB-relevant startup costs (precompression at boot: how long, does it delay readiness: time from process start to first 200 on /healthz); kill the binary afterwards; memory held per process; Go allocations in hot handlers (json encoding of the plan); DB pool sizing vs CloudNativePG defaults; whether the font preload/display strategy is right. Prefer measured numbers to opinions.` },
  { key: 'product-gaps', prompt: `Dimension: PRODUCT AND USER EXPERIENCE. Put yourself in the shoes of the three people who use this: the admin who set it up (usually the couple or the host), an editor relative in another country who covers some lines, and a viewer. Read web/src/index.html, app.js (the screens: overview/run-up, budget grid, tasks, sponsors, settings, archive, import/export), auth.js (screens), docs/architecture.md Roadmap "Open", README "What is not there". Then start the binary with \`${CD}SOIREE_DEMO_DATA=true SOIREE_LISTEN_ADDR=:18081 SOIREE_METRICS_ADDR=:19091 go run ./cmd/soiree &\` and fetch / to see the shell (no browser needed for structure; if e2e/node_modules already exists you may take a screenshot with Playwright, but do not install anything; kill the binary afterwards). Identify: half-built features that the schema and API support but the UI does not (phases, programme/run of show, parent/child items, lock_by surfacing, privacy export/erasure) and what finishing each would take; missing views a planner actually needs (a per-person "what I have covered / what I still owe" statement that can be shared or printed, a payments log with dates rather than a single paid figure, vendor-centric view, a printable/PDF summary, a read-only share link for someone without an account, currency rate date, guest headcount driving per-head lines, undo); confusing or risky flows (destructive actions without confirmation, silent save failures, what a viewer sees when they try to edit, first-run experience on an empty database, what the archive hides); onboarding gaps (no in-app help, how the admin invites people). For every idea say whether it sits inside the stated scope ("a tool a dozen people use for one evening", one event per deployment) and estimate effort. Ground every claim in what the code shows; do not describe screens you did not read.` },
  { key: 'measurements', prompt: `Dimension: MEASUREMENTS. You run tools and report exactly what they say; findings come from tool output, not opinion. You are the only reviewer allowed to run the database-backed Go tests, npm ci, and the browser suite. From the repository root run each of the following, capture output under ${OUT}/measurements/, and report results verbatim (trimmed) with the exact command. If a tool cannot be installed or fails for environmental reasons, say so precisely and move on; do not spend more than a few attempts on any one tool. 1) \`go test -short -cover ./... 2>&1 | tee ${OUT}/measurements/gotest-short.log | tail -40\` (report failures and per-package coverage). 2) If \`docker info\` shows a responding Server, start the suite CI runs in the background now and collect it at the end: \`go test -race -count=1 -cover -timeout 30m ./... > ${OUT}/measurements/gotest-full.log 2>&1 &\` (it pulls postgres:18-alpine once; report per-package times, the total, and any failure verbatim; the ci.yaml comment says internal/httpd alone took over 500 s under -race, so say what you measured). 3) \`go vet ./...\` and \`gofmt -l .\`. 4) \`go run golang.org/x/vuln/cmd/govulncheck@latest ./...\`. 5) \`go run honnef.co/go/tools/cmd/staticcheck@latest ./...\`. 6) \`golangci-lint run ./... --timeout 10m\` if installed, else \`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./... --timeout 10m\` (CI runs it with default config; report every finding verbatim). 7) \`go mod verify\` and \`go list -m -u -json all 2>/dev/null | grep -B2 -A2 '"Update"' | head -80\` for outdated direct deps (network may block; report). 8) Check the frontend for CSP violations statically: \`grep -n 'style="' web/src/index.html | head\`, \`grep -n "\\.style\\.\\|setAttribute('style'\\|innerHTML" web/src/app.js web/src/auth.js | wc -l\` and list a sample with line numbers, \`grep -n 'javascript:\\|onclick=\\|on[a-z]*=' web/src/index.html | head\`. 9) Start the binary: \`SOIREE_LISTEN_ADDR=:18082 SOIREE_METRICS_ADDR=:19092 SOIREE_EVENT_NAME=Measure go run ./cmd/soiree > ${OUT}/measurements/server.log 2>&1 &\` wait for /healthz, then with curl -sS -D - -o /dev/null print full response headers for /, the hashed app.js and styles.css that index.html references (grep them out of /), /sw.js, /manifest.webmanifest, /robots.txt, /healthz, /api/v1/plan (expect 404 without DB), and http://localhost:19092/metrics (list the metric names). Measure compressed sizes: for each asset referenced by index.html run curl -sS -H 'Accept-Encoding: br' -o /dev/null -w '%{size_download}\\n' and again with gzip and identity. Time startup from process start to first 200 on /healthz. Kill the server afterwards. 10) Lines of code per package: \`find . -name '*.go' -not -name '*_test.go' | xargs wc -l | sort -rn | head -30\` and test LOC similarly. 11) e2e: \`cd e2e && npm ci\`; if no matching Chromium is present under PLAYWRIGHT_BROWSERS_PATH (or the default cache) and the machine allows it, \`npx playwright install chromium\`; then \`npx playwright test --reporter=line 2>&1 | tail -80\` with a 20 minute cap (the config builds and starts the real binary itself; the API specs skip themselves if Docker is unusable). Findings: one per notable tool result (a failing test, a lint finding, a vulnerability, a size that contradicts the README, a missing header, a slow startup, a slow package), each with the verbatim evidence.` },
  { key: 'supply-chain', prompt: `Dimension: SUPPLY CHAIN, DEPENDENCIES AND RELEASE. Files: go.mod, go.sum, Dockerfile, .github/workflows/ci.yaml and release-please.yaml, renovate.json, release-please-config.json, .release-please-manifest.json, docs/verifying-releases.md, SECURITY.md, e2e/package.json and package-lock.json, .dockerignore. Look for: direct dependencies and whether each is needed (is testcontainers only imported from _test files? run \`${CD}go list -deps ./cmd/soiree | grep -c testcontainers\` to prove it is or is not linked into the binary), maintenance status and pinning of webpush-go, go-webauthn, brotli, cbor; go.mod's go directive vs CONTRIBUTING's stated floor vs the Dockerfile's builder image (does the module build with the stated floor at all: check for newer-only APIs used); toolchain directive; Renovate config correctness (does it actually cover the Dockerfile base image, the GO_VERSION env, the redocly version, npm in e2e; schedule; automerge policy; lockfile maintenance); action digest pins all present and matching their version comments; release-please config (the CHANGELOG has doubled entries for the same change because GitHub writes the pull request title into the merge commit body and release-please parses the whole message; confirm, and give the fix); GITHUB_TOKEN permissions per job least-privilege; cosign keyless identity regexp correctness in ci.yaml vs docs; SBOM step edge cases; Dockerfile: builder image digest pin, go mod download cache mount, reproducibility risks (-buildvcs default, SOURCE_DATE_EPOCH), .dockerignore completeness (e2e/node_modules, .git); image scanning absence; SECURITY.md accuracy (supported versions, response times, scope); licence file completeness (LICENSES/, the OFL text, the font CONTRIBUTING names).` },
]

const FINDINGS_SCHEMA = {
  type: 'object',
  properties: {
    findings: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          title: { type: 'string', description: 'one line, specific' },
          kind: { type: 'string', enum: ['bug', 'improvement'] },
          category: { type: 'string', enum: ['security', 'correctness', 'reliability', 'ux', 'accessibility', 'i18n', 'performance', 'maintainability', 'docs', 'testing', 'product', 'supply-chain', 'api', 'data-model'] },
          severity: { type: 'string', enum: ['high', 'medium', 'low'] },
          effort: { type: 'string', enum: ['small', 'medium', 'large'] },
          file: { type: 'string', description: 'repo-relative path' },
          line: { type: 'integer' },
          evidence: { type: 'string', description: 'quoted code or tool output you actually read, with line numbers' },
          problem: { type: 'string', description: 'what is wrong or missing and why it matters to a user or the owner' },
          recommendation: { type: 'string', description: 'concrete change: where and roughly how' },
          conflicts_with_documented_decision: { type: 'string', description: 'empty string if none; otherwise which decision and why it is still worth it' },
        },
        required: ['title', 'kind', 'category', 'severity', 'effort', 'file', 'evidence', 'problem', 'recommendation', 'conflicts_with_documented_decision'],
      },
    },
    coverage_notes: { type: 'string' },
  },
  required: ['findings', 'coverage_notes'],
}

const VERDICT_SCHEMA = {
  type: 'object',
  properties: {
    refuted: { type: 'boolean' },
    confidence: { type: 'string', enum: ['low', 'medium', 'high'] },
    reasoning: { type: 'string', description: 'what you read (file:line) and why it confirms or refutes' },
    revised_severity: { type: 'string', enum: ['high', 'medium', 'low'] },
    correction: { type: 'string', description: 'if the finding is partly right or could be sharper, the accurate version; else empty string' },
  },
  required: ['refuted', 'confidence', 'reasoning', 'revised_severity', 'correction'],
}

const COMBINED_SCHEMA = {
  type: 'object',
  properties: {
    fact_refuted: { type: 'boolean' },
    fact_confidence: { type: 'string', enum: ['low', 'medium', 'high'] },
    fact_reasoning: { type: 'string' },
    value_refuted: { type: 'boolean' },
    value_reasoning: { type: 'string' },
    revised_severity: { type: 'string', enum: ['high', 'medium', 'low'] },
    correction: { type: 'string' },
  },
  required: ['fact_refuted', 'fact_confidence', 'fact_reasoning', 'value_refuted', 'value_reasoning', 'revised_severity', 'correction'],
}

const FACTS = `Question, facts: is the claim true of the code right now? Open the cited file and lines, the surrounding code, the callers, the middleware chain in internal/httpd/server.go if relevant, and the tests (*_test.go, e2e/tests/*.spec.js) that cover the area. The claim is refuted if: the code does not do what the finding says; the problem is already handled elsewhere (a wrapper, middleware, a startup check, a constraint in a migration, a test that proves the opposite); the evidence is misquoted or misread; a claimed bug cannot be triggered with the input/state described. If you cannot confirm the claim from the code, it is refuted with confidence low. If the claim is partly right, it is not refuted, and you write the accurate version in correction.`

const VALUE = `Question, value: assuming the claim is true, is acting on it worth the maintainer's time for this project? Read CONTRIBUTING.md ("Two house rules"), docs/architecture.md "Roadmap" (the Done list, "Open", "Explicitly out of scope"), "Latency strategy" incl. "Deliberately excluded", and the feature section nearest the finding; README.md "Design notes" and "What is not there". The project is "a tool a dozen people use to plan one evening", one event per deployment, no build step, no third-party origins, strict CSP, migrations append-only. It is refuted if: it contradicts a documented deliberate decision and the finding does not acknowledge that and outweigh it; it merely restates an item already listed as known and deferred without adding a concrete plan or new information; it is out of scope (multi-event tenancy, plugins, analytics, marketing site); the benefit for a dozen-person tool is clearly smaller than the cost or the risk; it is a matter of taste with no user-visible or maintainer-visible effect.`

const SEVERITY = `Set revised_severity to what it deserves: high = data loss, security, money wrong, or a broken user flow; medium = real degradation or a clearly missing piece a user would hit; low = polish, hygiene, drift. If you have a sharper phrasing of the recommendation, put it in correction. Do not modify any file. Return only the structured verdict.`

function factsPrompt(f) {
  return `You are a skeptical senior engineer verifying ONE claimed finding about the soiree app, ${WHERE}. Your job is to try to REFUTE it on the facts.\n\n${FACTS}\n\n${SEVERITY}\n\nFINDING:\n${JSON.stringify(f, null, 2)}`
}

function valuePrompt(f) {
  return `You are the maintainer's devil's advocate for ONE proposed improvement to the soiree app, ${WHERE}. ASSUME the factual claim is true.\n\n${VALUE}\n\n${SEVERITY}\n\nFINDING:\n${JSON.stringify(f, null, 2)}`
}

function combinedPrompt(f) {
  return `You are a skeptical senior engineer verifying ONE claimed finding about the soiree app, ${WHERE}. Answer two questions in order.\n\n1. ${FACTS} Report it as fact_refuted.\n\n2. Only if fact_refuted is false (otherwise set value_refuted=true too): ${VALUE} Report it as value_refuted.\n\n${SEVERITY}\n\nFINDING:\n${JSON.stringify(f, null, 2)}`
}

function shortLabel(s) {
  return String(s || '').replace(/\s+/g, ' ').slice(0, 48)
}

function statusOf(real, worth) {
  if (!real || !worth) return 'unverified'
  if (real.refuted) return 'refuted-on-facts'
  if (worth.refuted) return 'refuted-on-value'
  return 'confirmed'
}

async function verifyOne(f, dimKey) {
  const label = `${dimKey}:${shortLabel(f.title)}`
  if (A.lean) {
    const v = await agent(combinedPrompt(f), { label: `verify:${label}`, phase: 'Verify', schema: COMBINED_SCHEMA })
    if (!v) return { ...f, dimension: dimKey, status: 'unverified', real: null, worth: null }
    const real = { refuted: v.fact_refuted, confidence: v.fact_confidence, reasoning: v.fact_reasoning, revised_severity: v.revised_severity, correction: v.correction }
    const worth = { refuted: v.value_refuted, confidence: v.fact_confidence, reasoning: v.value_reasoning, revised_severity: v.revised_severity, correction: '' }
    return { ...f, dimension: dimKey, status: statusOf(real, worth), real, worth }
  }
  const vs = await parallel([
    () => agent(factsPrompt(f), { label: `real:${label}`, phase: 'Verify', schema: VERDICT_SCHEMA }),
    () => agent(valuePrompt(f), { label: `worth:${label}`, phase: 'Verify', schema: VERDICT_SCHEMA }),
  ])
  return { ...f, dimension: dimKey, status: statusOf(vs[0], vs[1]), real: vs[0], worth: vs[1] }
}

async function verifyAll(found, dimKey) {
  if (!found || !found.findings) return { dimension: dimKey, coverage_notes: found ? found.coverage_notes : 'agent returned nothing', items: [] }
  log(`${dimKey}: ${found.findings.length} findings, verifying`)
  const items = (await parallel(found.findings.map(f => () => verifyOne(f, dimKey)))).filter(Boolean)
  log(`${dimKey}: ${items.filter(i => i.status === 'confirmed').length}/${items.length} confirmed`)
  return { dimension: dimKey, coverage_notes: found.coverage_notes, items }
}

const selectedKeys = (Array.isArray(A.dimensions) && A.dimensions.length) ? A.dimensions : DIMENSIONS.map(d => d.key)
const SELECTED = DIMENSIONS.filter(d => selectedKeys.includes(d.key))
log(`dimensions: ${SELECTED.map(d => d.key).join(', ')}${A.lean ? ' (lean: one skeptic per finding)' : ''}`)

phase('Find')
const round1 = (await pipeline(
  SELECTED,
  d => agent(`${COMMON}\n\n${d.prompt}`, { label: `find:${d.key}`, phase: 'Find', schema: FINDINGS_SCHEMA }),
  (found, d) => verifyAll(found, d.key),
)).filter(Boolean)
const all1 = round1.flatMap(r => r.items)
log(`round 1: ${all1.length} findings, ${all1.filter(i => i.status === 'confirmed').length} confirmed`)

let round2 = []
let critic = null
if (!A.skipCritique) {
  phase('Critique')
  const CRITIC_SCHEMA = {
    type: 'object',
    properties: {
      gaps: {
        type: 'array',
        items: {
          type: 'object',
          properties: {
            key: { type: 'string', description: 'short slug' },
            prompt: { type: 'string', description: 'a complete reviewer brief: files to read, what to look for, why it was likely missed' },
          },
          required: ['key', 'prompt'],
        },
      },
      rationale: { type: 'string' },
    },
    required: ['gaps', 'rationale'],
  }
  critic = await agent(`You are the completeness critic for a code review of the soiree app, ${WHERE}. Reviewers each covered one dimension. Below are the dimensions with their coverage notes and the titles of every finding (with verification status). Read the repository layout yourself (README.md "Layout"; ls -R internal web/src cmd e2e/tests migrations) and decide what was likely missed: a package nobody read (internal/sheetimport and cmd/soiree-import, internal/reminders rendering, internal/mailer message building, internal/push), a user flow nobody traced end to end (invitation mail -> set-password -> first plan load; attachment upload -> sweeper; archive turnover; session expiry while typing), or a class of problem no dimension owns (time zones across server, database and browser; Unicode and RTL in names; very large plans; browser support; the upgrade path for an existing database). Return up to 4 gaps, each as a complete reviewer brief a fresh agent can execute read-only; if a brief needs the binary, assign it a port in 18084-18089 with the metrics port 1000 higher. Do not repeat a dimension that was covered unless its coverage notes admit it did not finish; in that case name exactly what remained.

DIMENSIONS AND FINDINGS:
${JSON.stringify(round1.map(r => ({ dimension: r.dimension, coverage_notes: r.coverage_notes, findings: r.items.map(i => `${i.status}: ${i.title}`) })), null, 2)}`,
    { label: 'critic', phase: 'Critique', schema: CRITIC_SCHEMA })

  if (critic && critic.gaps && critic.gaps.length) {
    log(`critic found ${critic.gaps.length} gaps: ${critic.gaps.map(g => g.key).join(', ')}`)
    round2 = (await pipeline(
      critic.gaps,
      g => agent(`${COMMON}\n\nDimension: ${String(g.key).toUpperCase()} (second-round gap identified by a completeness critic).\n${g.prompt}`, { label: `find2:${g.key}`, phase: 'Find', schema: FINDINGS_SCHEMA }),
      (found, g) => verifyAll(found, `gap:${g.key}`),
    )).filter(Boolean)
    const all2 = round2.flatMap(r => r.items)
    log(`round 2: ${all2.length} findings, ${all2.filter(i => i.status === 'confirmed').length} confirmed`)
  } else {
    log('critic found no gaps')
  }
}

phase('Synthesize')
const everything = [...round1, ...round2].flatMap(r => r.items)
const confirmed = everything.filter(i => i.status === 'confirmed')
const factRefuted = everything.filter(i => i.status === 'refuted-on-facts')
const valueRefuted = everything.filter(i => i.status === 'refuted-on-value')
const unverified = everything.filter(i => i.status === 'unverified')

function slim(i) {
  return {
    dimension: i.dimension, title: i.title, kind: i.kind, category: i.category,
    severity: i.severity, effort: i.effort, file: i.file, line: i.line,
    evidence: i.evidence, problem: i.problem, recommendation: i.recommendation,
    conflicts_with_documented_decision: i.conflicts_with_documented_decision,
    reality_check: i.real ? { confidence: i.real.confidence, revised_severity: i.real.revised_severity, correction: i.real.correction, reasoning: i.real.reasoning } : null,
    value_check: i.worth ? { revised_severity: i.worth.revised_severity, correction: i.worth.correction, reasoning: i.worth.reasoning } : null,
  }
}

const report = await agent(`You are writing the final improvement report for the owner of the soiree app, ${WHERE}, who asked "look at the soiree app and tell me how to improve". You are an editor: merge, rank and phrase. You may open files to resolve a contradiction between two findings, but do not go looking for new ones, and do not modify any tracked file.

Inputs: CONFIRMED findings (a facts check and a value check both passed, with corrections), and REFUTED-ON-VALUE findings (true but judged not worth it; include at most a short "considered and set aside" list of the ones a maintainer would still want to know exist). Ignore the facts-refuted ones entirely.

Write Markdown to the file ${OUT}/synthesis.md (mkdir -p ${OUT}) AND return the same Markdown as your final output. Structure:
1. "Where it stands": 3 to 5 sentences of honest overall assessment, what is strong and what the real weaknesses are.
2. "Fix now": bugs and security issues, highest first. Each item: bold one-line title, then 1-3 sentences: what is wrong, where (file:line), what to do. Use the verifiers' corrections and revised severities; if the two verifiers disagree on severity, use the lower unless the facts check raised it.
3. "Quick wins": small-effort improvements worth a single sitting each.
4. "Structural": medium and large improvements to code, tests and operations, grouped by theme, each with the why and the first step.
5. "Product": feature and UX gaps worth building inside the project's stated scope, with effort.
6. "Docs drift": a compact list, sentence -> reality.
7. "Considered and set aside": one line each, with the reason.
Merge duplicates across dimensions into one item and keep every file:line reference that helps. Keep each item tight; no padding, and do not restate the project's design philosophy back to the owner. Use the maintainer's own vocabulary (run-up, plan, shadow copy, Store seam). Do not use em dashes. Do not invent anything not present in the inputs. Aim for roughly 1500-2500 words.

CONFIRMED (${confirmed.length}):
${JSON.stringify(confirmed.map(slim), null, 1)}

REFUTED ON VALUE (${valueRefuted.length}):
${JSON.stringify(valueRefuted.map(i => ({ title: i.title, file: i.file, why_set_aside: i.worth ? i.worth.reasoning : '' })), null, 1)}

UNVERIFIED (a verifier died; low confidence, include only if the evidence is self-evidently solid) (${unverified.length}):
${JSON.stringify(unverified.map(slim), null, 1)}`,
  { label: 'synthesis', phase: 'Synthesize' })

return {
  counts: {
    total: everything.length,
    confirmed: confirmed.length,
    refuted_on_facts: factRefuted.length,
    refuted_on_value: valueRefuted.length,
    unverified: unverified.length,
  },
  per_dimension: [...round1, ...round2].map(r => ({ dimension: r.dimension, total: r.items.length, confirmed: r.items.filter(i => i.status === 'confirmed').length, coverage_notes: r.coverage_notes })),
  critic_rationale: critic ? critic.rationale : null,
  refuted_on_facts_titles: factRefuted.map(i => `${i.dimension}: ${i.title}`),
  report_file: `${OUT}/synthesis.md`,
  report,
}
