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
	const site = "https://soiree.example.test"
	const link = site + "/#/set-password?token=abc"
	const event = "A Celebration"

	for _, purpose := range []store.TokenPurpose{store.PurposeInvite, store.PurposeReset} {
		enSubject, enBody := inviteMessage(purpose, link, "en", event, site)
		for _, lang := range languages {
			subject, body := inviteMessage(purpose, link, lang, event, site)
			// Naming the event is what tells the reader, and the filter, that
			// this is a mail somebody was expecting. A translation that drops
			// it reads like the phishing it is shaped like.
			if !strings.Contains(subject, event) || !strings.Contains(body, event) {
				t.Errorf("%s/%s does not name the event: %q / %q", lang, purpose, subject, body)
			}
			// The invitation offers somewhere to go when the link has gone
			// stale, and that somewhere cannot be the link itself: it works
			// once and is gone in a day.
			if purpose == store.PurposeInvite {
				if rest := strings.ReplaceAll(body, link, ""); !strings.Contains(rest, site) {
					t.Errorf("%s/%s offers nothing but the link itself: %q", lang, purpose, body)
				}
			}
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
	if subject, _ := inviteMessage(store.PurposeInvite, link, "xx", event, site); subject != event+": choose your password" {
		t.Errorf("an unknown language produced %q", subject)
	}

	// A deployment that has emptied its event name still sends a mail, and it
	// is the wording these mails had before they named anything.
	subject, body := inviteMessage(store.PurposeInvite, link, "en", "", site)
	if subject != "Your account is ready" || !strings.Contains(body, "An account has been created for you") {
		t.Errorf("with no event name the invitation reads %q / %q", subject, body)
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

// linkIn finds the link in a mail body. It is on a line of its own; see
// TestEveryLanguageHasItsOwnMails.
func linkIn(t *testing.T, body string) *url.URL {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "https://") {
			u, err := url.Parse(line)
			if err != nil {
				t.Fatalf("the link does not parse: %q: %v", line, err)
			}
			return u
		}
	}
	t.Fatalf("no link in the body: %q", body)
	return nil
}

// The reason a mail has a language at all. The deployment speaks one and the
// person being invited reads another; the admin says so, for this mail, and
// the mail and the screen its link opens follow.
//
// And nothing else does. The choice is not stored: what somebody reads is
// better learned from their own browser each time they arrive than fixed in a
// column by whoever invited them.
func TestAnInvitationIsWrittenInTheLanguageTheAdminChose(t *testing.T) {
	f := newFixtureIn(t, true, "id-ID")
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "linus@example.test", "role": "viewer", "language": "nl-NL"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	// Not on the account, in any spelling. It was a fact about one mail.
	if strings.Contains(rec.Body.String(), "language") || strings.Contains(rec.Body.String(), `"nl"`) {
		t.Errorf("the account carries the language of its invitation: %s", rec.Body)
	}

	sent := f.mail.messages()
	if len(sent) != 1 {
		t.Fatalf("mails = %d, want 1", len(sent))
	}
	if sent[0].subject != "Je account staat klaar" {
		t.Errorf("subject = %q, want the Dutch one", sent[0].subject)
	}

	// The link opens the page in the same language as the mail around it.
	u := linkIn(t, sent[0].body)
	if got := u.Query().Get("lang"); got != "nl" {
		t.Errorf("the link asks for lang=%q, want nl: %s", got, u)
	}
	// And adding a query string did not move the secret into it. The query is
	// sent to the server and lands in its logs; the fragment never is.
	if strings.Contains(u.RawQuery, "token") {
		t.Errorf("the token is in the query string: %s", u)
	}
	rec = f.do(t, http.MethodPost, "/api/v1/auth/set-password",
		map[string]string{"token": tokenFromLink(t, u.String()), "password": goodPassword}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set-password with the token from a ?lang= link: %d %s", rec.Code, rec.Body)
	}
}

// Nobody chose: the deployment's language, and a bare link, so that the page
// it opens decides for itself from the reader's browser.
func TestAMailNobodyChoseALanguageForIsInTheDeployments(t *testing.T) {
	f := newFixtureIn(t, true, "id-ID")
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "grace@example.test", "role": "editor"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	sent := f.mail.messages()
	if len(sent) != 1 || sent[0].subject != "Akunmu sudah siap" {
		t.Fatalf("mail = %+v, want the Indonesian invitation", sent)
	}
	if strings.Contains(sent[0].body, "lang=") {
		t.Errorf("a mail nobody chose a language for carries one in its link: %q", sent[0].body)
	}
}

// A reset has no admin in it. The sign-in screen says which language it is
// being read in, and that is the best evidence there is.
func TestAResetIsWrittenInTheLanguageItWasAskedForIn(t *testing.T) {
	f := newFixtureIn(t, true, "id-ID")
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)

	ask := func(body map[string]string) sentMail {
		t.Helper()
		before := len(f.mail.messages())
		rec := f.do(t, http.MethodPost, "/api/v1/auth/password-reset", body, nil)
		// 202 whatever was sent, the language included: this endpoint does not
		// make exceptions, because every exception is something to probe.
		if rec.Code != http.StatusAccepted {
			t.Fatalf("password-reset %v: %d %s", body, rec.Code, rec.Body)
		}
		sent := f.mail.messages()
		if len(sent) != before+1 {
			t.Fatalf("password-reset %v sent %d mails, want 1", body, len(sent)-before)
		}
		return sent[len(sent)-1]
	}

	if got := ask(map[string]string{"email": "ada@example.test", "language": "nl"}); got.subject != "Stel een nieuw wachtwoord in" {
		t.Errorf("asked for in Dutch, written as %q", got.subject)
	} else if linkIn(t, got.body).Query().Get("lang") != "nl" {
		t.Errorf("the Dutch reset's link does not open in Dutch: %q", got.body)
	}
	if got := ask(map[string]string{"email": "ada@example.test"}); got.subject != "Buat kata sandi baru" {
		t.Errorf("no language sent on an Indonesian deployment, written as %q", got.subject)
	}
	if got := ask(map[string]string{"email": "ada@example.test", "language": "tlh"}); got.subject != "Buat kata sandi baru" {
		t.Errorf("a language with no translation should fall back to the deployment's, got %q", got.subject)
	}
}

// Sending a link again is a new mail, and gets its own choice. It took no body
// at all before a mail had a language, and that has to go on working.
func TestReinvitingTakesALanguageAndStillNeedsNoBody(t *testing.T) {
	f := newFixtureIn(t, true, "en-US")
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "linus@example.test", "role": "viewer", "language": "fr"}, admin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_language") {
		t.Fatalf("a language with no translation: %d %s, want 400 invalid_language", rec.Code, rec.Body)
	}
	if n := len(f.mail.messages()); n != 0 {
		t.Fatalf("a refused invitation sent %d mails", n)
	}

	rec = f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "linus@example.test", "role": "viewer"}, admin)
	id := decodeTestBody[createUserResponse](t, rec).User.ID.String()

	rec = f.do(t, http.MethodPost, "/api/v1/users/"+id+"/invite", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("invite with no body at all: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(t, http.MethodPost, "/api/v1/users/"+id+"/invite", map[string]string{"language": "id"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("invite in Indonesian: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(t, http.MethodPost, "/api/v1/users/"+id+"/invite", map[string]string{"language": "fr"}, admin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_language") {
		t.Fatalf("invite in a language with no translation: %d %s", rec.Code, rec.Body)
	}

	sent := f.mail.messages()
	if len(sent) != 3 {
		t.Fatalf("mails = %d, want 3: the invitation and two re-sends", len(sent))
	}
	if sent[1].subject != "Your account is ready" || sent[2].subject != "Akunmu sudah siap" {
		t.Errorf("re-sends were %q then %q; want English, then Indonesian", sent[1].subject, sent[2].subject)
	}
}
