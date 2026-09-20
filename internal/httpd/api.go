package httpd

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Yornik/soiree/internal/store"
)

// The REST API over the store.
//
// It is mounted only when a database is configured. With no DSN these paths do
// not exist at all, so the binary still serves the frontend on its own — which
// is what a bare `docker run` does, and what the image smoke test checks.
//
// Nothing here is cached. Every response carries `no-store`: a budget two
// people are editing is the last thing that should be served from a proxy or a
// back/forward cache, and the service worker must not keep a copy either.

const (
	// apiPrefix is both the route prefix and, in metrics.go, the boundary for
	// classifying a request without putting its path in a label.
	apiPrefix = "/api/v1/"

	// maxRequestBytes bounds a write. The largest legitimate body here is a
	// note of a few paragraphs, so this is orders of magnitude of headroom and
	// still small enough that a hostile body costs nothing.
	maxRequestBytes = 64 << 10

	// readyzTimeout bounds the readiness probe's database check. A probe that
	// hangs is indistinguishable from one that fails, except that finding out
	// takes the prober's own timeout — during which this instance keeps
	// receiving traffic it cannot serve.
	readyzTimeout = 2 * time.Second
)

// Error codes. Stable strings, because a client branches on them.
const (
	errBadRequest    = "bad_request"
	errNotFound      = "not_found"
	errStaleRevision = "stale_revision"
	errTooLarge      = "payload_too_large"
	errConflict      = "conflict"
	errInternal      = "internal"
)

// apiError is the body of every non-2xx API response: one shape, so a client
// never has to guess. `current` is populated only on a 409, where it is the row
// as it now stands — the thing the client reconciles against instead of
// refetching the whole plan.
type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
	Current any    `json:"current,omitempty"`
}

// routeAPI mounts the API. Called only when a store is configured.
//
// It gets a mux of its own, mounted as a subtree, rather than being registered
// on the main one. That is what makes a wrong method on a real collection a 405
// with an Allow header: on the main mux the frontend's catch-all "/" matches
// every path and every method, which counts as a full match and stops Go's mux
// ever reaching its method-not-allowed branch.
func (s *Server) routeAPI(mux *http.ServeMux) {
	api := http.NewServeMux()
	api.HandleFunc("GET /plan", s.servePlan)
	api.HandleFunc("GET /events", s.serveEvents)
	api.HandleFunc("GET /version", serveVersion)

	// The currency is read once here rather than per request: it comes from the
	// environment and cannot change while the process runs. It is what turns
	// the minor units the store holds into the major units this API speaks —
	// see the note at the top of api_json.go.
	currency := s.cfg.Currency

	register(api, budgetItemEntity(s.store, currency))
	register(api, sponsorEntity(s.store, currency))
	register(api, taskEntity(s.store, currency))
	register(api, noteEntity(s.store, currency))
	register(api, phaseEntity(s.store, currency))
	register(api, programmeEntity(s.store, currency))
	api.HandleFunc("PATCH /settings", patchSettings(s.store, currency))
	if s.files != nil {
		s.files.route(api)
	}

	// Everything above is guarded as one subtree rather than per route.
	//
	// RequireWrite encodes the rule the API actually has — anybody signed in
	// may read, only an editor or an admin may write — and it is a property of
	// the method, not of the route. Attaching it per route is how one new route
	// added later ends up unguarded, which is exactly how this subtree spent
	// its first several releases: the middleware existed, the roles existed,
	// the sessions existed, and nothing here called any of it. A planner holds
	// people's names against amounts of money they owe each other, and all of
	// it was readable and writable by anyone who could reach the URL.
	//
	// A nil auth with a live store is a wiring mistake, not a deployment shape:
	// cmd/soiree builds both from the same DATABASE_URL. Refusing outright is
	// the only safe reading, because the alternative — quietly serving the API
	// unguarded — is the bug being fixed.
	guarded := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusServiceUnavailable, errInternal,
			"the API is not available because authentication is not configured")
	}))
	if s.auth != nil {
		guarded = s.auth.RequireWrite(withActor(api))
	}

	mux.Handle(apiPrefix, noStore(http.StripPrefix(strings.TrimSuffix(apiPrefix, "/"), guarded)))

	// Push is a subtree of its own rather than a hole in the guard above.
	//
	// It already decides its own authorisation, and decides it differently on
	// purpose: any live session may subscribe, because who *receives* a digest
	// is settled at send time from the account's role. A viewer registering a
	// device is harmless, and one promoted later starts receiving without
	// having to subscribe again. RequireWrite would refuse them.
	//
	// Mounted as a longer prefix so Go's mux prefers it, which keeps the rule a
	// property of where the routes live rather than a path exception inside the
	// middleware — the thing RequireWrite's own comment warns turns into an
	// unguarded route later.
	// The activity feed is an exact path rather than part of the subtree above,
	// because its rule is stricter than the subtree's: admins only. Go's mux
	// prefers the more specific pattern, so this is what answers a GET; any
	// other method falls through to the subtree, which has no such route.
	if s.auth != nil {
		mux.Handle("GET "+apiPrefix+"activity",
			noStore(s.auth.RequireRole(store.RoleAdmin)(http.HandlerFunc(s.serveActivity))))
	}

	push := http.NewServeMux()
	s.routePush(push)
	mux.Handle(apiPrefix+"push/", noStore(http.StripPrefix(strings.TrimSuffix(apiPrefix, "/"), push)))
}

// serveVersion says which release is running, to somebody signed in.
//
// Inside the guard on purpose. The public page does not name its build, for the
// reason /metrics is on a port of its own: "which version is this?" is the
// first thing anybody probing a deployment wants to know, and the only people
// with a use for the answer are the ones who can report a problem with it.
// The release, not the commit: that is what a person quotes in a bug report.
func serveVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Version string `json:"version"`
	}{Version})
}

// withActor names whoever is signed in as the author of anything they change.
//
// It sits inside the auth middleware, so the user is already in the context by
// the time this runs. store.WithActor puts it somewhere the write paths read on
// their own, which is why nine entities' worth of methods need no new argument.
//
// The ID only, never the address. store.Actor documents the reason and it is
// not incidental: change_log is append-only and outlives the account, so an
// email copied into it would survive the erasure that was supposed to remove
// it. The label is left to the store, which uses it for the kinds of actor that
// have no account at all — "system", "import".
func withActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := UserFrom(r.Context()); ok {
			id := u.ID
			next.ServeHTTP(w, r.WithContext(store.WithActor(r.Context(), store.Actor{ID: &id})))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// noStore marks the whole API subtree uncacheable on the way in, rather than
// leaving it to each handler on the way out.
//
// The handlers do set it themselves, but the responses the mux generates — the
// 405 for a wrong verb, the 404 for a path nothing serves — never reach a
// handler. Setting the header before anything writes covers those too, and a
// later Set of the same value is a no-op rather than a duplicate.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// register mounts one entity's three verbs on the API mux, whose paths are
// relative to the version prefix.
func register[T any](mux *http.ServeMux, e entity[T]) {
	base := "/" + e.collection
	mux.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) { handleCreate(w, r, e) })
	mux.HandleFunc("PATCH "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) { handlePatch(w, r, e) })
	mux.HandleFunc("DELETE "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) { handleDelete(w, r, e) })
}

// servePlan returns the whole plan in one round trip. Granularity is worth less
// than a single request when the origin is in one place and the readers are
// not.
func (s *Server) servePlan(w http.ResponseWriter, r *http.Request) {
	plan, err := s.store.LoadPlan(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSONCompressed(w, r, http.StatusOK, encodePlan(s.cfg.Currency, plan))
}

func handleCreate[T any](w http.ResponseWriter, r *http.Request, e entity[T]) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	row, err := e.decode(e.blank, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, err.Error())
		return
	}
	out, err := e.create(r.Context(), row)
	if err != nil {
		writeStoreError(w, r, e, err)
		return
	}
	writeJSON(w, http.StatusCreated, e.encode(out))
}

// handlePatch applies a partial update: read the row, merge the fields the body
// actually carried, write it back guarded by the revision the *caller* sent.
//
// The read in the middle is not a race. Whatever it returns, the UPDATE still
// names the caller's revision, so a write that slipped in between makes this
// one match no rows and come back as a conflict — which is the whole point.
func handlePatch[T any](w http.ResponseWriter, r *http.Request, e entity[T]) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	revision, ok := revisionFromBody(body)
	if !ok && e.revisioned {
		writeError(w, http.StatusBadRequest, errBadRequest,
			"a patch must carry the revision it is editing, or it would overwrite whatever is there now")
		return
	}

	current, err := e.load(r.Context(), id)
	if err != nil {
		writeStoreError(w, r, e, err)
		return
	}
	row, err := e.decode(current, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, err.Error())
		return
	}
	e.key(&row, id, revision)

	out, err := e.update(r.Context(), row)
	if err != nil {
		writeStoreError(w, r, e, err)
		return
	}
	writeJSON(w, http.StatusOK, e.encode(out))
}

// handleDelete removes a row, guarded by the same revision check as a patch:
// deleting a line somebody else has just edited would discard their edit with
// no more ceremony than deleting a stale one.
//
// The revision arrives as a query parameter rather than in a body, because a
// body on DELETE is poorly supported by enough of the stack that it is not
// worth the argument.
func handleDelete[T any](w http.ResponseWriter, r *http.Request, e entity[T]) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var revision int64
	if e.revisioned {
		raw := r.URL.Query().Get("revision")
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errBadRequest,
				"delete needs the revision it is deleting, as ?revision=N")
			return
		}
		revision = parsed
	}

	if err := e.remove(r.Context(), id, revision); err != nil {
		writeStoreError(w, r, e, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// pathID reads the {id} wildcard. A malformed uuid is the client's mistake, so
// it is a 400 rather than a 404: the row is not missing, the request is wrong.
func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, "id must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

// readBody reads a bounded request body.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, errTooLarge,
				fmt.Sprintf("body must be at most %d bytes", maxRequestBytes))
			return nil, false
		}
		writeError(w, http.StatusBadRequest, errBadRequest, "could not read the request body")
		return nil, false
	}
	return body, true
}

// revisionFromBody pulls the revision out of a write body.
//
// It is read separately from every other field because it is not one: it is the
// caller's claim about which version of the row they were looking at. Merged in
// with the rest it would default to whatever was just read, and a patch that
// forgot to send it would quietly overwrite somebody else's edit.
func revisionFromBody(body []byte) (int64, bool) {
	var envelope struct {
		Revision *int64 `json:"revision"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Revision == nil {
		return 0, false
	}
	return *envelope.Revision, true
}

// bodyFor is the contract every request DTO satisfies: merge yourself onto a
// row and say what is wrong if anything is.
//
// The currency is an argument rather than something the DTO knows because money
// arrives as major units and is stored as minor, and the exponent that converts
// between them comes from the ISO 4217 code. An UnmarshalJSON cannot reach it —
// it sees only its own bytes — so the conversion waits until here. The four
// bodies with no money column take it and ignore it, which is cheaper than two
// shapes of decoder.
type bodyFor[T any] interface {
	apply(currency string, row T) (T, error)
}

// decodeBody parses a request body onto a base row.
//
// Decoding is strict. An unknown field is a 400 rather than a silent no-op,
// because the field being silently ignored is as likely to be `unit` misspelt
// as it is to be something harmless, and nobody notices a money column that did
// not change. The fields a client legitimately echoes back are listed in
// `echoed` so that strictness does not make the obvious client illegal.
func decodeBody[B bodyFor[T], T any](body []byte, currency string, base T) (T, error) {
	var zero T
	var b B

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		if errors.Is(err, io.EOF) {
			return zero, errors.New("the request body is empty")
		}
		return zero, fmt.Errorf("could not read the request body: %w", err)
	}
	// Trailing content means the body was not the single JSON object it claims
	// to be, which is worth a complaint rather than a silent partial read.
	//
	// Read the next token rather than asking More(), which answers about the
	// array or object being read: it says nothing follows a stray `}` or `]`,
	// so a body a client concatenated wrongly passed a check named for
	// refusing exactly that. Only a clean body ends in io.EOF; a second object
	// ends in its `{`, and anything else in a syntax error.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return zero, errors.New("the request body must be a single JSON object")
	}
	return b.apply(currency, base)
}

// writeStoreError maps a store error onto a status.
//
// The order matters: a refused write re-reads the row, and that re-read can
// itself come back not-found when somebody deleted rather than edited. Those
// are different answers for the client — one is reconcilable, the other is not.
func writeStoreError[T any](w http.ResponseWriter, r *http.Request, e entity[T], err error) {
	var stale *store.StaleRevisionError
	if errors.As(err, &stale) {
		current, ok := stale.Current.(T)
		if !ok {
			// Only reachable if the store starts carrying a different type in
			// the conflict than the one it was asked to write.
			writeInternal(w, r, fmt.Errorf("conflict carried a %T, want the row type", stale.Current))
			return
		}
		writeJSON(w, http.StatusConflict, apiError{Error: errStaleRevision, Current: e.encode(current)})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound, "no such row")
		return
	}
	if status, code, message, ok := constraintError(err); ok {
		writeError(w, status, code, message)
		return
	}
	writeInternal(w, r, err)
}

// constraintError classifies the database's own rejections.
//
// Without this a budget item naming a phase that does not exist comes back as a
// 500, which says "this server is broken" when what happened is "your request
// referred to something that is not there". The client cannot act on a 500.
func constraintError(err error) (status int, code, message string, ok bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return 0, "", "", false
	}
	switch pgErr.Code {
	case "23503": // foreign_key_violation
		return http.StatusBadRequest, errBadRequest, "references a row that does not exist", true
	case "23505": // unique_violation
		return http.StatusConflict, errConflict, "that value is already taken", true
	case "23502", "23514": // not_null_violation, check_violation
		return http.StatusBadRequest, errBadRequest, "the database refused that value", true
	default:
		// Class 22 is "data exception": a figure past what its column holds, a
		// NUL byte in a text field, a date that does not exist. Those are all
		// properties of the value that was sent, so the caller is the only one
		// who can act on them. The whole class rather than a list of codes,
		// because a list is what lets the next column added bring back the 500
		// this translation exists to prevent. The cost is that a class 22 this
		// server caused itself, a literal built wrongly in some future query,
		// now answers 400 and writes no log line.
		if strings.HasPrefix(pgErr.Code, "22") {
			return http.StatusBadRequest, errBadRequest, "the database refused that value", true
		}
		return 0, "", "", false
	}
}

// statusClientClosedRequest is nginx's 499: not a standard status, and not one
// any client receives, because the connection it would go on is already gone.
// It exists so that a phone which locked its screen mid-read is not counted as
// this server failing, which is what a 500 on the same request tells every
// alert that reads the 5xx rate.
const statusClientClosedRequest = 499

// writeInternal reports a failure that is this server's fault.
//
// The error goes to the log and not to the client: it carries SQL and column
// names, which are nobody else's business. It does have to go somewhere,
// though — a 500 whose cause was dropped on the floor cannot be operated on.
// main installs the process logger as the default, so this is the same JSON
// stream as every other line.
//
// The request goes in the line for the same reason the error does. There is no
// access log here, and the metrics carry no id, so "api request failed" on its
// own is a fault with nothing to join it to. The matched pattern rather than
// the path, because the API is mounted behind StripPrefix and a handler
// therefore sees "/plan" where the operator is looking for "/api/v1/plan". A
// pattern also keeps a row id as "{id}", which is what the route label in
// metrics.go is careful about too. The actor is the account id, never the
// address, for the reason withActor gives.
func writeInternal(w http.ResponseWriter, r *http.Request, err error) {
	// A caller that hung up cancelled the query itself, so nothing here
	// failed and nobody is left to read an answer. Both halves of the test
	// matter: a database that genuinely failed while somebody happened to
	// close the tab is still worth an error. Deliberately narrow: pgx can
	// also surface a cancellation as a closed connection or as SQLSTATE
	// 57014, and those stay what they are today rather than becoming a
	// silence that hides a real fault.
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		slog.Debug("api request abandoned", "method", r.Method, "pattern", r.Pattern)
		w.WriteHeader(statusClientClosedRequest)
		return
	}
	slog.Error("api request failed", "err", err,
		"method", r.Method, "pattern", r.Pattern, "actor", actorID(r))
	writeError(w, http.StatusInternalServerError, errInternal, "something went wrong")
}

// actorID names whoever was signed in, and null where nobody was, which is the
// honest answer for the few routes that can fail before anybody is known.
func actorID(r *http.Request) any {
	if u, ok := UserFrom(r.Context()); ok {
		return u.ID
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Error: code, Message: message})
}

// writeJSON writes an API response. Marshalling to a buffer first is what
// allows a failure to still produce a 500 rather than a truncated 200 body.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	writeJSONBody(w, nil, status, payload)
}

// writeJSONCompressed is writeJSON for the two reads worth encoding: the whole
// plan, which every other open browser re-reads after anybody's edit and again
// on every reconnect, and the activity page. Both are tens of kilobytes of
// repetitive JSON to readers the origin may be 300 ms away from, and both are
// asked for far more often than anything here is written. That is the one place
// the static path's care about bytes is worth repeating per request.
//
// Only those two, deliberately. A response that reflects what the caller sent
// is how a length turns into a guess at what else is in it, and /events must
// never go through here at all: a compressed stream is a buffered one, and the
// point of that route is that a frame leaves the moment it exists. Neither the
// plan nor the activity page reflects the request, carries a token, or is
// readable by anybody who cannot already read all of it.
func writeJSONCompressed(w http.ResponseWriter, r *http.Request, status int, payload any) {
	writeJSONBody(w, r, status, payload)
}

// writeJSONBody is both of the above. A nil request means nothing negotiated an
// encoding, so nothing is encoded.
func writeJSONBody(w http.ResponseWriter, r *http.Request, status int, payload any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	// Never cached: this is shared state that two people are editing.
	h.Set("Cache-Control", "no-store")

	// A nil payload means a bodiless response — 204 from a delete, or a
	// logout. Marshalling it would write the four bytes "null" under a status
	// that promises no body at all.
	if payload == nil {
		w.WriteHeader(status)
		return
	}

	body, err := json.Marshal(payload)
	negotiated := r != nil
	if err != nil {
		body = []byte(`{"error":"internal","message":"could not encode the response"}`)
		status = http.StatusInternalServerError
		// A canned line saying this server failed. Encoding that saves
		// nothing, and what the caller accepts no longer changes the answer.
		negotiated = false
	}
	body = append(body, '\n')

	if negotiated {
		// On both branches, because what this route answers depends on the
		// header whether or not this particular body was big enough to encode.
		h.Set("Vary", "Accept-Encoding")
		// The same containment test serveBody makes of the asset path. A
		// client that lists gzip only to refuse it at q=0 gets an encoding it
		// can still read; one that wants none of it sends `identity`.
		if len(body) >= gzipMinBytes && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			if encoded := gzipJSON(body); encoded != nil {
				h.Set("Content-Encoding", "gzip")
				body = encoded
			}
		}
	}

	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// gzipMinBytes is the smallest body worth encoding. Below about a kilobyte the
// encoder's own header and trailer eat most of what it saves, and what is left
// is nothing beside the round trip that carried the request.
const gzipMinBytes = 1 << 10

// gzipWriters keeps encoders between requests. One carries a few hundred
// kilobytes of window, which is worth reusing on the route every open browser
// re-reads after anybody's edit, and not worth allocating afresh each time.
var gzipWriters = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

// gzipJSON encodes a response body, or returns nil to say it could not, in
// which case the caller sends what it had. Encoding into memory cannot fail
// today; a body that goes out raw is still a better answer than one that does
// not go out at all, in any future where it can.
//
// The default level rather than the best one: assets.go pays for the best once
// at startup and serves the result for the life of the process, while this runs
// on every read.
func gzipJSON(body []byte) []byte {
	zw := gzipWriters.Get().(*gzip.Writer)
	defer func() {
		// Pointed at nothing before it goes back, so a pooled encoder does not
		// hold this response's buffer until somebody borrows it again.
		zw.Reset(io.Discard)
		gzipWriters.Put(zw)
	}()

	var buf bytes.Buffer
	// JSON of this shape encodes to well under half its size, so this is one
	// generous allocation rather than a series of doublings.
	buf.Grow(len(body) / 2)
	zw.Reset(&buf)
	if _, err := zw.Write(body); err != nil {
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}
