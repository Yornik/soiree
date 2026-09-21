package store

// Deliberately an internal test, unlike the rest of this package's tests.
//
// The property worth proving is that a rolled-back write announces nothing, and
// there is no public call that reaches insertChange and then fails — which is
// itself a good thing, and exactly why the test has to reach inside. TestMain
// lives in the external store_test package; both compile into one binary, so
// pgtest.Pool works here too.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/pgtest"
)

// noticeWait is how long a test waits for something that should arrive
// promptly. Generous, because it is only ever spent when the test is failing.
const noticeWait = 10 * time.Second

func newNotifyStore(t *testing.T) *Store {
	t.Helper()
	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(pool)
}

// listenTo opens a listener and drains it into a channel for the test's
// lifetime, so an assertion can simply read the next notice.
func listenTo(t *testing.T, s *Store) <-chan ChangeNotice {
	t.Helper()

	l, err := s.Listen(t.Context())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	out := make(chan ChangeNotice, 64)
	ctx, cancel := context.WithCancel(t.Context())
	drained := make(chan struct{})

	// Stop the pump before closing the connection, in that order. A pgx
	// connection is one conversation and not safe for a Close that overlaps a
	// read — which is why the hub closes its listener only on the goroutine
	// that was reading it.
	t.Cleanup(func() {
		cancel()
		<-drained
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = l.Close(closeCtx)
	})

	go func() {
		defer close(drained)
		defer close(out)
		for {
			notice, err := l.Next(ctx)
			if err != nil {
				if errors.Is(err, ErrUnknownNotice) {
					continue
				}
				return
			}
			select {
			case out <- notice:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func nextNotice(t *testing.T, ch <-chan ChangeNotice) ChangeNotice {
	t.Helper()
	select {
	case notice, ok := <-ch:
		if !ok {
			t.Fatal("the listener stopped before a notice arrived")
		}
		return notice
	case <-time.After(noticeWait):
		t.Fatal("no change was announced")
		return ChangeNotice{}
	}
}

// TestAWriteAnnouncesItsEntityAndID is the whole point: an edit made by one
// process is announced to the others by identifier, so they know which row to
// re-read without polling for it.
func TestAWriteAnnouncesItsEntityAndID(t *testing.T) {
	s := newNotifyStore(t)
	notices := listenTo(t, s)

	item, err := s.CreateBudgetItem(t.Context(), BudgetItem{Item: "Venue deposit", Unit: 250000, Qty: 1}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got := nextNotice(t, notices)
	if got.Entity != EntityBudgetItems {
		t.Errorf("entity = %q, want %q", got.Entity, EntityBudgetItems)
	}
	if got.ID == nil || *got.ID != item.ID {
		t.Errorf("id = %v, want %v", got.ID, item.ID)
	}
	if got.Action != ChangeCreate {
		t.Errorf("action = %q, want %q", got.Action, ChangeCreate)
	}
	if got.Revision == nil || *got.Revision != item.Revision {
		t.Errorf("revision = %v, want %d", got.Revision, item.Revision)
	}

	// And an update announces the revision the row now carries, which is what
	// lets a client tell a change it caused from one it did not.
	item.Unit = 260000
	updated, err := s.UpdateBudgetItem(t.Context(), item, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got = nextNotice(t, notices)
	if got.Action != ChangeUpdate || got.Revision == nil || *got.Revision != updated.Revision {
		t.Errorf("update announced %+v, want action=update revision=%d", got, updated.Revision)
	}
}

// TestAnnouncementsCarryIdentifiersNotRows: the payload is capped at 8000 bytes
// by PostgreSQL, and a note alone can approach that. Nothing of the row's
// content may travel.
func TestAnnouncementsCarryIdentifiersNotRows(t *testing.T) {
	s := newNotifyStore(t)
	notices := listenTo(t, s)

	secret := strings.Repeat("a note that must not travel. ", 200)

	if _, err := s.CreateBudgetItem(t.Context(), BudgetItem{
		Item: "Venue deposit", Unit: 250000, Qty: 1, Note: secret,
	}, nil); err != nil {
		t.Fatalf("create: %v", err)
	}

	got := nextNotice(t, notices)
	if got.Entity != EntityBudgetItems {
		t.Fatalf("entity = %q", got.Entity)
	}
	// The notice type has no field that could hold it, which is the structural
	// half of the guarantee; this is the half that would notice a field being
	// added.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(raw) > 200 {
		t.Errorf("a notice is %d bytes; it should carry identifiers only", len(raw))
	}
	if strings.Contains(string(raw), "must not travel") {
		t.Error("the row's content travelled in the notification")
	}
}

// TestARolledBackWriteAnnouncesNothing is the property that placement buys.
//
// NOTIFY is queued inside the transaction and delivered only on commit, so a
// change that does not survive its transaction is never announced — and there
// is no compensating code that could get this wrong.
func TestARolledBackWriteAnnouncesNothing(t *testing.T) {
	s := newNotifyStore(t)
	notices := listenTo(t, s)

	doomed := uuid.New()
	sentinel := errors.New("rolled back on purpose")
	_, err := inTx(t.Context(), s, func(tx pgx.Tx) (struct{}, error) {
		if err := insertChange(t.Context(), tx, EntityBudgetItems, doomed, ChangeCreate,
			nil, changeSet{}, SystemActor); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("inTx: %v, want the sentinel", err)
	}

	// A committed write afterwards, so "nothing arrived" is evidence about the
	// rollback rather than about a listener that was never working.
	item, err := s.CreateBudgetItem(t.Context(), BudgetItem{Item: "Venue deposit", Unit: 250000, Qty: 1}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got := nextNotice(t, notices)
	if got.ID == nil || *got.ID == doomed {
		t.Fatalf("the rolled-back change was announced: %+v", got)
	}
	if *got.ID != item.ID {
		t.Fatalf("first notice was for %v, want the committed row %v", *got.ID, item.ID)
	}
}

// TestAccountChangesAreNotAnnounced: /api/v1/users is admin-only and this
// stream is not, so announcing account changes would hand out the existence of
// accounts through a door the users API keeps shut. The change log still
// records them.
func TestAccountChangesAreNotAnnounced(t *testing.T) {
	s := newNotifyStore(t)
	notices := listenTo(t, s)

	user, err := s.CreateUser(t.Context(), User{Email: "ada@example.test", Role: RoleEditor})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// A plan change after it, as the thing that proves the listener was alive.
	item, err := s.CreateBudgetItem(t.Context(), BudgetItem{Item: "Venue deposit", Unit: 250000, Qty: 1}, nil)
	if err != nil {
		t.Fatalf("create item: %v", err)
	}

	got := nextNotice(t, notices)
	if got.Entity == EntityUsers {
		t.Fatalf("an account change was announced: %+v", got)
	}
	if got.ID == nil || *got.ID != item.ID {
		t.Fatalf("first notice = %+v, want the budget item %v", got, item.ID)
	}

	// And the history still has it, which is the part that must not be lost.
	history, err := s.ChangeHistory(t.Context(), EntityUsers, user.ID, HistoryPage{})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) == 0 {
		t.Error("the account change was neither announced nor recorded")
	}
}

// TestANoticeIsIgnoredRatherThanFatal: nothing in the application writes to the
// channel except notifyChange, but a person at a psql prompt can. That must
// not look like a broken connection.
func TestAStrayNotificationIsReportedNotFatal(t *testing.T) {
	s := newNotifyStore(t)

	l, err := s.Listen(t.Context())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close(context.Background()) })

	if _, err := s.pool.Exec(t.Context(), `SELECT pg_notify($1, 'hello')`, ChangeChannel); err != nil {
		t.Fatalf("notify: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), noticeWait)
	defer cancel()
	if _, err := l.Next(ctx); !errors.Is(err, ErrUnknownNotice) {
		t.Fatalf("Next() = %v, want ErrUnknownNotice", err)
	}

	// Still usable afterwards — the connection was never the problem.
	item, err := s.CreateBudgetItem(t.Context(), BudgetItem{Item: "Venue deposit", Unit: 250000, Qty: 1}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := l.Next(ctx)
	if err != nil {
		t.Fatalf("Next() after a stray notification: %v", err)
	}
	if got.ID == nil || *got.ID != item.ID {
		t.Fatalf("notice = %+v, want %v", got, item.ID)
	}
}

// TestTheListenerDoesNotComeFromThePool: LISTEN state lives on the connection,
// so a pooled one would carry it back into the pool and hand it to an unrelated
// query. The observable form of that rule is that listening does not consume a
// pool slot.
func TestTheListenerDoesNotComeFromThePool(t *testing.T) {
	s := newNotifyStore(t)

	l, err := s.Listen(t.Context())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close(context.Background()) })

	var pid uint32
	if err := s.pool.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("backend pid: %v", err)
	}
	if pid == l.PID() {
		t.Error("the listener is using a connection from the pool")
	}
}

// A file is announced when it is confirmed, and when it goes — and at no other
// moment. Reserving room for an upload is nobody else's business yet: a page
// that re-read the plan on that notice would find nothing new in it.
func TestAFileIsAnnouncedWhenItIsConfirmedAndWhenItGoes(t *testing.T) {
	s := newNotifyStore(t)
	item, err := s.CreateBudgetItem(t.Context(), BudgetItem{Item: "Catering", Unit: 100, Qty: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	notices := listenTo(t, s)

	pending, err := s.BeginAttachment(t.Context(),
		Attachment{BudgetItemID: &item.ID, Name: "quote.pdf", ContentType: "application/pdf", Size: 10}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteAttachment(t.Context(), pending.ID); err != nil {
		t.Fatal(err)
	}

	// The first thing heard is the confirmation: had the reservation been
	// announced, it would have arrived ahead of it.
	got := nextNotice(t, notices)
	if got.Entity != EntityAttachments || got.Action != ChangeCreate || got.ID == nil || *got.ID != pending.ID {
		t.Fatalf("first notice = %+v, want the file's create", got)
	}
	// No revision: a file is never edited, and a client holding none must
	// read the plan again rather than compare.
	if got.Revision != nil {
		t.Errorf("revision = %d, want none", *got.Revision)
	}

	if _, err := s.DeleteAttachment(t.Context(), pending.ID); err != nil {
		t.Fatal(err)
	}
	got = nextNotice(t, notices)
	if got.Entity != EntityAttachments || got.Action != ChangeDelete {
		t.Fatalf("second notice = %+v, want the file's delete", got)
	}
}

// TestAnErasureAnnouncesTheRowsItRewrote. Erasure is the one write here that
// records nothing, because the entry would hold the very value being struck
// out. Announcing is separate from recording for exactly that reason: without
// a notice, a second browser goes on showing the erased name until somebody
// reloads it, and every edit it sends meanwhile comes back as a conflict it
// cannot explain.
func TestAnErasureAnnouncesTheRowsItRewrote(t *testing.T) {
	s := newNotifyStore(t)
	ctx := t.Context()

	task, err := s.CreateTask(ctx, Task{Name: "Confirm the caterer", Owner: "Ada"}, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Listening only from here, so what arrives is the erasure's own doing.
	notices := listenTo(t, s)

	if _, err := s.EraseSubject(ctx, ErasureRequest{Subject: SubjectRef{Aliases: []string{"Ada"}}}); err != nil {
		t.Fatalf("erase: %v", err)
	}

	after, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	got := nextNotice(t, notices)
	if got.Entity != EntityTasks || got.ID == nil || *got.ID != task.ID {
		t.Errorf("announced %+v, want the erased task %v", got, task.ID)
	}
	if got.Action != ChangeUpdate {
		t.Errorf("action = %q, want %q", got.Action, ChangeUpdate)
	}
	if got.Revision == nil || *got.Revision != after.Revision {
		t.Errorf("revision = %v, want the %d the row now carries", got.Revision, after.Revision)
	}
}
