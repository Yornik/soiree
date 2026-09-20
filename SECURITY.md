# Security policy

soiree is a self-hosted web application. An instance holds the names of real
people and the money they have agreed to put in, for people who never signed up
for anything, so a way to report a flaw privately matters more here than the
size of the project suggests.

Releases are signed, ship an SBOM and build provenance, and are built from
digest-pinned actions. If you want to check what you are running before you
report anything about it, [docs/verifying-releases.md](docs/verifying-releases.md)
is the exact set of commands.

## Supported versions

| Version | Supported |
|---|---|
| The latest release — newest `vX.Y.Z` tag, which is what `ghcr.io/yornik/soiree:latest` points at | Yes |
| Anything older | No |

The project has one maintainer. A fix lands on `main` and goes out in the next
release; nothing is backported, so upgrading is the only remediation on offer.
If that does not work for your deployment, say so in the report rather than
assuming it is understood.

Releases before v1.0.0 mounted `/api/v1` with no authorisation at all, so
anyone who could reach one could read and change everything in it. Those tags
are not merely unsupported: do not run them.

Images built before keyless signing was set up are genuinely unsigned and will
fail `cosign verify`. That failure is correct and is not a vulnerability.

## Reporting a vulnerability

**Report privately through GitHub, not in a public issue.**

Use the repository's **Security** tab, then **Report a vulnerability** —
directly at
<https://github.com/Yornik/soiree/security/advisories/new>. The report is
visible only to you and the maintainer, and it becomes the published advisory
once a fix exists, so there is nothing to re-file later.

If that option is not available to you, open a normal issue saying only that
you have a security report and asking for a private channel. Keep the details
out of it.

A report is easiest to act on with:

- **The image tag or commit.** Every instance stamps the pair into its startup
  log line, and exposes `soiree_build_info{version,commit}` on the metrics
  listener (`SOIREE_METRICS_ADDR`, never the public port, where `/metrics` is a
  404). Signed in, the release is at the foot of the page as well. So this is
  precisely answerable rather than a guess.
- The relevant `SOIREE_*` configuration, with any real event details removed.
- What you did, what happened, and what you expected instead.
- What an attacker gets out of it, and what access they need first — whether
  the finding needs network reach to the instance, a file the instance imports,
  or something else.

Proof-of-concept code is welcome. Please do not test against an instance that
is not yours; run your own with `docker run` or `docker compose up`.

## What to expect

This is a spare-time project maintained by one person, so the honest version:
there is no paid response time and none is promised here. What is intended,
rather than guaranteed, is a first reply within a few days, a decision on
whether the report is accepted within roughly two weeks, and a release rather
than a private patch once there is a fix.

If a report goes quiet for two weeks, a nudge on the same advisory thread is
welcome — it means it was missed, not declined.

Disclosure is coordinated: the advisory is published when the fixed release is
out, and it names you as the reporter unless you would rather it did not. There
is no bug bounty and no payment of any kind.

If a report is declined, you will be told why. "Out of scope" below is not a
way of avoiding that conversation.

## Scope

In scope — the code in this repository, the image built from it, and the path
by which that image is published:

- **Authentication and authorisation**, which since v1.0.0 is most of what is
  worth attacking here: the session and the cookie that carries it, the role
  check (`RequireWrite`) that guards the whole `/api/v1` subtree, single-use
  set-password and reset links, passkeys, the rate limiters in front of login
  and password reset together with the client address they key on and the
  `X-Forwarded-For` they believe when `SOIREE_TRUST_PROXY_HEADERS` is set, and
  what a viewer rather than an editor or an admin can reach on the live stream
  at `/api/v1/events` and on the activity feed.
- `internal/auth` — Argon2id password hashing, and the minting and checking of
  single-use tokens.
- `internal/httpd` — routing, cache and conditional-request handling, the
  headers the binary sets, its refusal of cross-origin writes, the rendered
  shell and service worker.
- `internal/objstore` and the attachment routes — what a presigned URL is
  signed for and how long it lives, the quota on total size, and the content
  type and filename a download is served under.
- `internal/push` — who may register a subscription, and the endpoint a
  subscription names, which is a URL this server then requests.
- `internal/mailer` and `internal/reminders` — header injection through
  anything that reaches a message, and the links built from `SOIREE_BASE_URL`.
- `internal/config` — environment parsing, and the config data block rendered
  into the page.
- `internal/store` and `internal/migrate` — SQL handling, constraint and
  cascade mistakes, the migration ledger and its checksum guard.
- `internal/sheetimport` and `cmd/soiree-import` — these parse spreadsheets
  nobody wrote for this tool, with `archive/zip` and `encoding/xml` for `.ods`.
  Malicious-input handling here is squarely in scope.
- `web/src` — script injection through planner data, and anything that would
  let the service worker serve content it should not.
- The `Dockerfile` and the published image.
- `.github/workflows/` and the release path — anything that would let code
  that is not in this repository end up inside a signed image.

Out of scope, because these are known and documented rather than undiscovered:

- **Missing TLS, HSTS and CSP on a bare `docker run`.** The binary sets
  `X-Content-Type-Options`, `Referrer-Policy` and `X-Frame-Options`; TLS, HSTS
  and a strict Content-Security-Policy are applied at the ingress in the
  deployed setup. A report that an instance you started with `docker run -p
  8080:8080` speaks plain HTTP is describing the deployment, not the software.
- **The browser-only mode.** Started without `DATABASE_URL`, the binary
  registers neither the API nor the auth routes: the planner runs on its own
  with its state in `localStorage`, there is no account to sign in to, and
  there is no server-side copy of anything to protect. "It has no login" is the
  documented shape of that mode. With a database behind it the API is guarded
  and the rows carry revisions, and a way past either of those is a finding.
- **Denial of service by flooding** an instance you can already reach. The
  limiters in front of login and password reset are per process rather than
  cluster-wide, and `internal/httpd/ratelimit.go` says so at the top, along
  with what they are and are not for. A report that they do not stop a
  distributed attack is describing what is already written down. A way for one
  client to slip past them is a different thing, and is in scope.
- **Scanner output with no demonstrated impact on this code.** `govulncheck`
  runs on every pull request and reports only vulnerabilities actually
  reachable from this binary. A CVE in a dependency along a path that is never
  called is not by itself a finding — send the reachable call path and it is.
- The bundled Bricolage Grotesque font, and the demo data behind
  `SOIREE_DEMO_DATA`, which is obviously fictional and only appears when asked
  for.
- Gaps the README and [docs/architecture.md](docs/architecture.md) already
  record: nothing scans an uploaded file, the subject export, erasure and purge
  in `internal/store/privacy.go` have no route or subcommand to invoke them,
  and a session that expires by itself leaves that browser's copy of the plan
  behind.

Anything not covered above: report it anyway. A wrong guess about scope costs
far less than an unreported flaw.
