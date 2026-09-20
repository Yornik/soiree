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
FROM golang:1.27.1-alpine@sha256:4cb7ac979db5fcc41cae44b2227ba5ab8a51e8807f40d9ba4dee20a0ad960b5b AS build

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
# account mail — so it needs a trust store. On scratch there is none, and the
# failure is an unhelpful "certificate signed by unknown authority" at the
# moment the first mail is sent, long after deploy.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=build /out/soiree /soiree

# Unprivileged, and nothing in the image is writable — the whole site lives in
# the binary and in memory.
USER 65532:65532

EXPOSE 8080
ENV SOIREE_LISTEN_ADDR=:8080

ENTRYPOINT ["/soiree"]
