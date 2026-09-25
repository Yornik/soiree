# The frontend is embedded into the binary and processed at startup, so there
# is no asset build stage — the Go compiler is the only build dependency.
#
# Pinned by digest as well as by tag, for the reason every action in the
# workflows is: a tag names a line of releases rather than one of them, and it
# moves. go1.27.0 and go1.27.1 compile this source to different bytes, so
# without the digest the binary somebody rebuilds from a tag months from now is
# not the binary that was signed. Renovate moves the tag, the digest and
# GO_VERSION in one pull request, and a test in cmd/soiree fails if the
# Dockerfile and ci.yaml ever name different toolchains.
FROM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build

WORKDIR /src

# Dependencies first, so edits to the source do not invalidate the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Stamped into the binary and surfaced as the soiree_build_info metric, so a
# running pod can be traced back to a commit.
ARG VERSION=dev
ARG COMMIT=none

# Static build: no cgo, trimmed paths, no symbol table. Produces a binary that
# runs on scratch.
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/soiree \
      ./cmd/soiree

FROM scratch

# Inbound traffic is plain HTTP behind a TLS-terminating proxy, but the binary
# makes outbound TLS connections of its own — SMTP for the reminder digest and
# account mail, the browser vendors' push services for a notification, and the
# attachments bucket to confirm or remove an upload — so it needs a trust
# store. On scratch there is none, and the failure is an unhelpful "certificate
# signed by unknown authority" at whichever of those comes first, long after
# deploy.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=build /out/soiree /soiree

# A copy of the image is a copy of everything it redistributes, and the font it
# carries is under the OFL, which asks for its notice to travel with each copy.
# Whoever holds the image has no way back to this repository, so the texts come
# along. /licenses is where a reader and a licence scanner look for them.
COPY LICENSE /licenses/LICENSE
COPY LICENSES/ /licenses/

# An ARG does not cross a FROM, so the two the build stage took are declared
# again here. Without this the labels below would not fail — they would expand
# to empty strings, which is worse.
ARG VERSION=dev
ARG COMMIT=none

# A pod carries no history. Without these a `docker inspect` on what is running
# says nothing about where it came from, and Renovate cannot find the release
# notes behind an image bump. The version matters twice over: the SBOM cannot
# name it (the build context excludes .git, and -trimpath drops the ldflags
# from the build info), so the release asset lists soiree itself as UNKNOWN and
# the label is the only place the version of the whole thing survives.
LABEL org.opencontainers.image.title="soiree" \
      org.opencontainers.image.source="https://github.com/Yornik/soiree" \
      org.opencontainers.image.licenses="MIT AND OFL-1.1" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

# Unprivileged, and nothing in the image is writable — the whole site lives in
# the binary and in memory.
USER 65532:65532

EXPOSE 8080
ENV SOIREE_LISTEN_ADDR=:8080

ENTRYPOINT ["/soiree"]
