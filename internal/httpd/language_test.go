package httpd

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Yornik/soiree/internal/store"
)

// A language in the list with no mail behind it would not fail anything: the
// switch in inviteMessage falls through to English, the account would be
// created, and somebody would be written to in the wrong language with no
// error anywhere. So the list and the templates are held together here.
func TestEveryLanguageHasItsOwnMails(t *testing.T) {
	const link = "https://soiree.example.test/#/set-password?token=abc"

	for _, purpose := range []store.TokenPurpose{store.PurposeInvite, store.PurposeReset} {
		enSubject, enBody := inviteMessage(purpose, link, "en")
		for _, lang := range languages {
			subject, body := inviteMessage(purpose, link, lang)
			if strings.TrimSpace(subject) == "" || strings.TrimSpace(body) == "" {
				t.Errorf("%s/%s: empty subject or body", lang, purpose)
			}
			if strings.Count(body, link) != 1 {
				t.Errorf("%s/%s: the body carries the link %d times, want once", lang, purpose, strings.Count(body, link))
			}
			// The link is the message. A mail client wraps a long line, and a
			// link with prose on the same line is a link somebody copies half of.
			if !strings.Contains(body, "\n\n"+link+"\n\n") {
				t.Errorf("%s/%s: the link is not on a line of its own", lang, purpose)
			}
			if lang != "en" && (subject == enSubject || body == enBody) {
				t.Errorf("%s/%s is in the list of languages and is written in English", lang, purpose)
			}
		}
	}

	// A tag this binary has no template for is answered in English rather than
	// with nothing. languageFor keeps one from getting this far; this is the
	// floor underneath it.
	if subject, _ := inviteMessage(store.PurposeInvite, link, "xx"); subject != "Your account is ready" {
		t.Errorf("an unknown language produced %q", subject)
	}
}

func TestParseLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"nl": "nl", "NL": "nl", " id ": "id", "nl-NL": "nl", "id_ID": "id", "en-GB": "en",
	} {
		if got, ok := parseLanguage(in); !ok || got != want {
			t.Errorf("parseLanguage(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "fr", "dutch", "n", "nl1", "../nl"} {
		if got, ok := parseLanguage(in); ok {
			t.Errorf("parseLanguage(%q) accepted it as %q", in, got)
		}
	}
	if got := languageOfLocale("fr-FR"); got != "en" {
		t.Errorf("a locale with no translation speaks %q, want en", got)
	}
}

// The reason the feature exists. The deployment speaks one language and the
// person being invited reads another; the admin says so, and everything that
// person then receives — the mail, and the screen its link opens — follows.
func TestAnInvitationIsWrittenInTheLanguageTheAdminChose(t *testing.T) {
	f := newFixtureIn(t, true, "id-ID")
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "linus@example.test", "role": "viewer", "language": "nl-NL"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	created := decodeTestBody[createUserResponse](t, rec)
	if created.User.Language == nil || *created.User.Language != "nl" {
		t.Fatalf("language = %v, want nl (normalised from nl-NL)", created.User.Language)
	}

	sent := f.mail.messages()
	if len(sent) != 1 {
		t.Fatalf("mails = %d, want 1", len(sent))
	}
	if sent[0].subject != "Je account staat klaar" {
		t.Errorf("subject = %q, want the Dutch one", sent[0].subject)
	}

	// The link opens the page in the same language as the mail around it.
	var link string
	for _, line := range strings.Split(sent[0].body, "\n") {
		if strings.HasPrefix(line, "https://") {
			link = line
		}
	}
	u, err := url.Parse(link)
	if err != nil || link == "" {
		t.Fatalf("no link in the body: %q (%v)", sent[0].body, err)
	}
	if got := u.Query().Get("lang"); got != "nl" {
		t.Errorf("the link asks for lang=%q, want nl: %s", got, link)
	}
	// And adding a query string did not move the secret into it. The query is
	// sent to the server and lands in its logs; the fragment never is.
	if u.Query().Get("token") != "" || strings.Contains(u.RawQuery, "token") {
		t.Errorf("the token is in the query string: %s", link)
	}
	token := tokenFromLink(t, link)

	rec = f.do(t, http.MethodPost, "/api/v1/auth/set-password",
		map[string]string{"token": token, "password": goodPassword}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set-password with the token from a ?lang= link: %d %s", rec.Code, rec.Body)
	}

	// A reset, later, is in their language too — it is the account's, not the
	// invitation's.
	f.do(t, http.MethodPost, "/api/v1/auth/password-reset", map[string]string{"email": "linus@example.test"}, nil)
	sent = f.mail.messages()
	if len(sent) != 2 || sent[1].subject != "Stel een nieuw wachtwoord in" {
		t.Errorf("reset mail = %+v, want the Dutch reset", sent[len(sent)-1])
	}
}

// Nothing chosen means the deployment's language, resolved when the mail is
// written rather than frozen into the row — and a link with no ?lang=, because
// the page it opens is already going to be in that language.
func TestAnAccountWithNoLanguageFollowsTheDeployment(t *testing.T) {
	f := newFixtureIn(t, true, "id-ID")
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "grace@example.test", "role": "editor"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got := decodeTestBody[createUserResponse](t, rec).User.Language; got != nil {
		t.Errorf("language = %q, want null: nothing was chosen, and the page has to be able to say so", *got)
	}

	sent := f.mail.messages()
	if len(sent) != 1 || sent[0].subject != "Akunmu sudah siap" {
		t.Fatalf("mail = %+v, want the Indonesian invitation", sent)
	}
	if strings.Contains(sent[0].body, "lang=") {
		t.Errorf("a link for an account with no language carries one: %q", sent[0].body)
	}
}

func TestLanguageIsValidatedAndPatchedInThreeStates(t *testing.T) {
	f := newFixture(t, false)
	ada := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "linus@example.test", "role": "viewer", "language": "fr"}, admin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_language") {
		t.Fatalf("a language with no translation: %d %s, want 400 invalid_language", rec.Code, rec.Body)
	}

	// With no SMTP the link comes back, and it carries the language as well.
	rec = f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "linus@example.test", "role": "viewer", "language": "id"}, admin)
	created := decodeTestBody[createUserResponse](t, rec)
	if !strings.Contains(created.SetPasswordURL, "/?lang=id#/set-password?token=") {
		t.Errorf("setPasswordUrl = %q, want it to open in Indonesian", created.SetPasswordURL)
	}
	linus := created.User

	patch := func(id string, body map[string]any) userDTO {
		t.Helper()
		rec := f.do(t, http.MethodPatch, "/api/v1/users/"+id, body, admin)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH %v: %d %s", body, rec.Code, rec.Body)
		}
		return decodeTestBody[userDTO](t, rec)
	}

	// Omitted: left alone. A role change must not quietly reset it.
	got := patch(linus.ID.String(), map[string]any{"revision": linus.Revision, "role": "editor"})
	if got.Language == nil || *got.Language != "id" {
		t.Errorf("a patch that did not mention language changed it to %v", got.Language)
	}
	// A tag: set.
	got = patch(linus.ID.String(), map[string]any{"revision": got.Revision, "language": "nl"})
	if got.Language == nil || *got.Language != "nl" {
		t.Errorf("language = %v, want nl", got.Language)
	}
	// Null: back to following the deployment.
	got = patch(linus.ID.String(), map[string]any{"revision": got.Revision, "language": nil})
	if got.Language != nil {
		t.Errorf("an explicit null left language at %q", *got.Language)
	}

	// An admin may change their own. The self-change rule is about not locking
	// yourself out, and a language cannot do that.
	me, err := f.store.User(t.Context(), ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	got = patch(ada.ID.String(), map[string]any{"revision": me.Revision, "language": "nl"})
	if got.Language == nil || *got.Language != "nl" {
		t.Errorf("an admin could not set their own language: %v", got.Language)
	}

	// And the session says so, which is how the page learns whose language to
	// speak.
	rec = f.do(t, http.MethodGet, "/api/v1/auth/session", nil, admin)
	if who := decodeTestBody[userDTO](t, rec); who.Language == nil || *who.Language != "nl" {
		t.Errorf("GET /auth/session reports language %v, want nl", who.Language)
	}
}
