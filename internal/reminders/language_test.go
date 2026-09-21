package reminders

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Yornik/soiree/internal/mailer"
	"github.com/Yornik/soiree/internal/store"
)

// digestIn renders the standing fixture the way a deployment whose locale is
// `locale` would receive it, through LoadConfig rather than a Config built by
// hand: which language a digest is in is a question about the environment, and
// the wiring from one to the other is what these tests are for.
func digestIn(t *testing.T, locale string) (Digest, mailer.Message) {
	t.Helper()
	clearEnv(t)
	t.Setenv("SOIREE_LOCALE", locale)
	t.Setenv("SOIREE_EVENT_NAME", "A Celebration")
	t.Setenv("SOIREE_CURRENCY", "EUR")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	d := Compose(cfg, at(t, "2030-01-17T09:00:00Z"), []store.BudgetItem{
		item(t, "Venue deposit", "Grand Hall", "2030-01-12", 250000, 0),
		item(t, "Florist", "Linus Flowers", "2030-01-22", 42050, 0),
	}, []store.Task{
		task(t, "Send invitations", "Grace", "2030-01-18", store.TaskInProgress),
	})

	msg, err := Render(d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return d, msg
}

// The invitation and the reset mail are written in the deployment's language
// when nobody chose one for them. The digest was English whatever SOIREE_LOCALE
// said, so the mail that arrives every week was the one mail in the wrong
// language.
func TestTheDigestFollowsTheDeploymentLanguage(t *testing.T) {
	for _, c := range []struct {
		locale string
		want   []string
	}{
		{"nl-NL", []string{"Taken", "te laat", "za 12 jan 2030"}},
		{"id-ID", []string{"Tugas", "terlambat", "Sab 12 Jan 2030"}},
	} {
		d, msg := digestIn(t, c.locale)
		for _, want := range append(c.want, "A Celebration") {
			if !strings.Contains(msg.Text, want) {
				t.Errorf("%s digest is missing %q:\n%s", c.locale, want, msg.Text)
			}
		}
		// The words this binary writes, not the names somebody typed into the
		// plan: those stay as they were entered.
		for _, gone := range []string{"Tasks", "overdue", "Sat 12 Jan 2030"} {
			if strings.Contains(msg.Text, gone) {
				t.Errorf("%s digest is still English (%q):\n%s", c.locale, gone, msg.Text)
			}
		}
		if !strings.Contains(msg.Subject, "A Celebration") {
			t.Errorf("%s subject = %q, want the event named", c.locale, msg.Subject)
		}

		// The lock screen too. It says the same clause the subject says, so
		// the one channel left in English would be the one read first.
		n := RenderPush(d, d.BaseURL)
		if n.Body == headlineIn(t, "en", d) {
			t.Errorf("%s notification is still English: %q", c.locale, n.Body)
		}
	}
}

// headlineIn is the same digest's headline in another language, which is how
// these tests ask "is this still the English one" without pinning the English
// wording twice.
func headlineIn(t *testing.T, language string, d Digest) string {
	t.Helper()
	d.Lang = language
	return headline(d)
}

// A locale with no translation behind it is answered in English rather than in
// half a language, which is the same floor the account mails have.
func TestAnUntranslatedLocaleIsAnsweredInEnglish(t *testing.T) {
	for _, locale := range []string{"", "fr-FR", "en-GB", "nonsense"} {
		_, msg := digestIn(t, locale)
		if !strings.Contains(msg.Text, "Tasks") {
			t.Errorf("locale %q did not fall back to English:\n%s", locale, msg.Text)
		}
	}
}

// A language in the list with no words behind it would fail nothing on its
// own: wordsFor falls through to English, the digest goes out, and a
// deployment is written to in a language nobody chose with no error anywhere.
// So the list and the tables are held together here.
func TestEveryLanguageHasItsOwnDigest(t *testing.T) {
	_, en := digestIn(t, "en")

	for _, lang := range languages {
		_, msg := digestIn(t, lang)
		if strings.TrimSpace(msg.Subject) == "" || strings.TrimSpace(msg.Text) == "" {
			t.Errorf("%s: empty subject or body", lang)
		}
		if lang != "en" && (msg.Subject == en.Subject || msg.Text == en.Text) {
			t.Errorf("%s is in the list of languages and its digest is written in English:\n%s", lang, msg.Text)
		}

		// A table's lines are format strings held in a variable, which vet's
		// printf check cannot see the way it saw the literals they replaced.
		// A verb too many or too few therefore reaches the reader as
		// %!d(MISSING), and this is the only thing left looking for it.
		for _, part := range []struct{ name, got string }{{"text", msg.Text}, {"html", msg.HTML}} {
			if strings.Contains(part.got, "%!") {
				t.Errorf("%s %s body has a formatting error in it:\n%s", lang, part.name, part.got)
			}
		}

		// Every line of the table, rather than the handful a fixture happens
		// to reach: a digest with no tasks in it would never have shown a
		// missing word for one.
		w := reflect.ValueOf(wordsFor(lang))
		for i := range w.NumField() {
			if w.Field(i).IsZero() {
				t.Errorf("%s has no %s", lang, w.Type().Field(i).Name)
			}
		}
	}
}
