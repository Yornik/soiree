package httpd

// Web Push subscriptions: the two endpoints a browser needs to turn deadline
// notifications on and off for the device it is running on.
//
// What the browser has, after the user grants permission, is a PushSubscription
// object: a URL at its own push service plus two keys that make the payload
// readable by that device and by nothing in between. It is useless to the
// browser on its own — only a server can post to it — so it has to be handed
// over, and these are the two endpoints that take it and give it back.
//
// Both require a session. A subscription is stored against an account, and the
// account is what decides whether it receives the digest; an unauthenticated
// POST here would be an open invitation to fill the table.

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/Yornik/soiree/internal/store"
)

const (
	// maxEndpointLen bounds the URL. Real ones are a couple of hundred
	// characters; this is headroom, and it is here so that a hostile client
	// cannot store a megabyte of URL per request.
	maxEndpointLen = 2048

	// maxPushKeyLen bounds the two encryption keys. p256dh is 87 base64url
	// characters and auth is 22, so anything near this is not a key.
	maxPushKeyLen = 256
)

// routePush mounts the subscription endpoints on the API mux, whose paths are
// relative to the version prefix.
//
// Nothing is mounted without the accounts surface. The routes exist only to
// record something against an account, so on a deployment that has no sessions
// at all they could never do anything but refuse — and a route that always
// refuses is worse than a 404, because it looks like a feature that is broken
// rather than one that is absent.
func (s *Server) routePush(api *http.ServeMux) {
	if s.auth == nil {
		return
	}
	// RequireAuth rather than a new check: authorisation is decided in one
	// place in this package, and this is that place. Any live session will do
	// — who *receives* the digest is decided at send time from the account's
	// role, not here, so a viewer subscribing is harmless and a viewer later
	// promoted to admin starts receiving notifications without re-subscribing.
	api.Handle("POST /push/subscriptions", s.auth.RequireAuth(http.HandlerFunc(s.handlePushSubscribe)))
	api.Handle("DELETE /push/subscriptions", s.auth.RequireAuth(http.HandlerFunc(s.handlePushUnsubscribe)))
}

// pushSubscriptionRequest is the browser's PushSubscription, as
// JSON.stringify() produces it.
//
// The shape is the Push API's, not this application's, so that the client can
// post the subscription object it already holds without transcribing it —
// transcription is where a p256dh ends up in the auth field. `expirationTime`
// is part of that object, is almost always null, and is deliberately accepted
// and ignored: decoding here is lenient for exactly this reason, unlike the
// strict decoder the CRUD API uses.
type pushSubscriptionRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// handlePushSubscribe stores a device, or refreshes one already stored.
//
// Idempotent on the endpoint, because the browser re-sends the same
// subscription every time the page loads: the Push API hands it back whatever
// already exists rather than minting a new one, so a client that posts on every
// visit — which is the correct client, since it is also how a rotated key gets
// through — must not create a row each time.
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFrom(r.Context())
	if !ok {
		// Unreachable behind RequireAuth, and kept because the alternative to
		// checking is writing a row owned by the zero uuid.
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	var req pushSubscriptionRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	endpoint, msg := checkEndpoint(req.Endpoint)
	if msg != "" {
		writeError(w, http.StatusBadRequest, errBadRequest, msg)
		return
	}
	p256dh := strings.TrimSpace(req.Keys.P256dh)
	auth := strings.TrimSpace(req.Keys.Auth)
	if p256dh == "" || auth == "" || len(p256dh) > maxPushKeyLen || len(auth) > maxPushKeyLen {
		// Both or neither: a subscription missing either key cannot be
		// encrypted to, so storing it would mean a row that fails at every
		// send until somebody notices.
		writeError(w, http.StatusBadRequest, errBadRequest,
			"keys.p256dh and keys.auth are both required, as the Push API supplies them")
		return
	}

	if _, err := s.store.SavePushSubscription(r.Context(), store.PushSubscription{
		UserID:   user.ID,
		Endpoint: endpoint,
		P256dh:   p256dh,
		Auth:     auth,
	}); err != nil {
		writeInternal(w, r, err)
		return
	}

	// No body. The client posted everything in it and has nothing to learn
	// from having it read back; what it wanted to know is the status.
	w.WriteHeader(http.StatusNoContent)
}

// handlePushUnsubscribe removes a device.
//
// The endpoint arrives as a query parameter rather than in a body, for the same
// reason the rest of this API puts a revision there: a body on DELETE is poorly
// supported by enough of the stack that it is not worth the argument.
//
// Always 204, whether or not anything was deleted. Unsubscribing twice is not a
// failure — the browser may well have dropped the subscription locally before
// it got here — and answering differently would also say whether a given
// endpoint belongs to somebody, which is not this endpoint's business.
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	endpoint, msg := checkEndpoint(r.URL.Query().Get("endpoint"))
	if msg != "" {
		writeError(w, http.StatusBadRequest, errBadRequest, msg)
		return
	}

	// Scoped to the caller. An endpoint is an unguessable URL rather than a
	// secret, and "unguessable" is not an authorisation model.
	if _, err := s.store.DeletePushSubscription(r.Context(), user.ID, endpoint); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkEndpoint validates a push endpoint, returning it trimmed, or a message
// saying what is wrong with it.
//
// https only, because every real push endpoint is. The host matters too: the
// digest run later POSTs to whatever is stored here, from inside whatever
// network this server runs in, so an account that may store a row must not be
// able to choose a target on that network.
func checkEndpoint(raw string) (endpoint, problem string) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "", "endpoint is required"
	case len(raw) > maxEndpointLen:
		return "", "endpoint is too long to be a push subscription"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", "endpoint must be the absolute https URL the Push API supplied"
	}
	if isInsideTheNetwork(u.Hostname()) {
		return "", "endpoint must be a push service, not an address on this network"
	}
	return raw, ""
}

// isInsideTheNetwork reports whether a host names the machine this runs on or
// something only its neighbours can reach.
//
// Written addresses only, and deliberately: resolving a name would answer for
// the moment the subscription was stored rather than the moment the digest
// posts, and would also refuse every endpoint whenever this process cannot
// reach a resolver, which is a subscription lost for a reason nobody could
// read. So this closes the direct forms — the ones a browser never produces —
// and it is a fence rather than a wall: a name someone points at a private
// address still passes, and internal/push follows redirects, so a public host
// can still send the sender somewhere else. A push service reached over the
// public internet is what this leaves.
//
// Hostname() rather than Host, so that a port or IPv6 brackets cannot carry an
// address past the check, and ParseIP folds ::ffff:10.0.0.5 onto 10.0.0.5 for
// the same reason.
func isInsideTheNetwork(host string) bool {
	// Trailing dot: the same name, fully qualified.
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast()
}
