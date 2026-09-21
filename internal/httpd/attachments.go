package httpd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/objstore"
	"github.com/Yornik/soiree/internal/store"
)

// Attachments: files on budget lines and tasks.
//
// No file passes through this server. A browser asks for somewhere to put one
// and is handed a URL, signed for exactly that many bytes of exactly that type;
// it uploads to the bucket itself, and then says so. A download is a redirect
// to a URL that lives for a minute. What this file does is decide — who may,
// how much, under what name, served how — and keep the database's account of
// it true.
//
// The order of an upload, and what each step can leave behind:
//
//	POST /attachments                a row, 'uploading'. Counts toward the
//	                                 quota at once; invisible to everyone.
//	PUT  <signed url>                the browser and the bucket. Not here.
//	POST /attachments/{id}/complete  the server looks for itself. Right size:
//	                                 'ready', recorded, announced. Anything
//	                                 else: the row and the object both go.
//
// A browser that vanishes after the first step leaves a row the sweeper takes
// back within the hour. One that vanishes after the second leaves an object as
// well, which the row's deletion queues for removal.

const (
	// uploadURLLifetime is how long somebody has to finish sending a file. It
	// has to cover the slowest realistic upload of the largest allowed file,
	// because the signature is checked when the request starts and a phone on a
	// poor connection may take most of this to get there.
	uploadURLLifetime = 15 * time.Minute

	// downloadURLLifetime only has to outlast one redirect. It is the window in
	// which a copied link works for somebody who is not signed in, so it is as
	// short as a slow connection allows.
	downloadURLLifetime = time.Minute

	// staleUploadAge is when an unconfirmed upload is given up on. Comfortably
	// longer than uploadURLLifetime, so the sweeper can never take a row whose
	// URL still works.
	staleUploadAge = time.Hour

	// garbageBatch bounds one sweep's deletions, so a backlog after an outage
	// is worked through over several ticks rather than in one long burst.
	garbageBatch = 200

	maxFileNameRunes = 200
	maxContentType   = 100

	errQuotaExceeded      = "quota_exceeded"
	errUploadIncomplete   = "upload_incomplete"
	errStorageUnavailable = "storage_unavailable"
)

// viewableTypes may open in the browser instead of being saved. Everything
// else is served as a download, whatever it claims to be.
//
// Short on purpose. The file comes from the bucket's origin, not this one, so
// even a hostile page could not reach a session here — but "a web page somebody
// uploaded, shown to you from a link in the planner you trust" is its own kind
// of problem, and nobody needs a receipt in HTML. SVG is absent because it is
// a document format that runs script, not an image format.
var viewableTypes = map[string]bool{
	"image/jpeg":      true,
	"image/png":       true,
	"image/webp":      true,
	"image/gif":       true,
	"application/pdf": true,
}

// objectStore is the bucket, as far as this file needs one. *objstore.Store is
// the implementation; the interface exists so a test can stand in a bucket
// that is unreachable, which a real one will not be on demand.
type objectStore interface {
	// Origin is where the browser is sent, which the Content-Security-Policy
	// has to name for an upload to be allowed to start. Asked of the bucket
	// that signs the address rather than derived again from the endpoint, so
	// the policy and the signature cannot come to disagree.
	Origin() string
	PresignPut(key string, size int64, contentType string, ttl time.Duration) objstore.Upload
	PresignGet(key string, ttl time.Duration, disposition, contentType string) string
	Head(ctx context.Context, key string) (int64, error)
	Delete(ctx context.Context, key string) error
}

// Attachments is the file surface: four routes and a sweeper.
type Attachments struct {
	store      *store.Store
	bucket     objectStore
	maxBytes   int64
	totalBytes int64
	log        *slog.Logger
}

// NewAttachments builds the surface. maxBytes is the most one file may be and
// totalBytes the most all of them may be together.
func NewAttachments(st *store.Store, bucket objectStore, maxBytes, totalBytes int64, log *slog.Logger) *Attachments {
	if log == nil {
		log = slog.Default()
	}
	return &Attachments{store: st, bucket: bucket, maxBytes: maxBytes, totalBytes: totalBytes, log: log}
}

// WithAttachments turns files on. Optional for the reason WithAuth is: a
// deployment with no bucket is a supported one, and then these routes do not
// exist at all — a request for one gets the API's ordinary 404.
func (s *Server) WithAttachments(a *Attachments) *Server {
	s.files = a
	return s
}

// route mounts the four routes on the guarded API mux. That placement is the
// authorisation: RequireWrite lets any session GET and only an editor or an
// admin do anything else, which is exactly the rule for files.
func (a *Attachments) route(api *http.ServeMux) {
	api.HandleFunc("POST /attachments", a.handleBegin)
	api.HandleFunc("POST /attachments/{id}/complete", a.handleComplete)
	api.HandleFunc("GET /attachments/{id}/content", a.handleContent)
	api.HandleFunc("DELETE /attachments/{id}", a.handleDelete)
}

// attachmentJSON is a file as the API describes it.
type attachmentJSON struct {
	ID           uuid.UUID  `json:"id"`
	BudgetItemID *uuid.UUID `json:"budgetItemId"`
	TaskID       *uuid.UUID `json:"taskId"`
	Name         string     `json:"name"`
	ContentType  string     `json:"contentType"`
	Size         int64      `json:"size"`
	// Viewable says whether `?inline=1` will be honoured for this file. The
	// list that decides lives here, once, rather than here and in the page.
	Viewable   bool       `json:"viewable"`
	UploadedBy *uuid.UUID `json:"uploadedBy"`
	CreatedAt  time.Time  `json:"createdAt"`
}

func encodeAttachment(a store.Attachment) attachmentJSON {
	return attachmentJSON{
		ID:           a.ID,
		BudgetItemID: a.BudgetItemID,
		TaskID:       a.TaskID,
		Name:         a.Name,
		ContentType:  a.ContentType,
		Size:         a.Size,
		Viewable:     viewableTypes[a.ContentType],
		UploadedBy:   a.UploadedBy,
		CreatedAt:    a.CreatedAt,
	}
}

type beginAttachmentRequest struct {
	BudgetItemID *uuid.UUID `json:"budgetItemId"`
	TaskID       *uuid.UUID `json:"taskId"`
	Name         string     `json:"name"`
	Size         int64      `json:"size"`
	ContentType  string     `json:"contentType"`
}

type uploadJSON struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

type beginAttachmentResponse struct {
	Attachment attachmentJSON `json:"attachment"`
	Upload     uploadJSON     `json:"upload"`
}

func (a *Attachments) handleBegin(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req beginAttachmentRequest
	// Strict, like every other write on this API: a misspelt field is a 400
	// rather than a file attached to nothing.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, errBadRequest, "the request body is empty")
			return
		}
		writeError(w, http.StatusBadRequest, errBadRequest, "could not read the request body: "+err.Error())
		return
	}
	// A token read rather than More(), for the reason decodeBody gives.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, errBadRequest, "the request body must be a single JSON object")
		return
	}

	if (req.BudgetItemID == nil) == (req.TaskID == nil) {
		writeError(w, http.StatusBadRequest, errBadRequest, "name exactly one of budgetItemId and taskId")
		return
	}
	name, ok := cleanFileName(req.Name)
	if !ok {
		writeError(w, http.StatusBadRequest, errBadRequest, "name must be a file name")
		return
	}
	if req.Size <= 0 {
		writeError(w, http.StatusBadRequest, errBadRequest, "size must be the file's length in bytes, above zero")
		return
	}
	if req.Size > a.maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errTooLarge, "that file is larger than this deployment allows")
		return
	}

	in := store.Attachment{
		BudgetItemID: req.BudgetItemID,
		TaskID:       req.TaskID,
		Name:         name,
		ContentType:  cleanContentType(req.ContentType),
		Size:         req.Size,
	}
	if u, ok := UserFrom(r.Context()); ok {
		in.UploadedBy = &u.ID
	}

	row, err := a.store.BeginAttachment(r.Context(), in, a.totalBytes)
	switch {
	case errors.Is(err, store.ErrQuotaExceeded):
		writeError(w, http.StatusConflict, errQuotaExceeded, "there is no room left for a file that size")
		return
	case err != nil:
		// A parent that does not exist is a foreign-key violation, which
		// constraintError already turns into a 400.
		if status, code, message, ok := constraintError(err); ok {
			writeError(w, status, code, message)
			return
		}
		writeInternal(w, r, err)
		return
	}

	up := a.bucket.PresignPut(store.ObjectKey(row.ID), row.Size, row.ContentType, uploadURLLifetime)
	writeJSON(w, http.StatusCreated, beginAttachmentResponse{
		Attachment: encodeAttachment(row),
		Upload: uploadJSON{
			URL:       up.URL,
			Method:    http.MethodPut,
			Headers:   up.Headers,
			ExpiresAt: time.Now().Add(uploadURLLifetime).UTC(),
		},
	})
}

// handleComplete is where the server stops taking the browser's word for it.
func (a *Attachments) handleComplete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	row, err := a.store.Attachment(r.Context(), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if row.Status == store.AttachmentReady {
		// A retry after a dropped response. The answer is the same one.
		writeJSON(w, http.StatusOK, encodeAttachment(row))
		return
	}

	key := store.ObjectKey(row.ID)
	size, err := a.bucket.Head(r.Context(), key)
	switch {
	case errors.Is(err, objstore.ErrNotFound):
		a.discard(r.Context(), row)
		writeError(w, http.StatusConflict, errUploadIncomplete, "no file arrived; start the upload again")
		return
	case err != nil:
		// Not knowing is not the same as knowing it failed. The row stays, so
		// the same request can simply be made again once the bucket answers.
		a.log.Error("could not check an uploaded file", "attachment", row.ID, "err", err)
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, errStorageUnavailable, "the file store did not answer; try again")
		return
	case size != row.Size:
		// The signature should have made this impossible. If it happened
		// anyway, the quota was computed from a number that is not true.
		a.log.Warn("an uploaded file is not the size that was declared",
			"attachment", row.ID, "declared", row.Size, "stored", size)
		a.discard(r.Context(), row)
		writeError(w, http.StatusConflict, errUploadIncomplete, "the file that arrived is not the size that was declared")
		return
	}

	row, err = a.store.CompleteAttachment(r.Context(), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, encodeAttachment(row))
}

// handleContent sends the browser to the file.
//
// A redirect rather than a URL in a JSON body, so that an ordinary link works:
// the browser follows it with the session cookie, is checked here, and lands on
// the bucket. Downloads therefore need no script and no CORS.
func (a *Attachments) handleContent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	row, err := a.store.Attachment(r.Context(), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if row.Status != store.AttachmentReady {
		// An unconfirmed upload does not exist yet, to anybody.
		writeError(w, http.StatusNotFound, errNotFound, "no such row")
		return
	}

	// What it is served as is decided here, from the allow-list, and signed
	// into the URL. The stored type is never trusted to pick its own
	// treatment.
	mode, contentType := "attachment", "application/octet-stream"
	if r.URL.Query().Get("inline") == "1" && viewableTypes[row.ContentType] {
		mode, contentType = "inline", row.ContentType
	}
	location := a.bucket.PresignGet(store.ObjectKey(row.ID), downloadURLLifetime,
		contentDisposition(mode, row.Name), contentType)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (a *Attachments) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	row, err := a.store.DeleteAttachment(r.Context(), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.removeObject(r.Context(), store.ObjectKey(row.ID))
	w.WriteHeader(http.StatusNoContent)
}

// discard removes a row whose upload came to nothing, and whatever arrived.
func (a *Attachments) discard(ctx context.Context, row store.Attachment) {
	if _, err := a.store.DeleteAttachment(ctx, row.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		a.log.Error("could not remove a failed upload", "attachment", row.ID, "err", err)
		return
	}
	a.removeObject(ctx, store.ObjectKey(row.ID))
}

// removeObject deletes an object now rather than at the next sweep. Failure is
// not an error for the caller: the row's deletion already queued this key, in
// the same transaction, and the sweeper will come back for it.
func (a *Attachments) removeObject(ctx context.Context, key string) {
	if err := a.bucket.Delete(ctx, key); err != nil {
		a.log.Warn("could not delete a file yet; it stays queued", "key", key, "err", err)
		return
	}
	if err := a.store.ForgetAttachmentGarbage(ctx, key); err != nil {
		a.log.Warn("could not clear a deleted file from the queue", "key", key, "err", err)
	}
}

func (a *Attachments) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound, "no such row")
		return
	}
	writeInternal(w, r, err)
}

// Sweep takes back abandoned uploads and removes queued objects, once at start
// and then hourly, until ctx is done.
//
// At start as well as on the tick, unlike the session sweeper: that one deletes
// rows nothing can use, whereas these are bytes in a bucket somebody pays for,
// and a deployment that restarts nightly would otherwise never reach its first
// tick. Safe on every replica, because deleting an object twice is a no-op.
func (a *Attachments) Sweep(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		a.sweepOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *Attachments) sweepOnce(ctx context.Context) {
	if n, err := a.store.SweepStaleUploads(ctx, staleUploadAge); err != nil {
		a.log.Error("could not sweep abandoned uploads", "err", err)
	} else if n > 0 {
		a.log.Info("abandoned uploads removed", "count", n)
	}

	keys, err := a.store.AttachmentGarbage(ctx, garbageBatch)
	if err != nil {
		a.log.Error("could not read the file deletion queue", "err", err)
		return
	}
	for _, key := range keys {
		if ctx.Err() != nil {
			return
		}
		a.removeObject(ctx, key)
	}
}

// cleanFileName turns what a device called a file into something safe to show
// and to offer back. It is never used as a path — the object is named by the
// row's id — so this is about what people see, and about one trick in
// particular.
//
// Dropped: any directory part, in either slash; control characters; and the
// Unicode direction overrides (U+202E and its relatives), which exist to make
// a name that ends in "fdp.exe" display as one that ends in "exe.pdf". They
// are written as code points here, and nowhere in this file as themselves, for
// the same reason. Refused: a name with nothing left, and bytes that are not
// text.
func cleanFileName(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	if i := strings.LastIndexAny(raw, `/\`); i >= 0 {
		raw = raw[i+1:]
	}
	name := strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r), isDirectionOverride(r):
			return -1
		}
		return r
	}, raw))
	if name == "" || name == "." || name == ".." {
		return "", false
	}

	if runes := []rune(name); len(runes) > maxFileNameRunes {
		// Keep the extension: it is how the person's device decides what to
		// open the download with.
		ext := ""
		if dot := strings.LastIndex(name, "."); dot > 0 {
			if e := []rune(name[dot:]); len(e) <= 12 {
				ext = string(e)
			}
		}
		keep := maxFileNameRunes - len([]rune(ext))
		name = strings.TrimSpace(string(runes[:keep])) + ext
	}
	return name, true
}

func isDirectionOverride(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200E || r == 0x200F
}

// cleanContentType keeps the media type and drops everything else. A browser
// sends an empty string for a file it does not recognise, which is not an
// error.
func cleanContentType(raw string) string {
	const unknown = "application/octet-stream"
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil || len(mediaType) > maxContentType || !strings.Contains(mediaType, "/") {
		return unknown
	}
	return mediaType
}

// contentDisposition names the download, twice, as RFC 6266 asks.
//
// `filename` is the fallback every client understands and may hold only plain
// ASCII, so anything else becomes an underscore there. `filename*` carries the
// real name, percent-encoded as UTF-8, and a client that understands it prefers
// it. Both are needed: a name in Indonesian or Dutch is the ordinary case here,
// not the exotic one.
func contentDisposition(mode, name string) string {
	var ascii, encoded strings.Builder
	for _, r := range name {
		if r < 0x20 || r >= 0x7F || r == '"' || r == '\\' {
			ascii.WriteByte('_')
		} else {
			ascii.WriteRune(r)
		}
	}
	const upperHex = "0123456789ABCDEF"
	for i := 0; i < len(name); i++ {
		c := name[i]
		// RFC 5987 attr-char.
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			encoded.WriteByte(c)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(upperHex[c>>4])
		encoded.WriteByte(upperHex[c&0x0F])
	}
	return mode + `; filename="` + ascii.String() + `"; filename*=UTF-8''` + encoded.String()
}
