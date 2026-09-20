package httpd

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/store"
)

// The activity feed: what everybody has been doing, newest first.
//
// Admin-only, and that is a decision about addresses rather than about the
// plan. Every entry names an account, and the list of accounts is something
// only an admin can read today; a feed open to every editor would hand the same
// list out through a second door, annotated with what each person did and when.
//
// It is a read of the change history the store has kept since before there was
// anything to show it with, translated into the API's own language on the way
// out: camelCase field names, because that is what every other response calls
// them, and money as decimal strings in major units, because a client that has
// learned "500.00" from GET /plan must not be handed 50000 here and left to
// guess.

const maxActivityLabel = 120

// moneyFields are stored as minor units and spoken as major-unit strings.
var moneyFields = map[string]map[string]bool{
	store.EntityBudgetItems: {"unit": true, "paid": true},
	store.EntitySettings:    {"ceiling": true},
}

// dateFields are `date` columns, which reach the log as instants and are spoken
// as the calendar days they are. These two are every `date` the schema has.
var dateFields = map[string]map[string]bool{
	store.EntityBudgetItems: {"lock_by": true},
	store.EntityTasks:       {"due": true},
}

type activityActorJSON struct {
	// ID and Email are null where there was no account, or where it has since
	// been deleted. Kind still says what sort of actor it was.
	ID    *uuid.UUID `json:"id"`
	Email *string    `json:"email"`
	Kind  string     `json:"kind"`
}

type activityChangeJSON struct {
	Field string          `json:"field"`
	Old   json.RawMessage `json:"old"`
	New   json.RawMessage `json:"new"`
}

type activityEntryJSON struct {
	ID       int64                `json:"id"`
	At       time.Time            `json:"at"`
	Entity   string               `json:"entity"`
	EntityID *uuid.UUID           `json:"entityId"`
	Action   string               `json:"action"`
	Label    *string              `json:"label"`
	Actor    activityActorJSON    `json:"actor"`
	Changes  []activityChangeJSON `json:"changes"`
}

type activityPageJSON struct {
	Entries []activityEntryJSON `json:"entries"`
	// NextBefore is what to send as `before` for the next page, or null when
	// this page was not full and there is nothing older.
	NextBefore *int64 `json:"nextBefore"`
}

func (s *Server) serveActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, errBadRequest, "limit must be a whole number from 1 to 200")
			return
		}
		limit = n
	}
	var before int64
	if raw := q.Get("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, errBadRequest, "before must be the id of an entry")
			return
		}
		before = n
	}

	entries, err := s.store.Activity(r.Context(), before, limit)
	if err != nil {
		writeInternal(w, err)
		return
	}

	page := activityPageJSON{Entries: make([]activityEntryJSON, 0, len(entries))}
	for _, e := range entries {
		page.Entries = append(page.Entries, encodeActivity(s.cfg.Currency, e))
	}
	if len(entries) == limit {
		last := entries[len(entries)-1].ID
		page.NextBefore = &last
	}
	writeJSON(w, http.StatusOK, page)
}

func encodeActivity(currency string, e store.ActivityEntry) activityEntryJSON {
	out := activityEntryJSON{
		ID:       e.ID,
		At:       e.At,
		Entity:   e.Entity,
		EntityID: e.EntityID,
		Action:   string(e.Action),
		Label:    shortLabel(e.Label),
		Actor:    activityActorJSON{ID: e.ActorID, Email: e.ActorEmail, Kind: e.ActorLabel},
		Changes:  make([]activityChangeJSON, 0, len(e.Changes)),
	}
	for field, change := range e.Changes {
		c := activityChangeJSON{Field: camelCase(field), Old: change.Old, New: change.New}
		if moneyFields[e.Entity][field] {
			c.Old, c.New = majorUnits(currency, c.Old), majorUnits(currency, c.New)
		}
		if dateFields[e.Entity][field] {
			c.Old, c.New = civilDates(c.Old), civilDates(c.New)
		}
		out.Changes = append(out.Changes, c)
	}
	// A map has no order, and a list that reshuffles between two reads of the
	// same entry looks like a change that did not happen.
	sort.Slice(out.Changes, func(i, j int) bool { return out.Changes[i].Field < out.Changes[j].Field })
	return out
}

// majorUnits turns a recorded amount into the string the rest of the API uses.
// Anything that is not a whole number — null on one side of a create or a
// delete, above all — is passed through as it is.
func majorUnits(currency string, raw json.RawMessage) json.RawMessage {
	// Checked by hand, because encoding/json does not: unmarshalling `null`
	// into an int64 succeeds and leaves it zero, which turned "there was no
	// amount, the row did not exist yet" into "the amount was 0.00" - a
	// statement about money that was never true.
	if strings.TrimSpace(string(raw)) == "null" {
		return raw
	}
	var minor int64
	if err := json.Unmarshal(raw, &minor); err != nil {
		return raw
	}
	out, err := json.Marshal(store.FormatMajor(currency, minor))
	if err != nil {
		return raw
	}
	return out
}

// civilDates turns a recorded instant back into the day it was always about.
// A `date` column scans into a time.Time at midnight UTC and the log keeps what
// json.Marshal makes of that, so the history holds "2030-05-01T00:00:00Z" where
// the rest of the API says "2030-05-01". That is the rendering civilDate exists
// to keep off the wire, and one a reader west of UTC turns into the day before.
//
// Undone here rather than at the write: the log is append-only, and entries
// written before this existed have to read correctly too. Anything that is not
// an instant is passed through as it is: null on one side of a create or a
// delete above all, and a day already spoken as one.
func civilDates(raw json.RawMessage) json.RawMessage {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return raw
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return raw
	}
	out, err := json.Marshal(t.UTC().Format(time.DateOnly))
	if err != nil {
		return raw
	}
	return out
}

// shortLabel keeps a name to a line. A note's "name" is its whole text.
func shortLabel(label *string) *string {
	if label == nil {
		return nil
	}
	runes := []rune(strings.Join(strings.Fields(*label), " "))
	if len(runes) > maxActivityLabel {
		runes = append(runes[:maxActivityLabel-1], '…')
	}
	s := string(runes)
	return &s
}

// camelCase turns a column name into the name the API uses for the same field:
// lock_by is lockBy, sponsor_ids is sponsorIds.
func camelCase(column string) string {
	parts := strings.Split(column, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}
