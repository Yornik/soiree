package httpd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/pgtest"
	"github.com/Yornik/soiree/internal/store"
	"github.com/Yornik/soiree/web"
)

// siteHost is the Host every request in this file arrives under. Set by hand
// because httptest.NewRequest says example.com, and the fallback for a browser
// that sends no Sec-Fetch-Site compares Origin with exactly this.
const siteHost = "soiree.example.test"

// newCrossOriginFixture is the whole public handler over a deployment with a
// mail relay, plus the metrics listener beside it.
//
// The whole handler, because the check under test wraps the mux rather than
// living on any route, so newFixture's bare mux would never meet it. With a
// relay, because that is the deployment where a forged invitation pays: the
// set-password link is mailed to whoever the request names.
func newCrossOriginFixture(t *testing.T) (*fixture, http.Handler) {
	t.Helper()

	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)

	fake := &fakeMailer{}
	a := NewAuth(AuthOptions{Store: st, Mailer: fake, BaseURL: "https://" + siteHost + "/"})
	a.params = cheapParams
	a.background = func(fn func(context.Context)) { fn(context.Background()) }

	s, err := New(
		config.Config{EventName: "Ada's Leaving Do", Currency: "EUR", Locale: "en-US"},
		web.FS(),
		WithStore(st),
	)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	return &fixture{h: s.WithAuth(a).Handler(), a: a, store: st, mail: fake}, s.MetricsHandler()
}

// send is one request as a browser would put it on the wire: a body under a
// Content-Type of the sender's choosing, and whichever of Sec-Fetch-Site and
// Origin the case is about. f.do cannot say any of that.
func (f *fixture) send(method, path, contentType, body string, headers map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = siteHost
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// SameSite=Lax is a site boundary and an origin is narrower than a site. A
// page on a sibling subdomain, or on another port of localhost, is same-site,
// so a form it submits arrives with the admin's session attached.
//
// A form cannot send application/json, and does not need to. With
// enctype=text/plain and one input whose *name* is most of a JSON object, the
// body a browser emits is `name=value` and a line break, which is the JSON
// below: the `=` lands inside a string nobody reads. The accounts decoder
// ignores both the Content-Type and the extra field, so before the origin
// check this created an admin and mailed the set-password link to the address
// the form chose.
func TestAFormOnASiblingOriginCannotInviteAnAdmin(t *testing.T) {
	f, metrics := newCrossOriginFixture(t)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	const formBody = `{"email":"mallory@example.net","role":"admin","x":"="}` + "\r\n"
	rec := f.send(http.MethodPost, "/api/v1/users", "text/plain", formBody, map[string]string{
		"Sec-Fetch-Site": "same-site",
		"Origin":         "https://wiki.example.test",
	}, cookie)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body)
	}
	if got := decodeTestBody[apiError](t, rec); got.Error != "cross_origin" {
		t.Errorf("error code = %q, want cross_origin", got.Error)
	}
	// The refusal is written before the mux is reached, and must still be an
	// answer of this server: the API's one error shape, under the headers
	// every other response carries.
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want the API's JSON error shape", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the refusal went out without the security headers")
	}

	if _, err := f.store.UserByEmail(t.Context(), "mallory@example.net"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the forged request created an account (lookup error = %v)", err)
	}
	if sent := f.mail.messages(); len(sent) != 0 {
		t.Errorf("the forged request mailed %d invitation(s), the first to %s", len(sent), sent[0].to)
	}

	// Counted like any other answer. A refusal that skipped the instrument
	// wrapper would make somebody probing this invisible on the dashboard.
	out := httptest.NewRecorder()
	metrics.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	const want = `soiree_http_requests_total{method="POST",route="api-other",status="403"} 1`
	if !strings.Contains(out.Body.String(), want) {
		t.Errorf("metrics do not count the refusal; want %s", want)
	}
}

// The other half: nothing that is not a browser on another origin may notice
// the check exists. A false refusal here is not a hardening, it is an outage
// for whoever it lands on, so each caller this project has is named.
func TestOnlyABrowserOnAnotherOriginIsRefused(t *testing.T) {
	f, _ := newCrossOriginFixture(t)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	cases := []struct {
		what    string
		method  string
		path    string
		headers map[string]string
		want    int
	}{
		{"the page itself", "POST", "/api/v1/users",
			map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://" + siteHost}, http.StatusCreated},
		// curl, the e2e suite's request context and every Go test in this
		// package: no browser, so neither header.
		{"a client that is not a browser", "POST", "/api/v1/users",
			nil, http.StatusCreated},
		// A browser from before 2023 sends Origin and no Sec-Fetch-Site.
		{"an older browser on the page itself", "POST", "/api/v1/users",
			map[string]string{"Origin": "https://" + siteHost}, http.StatusCreated},
		{"an older browser on a sibling origin", "POST", "/api/v1/users",
			map[string]string{"Origin": "https://wiki.example.test"}, http.StatusForbidden},
		{"another site entirely", "POST", "/api/v1/users",
			map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://example.net"}, http.StatusForbidden},
		// Sec-Fetch-Site is believed over Origin, because a browser that sends
		// it cannot be made to lie in it and a proxy may have rewritten Host.
		{"same-site, whatever Origin says", "POST", "/api/v1/users",
			map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://" + siteHost}, http.StatusForbidden},
		// A read is never refused: the link in an invitation is followed from
		// a mail client, and arrives cross-site by definition.
		{"a link followed from somewhere else", "GET", "/api/v1/users",
			map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusOK},
		// PATCH and DELETE from another origin were already stopped by a CORS
		// preflight nothing here answers. Now they are stopped here as well,
		// and do not depend on the browser asking first.
		{"a delete from a sibling origin", "DELETE", "/api/v1/push/subscriptions?endpoint=https%3A%2F%2Fpush.example.test%2Fsend%2Fabc",
			map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
	}

	for i, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			body := ""
			if tc.path == "/api/v1/users" && tc.method == "POST" {
				// An address per case, so a 201 is never a 409 in disguise.
				body = `{"email":"guest` + string(rune('a'+i)) + `@example.test","role":"viewer"}`
			}
			rec := f.send(tc.method, tc.path, "application/json", body, tc.headers, cookie)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body)
			}
			if tc.want == http.StatusForbidden {
				if got := decodeTestBody[apiError](t, rec); got.Error != "cross_origin" {
					t.Errorf("error code = %q, want cross_origin", got.Error)
				}
			}
		})
	}
}
