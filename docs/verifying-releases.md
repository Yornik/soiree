# Verifying a soiree release

Every released image is signed, and ships a bill of materials and build
provenance. None of that is worth anything unless somebody checks it, so this
page is the exact set of commands to run. You do not need to trust this
repository, the maintainer, or GHCR — only the public Sigstore transparency log
and GitHub's OIDC issuer. What GHCR could still do on its own is move a tag
onto a different image these workflows signed, which is what the version
annotation in every command below is there to catch.

Images are published to `ghcr.io/yornik/soiree`, `linux/amd64` only.

## What a release publishes

| Artifact | Where it lives | How to check it |
|---|---|---|
| Container image | `ghcr.io/yornik/soiree:vX.Y.Z` (and `:latest`, when that release is the newest one) | — |
| Cosign signature | Sigstore's transparency log + alongside the image in GHCR | `cosign verify` (below) |
| SBOM (SPDX) | Attached to the image as a BuildKit attestation, **and** as a release asset `soiree-vX.Y.Z-sbom.spdx.json` on the [GitHub release](https://github.com/Yornik/soiree/releases) | `docker buildx imagetools inspect` (below) |
| Build provenance (SLSA, `mode=max`) | Attached to the image as a BuildKit attestation | `docker buildx imagetools inspect` (below) |

## Verify the signature

Signing is **keyless**: there is no soiree public key to distribute and no
private key that could leak. The signing identity is the GitHub Actions
workflow itself, certified by Fulcio against GitHub's OIDC issuer and recorded
in the Rekor transparency log. So the question `cosign verify` answers is not
"was this signed by someone with a key" but **"was this built by this workflow,
in this repository"**.

What it does not answer on its own is **which release**. Every release-please
build signs under that one `@refs/heads/main` identity, so a certificate cannot
tell one version from another, and `:vX.Y.Z` pointed at any other image the
workflow has ever signed would verify as happily as the release it names. So
the signature carries the version too, in a cosign annotation, and every
command below asks for it with `-a`. Anything else exits non-zero with
`missing or incorrect annotation`.

That annotation arrives with the first release cut after this page was written,
so the releases before it do not carry one, including the one the `TAG=` line
below names until the next release is cut. Those are properly signed and
`-a version=` simply fails closed on them: drop the flag to check one, and take
the signature for what it then is, a statement about the workflow alone.
Nothing below v1.0.0 may be run whether it verifies or not, since those
releases mount `/api/v1` with no authorisation at all.

Install [cosign](https://github.com/sigstore/cosign). The cosign these commands
are exercised against is whichever one the digest-pinned
`sigstore/cosign-installer` step in the release workflows installs. Renovate
moves that pin on its own, so a version number written here would go stale
without anybody touching the page.

Every command below names one release. Set it once, and release-please keeps
this line at the newest release so the page never advertises an old one:

<!-- x-release-please-start-version -->

```sh
TAG=v1.2.1   # or whichever release you are checking
```

<!-- x-release-please-end -->

Normal releases are cut by release-please, which builds and signs the image in
the same `push`-to-`main` run that creates the tag. The certificate identity
therefore ends in `@refs/heads/main` — **not** `@refs/tags/vX.Y.Z`, which is the
mistake most people make first:

```sh
cosign verify \
  --certificate-identity 'https://github.com/Yornik/soiree/.github/workflows/release-please.yaml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  -a "version=${TAG}" \
  "ghcr.io/yornik/soiree:${TAG}"
```

A release cut by pushing a `vX.Y.Z` tag by hand is built and signed by `ci.yaml`
instead, and its identity does end in the tag ref:

```sh
cosign verify \
  --certificate-identity "https://github.com/Yornik/soiree/.github/workflows/ci.yaml@refs/tags/${TAG}" \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  -a "version=${TAG}" \
  "ghcr.io/yornik/soiree:${TAG}"
```

To accept either without knowing which path produced a given release, use a
regexp — anchored, with the dots escaped, so it cannot match a lookalike
repository:

```sh
cosign verify \
  --certificate-identity-regexp '^https://github\.com/Yornik/soiree/\.github/workflows/(release-please|ci)\.yaml@refs/(heads/main|tags/v.+)$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --certificate-github-workflow-repository 'Yornik/soiree' \
  -a "version=${TAG}" \
  "ghcr.io/yornik/soiree:${TAG}"
```

Success prints a verification block (`Certificate subject`, `Certificate issuer
URL`, and the Rekor entry) followed by the signature payload, whose `optional`
field carries the `version` that was asked for and the commit it was built
from. A non-zero exit means the image is not signed by that identity, or is not
the release you asked for — treat it as untrusted.

The release workflow runs these same three commands against the image it just
pushed, and fails if any of them does, so this page cannot quietly drift out of
step with what is actually published.

Note the case: the repository is `Yornik/soiree`, so the certificate identity is
capitalised, while the image path `ghcr.io/yornik/soiree` is lowercase because
GHCR requires it. Both are correct as written.

### Verify by digest

The signature is made over the image **digest**, so verifying a tag is only as
good as the tag. If you are pinning a deployment, resolve and verify the digest:

```sh
DIGEST=$(docker buildx imagetools inspect "ghcr.io/yornik/soiree:${TAG}" --format '{{ json .Manifest }}' | jq -r '.digest')
cosign verify \
  --certificate-identity 'https://github.com/Yornik/soiree/.github/workflows/release-please.yaml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  -a "version=${TAG}" \
  "ghcr.io/yornik/soiree@${DIGEST}"
```

The digest comes from the tag, so on its own that resolves nothing: a moved tag
hands you the digest of whatever it now points at. The `-a` is what makes the
pair worth reading, since the digest it gives you has to be one signed as the
release you asked for.

That digest is an OCI **index** digest: the SBOM and provenance ride along as
extra manifests beside the image, and the index is what `:vX.Y.Z` points at,
and what `:latest` points at too when that release is the newest one. Signing
the index covers all of it.

## Read the SBOM and the provenance

The SBOM is published twice, deliberately. As a release asset it can be read
without pulling anything:

```sh
gh release download "${TAG}" --repo Yornik/soiree --pattern 'soiree-*-sbom.spdx.json'
```

And as an attestation on the image itself, which is the authoritative copy — the
release asset is extracted from it during the release run:

```sh
docker buildx imagetools inspect "ghcr.io/yornik/soiree:${TAG}" --format '{{ json .SBOM }}'
docker buildx imagetools inspect "ghcr.io/yornik/soiree:${TAG}" --format '{{ json .Provenance }}'
```

These are BuildKit in-toto attestations stored in the image index, not cosign
attestations, so `cosign verify-attestation` will **not** find them. Their
integrity comes from being inside the index whose digest the cosign signature
covers: verify the digest as above, and the SBOM and provenance under it cannot
have been swapped.

The image is `scratch` plus one static binary, so the SBOM is effectively the Go
module graph: soiree itself and its handful of direct and transitive
dependencies. A short package list is expected here, not a sign of a truncated
document.

`mode=max` provenance records the source commit, the workflow that ran, the
build arguments, and the base image — enough to trace a running container back
to a line of code.

## Reproducible builds

The image is a single static Go binary built with `-trimpath` and
`CGO_ENABLED=0`, so the same source built with the same toolchain produces the
same binary. The toolchain is fixed: the `Dockerfile` pins the builder image by
digest as well as by tag, so a build of the same commit months later compiles
with the Go release that produced the signed binary. That digest is recorded as
the base image in the `mode=max` provenance too, so it can be read off a
release rather than out of the repository. CI checks what it can rather than
claiming it: the `Reproducible build` job builds twice on two independent
BuildKit instances with caching disabled, and fails if the binaries differ byte
for byte.

What that does **not** assert is a reproducible *image digest*. BuildKit stamps
the build time into the image config, so two builds of identical source yield
different image digests. Reproduce the binary, not the digest.

## Enforcing this in a cluster

Verification a human does once is a spot check. To make it a rule, run an
admission policy — [Sigstore policy-controller](https://docs.sigstore.dev/policy-controller/overview/)
or [Kyverno](https://kyverno.io/docs/policy-types/cluster-policy/verify-images/)
— configured with the same certificate identity and OIDC issuer as the commands
above, so unsigned or foreign-built images are refused at admission. Have it
require the `version` annotation as well, or it accepts every release this
repository has ever signed as every other one. That policy belongs in whatever
deploys soiree, not in this repository, and is not yet deployed for the
maintainer's own cluster.

## If verification fails

- **`no matching signatures`** — most often the certificate identity. Check
  whether the release came from `release-please.yaml@refs/heads/main` or
  `ci.yaml@refs/tags/vX.Y.Z`; the regexp form above covers both.
- **`missing or incorrect annotation`** means the image is signed by that
  identity but was not cut as the version you asked for. Either the tag now
  points at another release, which is the case this check exists for, or the
  release predates the annotation. Running the same command without `-a`
  answers which: if that passes, read the payload it prints.
- **Images built before this was set up** are genuinely unsigned. Signing starts
  with the first release cut after this change; earlier tags will fail
  verification and that failure is correct.
- **`:latest`** moves. Verify the version tag or the digest.
