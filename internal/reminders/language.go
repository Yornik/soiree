package reminders

import (
	"fmt"
	"strings"
	"time"
)

// The languages a digest can be written in.
//
// The same three the interface offers and the account mails answer in, and the
// list is repeated here rather than shared with internal/httpd on purpose:
// which languages exist is a fact about the strings compiled into a package,
// and these are this package's own. Adding one means a tag here and a table
// below; a test holds the two together.
var languages = []string{"en", "nl", "id"}

// fallbackLanguage is what a digest is written in when SOIREE_LOCALE names a
// language this binary has no words for.
const fallbackLanguage = "en"

// parseLanguage reads a language out of a locale. It takes a bare tag or a
// full one ("nl", "NL", "nl-NL", "nl_NL"), because SOIREE_LOCALE is a
// formatting locale first and an operator writes whatever their own system
// calls it, and keeps the primary subtag, which is all a translation is keyed
// on. The same rule the account mails read the same variable by.
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
// SOIREE_LOCALE when there is a digest in it, and English otherwise.
func languageOfLocale(locale string) string {
	if l, ok := parseLanguage(locale); ok {
		return l
	}
	return fallbackLanguage
}

// words is one language's half of a digest: everything it says that is not a
// name, a date or an amount somebody typed into the plan. A struct rather than
// a map, so that a line named wrongly is a compile error instead of a gap in
// somebody's mail.
//
// The counts are functions rather than strings because a plural is not a
// suffix: Dutch has "1 dag" and "2 dagen", and Indonesian marks no plural at
// all, so which form a number takes is a question only the language can answer.
type words struct {
	// title is the digest's own name, and titleNamed the same with the event
	// in it. The subject falls back to title when the event has no name.
	title      string
	titleNamed string

	// The one clause the subject line and the notification share.
	overdueAndSoon string
	overdueOnly    string
	toDecide       string

	// The line under the heading, in the two shapes a digest comes in.
	dueInWindow  string
	dueOrOverdue string

	deadlines     string
	deadlinesNote string
	tasks         string
	tasksNote     string

	unnamedItem string
	unnamedTask string
	thisEvent   string
	inProgress  string

	// The footer, one sentence per field, joined in the order they are
	// declared here.
	dates       string
	whyItCame   string
	openPlanner string
	replyGoes   string

	things   func(int) string
	items    func(int) string
	days     func(int) string
	relative func(int) string
	day      func(time.Time) string
}

var english = words{
	title:      "Deadlines",
	titleNamed: "Deadlines — %s",

	overdueAndSoon: "%d overdue, %d coming up",
	overdueOnly:    "%d overdue",
	toDecide:       "%s to decide in the next %s",

	dueInWindow:  "%s in the next %s.",
	dueOrOverdue: "%s due or overdue, %d of them already past.",

	deadlines:     "Decisions to lock in",
	deadlinesNote: "lock-by dates up to %s",
	tasks:         "Tasks",
	tasksNote:     "due up to %s",

	unnamedItem: "(unnamed line)",
	unnamedTask: "(unnamed task)",
	thisEvent:   "this event",
	inProgress:  "in progress",

	dates: "Dates are the days recorded against each line, shown exactly as " +
		"stored. Today is %s in %s.",
	whyItCame: "You receive this because you are an active admin of %s, or " +
		"because whoever runs it added your address. There is no per-person " +
		"off switch: the digest stops when that address is removed from the " +
		"reminder settings, or when the account is no longer an active admin.",
	openPlanner: "Open the planner: %s",
	replyGoes: "Every recipient gets a copy of their own, so a reply reaches " +
		"the address this was sent from and nobody else on the list.",

	things:   plural("thing", "things"),
	items:    plural("item", "items"),
	days:     plural("day", "days"),
	relative: relativeWords("overdue by %d days", "overdue since yesterday", "today", "tomorrow", "in %d days"),
	// Go's reference layout is written in English, so this is the one language
	// whose dates need no names of their own.
	day: func(t time.Time) string { return t.Format(dayFormat) },
}

var dutch = words{
	title:      "Deadlines",
	titleNamed: "Deadlines: %s",

	overdueAndSoon: "%d te laat, %d op komst",
	overdueOnly:    "%d te laat",
	toDecide:       "%s te beslissen in de komende %s",

	dueInWindow:  "%s in de komende %s.",
	dueOrOverdue: "%s te doen, waarvan %d al te laat.",

	deadlines:     "Beslissingen om vast te leggen",
	deadlinesNote: "beslisdatums tot en met %s",
	tasks:         "Taken",
	tasksNote:     "deadlines tot en met %s",

	unnamedItem: "(naamloze regel)",
	unnamedTask: "(naamloze taak)",
	thisEvent:   "dit evenement",
	inProgress:  "bezig",

	dates: "Datums zijn de dagen die bij elke regel zijn vastgelegd, precies " +
		"zoals ze zijn opgeslagen. Vandaag is het %s in %s.",
	whyItCame: "Je krijgt dit omdat je actief beheerder van %s bent, of omdat " +
		"degene die het draait jouw adres heeft toegevoegd. Er is geen knop " +
		"per persoon: deze mail stopt zodra dat adres uit de " +
		"herinneringsinstellingen verdwijnt, of zodra het account geen actief " +
		"beheerder meer is.",
	openPlanner: "Open de planner: %s",
	replyGoes: "Iedere ontvanger krijgt een eigen kopie, dus een antwoord komt " +
		"alleen aan op het adres waarvandaan dit verstuurd is en bij niemand " +
		"anders op de lijst.",

	things:   plural("ding", "dingen"),
	items:    plural("item", "items"),
	days:     plural("dag", "dagen"),
	relative: relativeWords("%d dagen te laat", "sinds gisteren te laat", "vandaag", "morgen", "over %d dagen"),
	day: dayNames(
		[7]string{"zo", "ma", "di", "wo", "do", "vr", "za"},
		[12]string{"jan", "feb", "mrt", "apr", "mei", "jun", "jul", "aug", "sep", "okt", "nov", "dec"}),
}

var indonesian = words{
	title:      "Tenggat waktu",
	titleNamed: "Tenggat waktu: %s",

	overdueAndSoon: "%d terlambat, %d akan datang",
	overdueOnly:    "%d terlambat",
	toDecide:       "%s untuk diputuskan dalam %s ke depan",

	dueInWindow:  "%s dalam %s ke depan.",
	dueOrOverdue: "%s harus dikerjakan, %d di antaranya sudah terlambat.",

	deadlines:     "Keputusan yang harus diambil",
	deadlinesNote: "tanggal keputusan sampai %s",
	tasks:         "Tugas",
	tasksNote:     "tenggat sampai %s",

	unnamedItem: "(baris tanpa nama)",
	unnamedTask: "(tugas tanpa nama)",
	thisEvent:   "acara ini",
	inProgress:  "sedang dikerjakan",

	dates: "Tanggal adalah hari yang tercatat pada tiap baris, ditampilkan " +
		"persis seperti tersimpan. Hari ini %s di %s.",
	whyItCame: "Kamu menerima ini karena kamu admin aktif %s, atau karena " +
		"pengelolanya menambahkan alamatmu. Tidak ada tombol mati per orang: " +
		"pesan ini berhenti ketika alamat itu dihapus dari pengaturan " +
		"pengingat, atau ketika akun itu bukan admin aktif lagi.",
	openPlanner: "Buka perencana: %s",
	replyGoes: "Setiap penerima mendapat salinannya sendiri, jadi balasan " +
		"hanya sampai ke alamat pengirimnya dan tidak ke siapa pun lain dalam " +
		"daftar.",

	things:   invariant("hal"),
	items:    invariant("item"),
	days:     invariant("hari"),
	relative: relativeWords("terlambat %d hari", "terlambat sejak kemarin", "hari ini", "besok", "%d hari lagi"),
	day: dayNames(
		[7]string{"Min", "Sen", "Sel", "Rab", "Kam", "Jum", "Sab"},
		[12]string{"Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}),
}

// wordsFor is the table a digest is written from: the deployment's language,
// or English for a tag this binary has no words for. The fallback is the same
// floor the account mails have: a mail in English is readable, and half of one
// in each language is not.
func wordsFor(language string) words {
	switch language {
	case "nl":
		return dutch
	case "id":
		return indonesian
	}
	return english
}

// plural writes "1 day" and "3 days".
func plural(one, many string) func(int) string {
	return func(n int) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
}

// invariant writes "1 hari" and "3 hari". Indonesian marks no plural, and a
// table of two forms per noun would have invented one.
func invariant(noun string) func(int) string {
	return func(n int) string {
		return fmt.Sprintf("%d %s", n, noun)
	}
}

// relativeWords says how far off a date is in words, because "Fri 20 Feb" and
// "four days late" are answers to different questions and the second is the one
// that makes somebody act.
func relativeWords(late, yesterday, today, tomorrow, ahead string) func(int) string {
	return func(days int) string {
		switch {
		case days < -1:
			return fmt.Sprintf(late, -days)
		case days == -1:
			return yesterday
		case days == 0:
			return today
		case days == 1:
			return tomorrow
		default:
			return fmt.Sprintf(ahead, days)
		}
	}
}

// dayNames writes the same four fields dayFormat writes, weekday, day, month
// and year, from a language's own names, and never from the machine's locale:
// a mail is rendered wherever the scheduler happens to run.
func dayNames(weekdays [7]string, months [12]string) func(time.Time) string {
	return func(t time.Time) string {
		return fmt.Sprintf("%s %d %s %d",
			weekdays[int(t.Weekday())], t.Day(), months[int(t.Month())-1], t.Year())
	}
}
