# Contributing

Thanks for looking. This is a small project with a narrow purpose — a tool a
dozen people use to plan one evening — so the fastest way to get a change
merged is to open an issue first and check the idea fits. Bug fixes and
anything in the roadmap in [docs/architecture.md](docs/architecture.md) never
need that conversation.

Everyone taking part is expected to follow the
[Code of Conduct](CODE_OF_CONDUCT.md). Security flaws go through
[SECURITY.md](SECURITY.md), not a pull request or a public issue.

## Running it

The Go toolchain is the only dependency for the application itself. The
frontend is embedded with `//go:embed` and processed at startup, so there is no
asset build and nothing to install for `web/src`.

```bash
go run ./cmd/soiree                        # http://localhost:8080
SOIREE_DEMO_DATA=true go run ./cmd/soiree  # with obviously fake sample data
```

The image is built with Go 1.27 and CI tests on the same version; `go.mod`
declares 1.26 as the floor.

## Tests

```bash
go test -short ./...    # no Docker required
go test -race ./...     # the full suite: needs Docker
```

`-short` is the everyday loop. Anything that needs a container skips itself
under it: the tests that talk to PostgreSQL start a real `postgres:18-alpine`
through testcontainers, the same major version the production cluster runs,
and the attachment tests start a real MinIO. Mocking either would defeat its
purpose: a fake database would happily accept a cascade that does not exist and
a `CHECK` that never fires, and a fake bucket, written by whoever wrote the
signer, would agree with the signer by construction.

Note that `-short` is a local convenience, not a lower bar: CI runs
`go test -race -cover ./...`, the full suite rather than the `-short` subset.
Run it yourself before opening a pull request if your change goes anywhere near
`internal/store`, `internal/migrate` or `migrations/`.

For work against a database by hand, `compose.yaml` brings up a throwaway
PostgreSQL alongside the app:

```bash
docker compose up --build   # http://localhost:8080
docker compose down         # leaves nothing behind
```

Its data directory is a tmpfs and durability is off, so every run starts from
the migrations rather than from whatever a previous branch left behind. That is
a development-only setting — never point it at data you care about.

Worth knowing before you go looking for it: the binary mounts `/api/v1` only
when `DATABASE_URL` is set. The compose stack sets it, so the app in that stack
is the shared planner against a real database. A plain `go run ./cmd/soiree`
leaves it unset and serves the planner alone, with state in the browser — a
supported mode, not a broken one.

## Commits

The release is cut by
[release-please](https://github.com/googleapis/release-please), which reads
commit subjects, so the
[Conventional Commits](https://www.conventionalcommits.org/) format is load
bearing rather than a style preference:

```
feat(store): record who last changed a budget line
fix(httpd): serve the service worker with no-cache
docs(security): add a disclosure policy
```

- `feat:` — a minor bump and a changelog entry
- `fix:` — a patch bump and a changelog entry
- `docs:`, `chore:`, `ci:`, `refactor:`, `test:`, `build:` — no release
- `!` after the type, or a `BREAKING CHANGE:` footer — a major bump. The
  repository is past 1.0.0, so this is 1.x to 2.0.0. Use it only when you mean
  that.

A wrong type is not cosmetic: `feat:` on a documentation-only change mints a
release nobody meant to cut. Whatever subject lands on `main` is what
release-please reads.

Pull requests are squashed rather than merged, so one commit per pull request
lands on `main` and the pull request title is the subject that matters. That is
a rule rather than a preference: GitHub writes the title of a merged pull
request into the merge commit's body, release-please reads that body as a
commit of its own, and the change is then listed in `CHANGELOG.md` twice, once
under the branch commit and once under the merge.

Branches follow `feat/<scope>`, `fix/<scope>` and so on, branched from current
`main`.

## What CI checks

Seven jobs run on every pull request
([`.github/workflows/checks.yaml`](.github/workflows/checks.yaml), which
`ci.yaml` calls). All of them must pass.

| Job | What it does |
|---|---|
| Lint | `gofmt -l .` must print nothing, then `go vet ./...`, then `golangci-lint` |
| Test | `go test -race -cover ./...` — the full suite, Docker included |
| Browser tests | The Playwright specs in `e2e/`, against the real binary, which the suite builds and starts itself |
| API description | `redocly lint` on `api/openapi.yaml` — valid OpenAPI 3.1; that the routes it describes exist is checked by the Go tests |
| Vulnerabilities | `govulncheck ./...` |
| Build image | Builds the `Dockerfile`, runs the image, and smoke tests it |
| Reproducible build | Builds the binary twice and compares the bytes |

Three of those are worth expanding on.

**golangci-lint** runs with no configuration file in the repository, so it uses
its default linter set — which includes `errcheck`. An ignored error is a
failure, including in tests; write `_ =` where dropping the value is genuinely
what you mean.

**The smoke test** exists because a green unit suite does not prove the image
boots. It starts the built image, waits for `/healthz`, checks that
`SOIREE_EVENT_NAME` actually reaches the rendered page, and fetches `/sw.js`.
If you change the startup pipeline in `internal/httpd/assets.go`, this is the
job that catches an asset that no longer resolves.

**The reproducibility check** builds the binary on two independent BuildKit
instances with caching off and fails if the two are not byte-identical. So
nothing may make the build depend on when or where it ran: no embedded build
timestamp, no generated file that is not committed, no absolute path leaking
into the binary. It compares the binary, not the image digest — BuildKit stamps
a build time into the image config, so identical source still yields different
image digests. That is expected.

Releases add cosign signing, SBOM publication and a verification step that
re-runs the exact commands in
[docs/verifying-releases.md](docs/verifying-releases.md) against the image that
was just pushed. Actions are pinned to commit digests with the readable version
in a trailing comment; Renovate maintains both, so leave the comment format
alone.

## Two house rules

These are the ones that get a pull request sent back, and neither is obvious
from reading the code.

### No third-party origins in the frontend

Not a font from a CDN, not a script, not an icon set, not an analytics beacon,
not a preconnect to somewhere else. The people using an instance are spread
across the world and there is no CDN in front of the origin. Each extra origin
costs a DNS lookup, a TCP connection and a TLS handshake before first paint —
about a second on a 300 ms link, which is more than the entire rest of the
page. First paint is currently about 21 kB: the shell plus the stylesheet that
blocks it, which the README's table measures.

The binary sends a strict Content-Security-Policy of its own, which is why the
page carries no inline script that executes and no inline `style=` attribute
anywhere. Configuration reaches the browser as a
`<script type="application/json">` data block — data, not executable script, so
`script-src 'self'` permits it. A deployment whose proxy would rather send the
policy sets `SOIREE_CSP=off`.

Both halves are checked in CI, though neither check is exhaustive:
`TestNoExternalOrigins` reads the shell, the worker and the stylesheet for an
absolute URL, and every browser test now runs under the policy, so a resource
it blocks fails whichever spec needs it, and `e2e/tests/csp.spec.js` fails on
any violation the flows it drives report. Review is still what catches the
rest. If you need something a third party provides, vendor it into `web/src` or
do without it. The right place to argue the point is an issue, not a pull
request.

### Migrations are append-only

`internal/migrate` takes a SHA-256 of every migration file and records it in
`schema_migrations` when it applies it. On the next run it compares them, and a
recorded migration whose bytes have changed is a hard error:

```
migration 0003_plan.sql was modified after it was applied (recorded …, file …)
```

That is deliberate. A changed migration means two databases that both report
the same schema version no longer have the same schema, and refusing to proceed
is the only honest response.

So: never edit a migration that has been applied anywhere, including on someone
else's development database. Add `NNNN_name.sql` with the next number instead —
versions must be unique and are applied in numeric order. Editing an unapplied
migration you added in the same, unmerged pull request is fine; recreate your
local database (`docker compose down && docker compose up --build`) if it has
already run.

The checksum covers the whole file, so a comment and a line ending count for
as much as the SQL does. Correct a stale comment in a migration with
`COMMENT ON` in a new one — `0009_phase_revision.sql` is the precedent — or in
`docs/architecture.md`; never in place. `releasedChecksums` in
`internal/migrate/released_test.go` pins the bytes of every migration that has
shipped, so a file that changes fails `go test -short` instead of somebody's
next deploy. A new migration adds its line there in the pull request that adds
it.

Append-only is about the bytes. The other half of the rule is about time: a
migration has to leave the previous release's binary working. The new pod
migrates before it listens while the old one is still serving, so every rollout
runs the release before yours against your schema, and a rollback runs it for
longer. Add columns nullable or with a default, the way
`0009_phase_revision.sql` does; drop or rename one only in a release after the
code stopped reading it. `change_log` carries a trigger that refuses every
UPDATE and DELETE on it, so a migration that has to touch those rows disables
and re-enables it in the same file: the comment above that trigger in
`migrations/0007_audit.sql` asks for exactly that, deliberately rather than by
accident.

## Style

Follow what is there. Comments explain *why* a thing is the way it is, not what
the line does — the existing code and `docs/architecture.md` are the reference
for the tone. Prose wraps at about 80 columns. Add the reasoning for a
non-obvious decision where a reader will meet it, and put anything longer in
`docs/`.

## Licensing

Contributions are accepted under the MIT license in [LICENSE](LICENSE). There
is no CLA. The bundled Bricolage Grotesque font is separately licensed under
the SIL Open Font License 1.1
([LICENSES/OFL-Bricolage-Grotesque.txt](LICENSES/OFL-Bricolage-Grotesque.txt))
and is not covered by that.
