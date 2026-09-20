package httpd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Yornik/soiree/internal/store"
)

const activityPassword = "correct horse battery staple"

// The feed names accounts, and the list of accounts is an admin's to read. An
// editor may change the plan and still may not read this.
func TestTheActivityFeedIsForAdminsOnly(t *testing.T) {
	f := newPushFixture(t)
	for email, role := range map[string]store.Role{
		"admin@example.test": store.RoleAdmin, "editor@example.test": store.RoleEditor, "viewer@example.test": store.RoleViewer,
	} {
		f.seed(t, email, role, activityPassword)
	}

	if rec := f.do(t, http.MethodGet, "/api/v1/activity", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("nobody: %d, want 401", rec.Code)
	}
	for _, email := range []string{"editor@example.test", "viewer@example.test"} {
		rec := f.do(t, http.MethodGet, "/api/v1/activity", nil, f.login(t, email, activityPassword))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", email, rec.Code, rec.Body)
		}
	}
	rec := f.do(t, http.MethodGet, "/api/v1/activity", nil, f.login(t, "admin@example.test", activityPassword))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
}

// The feed speaks the API's language, not the database's: a client that learned
// "500.00" and `lockBy` from GET /plan must not meet 50000 and `lock_by` here.
func TestTheActivityFeedSpeaksTheAPIsLanguage(t *testing.T) {
	f := newPushFixture(t)
	f.seed(t, "admin@example.test", store.RoleAdmin, activityPassword)
	admin := f.login(t, "admin@example.test", activityPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/budget-items",
		map[string]any{"item": "Venue", "unit": "2500.00", "qty": 1, "paid": "0", "lockBy": "2030-05-01"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decodeRec[budgetItemJSON](t, rec)
	rec = f.do(t, http.MethodPatch, "/api/v1/budget-items/"+created.ID.String(),
		map[string]any{"paid": "500.00", "revision": created.Revision}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}

	rec = f.do(t, http.MethodGet, "/api/v1/activity", nil, admin)
	page := decodeRec[activityPageJSON](t, rec)
	if len(page.Entries) < 2 {
		t.Fatalf("feed = %s", rec.Body)
	}

	edit := page.Entries[0]
	if edit.Entity != store.EntityBudgetItems || edit.Action != "update" || edit.Label == nil || *edit.Label != "Venue" {
		t.Fatalf("newest entry = %+v", edit)
	}
	if edit.Actor.Email == nil || *edit.Actor.Email != "admin@example.test" || edit.Actor.Kind != "user" {
		t.Errorf("actor = %+v", edit.Actor)
	}
	if len(edit.Changes) != 1 || edit.Changes[0].Field != "paid" ||
		string(edit.Changes[0].Old) != `"0.00"` || string(edit.Changes[0].New) != `"500.00"` {
		t.Errorf("changes = %s", rec.Body)
	}

	made := page.Entries[1]
	fields := map[string]string{}
	for _, c := range made.Changes {
		fields[c.Field] = string(c.New)
	}
	if fields["unit"] != `"2500.00"` {
		t.Errorf("unit on create = %s, want the major-unit string", fields["unit"])
	}
	if _, ok := fields["lockBy"]; !ok {
		t.Errorf("no lockBy among %v: column names are leaking", fields)
	}
	for name := range fields {
		if strings.Contains(name, "_") {
			t.Errorf("field %q is a column name, not an API name", name)
		}
	}
	// A create has nothing before it, and says so with null rather than "0.00".
	for _, c := range made.Changes {
		if c.Field == "unit" && string(c.Old) != "null" {
			t.Errorf("unit before the row existed = %s", c.Old)
		}
	}
}

// A `date` column scans into a time.Time, so the log records the instant Go
// marshals it as. The feed has to undo that on the way out: lockBy and due are
// calendar days in every other response, and a client west of UTC that reads
// "2030-05-01T00:00:00Z" as an instant shows the 30th of April.
func TestTheActivityFeedSpeaksDatesAsDays(t *testing.T) {
	f := newPushFixture(t)
	f.seed(t, "admin@example.test", store.RoleAdmin, activityPassword)
	admin := f.login(t, "admin@example.test", activityPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/budget-items",
		map[string]any{"item": "Venue", "unit": "2500.00", "qty": 1, "lockBy": "2030-05-01"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create the line: %d %s", rec.Code, rec.Body)
	}
	line := decodeRec[budgetItemJSON](t, rec)
	rec = f.do(t, http.MethodPatch, "/api/v1/budget-items/"+line.ID.String(),
		map[string]any{"lockBy": "2030-06-01", "revision": line.Revision}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch the line: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(t, http.MethodPost, "/api/v1/tasks",
		map[string]any{"name": "Book the hall", "due": "2030-04-01"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create the task: %d %s", rec.Code, rec.Body)
	}

	rec = f.do(t, http.MethodGet, "/api/v1/activity", nil, admin)
	page := decodeRec[activityPageJSON](t, rec)

	changed := func(entity, action, field string) (activityChangeJSON, bool) {
		for _, e := range page.Entries {
			if e.Entity != entity || e.Action != action {
				continue
			}
			for _, c := range e.Changes {
				if c.Field == field {
					return c, true
				}
			}
		}
		return activityChangeJSON{}, false
	}

	edit, ok := changed(store.EntityBudgetItems, "update", "lockBy")
	if !ok {
		t.Fatalf("no lockBy among the changes: %s", rec.Body)
	}
	if string(edit.Old) != `"2030-05-01"` || string(edit.New) != `"2030-06-01"` {
		t.Errorf("lockBy moved from %s to %s, want the days the client sent", edit.Old, edit.New)
	}
	due, ok := changed(store.EntityTasks, "create", "due")
	if !ok {
		t.Fatalf("no due among the changes: %s", rec.Body)
	}
	// The row did not exist before a create, and "no date yet" stays null
	// rather than becoming a day nobody chose.
	if string(due.Old) != "null" || string(due.New) != `"2030-04-01"` {
		t.Errorf("due went from %s to %s, want null -> the day the client sent", due.Old, due.New)
	}
}

func TestTheActivityFeedPages(t *testing.T) {
	f := newPushFixture(t)
	f.seed(t, "admin@example.test", store.RoleAdmin, activityPassword)
	admin := f.login(t, "admin@example.test", activityPassword)
	for _, name := range []string{"a", "b", "c"} {
		if _, err := f.store.CreateTask(t.Context(), store.Task{Name: name}, nil); err != nil {
			t.Fatal(err)
		}
	}

	rec := f.do(t, http.MethodGet, "/api/v1/activity?limit=2", nil, admin)
	first := decodeRec[activityPageJSON](t, rec)
	if len(first.Entries) != 2 || first.NextBefore == nil || *first.NextBefore != first.Entries[1].ID {
		t.Fatalf("first page = %s", rec.Body)
	}
	rec = f.do(t, http.MethodGet, "/api/v1/activity?limit=200&before="+itoa(*first.NextBefore), nil, admin)
	rest := decodeRec[activityPageJSON](t, rec)
	if rest.NextBefore != nil {
		t.Errorf("a page that was not full still offers another: %s", rec.Body)
	}
	if len(rest.Entries) == 0 || rest.Entries[0].ID >= *first.NextBefore {
		t.Errorf("second page = %s", rec.Body)
	}

	for _, bad := range []string{"limit=0", "limit=201", "limit=lots", "before=0", "before=-4", "before=x",
		// A filter nobody can spell is worse than no filter: a table name with
		// a typo in it would read as a feed with nothing in it, and an empty
		// feed looks like an answer.
		"entity=budget_item", "entity=budget_items&entityId=the-venue",
		"entityId=6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		// The settings singleton is one row with no id, so an id beside it
		// matches nothing whatever is asked, and that empty feed reads as an
		// answer in the same way.
		"entity=settings&entityId=6ba7b810-9dad-11d1-80b4-00c04fd430c8"} {
		if rec := f.do(t, http.MethodGet, "/api/v1/activity?"+bad, nil, admin); rec.Code != http.StatusBadRequest {
			t.Errorf("?%s: %d, want 400", bad, rec.Code)
		}
	}
}

// The log exists to answer "who moved the venue figure, and when". Paging the
// whole feed backwards to find one line is not that answer, so the feed
// narrows: to an entity, and to one row of it.
func TestTheActivityFeedNarrowsToOneRow(t *testing.T) {
	f := newPushFixture(t)
	f.seed(t, "admin@example.test", store.RoleAdmin, activityPassword)
	admin := f.login(t, "admin@example.test", activityPassword)

	line := func(item string) budgetItemJSON {
		rec := f.do(t, http.MethodPost, "/api/v1/budget-items",
			map[string]any{"item": item, "unit": "2500.00", "qty": 1}, admin)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", item, rec.Code, rec.Body)
		}
		return decodeRec[budgetItemJSON](t, rec)
	}
	venue, band := line("Venue"), line("Band")
	rec := f.do(t, http.MethodPatch, "/api/v1/budget-items/"+venue.ID.String(),
		map[string]any{"paid": "500.00", "revision": venue.Revision}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch the venue: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(t, http.MethodPost, "/api/v1/tasks", map[string]any{"name": "Book the hall"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create the task: %d %s", rec.Code, rec.Body)
	}

	venueOnly := "/api/v1/activity?entity=budget_items&entityId=" + venue.ID.String()
	rec = f.do(t, http.MethodGet, venueOnly, nil, admin)
	page := decodeRec[activityPageJSON](t, rec)
	if len(page.Entries) != 2 {
		t.Fatalf("the venue's history has %d entries, want the create and the payment: %s", len(page.Entries), rec.Body)
	}
	for _, e := range page.Entries {
		if e.Entity != store.EntityBudgetItems || e.EntityID == nil || *e.EntityID != venue.ID {
			t.Errorf("entry %d is %s %v, and the venue is what was asked about", e.ID, e.Entity, e.EntityID)
		}
	}

	// An entity on its own is every row of it, which is how the settings
	// singleton, a row with no id, is asked for too.
	rec = f.do(t, http.MethodGet, "/api/v1/activity?entity=tasks", nil, admin)
	tasks := decodeRec[activityPageJSON](t, rec)
	if len(tasks.Entries) != 1 || tasks.Entries[0].Entity != store.EntityTasks {
		t.Errorf("the tasks feed = %s", rec.Body)
	}

	// The narrowing is the query's, not the page's: a page of one holds one of
	// the venue's own entries and offers the one before it, where a feed read
	// whole and sifted afterwards would hand back an empty page and an id to
	// somebody else's row.
	rec = f.do(t, http.MethodGet, venueOnly+"&limit=1", nil, admin)
	first := decodeRec[activityPageJSON](t, rec)
	if len(first.Entries) != 1 || first.NextBefore == nil {
		t.Fatalf("a filtered first page = %s", rec.Body)
	}
	rec = f.do(t, http.MethodGet, venueOnly+"&limit=1&before="+itoa(*first.NextBefore), nil, admin)
	next := decodeRec[activityPageJSON](t, rec)
	if len(next.Entries) != 1 || next.Entries[0].ID >= first.Entries[0].ID {
		t.Fatalf("the page after it = %s", rec.Body)
	}
	if next.Entries[0].EntityID == nil || *next.Entries[0].EntityID != venue.ID {
		t.Errorf("paging the venue's history reached %v", next.Entries[0].EntityID)
	}

	for _, e := range append(append([]activityEntryJSON{}, page.Entries...), next.Entries...) {
		if e.EntityID != nil && *e.EntityID == band.ID {
			t.Errorf("entry %d is the band's", e.ID)
		}
	}
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{digits[n%10]}, b...)
	}
	return string(b)
}

func TestCamelCase(t *testing.T) {
	for in, want := range map[string]string{
		"paid": "paid", "lock_by": "lockBy", "sponsor_ids": "sponsorIds", "budget_item_id": "budgetItemId", "content_type": "contentType",
	} {
		if got := camelCase(in); got != want {
			t.Errorf("camelCase(%q) = %q, want %q", in, got, want)
		}
	}
}
