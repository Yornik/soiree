package httpd

import (
	"strings"

	"github.com/Yornik/soiree/internal/store"
)

// The languages this binary can write to somebody in.
//
// The same three the interface ships, and the list is here rather than in the
// database on purpose: which languages exist is a fact about the templates
// compiled into this package, so this is the one place that can know. The
// column only checks that a value is shaped like a tag.
//
// Adding one means adding it here, a case to inviteMessage, and a column to
// the tables in web/src/app.js and web/src/auth.js. A test holds the first two
// together; the page's own tests hold the rest.
var languages = []string{"en", "nl", "id"}

// fallbackLanguage is what is spoken when nothing says otherwise: an account
// with no language on a deployment whose locale is not one of the above.
const fallbackLanguage = "en"

// parseLanguage reads a language somebody chose. It takes a bare tag or a
// locale — "nl", "NL", "nl-NL", "nl_NL" — because the people filling this in
// are copying whatever their own system calls it, and keeps the primary
// subtag, which is all a translation is keyed on.
func parseLanguage(s string) (string, bool) {
	tag := strings.ToLower(strings.TrimSpace(s))
	tag, _, _ = strings.Cut(strings.ReplaceAll(tag, "_", "-"), "-")
	for _, l := range languages {
		if tag == l {
			return l, true
		}
	}
	return "", false
}

// languageOfLocale is the deployment's own language: the primary subtag of
// SOIREE_LOCALE when there is a translation for it, and English otherwise.
// This is the same rule the page applies to the same value, so an account with
// no language of its own is written to in the language its planner opens in.
func languageOfLocale(locale string) string {
	if l, ok := parseLanguage(locale); ok {
		return l
	}
	return fallbackLanguage
}

// languageFor is the language to write to this person in.
//
// Their own if they have one and this binary still speaks it — a value can
// outlive its translation if one is ever withdrawn, and a mail in the
// deployment's language is a better answer to that than no mail.
func (a *Auth) languageFor(u store.User) string {
	if u.Language != nil {
		if l, ok := parseLanguage(*u.Language); ok {
			return l
		}
	}
	return a.defaultLanguage
}
