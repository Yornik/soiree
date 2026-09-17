package httpd

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/store"
)

// Wire types for the API.
//
// The store's structs are not marshalled directly. Two reasons, both of which
// would otherwise show up as a bug rather than as ugliness: the `db` tags would
// give the browser snake_case for some fields and Go field names for others,
// and a `date` column read into a time.Time marshals as a full RFC3339
// timestamp, which is not the same value everywhere on earth.
//
// Money crosses this boundary as an integer in minor units, exactly as stored.
// A JSON number in major units is a float in every parser the browser has, and
// a budget that loses a cent per line is worse than one that asks the client to
// know the currency's exponent — which it already does, from the config block
// in the page.

// civilDate is a calendar date with no time and no zone, which is what a
// `date` column holds. Left as a time.Time it would marshal as
// "2026-10-31T00:00:00Z", and a reader east of UTC would render that as the
// 30th — the same failure the event date's timezone rule exists to prevent.
type civilDate time.Time

func (d civilDate) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(d).Format(time.DateOnly))
}

func (d *civilDate) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("must be a date string like 2026-10-31")
	}
	// time.Parse with a date-only layout yields UTC midnight, which is what the
	// column round-trips to.
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return fmt.Errorf("must be a date like 2026-10-31, got %q", s)
	}
	*d = civilDate(t)
	return nil
}

func dateOrNil(t *time.Time) *civilDate {
	if t == nil {
		return nil
	}
	d := civilDate(*t)
	return &d
}

// idsOrEmpty keeps an absent list out of the response as `[]` rather than
// `null`, so the browser never has to check which of the two it got.
func idsOrEmpty(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

// optional distinguishes "absent from the body" from "present and null", which
// a plain pointer cannot. A PATCH has to leave an omitted field alone and clear
// one sent as null, and those are different requests.
type optional[T any] struct {
	set bool
	// value is nil when the field was present and null.
	value *T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.set = true
	if string(b) == "null" {
		o.value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.value = &v
	return nil
}

// fieldErrs collects the first problem found while applying a body, so the
// per-field code below stays a flat list instead of an error check per line.
type fieldErrs struct{ err error }

func (f *fieldErrs) fail(field, msg string) {
	if f.err == nil {
		f.err = fmt.Errorf("%s %s", field, msg)
	}
}

// setValue applies a non-nullable field. Explicit null is refused rather than
// coerced to the zero value: `{"unit": null}` is a client bug, and silently
// writing 0 into a money column is the expensive way to find out.
func setValue[T any](f *fieldErrs, field string, o optional[T], dst *T) {
	if !o.set {
		return
	}
	if o.value == nil {
		f.fail(field, "must not be null")
		return
	}
	*dst = *o.value
}

// setPtr applies a nullable field, where null means "clear it".
func setPtr[T any](o optional[T], dst **T) {
	if !o.set {
		return
	}
	*dst = o.value
}

// setDate applies a nullable date, converting out of the wire type.
func setDate(o optional[civilDate], dst **time.Time) {
	if !o.set {
		return
	}
	if o.value == nil {
		*dst = nil
		return
	}
	t := time.Time(*o.value)
	*dst = &t
}

// asAny adapts a typed encoder to the entity descriptor's func(T) any, which
// is what lets one set of generic handlers serve six tables.
func asAny[T, D any](f func(T) D) func(T) any {
	return func(v T) any { return f(v) }
}

// echoed holds the read-only fields a client sends back when it PATCHes a row
// it is already holding. They are accepted and ignored.
//
// Decoding is strict (see decodeBody), because `{"unitt": 550}` silently doing
// nothing to a money figure is precisely the bug nobody catches. Strictness
// would also reject the obvious client — read a row, change one field, send the
// whole thing back — so the fields that round-trip are named here and dropped.
// The revision is read separately, by revisionFromBody: it is not a field of
// the row, it is the caller's claim about which version they edited.
type echoed struct {
	ID        json.RawMessage `json:"id"`
	Revision  json.RawMessage `json:"revision"`
	UpdatedAt json.RawMessage `json:"updatedAt"`
	UpdatedBy json.RawMessage `json:"updatedBy"`
}

// --- responses ---------------------------------------------------------

type settingsJSON struct {
	Ceiling      int64     `json:"ceiling"`
	InflationPct float64   `json:"inflationPct"`
	FxRate       float64   `json:"fxRate"`
	SplitEvenly  bool      `json:"splitEvenly"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func encodeSettings(s store.Settings) settingsJSON {
	return settingsJSON{
		Ceiling:      s.Ceiling,
		InflationPct: s.InflationPct,
		FxRate:       s.FxRate,
		SplitEvenly:  s.SplitEvenly,
		Revision:     s.Revision,
		UpdatedAt:    s.UpdatedAt,
	}
}

type phaseJSON struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Position int32     `json:"position"`
}

func encodePhase(p store.Phase) phaseJSON {
	return phaseJSON{ID: p.ID, Name: p.Name, Position: p.Position}
}

type sponsorJSON struct {
	ID        uuid.UUID  `json:"id"`
	Code      string     `json:"code"`
	Name      string     `json:"name"`
	Position  int32      `json:"position"`
	Revision  int64      `json:"revision"`
	UpdatedAt time.Time  `json:"updatedAt"`
	UpdatedBy *uuid.UUID `json:"updatedBy"`
}

func encodeSponsor(s store.Sponsor) sponsorJSON {
	return sponsorJSON{
		ID: s.ID, Code: s.Code, Name: s.Name, Position: s.Position,
		Revision: s.Revision, UpdatedAt: s.UpdatedAt, UpdatedBy: s.UpdatedBy,
	}
}

type budgetItemJSON struct {
	ID       uuid.UUID  `json:"id"`
	PhaseID  *uuid.UUID `json:"phaseId"`
	ParentID *uuid.UUID `json:"parentId"`
	Item     string     `json:"item"`
	Vendor   string     `json:"vendor"`
	// Minor units. See the note at the top of this file.
	Unit      int64       `json:"unit"`
	Qty       float64     `json:"qty"`
	Paid      int64       `json:"paid"`
	LockBy    *civilDate  `json:"lockBy"`
	Note      string      `json:"note"`
	Position  int32       `json:"position"`
	Sponsors  []uuid.UUID `json:"sponsors"`
	Revision  int64       `json:"revision"`
	UpdatedAt time.Time   `json:"updatedAt"`
	UpdatedBy *uuid.UUID  `json:"updatedBy"`
}

func encodeBudgetItem(b store.BudgetItem) budgetItemJSON {
	return budgetItemJSON{
		ID: b.ID, PhaseID: b.PhaseID, ParentID: b.ParentID,
		Item: b.Item, Vendor: b.Vendor,
		Unit: b.Unit, Qty: b.Qty, Paid: b.Paid,
		LockBy: dateOrNil(b.LockBy), Note: b.Note, Position: b.Position,
		Sponsors: idsOrEmpty(b.SponsorIDs),
		Revision: b.Revision, UpdatedAt: b.UpdatedAt, UpdatedBy: b.UpdatedBy,
	}
}

type programmeJSON struct {
	ID           uuid.UUID  `json:"id"`
	Title        string     `json:"title"`
	Note         string     `json:"note"`
	Position     int32      `json:"position"`
	BudgetItemID *uuid.UUID `json:"budgetItemId"`
	Revision     int64      `json:"revision"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

func encodeProgrammeEntry(p store.ProgrammeEntry) programmeJSON {
	return programmeJSON{
		ID: p.ID, Title: p.Title, Note: p.Note, Position: p.Position,
		BudgetItemID: p.BudgetItemID, Revision: p.Revision, UpdatedAt: p.UpdatedAt,
	}
}

type taskJSON struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Owner     string     `json:"owner"`
	Due       *civilDate `json:"due"`
	Status    string     `json:"status"`
	Position  int32      `json:"position"`
	Revision  int64      `json:"revision"`
	UpdatedAt time.Time  `json:"updatedAt"`
	UpdatedBy *uuid.UUID `json:"updatedBy"`
}

func encodeTask(t store.Task) taskJSON {
	return taskJSON{
		ID: t.ID, Name: t.Name, Owner: t.Owner, Due: dateOrNil(t.Due),
		Status: string(t.Status), Position: t.Position,
		Revision: t.Revision, UpdatedAt: t.UpdatedAt, UpdatedBy: t.UpdatedBy,
	}
}

type noteJSON struct {
	ID        uuid.UUID `json:"id"`
	Text      string    `json:"text"`
	Position  int32     `json:"position"`
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func encodeNote(n store.Note) noteJSON {
	return noteJSON{ID: n.ID, Text: n.Text, Position: n.Position, Revision: n.Revision, UpdatedAt: n.UpdatedAt}
}

// planJSON is the whole plan in one response.
//
// The keys are the lowerCamelCase of the store.Plan field names, without
// exception, so there is one rule to remember rather than seven. Every list is
// present and non-null even when empty.
type planJSON struct {
	Settings    settingsJSON     `json:"settings"`
	Phases      []phaseJSON      `json:"phases"`
	Sponsors    []sponsorJSON    `json:"sponsors"`
	BudgetItems []budgetItemJSON `json:"budgetItems"`
	Programme   []programmeJSON  `json:"programme"`
	Tasks       []taskJSON       `json:"tasks"`
	Notes       []noteJSON       `json:"notes"`
}

func encodePlan(p store.Plan) planJSON {
	out := planJSON{
		Settings:    encodeSettings(p.Settings),
		Phases:      make([]phaseJSON, 0, len(p.Phases)),
		Sponsors:    make([]sponsorJSON, 0, len(p.Sponsors)),
		BudgetItems: make([]budgetItemJSON, 0, len(p.BudgetItems)),
		Programme:   make([]programmeJSON, 0, len(p.Programme)),
		Tasks:       make([]taskJSON, 0, len(p.Tasks)),
		Notes:       make([]noteJSON, 0, len(p.Notes)),
	}
	for _, v := range p.Phases {
		out.Phases = append(out.Phases, encodePhase(v))
	}
	for _, v := range p.Sponsors {
		out.Sponsors = append(out.Sponsors, encodeSponsor(v))
	}
	for _, v := range p.BudgetItems {
		out.BudgetItems = append(out.BudgetItems, encodeBudgetItem(v))
	}
	for _, v := range p.Programme {
		out.Programme = append(out.Programme, encodeProgrammeEntry(v))
	}
	for _, v := range p.Tasks {
		out.Tasks = append(out.Tasks, encodeTask(v))
	}
	for _, v := range p.Notes {
		out.Notes = append(out.Notes, encodeNote(v))
	}
	return out
}
