package store_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/store"
)

const roomy = 1 << 30

func newLine(t *testing.T, s *store.Store, name string) store.BudgetItem {
	t.Helper()
	item, err := s.CreateBudgetItem(t.Context(), store.BudgetItem{Item: name, Unit: 100, Qty: 1}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}
	return item
}

// readyFile takes a file all the way to confirmed, which is the only state the
// rest of the application ever sees.
func readyFile(t *testing.T, s *store.Store, in store.Attachment) store.Attachment {
	t.Helper()
	if in.ContentType == "" {
		in.ContentType = "application/pdf"
	}
	if in.Size == 0 {
		in.Size = 1000
	}
	a, err := s.BeginAttachment(t.Context(), in, roomy)
	if err != nil {
		t.Fatalf("begin %q: %v", in.Name, err)
	}
	a, err = s.CompleteAttachment(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("complete %q: %v", in.Name, err)
	}
	return a
}

func garbage(t *testing.T, s *store.Store) []string {
	t.Helper()
	keys, err := s.AttachmentGarbage(t.Context(), 100)
	if err != nil {
		t.Fatalf("garbage: %v", err)
	}
	return keys
}

// A file nobody confirmed is a file nobody else can see. The plan is what the
// page draws from, so this is the property that keeps a half-finished upload —
// or one that lied about its size and was refused — out of everybody's list.
func TestOnlyAConfirmedFileIsInThePlan(t *testing.T) {
	s := newStore(t)
	line := newLine(t, s, "Catering")

	pending, err := s.BeginAttachment(t.Context(),
		store.Attachment{BudgetItemID: &line.ID, Name: "quote.pdf", ContentType: "application/pdf", Size: 10}, roomy)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if pending.Status != store.AttachmentUploading {
		t.Fatalf("a new row is %q, want uploading", pending.Status)
	}
	plan, err := s.LoadPlan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Attachments) != 0 {
		t.Fatalf("an unconfirmed upload is in the plan: %+v", plan.Attachments)
	}

	if _, err := s.CompleteAttachment(t.Context(), pending.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
	plan, err = s.LoadPlan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Attachments) != 1 || plan.Attachments[0].ID != pending.ID || plan.Attachments[0].Status != store.AttachmentReady {
		t.Fatalf("plan attachments = %+v, want the one confirmed file", plan.Attachments)
	}
}

// The schema's promise, tested against the schema: a fake would accept a file
// on two rows, or on none.
func TestAFileBelongsToExactlyOneRow(t *testing.T) {
	s := newStore(t)
	line := newLine(t, s, "Venue")
	task, err := s.CreateTask(t.Context(), store.Task{Name: "Sign the contract"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stranger := uuid.New()

	for name, in := range map[string]store.Attachment{
		"no parent":       {},
		"both parents":    {BudgetItemID: &line.ID, TaskID: &task.ID},
		"a line not here": {BudgetItemID: &stranger},
		"a task not here": {TaskID: &stranger},
	} {
		in.Name, in.ContentType, in.Size = "x.pdf", "application/pdf", 1
		if _, err := s.BeginAttachment(t.Context(), in, roomy); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, in := range map[string]store.Attachment{
		"on a line": {BudgetItemID: &line.ID},
		"on a task": {TaskID: &task.ID},
	} {
		in.Name, in.ContentType, in.Size = "x.pdf", "application/pdf", 1
		if _, err := s.BeginAttachment(t.Context(), in, roomy); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Deleting a line takes its files with it, and the files of every line under
// it — and every one of their objects is written down for removal, by the
// database, in the same transaction. This is the test for the trigger: nothing
// in Go is told which rows a cascade removed.
func TestDeletingALineQueuesEveryObjectUnderIt(t *testing.T) {
	s := newStore(t)
	parent := newLine(t, s, "Catering")
	child, err := s.CreateBudgetItem(t.Context(), store.BudgetItem{Item: "Dessert", ParentID: &parent.ID, Unit: 5, Qty: 40}, nil)
	if err != nil {
		t.Fatal(err)
	}
	other := newLine(t, s, "Flowers")

	onParent := readyFile(t, s, store.Attachment{BudgetItemID: &parent.ID, Name: "quote.pdf"})
	onChild := readyFile(t, s, store.Attachment{BudgetItemID: &child.ID, Name: "menu.pdf"})
	kept := readyFile(t, s, store.Attachment{BudgetItemID: &other.ID, Name: "florist.pdf"})
	// Never confirmed. There may still be bytes in the bucket for it.
	abandoned, err := s.BeginAttachment(t.Context(),
		store.Attachment{BudgetItemID: &child.ID, Name: "half.pdf", ContentType: "application/pdf", Size: 5}, roomy)
	if err != nil {
		t.Fatal(err)
	}

	if got := garbage(t, s); len(got) != 0 {
		t.Fatalf("garbage before any delete: %v", got)
	}
	if err := s.DeleteBudgetItem(t.Context(), parent.ID, parent.Revision); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// ObjectKey is what the server will ask the bucket to remove; the trigger
	// is what wrote these. If the two spellings ever differ, this is where it
	// shows — rather than as objects that are never deleted.
	want := []string{store.ObjectKey(onParent.ID), store.ObjectKey(onChild.ID), store.ObjectKey(abandoned.ID)}
	got := garbage(t, s)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("garbage\n got %v\nwant %v", got, want)
	}

	plan, err := s.LoadPlan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Attachments) != 1 || plan.Attachments[0].ID != kept.ID {
		t.Fatalf("plan attachments = %+v, want only the florist's", plan.Attachments)
	}

	// The history knows what went, although a cascade removed it. Confirmed
	// files only: the abandoned one was never in the history to be taken out.
	for _, gone := range []store.Attachment{onParent, onChild} {
		entries, err := s.ChangeHistory(t.Context(), store.EntityAttachments, gone.ID, store.HistoryPage{})
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 || entries[0].Action != store.ChangeDelete || entries[1].Action != store.ChangeCreate {
			t.Errorf("%s: history = %+v, want a create then a delete", gone.Name, entries)
		}
	}
	if entries, _ := s.ChangeHistory(t.Context(), store.EntityAttachments, abandoned.ID, store.HistoryPage{}); len(entries) != 0 {
		t.Errorf("an upload nobody confirmed has history: %+v", entries)
	}

	if err := s.ForgetAttachmentGarbage(t.Context(), got[0]); err != nil {
		t.Fatal(err)
	}
	if left := garbage(t, s); len(left) != 2 {
		t.Fatalf("after forgetting one key, %d remain", len(left))
	}
}

func TestDeletingATaskRecordsItsFiles(t *testing.T) {
	s := newStore(t)
	task, err := s.CreateTask(t.Context(), store.Task{Name: "Book the hall"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := readyFile(t, s, store.Attachment{TaskID: &task.ID, Name: "floor plan.pdf"})

	// A stale delete must leave everything as it was — the file, and a history
	// that does not claim otherwise.
	if err := s.DeleteTask(t.Context(), task.ID, task.Revision+7); !errors.Is(err, store.ErrStaleRevision) {
		t.Fatalf("stale delete: %v, want a conflict", err)
	}
	if entries, _ := s.ChangeHistory(t.Context(), store.EntityAttachments, file.ID, store.HistoryPage{}); len(entries) != 1 {
		t.Fatalf("a refused delete left %d history entries for the file, want the create alone", len(entries))
	}
	if got := garbage(t, s); len(got) != 0 {
		t.Fatalf("a refused delete queued %v", got)
	}

	if err := s.DeleteTask(t.Context(), task.ID, task.Revision); err != nil {
		t.Fatalf("delete: %v", err)
	}
	entries, err := s.ChangeHistory(t.Context(), store.EntityAttachments, file.ID, store.HistoryPage{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Action != store.ChangeDelete {
		t.Fatalf("history = %+v, want the delete recorded", entries)
	}
	if name := string(entries[0].Changes["name"].Old); name != `"floor plan.pdf"` {
		t.Errorf("the delete entry names %s, want the file", name)
	}
	if got := garbage(t, s); !slices.Equal(got, []string{store.ObjectKey(file.ID)}) {
		t.Fatalf("garbage = %v", got)
	}
}

// What is in the history, and what is deliberately not.
func TestAFileIsRecordedWithoutItsUploader(t *testing.T) {
	s := newStore(t)
	line := newLine(t, s, "Band")
	file := readyFile(t, s, store.Attachment{BudgetItemID: &line.ID, Name: "rider.pdf", Size: 4321})

	entries, err := s.ChangeHistory(t.Context(), store.EntityAttachments, file.ID, store.HistoryPage{})
	if err != nil || len(entries) != 1 {
		t.Fatalf("history = %+v, %v", entries, err)
	}
	changes := entries[0].Changes
	for _, field := range []string{"budget_item_id", "name", "content_type", "size"} {
		if _, ok := changes[field]; !ok {
			t.Errorf("%s is not recorded", field)
		}
	}
	for _, field := range []string{"uploaded_by", "status", "created_at"} {
		if _, ok := changes[field]; ok {
			t.Errorf("%s is recorded, and should not be", field)
		}
	}

	// Confirming twice is a retry, not a second event.
	if _, err := s.CompleteAttachment(t.Context(), file.ID); err != nil {
		t.Fatalf("second complete: %v", err)
	}
	if entries, _ := s.ChangeHistory(t.Context(), store.EntityAttachments, file.ID, store.HistoryPage{}); len(entries) != 1 {
		t.Errorf("confirming twice left %d entries", len(entries))
	}
}

func TestTheQuotaCountsUploadsInProgress(t *testing.T) {
	s := newStore(t)
	line := newLine(t, s, "Venue")
	file := func(size int64) store.Attachment {
		return store.Attachment{BudgetItemID: &line.ID, Name: "x.pdf", ContentType: "application/pdf", Size: size}
	}

	first, err := s.BeginAttachment(t.Context(), file(600), 1000)
	if err != nil {
		t.Fatalf("600 of 1000: %v", err)
	}
	// Not confirmed, and it still holds its space.
	if _, err := s.BeginAttachment(t.Context(), file(401), 1000); !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("600 pending + 401 of 1000: %v, want ErrQuotaExceeded", err)
	}
	if _, err := s.BeginAttachment(t.Context(), file(400), 1000); err != nil {
		t.Fatalf("600 + 400 of 1000 is exactly full, and allowed: %v", err)
	}
	if _, err := s.BeginAttachment(t.Context(), file(1), 1000); !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("one byte past full: %v", err)
	}
	// Removing a file gives its space back.
	if _, err := s.DeleteAttachment(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginAttachment(t.Context(), file(600), 1000); err != nil {
		t.Fatalf("after deleting 600: %v", err)
	}
}

// Twenty people each choosing a file at the same instant, with room for five.
// Without the lock each reads the total before any of the others has inserted,
// every one of them fits, and the cap is exceeded four times over.
func TestTheQuotaHoldsUnderConcurrency(t *testing.T) {
	s := newStore(t)
	line := newLine(t, s, "Venue")

	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for range 20 {
		wg.Go(func() {
			_, err := s.BeginAttachment(t.Context(),
				store.Attachment{BudgetItemID: &line.ID, Name: "x.pdf", ContentType: "application/pdf", Size: 100}, 500)
			if err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			} else if !errors.Is(err, store.ErrQuotaExceeded) {
				t.Errorf("begin: %v", err)
			}
		})
	}
	wg.Wait()
	if accepted != 5 {
		t.Fatalf("%d uploads of 100 accepted under a cap of 500, want exactly 5", accepted)
	}
}

func TestTheSweeperTakesBackAbandonedUploads(t *testing.T) {
	s := newStore(t)
	line := newLine(t, s, "Venue")
	begin := func(name string) store.Attachment {
		a, err := s.BeginAttachment(t.Context(),
			store.Attachment{BudgetItemID: &line.ID, Name: name, ContentType: "application/pdf", Size: 1}, roomy)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	old, fresh := begin("old.pdf"), begin("fresh.pdf")
	done := readyFile(t, s, store.Attachment{BudgetItemID: &line.ID, Name: "done.pdf"})

	// Age two of them. The confirmed one is old as well, and must survive: the
	// sweeper is for uploads nobody finished, not for files.
	if _, err := s.Pool().Exec(t.Context(),
		`UPDATE attachments SET created_at = now() - interval '2 hours' WHERE id = ANY($1)`,
		[]uuid.UUID{old.ID, done.ID}); err != nil {
		t.Fatal(err)
	}

	n, err := s.SweepStaleUploads(t.Context(), time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("swept %d, %v; want 1", n, err)
	}
	if _, err := s.Attachment(t.Context(), old.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the abandoned upload is still there: %v", err)
	}
	for _, keep := range []store.Attachment{fresh, done} {
		if _, err := s.Attachment(t.Context(), keep.ID); err != nil {
			t.Errorf("%s was swept: %v", keep.Name, err)
		}
	}
	if got := garbage(t, s); !slices.Equal(got, []string{store.ObjectKey(old.ID)}) {
		t.Errorf("garbage = %v, want the abandoned upload's object", got)
	}
}
