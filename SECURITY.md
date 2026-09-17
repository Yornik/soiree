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

The project is pre-1.0 and has one maintainer. A fix lands on `main` and goes
out in the next release; nothing is backported, so upgrading is the only
remediation on offer. If that does not work for your deployment, say so in the
report rather than assuming it is understood.

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

- **The image tag or commit.** Every instance exposes
  `soiree_build_info{version,commit}` on `/metrics`, and stamps the same pair
  into its startup log line, so this is precisely answerable rather than a
  guess.
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
by which that image is published. The store and migration layers are included
even though the binary does not use them yet; they ship shortly and a flaw
found now is cheaper than one found later:

- `internal/httpd` — routing, cache and conditional-request handling, the
  headers the binary sets, the rendered shell and service worker.
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

- **There is no authentication or authorization.** Accounts and roles are step 4
  of the roadmap in [docs/architecture.md](docs/architecture.md). Today, anyone
  who can reach an instance can read and change everything in it. Who can reach
  it is the operator's decision, and "the app has no login" is the documented
  current state, not a finding.
- **Missing TLS, HSTS and CSP on a bare `docker run`.** The binary sets
  `X-Content-Type-Options`, `Referrer-Policy` and `X-Frame-Options`; TLS, HSTS
  and a strict Content-Security-Policy are applied at the ingress in the
  deployed setup. A report that an instance you started with `docker run -p
  8080:8080` speaks plain HTTP is describing the deployment, not the software.
- **Last-write-wins on concurrent edits**, and state living in the browser's
  `localStorage`. Both are known limitations with per-row revisions and a
  server-side store on the roadmap.
- **Denial of service by flooding** an instance you can already reach. There is
  no rate limiting yet, and this is not news.
- **Scanner output with no demonstrated impact on this code.** `govulncheck`
  runs on every pull request and reports only vulnerabilities actually
  reachable from this binary. A CVE in a dependency along a path that is never
  called is not by itself a finding — send the reachable call path and it is.
- The bundled Fraunces font, and the demo data behind `SOIREE_DEMO_DATA`, which
  is obviously fictional and only appears when asked for.
- Missing roadmap features in `docs/architecture.md` — audit trail, data
  protection tooling, deadline reminders. They are already recorded as missing.

Anything not covered above: report it anyway. A wrong guess about scope costs
far less than an unreported flaw.
