package httpd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/objstore"
	"github.com/Yornik/soiree/internal/pgtest"
	"github.com/Yornik/soiree/internal/s3test"
	"github.com/Yornik/soiree/internal/store"
	"github.com/Yornik/soiree/web"
)

const filesPassword = "correct horse battery staple"

// switchableBucket is a real bucket that can be told to stop answering, which
// a real one will not do on demand.
type switchableBucket struct {
	objectStore
	down bool
	// lie, when set, is the size Head reports instead of the true one.
	lie int64
}

var errBucketDown = errors.New("bucket unreachable")

func (b *switchableBucket) Head(ctx context.Context, key string) (int64, error) {
	if b.down {
		return 0, errBucketDown
	}
	size, err := b.objectStore.Head(ctx, key)
	if err == nil && b.lie != 0 {
		return b.lie, nil
	}
	return size, err
}

func (b *switchableBucket) Delete(ctx context.Context, key string) error {
	if b.down {
		return errBucketDown
	}
	return b.objectStore.Delete(ctx, key)
}

type filesFixture struct {
	*fixture
	files  *Attachments
	bucket *switchableBucket
	raw    *objstore.Store

	editor, viewer *http.Cookie
	line           store.BudgetItem
}

// newFilesFixture is the whole server with a real bucket behind it: the routes
// under test are mounted by routeAPI and guarded by the accounts middleware, so
// anything less would be testing a different arrangement from the one that
// ships.
func newFilesFixture(t *testing.T, maxBytes, totalBytes int64) *filesFixture {
	t.Helper()

	b := s3test.Get(t, startMinIO)
	raw, err := objstore.New(objstore.Config{
		Endpoint: b.Endpoint, Region: b.Region, Bucket: b.Name,
		AccessKeyID: b.AccessKeyID, SecretAccessKey: b.SecretAccessKey,
	})
	if err != nil {
		t.Fatal(err)
	}

	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)

	a := NewAuth(AuthOptions{Store: st, BaseURL: "https://soiree.example.test"})
	a.params = cheapParams
	a.background = func(fn func(context.Context)) { fn(context.Background()) }

	s, err := New(config.Config{EventName: "Ada's Leaving Do", Currency: "EUR", Locale: "en-US"}, web.FS(), WithStore(st))
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	bucket := &switchableBucket{objectStore: raw}
	files := NewAttachments(st, bucket, maxBytes, totalBytes, nil)

	f := &filesFixture{
		fixture: &fixture{h: s.WithAuth(a).WithAttachments(files).Handler(), a: a, store: st, mail: &fakeMailer{}},
		files:   files, bucket: bucket, raw: raw,
	}
	f.seed(t, "editor@example.test", store.RoleEditor, filesPassword)
	f.seed(t, "viewer@example.test", store.RoleViewer, filesPassword)
	f.editor = f.login(t, "editor@example.test", filesPassword)
	f.viewer = f.login(t, "viewer@example.test", filesPassword)

	f.line, err = st.CreateBudgetItem(t.Context(), store.BudgetItem{Item: "Catering", Unit: 4500, Qty: 40}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func decodeRec[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return out
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return decodeRec[apiError](t, rec).Error
}

// begin asks for an upload URL for a file on the fixture's budget line.
func (f *filesFixture) begin(t *testing.T, name, contentType string, size int) beginAttachmentResponse {
	t.Helper()
	rec := f.do(t, http.MethodPost, "/api/v1/attachments", map[string]any{
		"budgetItemId": f.line.ID, "name": name, "contentType": contentType, "size": size,
	}, f.editor)
	if rec.Code != http.StatusCreated {
		t.Fatalf("begin %q: %d %s", name, rec.Code, rec.Body)
	}
	return decodeRec[beginAttachmentResponse](t, rec)
}

// send is the browser's part: one PUT to the signed URL, with the headers it
// was told to use and nothing else.
func send(t *testing.T, up uploadJSON, body []byte) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), up.Method, up.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range up.Headers {
		req.Header.Set(name, value)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

// upload takes a file all the way to ready.
func (f *filesFixture) upload(t *testing.T, name, contentType string, body []byte) attachmentJSON {
	t.Helper()
	res := f.begin(t, name, contentType, len(body))
	if code := send(t, res.Upload, body); code != http.StatusOK {
		t.Fatalf("PUT %q = %d", name, code)
	}
	rec := f.do(t, http.MethodPost, "/api/v1/attachments/"+res.Attachment.ID.String()+"/complete", nil, f.editor)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete %q: %d %s", name, rec.Code, rec.Body)
	}
	t.Cleanup(func() { _ = f.raw.Delete(context.Background(), store.ObjectKey(res.Attachment.ID)) })
	return decodeRec[attachmentJSON](t, rec)
}

// fetch follows a content link the way a browser does: to this server with the
// session, then to wherever it is sent, without it.
func (f *filesFixture) fetch(t *testing.T, id uuid.UUID, query string, cookie *http.Cookie) (*http.Response, []byte) {
	t.Helper()
	rec := f.do(t, http.MethodGet, "/api/v1/attachments/"+id.String()+"/content"+query, nil, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("content: %d %s, want a 303", rec.Code, rec.Body)
	}
	res, err := http.Get(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

func (f *filesFixture) planFiles(t *testing.T) []attachmentJSON {
	t.Helper()
	rec := f.do(t, http.MethodGet, "/api/v1/plan", nil, f.viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("plan: %d", rec.Code)
	}
	return decodeRec[planJSON](t, rec).Attachments
}

func TestAFileGoesUpAndComesBackDown(t *testing.T) {
	f := newFilesFixture(t, 1<<20, 1<<24)
	body := []byte("%PDF-1.7 a caterer's quote, or near enough\n")

	res := f.begin(t, "Quote – café.pdf", "application/pdf", len(body))
	t.Cleanup(func() { _ = f.raw.Delete(context.Background(), store.ObjectKey(res.Attachment.ID)) })
	if code := send(t, res.Upload, body); code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}
	// Uploaded, and still nobody's: the bytes being in the bucket is not what
	// makes a file exist.
	if got := f.planFiles(t); len(got) != 0 {
		t.Fatalf("a file nobody has confirmed is in the plan: %+v", got)
	}
	rec := f.do(t, http.MethodPost, "/api/v1/attachments/"+res.Attachment.ID.String()+"/complete", nil, f.editor)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body)
	}
	file := decodeRec[attachmentJSON](t, rec)

	listed := f.planFiles(t)
	if len(listed) != 1 || listed[0].ID != file.ID || !listed[0].Viewable || listed[0].Size != int64(len(body)) {
		t.Fatalf("plan attachments = %+v", listed)
	}

	// A viewer can read it. Saved by default, under its real name.
	got, bytesBack := f.fetch(t, file.ID, "", f.viewer)
	if got.StatusCode != http.StatusOK || !bytes.Equal(bytesBack, body) {
		t.Fatalf("download = %d, %q", got.StatusCode, bytesBack)
	}
	cd := got.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, "filename*=UTF-8''Quote%20%E2%80%93%20caf%C3%A9.pdf") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("a download was served as %q", ct)
	}

	// A PDF may open in the browser when asked to.
	got, _ = f.fetch(t, file.ID, "?inline=1", f.viewer)
	if cd := got.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "inline;") {
		t.Errorf("inline PDF: Content-Disposition = %q", cd)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("inline PDF: Content-Type = %q", ct)
	}

	rec = f.do(t, http.MethodDelete, "/api/v1/attachments/"+file.ID.String(), nil, f.editor)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.raw.Head(t.Context(), store.ObjectKey(file.ID)); !errors.Is(err, objstore.ErrNotFound) {
		t.Errorf("the object outlived its row: %v", err)
	}
	// Removed at once, so nothing is left waiting for the sweeper.
	if left, _ := f.store.AttachmentGarbage(t.Context(), 10); len(left) != 0 {
		t.Errorf("the deletion queue after an immediate delete: %v", left)
	}
	if got := f.planFiles(t); len(got) != 0 {
		t.Fatalf("plan attachments after delete = %+v", got)
	}
}

// The reason downloads are decided here and signed into the URL. A page
// somebody uploaded must never be rendered because somebody asked nicely.
func TestAWebPageIsNeverServedAsOne(t *testing.T) {
	f := newFilesFixture(t, 1<<20, 1<<24)
	for _, c := range []struct{ name, contentType string }{
		{"page.html", "text/html"},
		{"drawing.svg", "image/svg+xml"},
		{"script.js", "text/javascript"},
	} {
		file := f.upload(t, c.name, c.contentType, []byte("<script>alert(1)</script>"))
		if file.Viewable {
			t.Errorf("%s is advertised as viewable", c.name)
		}
		got, _ := f.fetch(t, file.ID, "?inline=1", f.viewer)
		if cd := got.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
			t.Errorf("%s with ?inline=1: Content-Disposition = %q", c.name, cd)
		}
		if ct := got.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("%s with ?inline=1: Content-Type = %q", c.name, ct)
		}
	}
}

func TestAViewerDownloadsAndNothingElse(t *testing.T) {
	f := newFilesFixture(t, 1<<20, 1<<24)
	file := f.upload(t, "quote.pdf", "application/pdf", []byte("x"))
	path := "/api/v1/attachments/" + file.ID.String()

	for name, c := range map[string]struct {
		method, path string
		body         any
	}{
		"begin":    {http.MethodPost, "/api/v1/attachments", map[string]any{"budgetItemId": f.line.ID, "name": "a.pdf", "size": 1, "contentType": "application/pdf"}},
		"complete": {http.MethodPost, path + "/complete", nil},
		"delete":   {http.MethodDelete, path, nil},
	} {
		rec := f.do(t, c.method, c.path, c.body, f.viewer)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "read_only" {
			t.Errorf("viewer %s: %d %s, want 403 read_only", name, rec.Code, rec.Body)
		}
		if rec := f.do(t, c.method, c.path, c.body, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("nobody %s: %d, want 401", name, rec.Code)
		}
	}
	if rec := f.do(t, http.MethodGet, path+"/content", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("a download without a session: %d, want 401", rec.Code)
	}
	if got, _ := f.fetch(t, file.ID, "", f.viewer); got.StatusCode != http.StatusOK {
		t.Errorf("a viewer's download: %d", got.StatusCode)
	}
}

func TestWhatBeginRefuses(t *testing.T) {
	f := newFilesFixture(t, 1000, 1500)
	task, err := f.store.CreateTask(t.Context(), store.Task{Name: "Sign"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ok := func() map[string]any {
		return map[string]any{"budgetItemId": f.line.ID, "name": "a.pdf", "size": 10, "contentType": "application/pdf"}
	}

	for name, c := range map[string]struct {
		mutate func(map[string]any)
		status int
		code   string
	}{
		"no parent":                {func(m map[string]any) { delete(m, "budgetItemId") }, 400, errBadRequest},
		"two parents":              {func(m map[string]any) { m["taskId"] = task.ID }, 400, errBadRequest},
		"a parent not here":        {func(m map[string]any) { m["budgetItemId"] = uuid.New() }, 400, errBadRequest},
		"no name":                  {func(m map[string]any) { m["name"] = "  " }, 400, errBadRequest},
		"only a directory":         {func(m map[string]any) { m["name"] = "../../" }, 400, errBadRequest},
		"no size":                  {func(m map[string]any) { m["size"] = 0 }, 400, errBadRequest},
		"a field it does not know": {func(m map[string]any) { m["sizeBytes"] = 10 }, 400, errBadRequest},
		"over the file limit":      {func(m map[string]any) { m["size"] = 1001 }, 413, errTooLarge},
	} {
		body := ok()
		c.mutate(body)
		rec := f.do(t, http.MethodPost, "/api/v1/attachments", body, f.editor)
		if rec.Code != c.status || errorCode(t, rec) != c.code {
			t.Errorf("%s: %d %s, want %d %s", name, rec.Code, rec.Body, c.status, c.code)
		}
	}

	// The total: 1000 fits in 1500, a second 1000 does not — though the first
	// was never uploaded, let alone confirmed.
	body := ok()
	body["size"] = 1000
	if rec := f.do(t, http.MethodPost, "/api/v1/attachments", body, f.editor); rec.Code != http.StatusCreated {
		t.Fatalf("first 1000 of 1500: %d %s", rec.Code, rec.Body)
	}
	rec := f.do(t, http.MethodPost, "/api/v1/attachments", body, f.editor)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != errQuotaExceeded {
		t.Errorf("second 1000 of 1500: %d %s, want 409 %s", rec.Code, rec.Body, errQuotaExceeded)
	}
}

// The name a device sends is cleaned, and the file is on a task as readily as
// on a budget line.
func TestTheStoredNameIsTheCleanedOne(t *testing.T) {
	f := newFilesFixture(t, 1<<20, 1<<24)
	task, err := f.store.CreateTask(t.Context(), store.Task{Name: "Book the hall"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := f.do(t, http.MethodPost, "/api/v1/attachments", map[string]any{
		"taskId": task.ID, "name": `C:\Users\ada\Desktop\floor plan.pdf`, "size": 3, "contentType": "Application/PDF; charset=binary",
	}, f.editor)
	if rec.Code != http.StatusCreated {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body)
	}
	got := decodeRec[beginAttachmentResponse](t, rec)
	if got.Attachment.Name != "floor plan.pdf" || got.Attachment.ContentType != "application/pdf" {
		t.Errorf("stored as %q, %q", got.Attachment.Name, got.Attachment.ContentType)
	}
	if got.Attachment.TaskID == nil || *got.Attachment.TaskID != task.ID || got.Attachment.BudgetItemID != nil {
		t.Errorf("parent = %+v", got.Attachment)
	}
	if got.Upload.Method != http.MethodPut || got.Upload.Headers["Content-Type"] != "application/pdf" {
		t.Errorf("upload = %+v", got.Upload)
	}
	if until := time.Until(got.Upload.ExpiresAt); until < 14*time.Minute || until > 16*time.Minute {
		t.Errorf("the upload URL expires in %v", until)
	}
}

// Confirming is the server looking for itself. With nothing there, the row
// goes: it must not sit in the quota, and it must never become a file.
func TestAnUploadThatNeverArrivedIsDiscarded(t *testing.T) {
	f := newFilesFixture(t, 1<<20, 1<<24)
	res := f.begin(t, "quote.pdf", "application/pdf", 10)
	path := "/api/v1/attachments/" + res.Attachment.ID.String()

	rec := f.do(t, http.MethodPost, path+"/complete", nil, f.editor)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != errUploadIncomplete {
		t.Fatalf("complete with no upload: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.store.Attachment(t.Context(), res.Attachment.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the row is still there: %v", err)
	}
	// An unconfirmed file cannot be downloaded either, before or after.
	if rec := f.do(t, http.MethodGet, path+"/content", nil, f.editor); rec.Code != http.StatusNotFound {
		t.Errorf("content of a discarded upload: %d", rec.Code)
	}
}

func TestAnUnconfirmedFileCannotBeDownloaded(t *testing.T) {
	f := newFilesFixture(t, 1<<20, 1<<24)
	res := f.begin(t, "quote.pdf", "application/pdf", 1)
	if code := send(t, res.Upload, []byte("x")); code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}
	t.Cleanup(func() { _ = f.raw.Delete(context.Background(), store.ObjectKey(res.Attachment.ID)) })
	rec := f.do(t, http.MethodGet, "/api/v1/attachments/"+res.Attachment.ID.String()+"/content", nil, f.editor)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("content before complete: %d, want 404", rec.Code)
	}
}

// Not knowing is not the same as knowing it failed. A bucket that does not
// answer must cost nobody their upload: the same request works once it is back.
func TestABucketThatDoesNotAnswerLosesNothing(t *testing.T) {
	f := newFilesFixture(t, 1<<20, 1<<24)
	res := f.begin(t, "quote.pdf", "application/pdf", 1)
	if code := send(t, res.Upload, []byte("x")); code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}
	t.Cleanup(func() { _ = f.raw.Delete(context.Background(), store.ObjectKey(res.Attachment.ID)) })
	path := "/api/v1/attachments/" + res.Attachment.ID.String() + "/complete"

	f.bucket.down = true
	rec := f.do(t, http.MethodPost, path, nil, f.editor)
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != errStorageUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("complete while the bucket is down: %d %s", rec.Code, rec.Body)
	}
	if row, err := f.store.Attachment(t.Context(), res.Attachment.ID); err != nil || row.Status != store.AttachmentUploading {
		t.Fatalf("the row after a failed check: %+v, %v", row, err)
	}

	f.bucket.down = false
	if rec := f.do(t, http.MethodPost, path, nil, f.editor); rec.Code != http.StatusOK {
		t.Fatalf("complete once it is back: %d %s", rec.Code, rec.Body)
	}
	// And again: a retry after a lost response is the same answer, not an error.
	if rec := f.do(t, http.MethodPost, path, nil, f.editor); rec.Code != http.StatusOK {
		t.Fatalf("complete a second time: %d %s", rec.Code, rec.Body)
	}
}

// The signature is supposed to make this impossible. If a bucket lets it
// happen anyway, the quota was computed from a number that is not true, and the
// file has to go rather than be trusted.
func TestAFileOfTheWrongSizeIsDiscarded(t *testing.T) {
	f := newFilesFixture(t, 1<<20, 1<<24)
	res := f.begin(t, "quote.pdf", "application/pdf", 1)
	if code := send(t, res.Upload, []byte("x")); code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}
	f.bucket.lie = 999_999

	rec := f.do(t, http.MethodPost, "/api/v1/attachments/"+res.Attachment.ID.String()+"/complete", nil, f.editor)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != errUploadIncomplete {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.store.Attachment(t.Context(), res.Attachment.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the row is still there: %v", err)
	}
	if _, err := f.raw.Head(t.Context(), store.ObjectKey(res.Attachment.ID)); !errors.Is(err, objstore.ErrNotFound) {
		t.Errorf("the object is still there: %v", err)
	}
}

// Deleting a budget line takes its files' rows with it, inside the database.
// Nothing tells this server which. The queue the trigger wrote is how the
// objects still get removed — including after a spell when the bucket was down.
func TestTheSweeperRemovesWhatACascadeLeftBehind(t *testing.T) {
	f := newFilesFixture(t, 1<<20, 1<<24)
	file := f.upload(t, "quote.pdf", "application/pdf", []byte("x"))
	key := store.ObjectKey(file.ID)

	rec := f.do(t, http.MethodDelete, "/api/v1/budget-items/"+f.line.ID.String()+"?revision=1", nil, f.editor)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete the line: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.raw.Head(t.Context(), key); err != nil {
		t.Fatalf("the object should still be there, waiting: %v", err)
	}

	f.bucket.down = true
	f.files.sweepOnce(t.Context())
	if queued, _ := f.store.AttachmentGarbage(t.Context(), 10); len(queued) != 1 || queued[0] != key {
		t.Fatalf("after a sweep with the bucket down, the queue is %v; the key must wait", queued)
	}

	f.bucket.down = false
	f.files.sweepOnce(t.Context())
	if _, err := f.raw.Head(t.Context(), key); !errors.Is(err, objstore.ErrNotFound) {
		t.Errorf("the object survived the sweep: %v", err)
	}
	if queued, _ := f.store.AttachmentGarbage(t.Context(), 10); len(queued) != 0 {
		t.Errorf("the queue after the sweep: %v", queued)
	}
}

// A deployment with no bucket: the routes are not there, and the plan still
// has the collection, empty, so a client needs no second shape.
func TestWithNoBucketThereAreNoFileRoutes(t *testing.T) {
	f := newPushFixture(t)
	f.seed(t, "editor@example.test", store.RoleEditor, filesPassword)
	cookie := f.login(t, "editor@example.test", filesPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/attachments", map[string]any{"name": "a.pdf"}, cookie)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /attachments with no bucket: %d, want 404", rec.Code)
	}
	rec = f.do(t, http.MethodGet, "/api/v1/plan", nil, cookie)
	if !strings.Contains(rec.Body.String(), `"attachments":[]`) {
		t.Errorf("the plan has no empty attachments collection: %s", rec.Body)
	}
}

func TestCleanFileName(t *testing.T) {
	long := strings.Repeat("é", 300)
	for in, want := range map[string]string{
		"quote.pdf":                      "quote.pdf",
		"  spaced out .pdf  ":            "spaced out .pdf",
		"/etc/passwd":                    "passwd",
		`..\..\windows\system.ini`:       "system.ini",
		"tab\tand\nnewline.pdf":          "tabandnewline.pdf",
		"invoice\u202Efdp.exe":           "invoicefdp.exe", // the override goes; what is left reads as what it is
		"Kuitansi – Bu Dewi (lunas).jpg": "Kuitansi – Bu Dewi (lunas).jpg",
		long + ".pdf":                    strings.Repeat("é", 196) + ".pdf",
	} {
		got, ok := cleanFileName(in)
		if !ok || got != want {
			t.Errorf("cleanFileName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "   ", "/", "..", "dir/", "\x00", "\xff\xfe"} {
		if got, ok := cleanFileName(in); ok {
			t.Errorf("cleanFileName(%q) = %q, want a refusal", in, got)
		}
	}
}

func TestContentDisposition(t *testing.T) {
	for _, c := range []struct{ mode, name, want string }{
		{"attachment", "quote.pdf", `attachment; filename="quote.pdf"; filename*=UTF-8''quote.pdf`},
		{"inline", "floor plan.pdf", `inline; filename="floor plan.pdf"; filename*=UTF-8''floor%20plan.pdf`},
		{"attachment", `a"b\c.pdf`, `attachment; filename="a_b_c.pdf"; filename*=UTF-8''a%22b%5Cc.pdf`},
		{"attachment", "café.pdf", `attachment; filename="caf_.pdf"; filename*=UTF-8''caf%C3%A9.pdf`},
	} {
		if got := contentDisposition(c.mode, c.name); got != c.want {
			t.Errorf("contentDisposition(%q, %q)\n got %s\nwant %s", c.mode, c.name, got, c.want)
		}
	}
}

func TestCleanContentType(t *testing.T) {
	for in, want := range map[string]string{
		"application/pdf":               "application/pdf",
		"Image/JPEG":                    "image/jpeg",
		"text/html; charset=utf-8":      "text/html",
		"":                              "application/octet-stream",
		"nonsense":                      "application/octet-stream",
		"a/" + strings.Repeat("b", 200): "application/octet-stream",
	} {
		if got := cleanContentType(in); got != want {
			t.Errorf("cleanContentType(%q) = %q, want %q", in, got, want)
		}
	}
}
