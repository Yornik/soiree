# The frontend is embedded into the binary and processed at startup, so there
# is no asset build stage — the Go compiler is the only build dependency.
FROM golang:1.27-alpine AS build

WORKDIR /src

# Dependencies first, so edits to the source do not invalidate the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static build: no cgo, trimmed paths, no symbol table. Produces a binary that
# runs on scratch.
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/soiree \
      ./cmd/soiree

FROM scratch

# Serves over plain HTTP behind a TLS-terminating proxy, so no CA bundle is
# needed. Add one here if the app ever makes outbound HTTPS calls.
COPY --from=build /out/soiree /soiree

# Unprivileged, and nothing in the image is writable — the whole site lives in
# the binary and in memory.
USER 65532:65532

EXPOSE 8080
ENV SOIREE_LISTEN_ADDR=:8080

ENTRYPOINT ["/soiree"]
