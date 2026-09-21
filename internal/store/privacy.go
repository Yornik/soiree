package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Data protection — roadmap item 10 in docs/architecture.md.
//
// This schema holds names and financial obligations for people who never
// signed up for anything: a sponsor is entered *by somebody else*, and the
// person it describes may never see this application. Export and erasure are
// therefore obligations rather than paperwork, and they are implemented at the
// store layer because that is the only place that can see all of it at once.
//
// The awkward part is that "person" is not a table here. One human being can
// appear as a `sponsors` row, as a `users` account, as a string in
// `tasks.owner`, as a string in `budget_items.vendor`, and as a name written
// into prose in a note. Only the first two are keys. The rest is free text,
// and free text cannot be joined — which is the honest limit on how complete
// either operation can be. See SubjectRef and SubjectExport.Caveats.
//
// The second awkward part is that erasure must not change what the event
// cost. Budget lines carry money in their own columns and carry attribution in
// a join table, so removing a person never has to remove a line — and this
// file never does. See EraseSubject.
//
// The third awkward part is age, and it is why nothing calls any of this yet.
// It was written against the schema as it stood at migration 0005, and every
// table added since that holds personal data was invisible to it.
//
// `change_log` (0007) no longer is. It records what each change altered, field
// by field, so it keeps names, owner cells, prose and an account's address as
// they stood before somebody corrected them, and both erasure and the purge
// reach it now. That took a migration rather than a statement, because the
// table refuses every UPDATE and DELETE by trigger: migration 0013 is the
// decision that an erasure outranks the evidence, and it is deliberately
// narrow. See redactLog.
//
// The rest is still invisible: `sessions` and `password_tokens` (0008);
// `push_subscriptions` (0010), one row per browser that accepted reminders;
// `passkey_credentials` (0011), each with the label its device was given; and
// `attachments` (0012), which records what a person's device called a file. So
// an export assembled here is short by all of them and says so, an anonymised
// account keeps its sessions, passkeys and subscriptions, and a file keeps the
// name it was uploaded under. Reading those tables is ordinary work in
// collectSubject, with no decision left to make first. Roadmap item 10 in
// docs/architecture.md carries the same list.

// Tombstone replaces a name in a field whose entire content was that name, and
// stands in for a name struck out of prose. It is deliberately not the empty
// string: "nobody owns this task" and "the person who owned this task has been
// erased" are different facts, and flattening them loses the second one.
const Tombstone = "(erased)"

// erasedEmailDomain is `.invalid`, reserved by RFC 2606 and guaranteed never
// to resolve. An anonymised account has to keep *some* address because the
// column is NOT NULL and uniquely indexed; it must not keep one that could
// ever deliver mail to a person.
const erasedEmailDomain = "invalid"

// ErrNoSubject reports an export or erasure that names nobody. Refused rather
// than run: a SubjectRef with nothing in it describes every person and none of
// them, and the failure mode of guessing is either an export of the whole
// ledger or an erasure of somebody else.
var ErrNoSubject = errors.New("store: no subject named")

// SubjectRef names the person to export or erase.
//
// SponsorID and UserID are the only two reliable handles: they are primary
// keys. Aliases exist because the rest of the personal data in this schema is
// free text, and covers the names a person is written down as that the keys
// cannot reach — a nickname in an owner cell, a maiden name in a note.
//
// Note what is *not* here and cannot be: a link between a sponsor record and
// an account. The schema has none — `sponsors` has no email and `users` has no
// name — so if the person both sponsors the event and has a login, the caller
// has to supply both ids. Matching them up by name would be a guess, and a
// guess that erases the wrong account is worse than an incomplete one.
type SubjectRef struct {
	// SponsorID is their row in `sponsors`, if somebody entered them as one.
	SponsorID *uuid.UUID `json:"sponsorId,omitempty"`
	// UserID is their account, if they have one.
	UserID *uuid.UUID `json:"userId,omitempty"`
	// Aliases are further names to match free text on, beyond the sponsor's
	// code and name and the account's address, which are added automatically.
	Aliases []string `json:"aliases,omitempty"`
}

// Match reasons. Every row in an export carries at least one, so the person
// reading it can see *why* this application holds that row about them — which
// is half of what a subject access request is actually asking.
const (
	MatchSponsor   = "sponsor"   // attributed to them in budget_item_sponsors
	MatchVendor    = "vendor"    // their name is the vendor on the line
	MatchOwner     = "owner"     // their name is in tasks.owner
	MatchProse     = "prose"     // their name is written into free text
	MatchUpdatedBy = "updatedBy" // they were the last to change the row
)

// Matched is a row together with the reasons it is the subject's.
type Matched[T any] struct {
	On  []string `json:"matchedOn"`
	Row T        `json:"row"`
}

// ExportedUser is the account as handed to the person it belongs to.
//
// It is deliberately not User: the Argon2id hash is data held about them, but
// handing it over does nothing for them and hands an offline cracking target
// to anyone who intercepts the export. That a password exists is disclosed;
// the hash is not.
type ExportedUser struct {
	ID          uuid.UUID  `json:"id"`
	Email       string     `json:"email"`
	Role        Role       `json:"role"`
	HasPassword bool       `json:"hasPassword"`
	Status      UserStatus `json:"status"`
	CreatedBy   *uuid.UUID `json:"createdBy,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	Revision    int64      `json:"revision"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// SubjectExport is everything this deployment holds about one person.
//
// The entity structs marshal with their Go field names, because they carry no
// `json` tags: the wire shape of the API belongs to the handler layer, not
// here. What this type fixes is the *contents* — which rows, and why.
type SubjectExport struct {
	GeneratedAt time.Time  `json:"generatedAt"`
	Subject     SubjectRef `json:"subject"`
	// Aliases is what the free-text matching actually ran against, expanded
	// from the sponsor and account records. Included because a person reading
	// their export should be able to see that "Ada L." was never searched for.
	Aliases []string `json:"aliases"`

	Sponsor *Sponsor        `json:"sponsor,omitempty"`
	User    *ExportedUser   `json:"user,omitempty"`
	UIPrefs json.RawMessage `json:"uiPrefs,omitempty"`

	BudgetItems []Matched[BudgetItem]     `json:"budgetItems"`
	Tasks       []Matched[Task]           `json:"tasks"`
	Notes       []Matched[Note]           `json:"notes"`
	Programme   []Matched[ProgrammeEntry] `json:"programme"`

	// Caveats says what this export could not reliably find. An export that
	// silently claims to be complete when the schema cannot make it complete
	// is worse than one that admits the gap.
	Caveats []string `json:"caveats"`
}

// JSON renders the export as the document handed to the person. Indented,
// because a human reads it.
func (e SubjectExport) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal subject export: %w", err)
	}
	return b, nil
}

// ExportSubject gathers everything held about one person.
//
// It runs in one repeatable-read transaction for the same reason LoadPlan
// does: an export assembled from a moving database can contain a budget line
// whose sponsor attribution it also reports as absent, and an export that
// contradicts itself is not evidence of anything.
//
// It deliberately does *not* include the accounts this person created, nor the
// names of the other sponsors on a line they share. Those are other people's
// data, and a subject access request is not a route to it. Co-sponsor ids
// survive on the budget lines — they are opaque and the caller needs them to
// see that a line is split — but nothing here resolves them to a name.
func (s *Store) ExportSubject(ctx context.Context, ref SubjectRef) (SubjectExport, error) {
	if !ref.names() {
		return SubjectExport{}, ErrNoSubject
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return SubjectExport{}, fmt.Errorf("begin subject export: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	sub, err := resolveSubject(ctx, tx, ref)
	if err != nil {
		return SubjectExport{}, err
	}

	out := SubjectExport{
		GeneratedAt: time.Now().UTC(),
		Subject:     ref,
		Aliases:     sub.aliases,
		Caveats:     sub.caveats(),
	}
	if sub.sponsor != nil {
		sponsor := *sub.sponsor
		sponsor.UpdatedBy = sub.ownActor(sponsor.UpdatedBy)
		out.Sponsor = &sponsor
	}
	if sub.user != nil {
		out.User = &ExportedUser{
			ID:          sub.user.ID,
			Email:       sub.user.Email,
			Role:        sub.user.Role,
			HasPassword: sub.user.PasswordHash != nil,
			Status:      sub.user.Status,
			CreatedBy:   sub.ownActor(sub.user.CreatedBy),
			CreatedAt:   sub.user.CreatedAt,
			Revision:    sub.user.Revision,
			UpdatedAt:   sub.user.UpdatedAt,
		}
		prefs, err := queryScalars[json.RawMessage](ctx, tx, "user_ui_prefs",
			`SELECT prefs FROM user_ui_prefs WHERE user_id = $1`, sub.user.ID)
		if err != nil {
			return SubjectExport{}, err
		}
		if len(prefs) == 1 {
			out.UIPrefs = prefs[0]
		}
	}

	rows, err := collectSubject(ctx, tx, sub)
	if err != nil {
		return SubjectExport{}, err
	}
	// An `updated_by` pointing at somebody else is a fact about that somebody
	// else, and it rides along on rows that are in here for an unrelated
	// reason — Grace last edited the catering line that Ada co-sponsors. The
	// co-sponsor ids stay, because without them the line stops reading as
	// shared and the subject cannot see that the obligation is split; an
	// editor's id carries nothing for the subject at all, so it goes.
	for i := range rows.budgetItems {
		rows.budgetItems[i].Row.UpdatedBy = sub.ownActor(rows.budgetItems[i].Row.UpdatedBy)
	}
	for i := range rows.tasks {
		rows.tasks[i].Row.UpdatedBy = sub.ownActor(rows.tasks[i].Row.UpdatedBy)
	}
	out.BudgetItems = rows.budgetItems
	out.Tasks = rows.tasks
	out.Notes = rows.notes
	out.Programme = rows.programme

	if err := tx.Commit(ctx); err != nil {
		return SubjectExport{}, fmt.Errorf("commit subject export: %w", err)
	}
	return out, nil
}

// ErasureMode picks between the two ways of honouring an erasure request.
type ErasureMode string

const (
	// EraseAnonymise keeps every row and destroys the identifying fields in
	// it. This is the zero value, and the right default for anybody woven into
	// shared costs — see EraseSubject for why.
	EraseAnonymise ErasureMode = "anonymise"
	// EraseDelete removes the person's own rows outright. Their budget lines
	// still survive with their amounts; what goes is the attribution.
	EraseDelete ErasureMode = "delete"
)

// ErasureRequest is one person's erasure, and how to carry it out.
type ErasureRequest struct {
	Subject SubjectRef  `json:"subject"`
	Mode    ErasureMode `json:"mode,omitempty"`
	// KeepProse leaves free text alone.
	//
	// The default is to strike the name out of notes, because a note reading
	// "Ada is chasing the caterer" is exactly the personal data an erasure was
	// asked about. It is off by a flag rather than on by one because leaving it
	// is the unsafe choice, and the unsafe choice should have to be typed.
	//
	// It exists at all because prose redaction is the lossy part: matching is
	// whole-word and case-insensitive, so it cannot know that "Ada" in "Ada
	// will confirm" is the person and "Nevada" is not a person at all. It
	// handles that case correctly; it cannot handle a nickname it was never
	// told about.
	KeepProse bool `json:"keepProse,omitempty"`
}

// ErasureResult reports what this call changed. A second call over the same
// subject reports nothing, because there is nothing left to change — that is
// what makes erasure idempotent rather than what makes it incomplete.
type ErasureResult struct {
	Mode ErasureMode `json:"mode"`
	// Aliases is what was matched on, so the operator can see whether the
	// erasure looked for the right names. It is the one place the person's
	// name survives the call, so it belongs in a reply to whoever asked and
	// not in an application log.
	Aliases []string `json:"aliases"`

	SponsorAnonymised bool `json:"sponsorAnonymised"`
	SponsorDeleted    bool `json:"sponsorDeleted"`
	AccountAnonymised bool `json:"accountAnonymised"`
	AccountDeleted    bool `json:"accountDeleted"`
	UIPrefsDeleted    bool `json:"uiPrefsDeleted"`

	TaskOwnersRedacted int `json:"taskOwnersRedacted"`
	VendorsRedacted    int `json:"vendorsRedacted"`
	ProseRedacted      int `json:"proseRedacted"`
	// HistoryRedacted counts the change_log entries this call struck the
	// person out of. It is reported separately from the rows because it is the
	// part that took a migration to make possible at all.
	HistoryRedacted int `json:"historyRedacted"`

	// BudgetItemsRetained counts the lines that named this person and were
	// kept, amounts untouched. It is the number that says the books still add
	// up, and it belongs in whatever record is kept of honouring the request.
	BudgetItemsRetained int `json:"budgetItemsRetained"`
}

// EraseSubject erases one person while leaving the books correct.
//
// # Why anonymise rather than delete
//
// Deleting a sponsor is supported and is what EraseDelete does, but it is not
// the default, because of what the attribution means. `budget_item_sponsors`
// is not decoration: the browser divides each line by the number of sponsors
// on it (see sponsorShare and renderSplit in web/src/app.js, and the
// `split_evenly` setting the schema carries for it). Remove Ada from a line
// she shared with Grace and Linus and the same €900 that was three ways is now
// two — Grace and Linus each silently owe more than they agreed to. The grand
// total is unchanged, but what each remaining person owes is not, and that is
// the number people argue about.
//
// Anonymising keeps the row and the attribution and destroys the name. The
// line is still split three ways, one of the three is now nameless, and nobody
// else's obligation moved. The same reasoning applies to an account: keeping
// the row keeps `updated_by` meaning "the same person changed these four
// figures" without saying who, which is the audit property roadmap item 8
// exists to protect. Deleting the account nulls every attribution it ever made
// — the schema's own ON DELETE SET NULL — and that history cannot be rebuilt.
//
// # What is never done
//
// No budget line is ever deleted, in either mode, and no statement here
// touches `unit`, `qty` or `paid`. An erasure that changes what the event cost
// is not an erasure, it is a corruption with a good excuse. Tasks are not
// deleted either: a task is shared work that happens to name an owner, not the
// owner's property.
//
// # What it reaches, and what it does not
//
// The statements below rewrite the rows, and redactLog strikes the same
// values out of what the change log recorded about them, which is what stops
// the name standing in an append-only table and on the Activity screen an
// admin reads. That is the whole of migration 0013's purpose.
//
// File names in `attachments` are not touched. In anonymise mode the account's
// `sessions`, `password_tokens`, `passkey_credentials` and
// `push_subscriptions` rows stay behind; none of them can be used to sign in
// or be sent anything, because every one of those paths requires an active
// account and this one is now disabled, but they are retained personal data
// all the same. EraseDelete takes them with the account, by cascade.
//
// Nothing here records what it changed, and that is not an omission: the entry
// would hold the value being struck out, in the table this function has just
// had to ask a trigger's permission to clean. What it changed is reported to
// the caller in ErasureResult instead. The rows it rewrites are announced
// though, so another browser re-reads them instead of showing the old name
// until somebody reloads it. See announceErasure.
//
// # No revision check
//
// Unlike every other write in this package, erasure takes no revision. A
// request to be erased is not an edit of a cell, and refusing it because
// somebody happened to rename a column three seconds ago would be absurd. The
// whole operation runs in one transaction, so a half-erased person is never
// visible.
func (s *Store) EraseSubject(ctx context.Context, req ErasureRequest) (ErasureResult, error) {
	if !req.Subject.names() {
		return ErasureResult{}, ErrNoSubject
	}
	mode := req.Mode
	if mode == "" {
		mode = EraseAnonymise
	}
	if mode != EraseAnonymise && mode != EraseDelete {
		return ErasureResult{}, fmt.Errorf("store: unknown erasure mode %q", req.Mode)
	}

	return inTx(ctx, s, func(tx pgx.Tx) (ErasureResult, error) {
		// Resolved first, and once: anonymising the sponsor destroys the very
		// names the free-text matching needs. Everything below runs against
		// this one snapshot of who the person is.
		sub, err := resolveSubject(ctx, tx, req.Subject)
		if err != nil {
			return ErasureResult{}, err
		}
		res := ErasureResult{Mode: mode, Aliases: sub.aliases}

		// Counted before anything is rewritten, because redacting a vendor or
		// a note is exactly what stops a line matching afterwards.
		rows, err := collectSubject(ctx, tx, sub)
		if err != nil {
			return ErasureResult{}, err
		}
		res.BudgetItemsRetained = len(rows.budgetItems)

		if sub.sponsor != nil {
			switch mode {
			case EraseDelete:
				// The attributions cascade away with the row. The lines
				// themselves do not: `budget_item_sponsors` cascades on
				// sponsor_id, `budget_items` has no foreign key to sponsors at
				// all, so the money cannot follow the person out.
				tag, err := tx.Exec(ctx, `DELETE FROM sponsors WHERE id = $1`, sub.sponsor.ID)
				if err != nil {
					return ErasureResult{}, fmt.Errorf("sponsors: %w", err)
				}
				res.SponsorDeleted = tag.RowsAffected() > 0
				if res.SponsorDeleted {
					// The row is gone rather than rewritten, so the notice is
					// a delete and carries no revision to compare.
					if err := notifyChange(ctx, tx, ChangeNotice{
						Entity: EntitySponsors, ID: &sub.sponsor.ID, Action: ChangeDelete,
					}); err != nil {
						return ErasureResult{}, err
					}
				}
			default:
				// The code keeps an id fragment so two erased contributors
				// remain distinguishable in the grid — they are covering
				// different lines and collapsing them into one label would
				// misstate both. It is derived from the row's own random uuid,
				// so it identifies nobody and is the same value every time,
				// which is what makes the guard below idempotent.
				code := erasedSponsorCode(sub.sponsor.ID)
				rows, err := queryAll[redactedRow](ctx, tx, "sponsors",
					`UPDATE sponsors
					    SET code = $2, name = '', revision = revision + 1, updated_at = now()
					  WHERE id = $1 AND (code <> $2 OR name <> '')
					  RETURNING id, revision`,
					sub.sponsor.ID, code)
				if err != nil {
					return ErasureResult{}, err
				}
				res.SponsorAnonymised = len(rows) > 0
				if err := announceErasure(ctx, tx, EntitySponsors, rows); err != nil {
					return ErasureResult{}, err
				}
			}
		}

		if sub.user != nil {
			// Preferences go in both modes. They are purely personal — one
			// person's column widths — and carry nothing anybody else relies
			// on, so there is nothing to weigh against deleting them.
			tag, err := tx.Exec(ctx, `DELETE FROM user_ui_prefs WHERE user_id = $1`, sub.user.ID)
			if err != nil {
				return ErasureResult{}, fmt.Errorf("user_ui_prefs: %w", err)
			}
			res.UIPrefsDeleted = tag.RowsAffected() > 0

			switch mode {
			case EraseDelete:
				tag, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, sub.user.ID)
				if err != nil {
					return ErasureResult{}, fmt.Errorf("users: %w", err)
				}
				res.AccountDeleted = tag.RowsAffected() > 0
			default:
				// The address is the identity; the hash is the credential.
				// Both go, and the status says so, so the account cannot be
				// logged into or mailed while its attributions stay intact.
				email := erasedEmail(sub.user.ID)
				tag, err := tx.Exec(ctx,
					`UPDATE users
					    SET email = $2, password_hash = NULL, status = 'disabled',
					        revision = revision + 1, updated_at = now()
					  WHERE id = $1
					    AND (email <> $2 OR password_hash IS NOT NULL OR status <> 'disabled')`,
					sub.user.ID, email)
				if err != nil {
					return ErasureResult{}, fmt.Errorf("users: %w", err)
				}
				res.AccountAnonymised = tag.RowsAffected() > 0
			}
			// Neither branch announces itself, and not by oversight: `users`
			// is in unannouncedEntities, because the stream is as open as the
			// rest of /api/v1 while the accounts API is admin-only.
		}

		if len(sub.folded) > 0 {
			// Owner and vendor are whole-field names, so they are matched
			// exactly after trimming and case-folding rather than by
			// substring: a field whose entire content is the person's name is
			// a different kind of data from a sentence that mentions it, and
			// deserves the treatment that cannot be wrong.
			n, err := redactWholeField(ctx, tx, "tasks", "owner", sub.folded)
			if err != nil {
				return ErasureResult{}, err
			}
			res.TaskOwnersRedacted = n

			// Note what this statement does not set: unit, qty, paid. The
			// vendor's name is personal data when the vendor is somebody's
			// brother; what the line costs never is.
			if n, err = redactWholeField(ctx, tx, "budget_items", "vendor", sub.folded); err != nil {
				return ErasureResult{}, err
			}
			res.VendorsRedacted = n
		}

		if !req.KeepProse && sub.prose != nil {
			for _, col := range proseColumns {
				n, err := redactProse(ctx, tx, col, sub.prose)
				if err != nil {
					return ErasureResult{}, err
				}
				res.ProseRedacted += n
			}
		}

		// Last, because it is the copy rather than the original: the log holds
		// each of the values above as it stood when it was written, and until
		// migration 0013 no statement could touch them at all.
		n, err := redactLog(ctx, tx, logSubjectEntities, sub.logMatcher(req.KeepProse))
		if err != nil {
			return ErasureResult{}, err
		}
		res.HistoryRedacted = n

		return res, nil
	})
}

// PurgeOptions tunes the retention sweep.
type PurgeOptions struct {
	// IncludeAccounts also removes every account and its preferences. Off by
	// default, because a deployment with no accounts left cannot be logged
	// into to confirm that the purge happened — the plan is the thing whose
	// retention was decided, and accounts are a separate decision.
	IncludeAccounts bool `json:"includeAccounts,omitempty"`
}

// PurgeResult counts what a purge removed, per table. It is the only record
// that a purge happened, so it is worth keeping wherever the decision to run
// one was written down.
type PurgeResult struct {
	PurgedAt         time.Time `json:"purgedAt"`
	IncludedAccounts bool      `json:"includedAccounts"`
	SettingsReset    bool      `json:"settingsReset"`

	Attributions     int64 `json:"attributions"`
	ProgrammeEntries int64 `json:"programmeEntries"`
	BudgetItems      int64 `json:"budgetItems"`
	Sponsors         int64 `json:"sponsors"`
	Phases           int64 `json:"phases"`
	Tasks            int64 `json:"tasks"`
	Notes            int64 `json:"notes"`
	UIPrefs          int64 `json:"uiPrefs"`
	Users            int64 `json:"users"`
	// ChangeLogRedacted counts the history entries the purge struck the plan
	// out of. The entries are not deleted, which PurgeEvent explains.
	ChangeLogRedacted int64 `json:"changeLogRedacted"`
}

// PurgeEvent empties the event's data wholesale — the retention decision that
// roadmap item 10 asks for, for use after the evening is over.
//
// It is a store method and nothing else. It is not on a schedule, not behind a
// route, and not called by anything: irreversibly destroying the plan is a
// decision a person makes on a day, not a timer, and wiring it to either would
// make a bug in a cron expression indistinguishable from a policy.
//
// DELETE rather than TRUNCATE, deliberately. TRUNCATE on a table with
// referencing foreign keys needs CASCADE, and TRUNCATE ... CASCADE takes the
// referencing tables with it regardless of what their ON DELETE clause says —
// the opposite of the SET NULL semantics this schema chose. At the scale this
// application runs at, DELETE costs nothing and behaves as written.
//
// `change_log` is struck out rather than emptied. The history of a purged plan
// is a full copy of that plan, names included, so leaving it alone would empty
// the tables and keep the record of them. Deleting it is not the answer
// either, and not only because the trigger refuses every DELETE: `GET /plan`
// asks this table whether the plan has ever been written to, so a purge that
// emptied it would leave the plan reading as one nobody had filled in yet, and
// the first browser still holding a copy would put every purged row back. So
// the entries stay and the words in them go, which is what migration 0013
// permits.
//
// The words and not the figures, which is the limit worth knowing before
// anybody calls this a clean slate. A purge strikes out the fields that can
// name a person, meaning the two lists in redactLog and nothing else in the
// entry, so every recorded amount, quantity, date, position and row id
// survives it exactly as it was written. The financial shape of a purged plan
// is therefore still readable in its history; what is no longer there is
// anybody's name. That is the same restraint the live columns are given, and
// taking the figures as well is a retention decision of its own rather than
// something this function should do quietly.
//
// Attachments do go, by cascade from the budget line or task they hang on, and
// that cascade queues each object key in `attachment_garbage` for the sweep
// that deletes it from the bucket. PurgeResult does not count them, and what a
// file was called stays in its `attachments` entries as a stage of the evening
// stays in its `phases` ones: neither entity is one redactLog opens.
//
// The `settings` singleton is reset rather than deleted: its CHECK (id) allows
// exactly one row and Settings() documents a missing one as a tampered schema,
// so deleting it would leave the deployment unable to start rather than empty.
func (s *Store) PurgeEvent(ctx context.Context, opts PurgeOptions) (PurgeResult, error) {
	return inTx(ctx, s, func(tx pgx.Tx) (PurgeResult, error) {
		res := PurgeResult{
			PurgedAt:         time.Now().UTC(),
			IncludedAccounts: opts.IncludeAccounts,
		}

		type step struct {
			table string
			into  *int64
		}
		// In foreign-key order, so every count is a row this statement
		// removed rather than one a cascade removed on its way past.
		steps := []step{
			{"budget_item_sponsors", &res.Attributions},
			{"programme_entries", &res.ProgrammeEntries},
			{"budget_items", &res.BudgetItems},
			{"sponsors", &res.Sponsors},
			{"phases", &res.Phases},
			{"tasks", &res.Tasks},
			{"notes", &res.Notes},
		}
		if opts.IncludeAccounts {
			steps = append(steps,
				step{"user_ui_prefs", &res.UIPrefs},
				step{"users", &res.Users},
			)
		}

		for _, step := range steps {
			// The table names are constants in this file, never caller input.
			tag, err := tx.Exec(ctx, `DELETE FROM `+step.table)
			if err != nil {
				return PurgeResult{}, fmt.Errorf("%s: %w", step.table, err)
			}
			*step.into = tag.RowsAffected()
		}

		// The history of what was just emptied, struck out rather than
		// deleted. Which entries go depends on what this purge took: the plan
		// always, the accounts only when they went with it, so an account that
		// survives keeps the record of its own address changing.
		entities := logPlanEntities
		if opts.IncludeAccounts {
			entities = logSubjectEntities
		}
		redacted, err := redactLog(ctx, tx, entities, func(string, *uuid.UUID, string, string) bool {
			// No subject: a purge is a decision about the whole plan, so every
			// recorded name, vendor, owner cell and sentence goes.
			return true
		})
		if err != nil {
			return PurgeResult{}, err
		}
		res.ChangeLogRedacted = int64(redacted)

		// The revision is bumped rather than reset, so a browser still holding
		// the pre-purge settings gets a conflict instead of writing the old
		// ceiling back over an emptied plan.
		tag, err := tx.Exec(ctx,
			`UPDATE settings
			    SET ceiling = 0, inflation_pct = 0, fx_rate = 0, split_evenly = false,
			        revision = revision + 1, updated_at = now()
			  WHERE id = true`)
		if err != nil {
			return PurgeResult{}, fmt.Errorf("settings: %w", err)
		}
		res.SettingsReset = tag.RowsAffected() > 0

		return res, nil
	})
}

// --- resolution and matching -------------------------------------------------

// names reports whether the ref identifies anybody at all.
func (r SubjectRef) names() bool {
	if r.SponsorID != nil || r.UserID != nil {
		return true
	}
	for _, a := range r.Aliases {
		if strings.TrimSpace(a) != "" {
			return true
		}
	}
	return false
}

// subject is a SubjectRef with its records read and its names expanded. Export
// and erasure both build one and then share every matcher below, so the two
// operations cannot disagree about who somebody is — an export that finds a
// row the erasure then misses is the failure mode that matters here.
type subject struct {
	ref     SubjectRef
	sponsor *Sponsor
	user    *User
	// aliases are the names as written; folded are the same, trimmed and
	// lower-cased, for whole-field comparison.
	aliases []string
	folded  []string
	// prose matches any alias as a whole word, case-insensitively. Nil when
	// there is nothing to match.
	prose *regexp.Regexp
}

// resolveSubject reads the keyed records and expands the names to match on.
//
// A missing sponsor or account is not an error: erasure has to stay callable
// against a person who is already half gone, which is most of what makes it
// idempotent.
func resolveSubject(ctx context.Context, q querier, ref SubjectRef) (subject, error) {
	sub := subject{ref: ref}

	if ref.SponsorID != nil {
		sp, err := queryOne[Sponsor](ctx, q, "sponsors",
			`SELECT `+sponsorColumns+` FROM sponsors WHERE id = $1`, *ref.SponsorID)
		switch {
		case err == nil:
			sub.sponsor = &sp
		case errors.Is(err, ErrNotFound):
		default:
			return subject{}, err
		}
	}
	if ref.UserID != nil {
		u, err := queryOne[User](ctx, q, "users",
			`SELECT `+userColumns+` FROM users WHERE id = $1`, *ref.UserID)
		switch {
		case err == nil:
			sub.user = &u
		case errors.Is(err, ErrNotFound):
		default:
			return subject{}, err
		}
	}

	names := slices.Clone(ref.Aliases)
	if sub.sponsor != nil {
		names = append(names, sub.sponsor.Code, sub.sponsor.Name)
	}
	if sub.user != nil {
		// The whole address, never the local part on its own: "ada" guessed
		// out of ada@example.test would redact every Ada in the plan, and one
		// of them may be somebody else.
		names = append(names, sub.user.Email)
	}
	sub.aliases, sub.folded = normaliseAliases(names)

	prose, err := proseMatcher(sub.aliases)
	if err != nil {
		return subject{}, err
	}
	sub.prose = prose

	return sub, nil
}

// caveats says what this subject's export could not reliably reach. Written
// from what the schema is, not from what was found, because the gaps are
// structural.
func (sub subject) caveats() []string {
	out := []string{
		"tasks.owner and budget_items.vendor are free text, not foreign keys: only the names listed under `aliases` were matched, exactly. A nickname, a misspelling, initials, or a name written as \"Ada's brother\" is not locatable by any query this schema supports.",
		"free text (budget line notes, the notes list, the programme) was matched on whole-word occurrences of those same names, so a mention that never spells the name out is not found.",
		"the change history was not read. `change_log` records what each change altered, field by field, so values as they stood before somebody corrected them, including a name, an owner cell and the address on an account, are held there and are not in this document.",
		"uploaded files were not read. `attachments` records the name a file was given by the device it came from, and which account uploaded it; neither is listed here, and the files themselves are in object storage rather than in the database.",
		"the account's sign-in records were not read. `sessions`, `password_tokens`, `passkey_credentials` and `push_subscriptions` hold rows about an account, among them the label a passkey's device was given and the address a browser is sent reminders at, and none of them is listed here.",
	}
	if sub.ref.SponsorID == nil {
		out = append(out, "no sponsor record was named, so nothing was exported from `sponsors` or from the budget attributions that reference it.")
	}
	if sub.ref.UserID == nil {
		out = append(out, "no account was named. The schema has no link between a sponsor and an account — `sponsors` holds no address and `users` holds no name — so if this person also has a login, its id has to be supplied by the caller; it cannot be inferred.")
	}
	return out
}

// subjectRows is every row the subject appears in.
type subjectRows struct {
	budgetItems []Matched[BudgetItem]
	tasks       []Matched[Task]
	notes       []Matched[Note]
	programme   []Matched[ProgrammeEntry]
}

// collectSubject reads the tables that can name a person and keeps the rows
// that do.
//
// It reads them whole and filters in Go rather than pushing the predicate into
// SQL. Two reasons, in this order: the prose match has to be a Go regexp —
// whole-word matching is what stops "Ada" redacting "Nevada", and getting that
// right across two regex dialects is a way to be subtly wrong in one of them —
// and the same predicate then serves the export and the erasure. The cost is a
// full read of a table that this application's own architecture notes describe
// as holding tens of rows.
//
// This is the single place that knows what "rows about a person" means, which
// is what makes it the place to add a table holding personal data: add it here
// and both operations pick it up. It is also where the gap listed at the top
// of this file is, because the tables added since migration 0005 were never
// added here. `change_log` is the exception in both directions: an erasure
// reaches it, through redactLog and the trigger exception migration 0013 had
// to make for it, and an export still does not. Everything it holds about the
// subject is a value one of these rows carried earlier, so adding it is a
// decision about how much of a shared row's history belongs in one person's
// export rather than a matter of reading another table.
func collectSubject(ctx context.Context, q querier, sub subject) (subjectRows, error) {
	var out subjectRows

	attributed := map[uuid.UUID]bool{}
	if sub.sponsor != nil {
		ids, err := queryScalars[uuid.UUID](ctx, q, "budget_item_sponsors",
			`SELECT budget_item_id FROM budget_item_sponsors WHERE sponsor_id = $1`, sub.sponsor.ID)
		if err != nil {
			return subjectRows{}, err
		}
		for _, id := range ids {
			attributed[id] = true
		}
	}

	items, err := queryAll[BudgetItem](ctx, q, "budget_items",
		`SELECT `+budgetItemColumns+` FROM budget_items ORDER BY position, id`)
	if err != nil {
		return subjectRows{}, err
	}
	links, err := sponsorLinks(ctx, q)
	if err != nil {
		return subjectRows{}, err
	}
	for _, it := range items {
		var on []string
		if attributed[it.ID] {
			on = append(on, MatchSponsor)
		}
		isVendor := sub.matchesWholeField(it.Vendor)
		if isVendor {
			on = append(on, MatchVendor)
		}
		// A vendor cell reading "Ada Photography" is not her, but it does
		// write her name down, so it is data held about her and the erasure
		// will strike it out. The export has to agree, or it reports rows the
		// erasure changes as rows it never held.
		if sub.matchesProse(it.Note) || sub.matchesProse(it.Item) ||
			(!isVendor && sub.matchesProse(it.Vendor)) {
			on = append(on, MatchProse)
		}
		if sub.isActor(it.UpdatedBy) {
			on = append(on, MatchUpdatedBy)
		}
		if len(on) > 0 {
			it.SponsorIDs = links[it.ID]
			out.budgetItems = append(out.budgetItems, Matched[BudgetItem]{On: on, Row: it})
		}
	}

	tasks, err := queryAll[Task](ctx, q, "tasks", `SELECT `+taskColumns+` FROM tasks ORDER BY position, id`)
	if err != nil {
		return subjectRows{}, err
	}
	for _, t := range tasks {
		var on []string
		owned := sub.matchesWholeField(t.Owner)
		if owned {
			on = append(on, MatchOwner)
		}
		// "Ada's brother" owns the task, not Ada — but her name is written in
		// the cell, so it is hers to see and hers to have struck out.
		if sub.matchesProse(t.Name) || (!owned && sub.matchesProse(t.Owner)) {
			on = append(on, MatchProse)
		}
		if sub.isActor(t.UpdatedBy) {
			on = append(on, MatchUpdatedBy)
		}
		if len(on) > 0 {
			out.tasks = append(out.tasks, Matched[Task]{On: on, Row: t})
		}
	}

	notes, err := queryAll[Note](ctx, q, "notes", `SELECT `+noteColumns+` FROM notes ORDER BY position, id`)
	if err != nil {
		return subjectRows{}, err
	}
	for _, n := range notes {
		if sub.matchesProse(n.Text) {
			out.notes = append(out.notes, Matched[Note]{On: []string{MatchProse}, Row: n})
		}
	}

	programme, err := queryAll[ProgrammeEntry](ctx, q, "programme_entries",
		`SELECT `+programmeColumns+` FROM programme_entries ORDER BY position, id`)
	if err != nil {
		return subjectRows{}, err
	}
	for _, p := range programme {
		if sub.matchesProse(p.Title) || sub.matchesProse(p.Note) {
			out.programme = append(out.programme, Matched[ProgrammeEntry]{On: []string{MatchProse}, Row: p})
		}
	}

	return out, nil
}

// matchesWholeField reports whether a field whose entire content is a name is
// this person's. Trimmed and case-folded, never substring: "Ada" must not
// match an owner cell reading "Ada's brother", who is a different person.
func (sub subject) matchesWholeField(v string) bool {
	if len(sub.folded) == 0 {
		return false
	}
	f := strings.ToLower(strings.TrimSpace(v))
	if f == "" {
		return false
	}
	return slices.Contains(sub.folded, f)
}

// matchesProse reports whether a sentence mentions this person by name.
func (sub subject) matchesProse(v string) bool {
	return sub.prose != nil && sub.prose.MatchString(v)
}

// isActor reports whether an updated_by attribution is this person's account.
func (sub subject) isActor(actor *uuid.UUID) bool {
	return actor != nil && sub.user != nil && *actor == sub.user.ID
}

// ownActor keeps an attribution only when it is the subject's own, so an
// export cannot hand one person the identifier of another as a side effect of
// exporting a row they share.
func (sub subject) ownActor(actor *uuid.UUID) *uuid.UUID {
	if sub.isActor(actor) {
		return actor
	}
	return nil
}

// normaliseAliases drops blanks, de-duplicates case-insensitively, and returns
// the names as written alongside their folded forms.
//
// Dropping blanks is not tidiness: an already-anonymised sponsor has an empty
// name, and an empty alias would match every unassigned task in the plan.
func normaliseAliases(in []string) (display, folded []string) {
	seen := map[string]struct{}{}
	for _, raw := range in {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		f := strings.ToLower(name)
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		display = append(display, name)
		folded = append(folded, f)
	}
	return display, folded
}

// proseMatcher builds the whole-word, case-insensitive matcher for a set of
// names.
//
// `\b` is what keeps this honest: without it, erasing "Ada" rewrites "Nevada"
// and "Adamant" and quietly corrupts a shared ledger in the name of privacy.
// Longest alias first, so "Ada Lovelace" is struck out whole rather than
// leaving " Lovelace" behind.
func proseMatcher(aliases []string) (*regexp.Regexp, error) {
	if len(aliases) == 0 {
		return nil, nil
	}
	parts := make([]string, 0, len(aliases))
	for _, a := range aliases {
		parts = append(parts, regexp.QuoteMeta(a))
	}
	slices.SortFunc(parts, func(a, b string) int {
		if d := len(b) - len(a); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})

	re, err := regexp.Compile(`(?i)\b(?:` + strings.Join(parts, "|") + `)\b`)
	if err != nil {
		return nil, fmt.Errorf("store: build name matcher: %w", err)
	}
	return re, nil
}

// proseColumns are the free-text columns a person's name can end up written
// into. Table and column names only — never caller input, which is what makes
// the interpolation in redactProse safe.
//
// `tasks.owner` and `budget_items.vendor` appear here as well as in the
// whole-field pass, and the second pass is not redundant: the exact match
// handles an owner cell reading "Ada", and this one handles "Ada's brother",
// who is somebody else entirely but whose cell still writes her name down.
var proseColumns = []struct{ table, column string }{
	{"budget_items", "item"},
	{"budget_items", "note"},
	{"budget_items", "vendor"},
	{"notes", "text"},
	{"programme_entries", "title"},
	{"programme_entries", "note"},
	{"tasks", "name"},
	{"tasks", "owner"},
}

// redactWholeField replaces a whole-field name with the tombstone. The
// `<> $2` guard is what makes a second run a no-op rather than a second
// revision bump.
//
// `updated_by` is deliberately not touched here or in redactProse. An erasure
// is not somebody's edit, and overwriting the attribution would destroy the
// record of who last worked on the row to make room for a name this package
// does not have.
func redactWholeField(ctx context.Context, tx pgx.Tx, table, column string, folded []string) (int, error) {
	rows, err := queryAll[redactedRow](ctx, tx, table,
		`UPDATE `+table+`
		    SET `+column+` = $2, revision = revision + 1, updated_at = now()
		  WHERE lower(btrim(`+column+`)) = ANY($1::text[]) AND `+column+` <> $2
		  RETURNING id, revision`,
		folded, Tombstone)
	if err != nil {
		return 0, err
	}
	if err := announceErasure(ctx, tx, table, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// redactProse strikes the name out of one free-text column.
//
// Read, rewrite in Go, write back the rows that changed — rather than a SQL
// regexp_replace — so that the matching is the same engine, with the same
// word-boundary rules, that decided the row was the subject's in the first
// place. Rows are collected in full before the first update, because a query
// still streaming holds the connection.
func redactProse(ctx context.Context, tx pgx.Tx, col struct{ table, column string }, re *regexp.Regexp) (int, error) {
	type textRow struct {
		ID   uuid.UUID `db:"id"`
		Text string    `db:"text"`
	}

	rows, err := queryAll[textRow](ctx, tx, col.table,
		`SELECT id, `+col.column+` AS text FROM `+col.table)
	if err != nil {
		return 0, err
	}

	n := 0
	for _, r := range rows {
		redacted := re.ReplaceAllString(r.Text, Tombstone)
		if redacted == r.Text {
			continue
		}
		written, err := queryAll[redactedRow](ctx, tx, col.table,
			`UPDATE `+col.table+`
			    SET `+col.column+` = $2, revision = revision + 1, updated_at = now()
			  WHERE id = $1
			  RETURNING id, revision`,
			r.ID, redacted)
		if err != nil {
			return 0, err
		}
		if err := announceErasure(ctx, tx, col.table, written); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// erasedSponsorCode is the label an erased contributor keeps in the grid.
// Derived from the row's own random uuid, so it names nobody and is stable
// across repeated erasures of the same person.
func erasedSponsorCode(id uuid.UUID) string {
	return "erased-" + id.String()[:8]
}

// erasedEmail is an address that is unique (the column's index requires it)
// and undeliverable (RFC 2606 requires that of `.invalid`).
func erasedEmail(id uuid.UUID) string {
	return "erased-" + id.String() + "@" + erasedEmailDomain
}

// --- the change log ----------------------------------------------------------

// redactedRow is a row an erasure rewrote: what a change notice needs to name
// it, and nothing else.
type redactedRow struct {
	ID       uuid.UUID `db:"id"`
	Revision int64     `db:"revision"`
}

// announceErasure tells open browsers to re-read the rows an erasure changed.
//
// It is the one place in this package that announces a change without also
// recording one, and notify.go explains why the two are otherwise a single
// act. Erasure cannot record: the entry would hold the very value being struck
// out, in the table this file has to ask a trigger's permission to clean. So
// the two halves come apart here, deliberately and in one direction only. The
// alternative is what the erasure used to do, which is nothing: the row's
// revision moves, no notice goes out, and a second browser goes on showing the
// erased name until somebody reloads it while every edit it sends comes back
// as a conflict nobody can explain.
func announceErasure(ctx context.Context, tx pgx.Tx, entity string, rows []redactedRow) error {
	for _, r := range rows {
		if err := notifyChange(ctx, tx, ChangeNotice{
			Entity:   entity,
			ID:       &r.ID,
			Action:   ChangeUpdate,
			Revision: &r.Revision,
		}); err != nil {
			return err
		}
	}
	return nil
}

// tombstoneValue is the Tombstone as it stands in a recorded diff. The trigger
// added by migration 0013 compares against this exact literal, so the Go
// constant and the SQL one are the same string in two languages.
var tombstoneValue = json.RawMessage(`"` + Tombstone + `"`)

// logKeyedFields are the fields whose recorded value is the person's because
// of the row the entry is about rather than because of what it says. Every
// name this sponsor ever had is hers, the misspelt one somebody corrected
// included, and matching on the current spelling would leave the others.
var logKeyedFields = map[string][]string{
	EntitySponsors: {"code", "name"},
	EntityUsers:    {"email"},
}

// logMatchedFields are the free-text fields, taken from proseColumns so that
// the history is struck out exactly where the live column is. A field the
// erasure leaves standing on the row is not one it may destroy the record of:
// `phases.name` is a stage of the evening and `attachments.name` is a file, so
// neither is reached here for the same reason neither is reached there.
var logMatchedFields = func() map[string][]string {
	out := map[string][]string{}
	for _, col := range proseColumns {
		out[col.table] = append(out[col.table], col.column)
	}
	return out
}()

// logWholeFields are the two columns matched exactly rather than as prose, and
// so struck out whether or not KeepProse was asked for: a cell whose entire
// content is somebody's name is not a sentence mentioning them.
var logWholeFields = map[string]string{
	EntityTasks:       "owner",
	EntityBudgetItems: "vendor",
}

// logPlanEntities are the entities whose recorded values belong to the plan
// rather than to an account, and logSubjectEntities adds the account. A purge
// reads the first, or the second when it is taking the accounts with it; an
// erasure always reads the second.
var (
	logPlanEntities = []string{
		EntityBudgetItems, EntityNotes, EntityProgrammeEntries, EntitySponsors, EntityTasks,
	}
	logSubjectEntities = slices.Concat(logPlanEntities, []string{EntityUsers})
)

// logMatch decides whether one recorded value is to be struck out. It is given
// the row the entry is about as well as the value, because the keyed fields
// are answered by the row and not by the text.
type logMatch func(entity string, id *uuid.UUID, field, value string) bool

// logMatcher is the erasure's answer to that question, and it gives the same
// one the live columns got: the keyed fields of this person's own sponsor row
// and account, the two whole-field cells that hold nothing but a name, and,
// unless the caller asked to keep prose, every sentence that mentions her.
func (sub subject) logMatcher(keepProse bool) logMatch {
	return func(entity string, id *uuid.UUID, field, value string) bool {
		switch entity {
		case EntitySponsors:
			return sub.sponsor != nil && id != nil && *id == sub.sponsor.ID
		case EntityUsers:
			return sub.user != nil && id != nil && *id == sub.user.ID
		}
		if logWholeFields[entity] == field && sub.matchesWholeField(value) {
			return true
		}
		return !keepProse && sub.matchesProse(value)
	}
}

// redactLog strikes values out of the change history.
//
// The log records what each write altered, field by field, so it holds every
// value a row ever carried and an erasure that stops at the rows has moved the
// name rather than removed it. Migration 0013 is what makes this possible at
// all, and it allows exactly this shape: `changes` alone may move, the same
// keys have to be there afterwards, and a recorded value may only become the
// tombstone. So a value goes whole rather than being edited the way redactProse
// edits a row. A note that read "Ada is chasing the venue" leaves "(erased)"
// in the history where the row keeps "(erased) is chasing the venue", because
// a trigger that accepted a rewritten sentence could not tell a redaction from
// an edit.
//
// Read whole and filtered in Go, as collectSubject is and for the same reason:
// the match has to be the same engine, with the same word boundaries, that
// decided the row was the subject's in the first place.
func redactLog(ctx context.Context, tx pgx.Tx, entities []string, match logMatch) (int, error) {
	type logRow struct {
		ID       int64                  `db:"id"`
		Entity   string                 `db:"entity"`
		EntityID *uuid.UUID             `db:"entity_id"`
		Changes  map[string]FieldChange `db:"changes"`
	}

	rows, err := queryAll[logRow](ctx, tx, "change_log",
		`SELECT id, entity, entity_id, changes
		   FROM change_log
		  WHERE entity = ANY($1)
		  ORDER BY id`, entities)
	if err != nil {
		return 0, err
	}

	n := 0
	for _, r := range rows {
		rewritten := false
		for _, field := range slices.Concat(logKeyedFields[r.Entity], logMatchedFields[r.Entity]) {
			recorded, ok := r.Changes[field]
			if !ok {
				continue
			}
			// Both sides, because a value somebody later corrected writes the
			// person down twice: once as what it was and once as what it
			// became.
			old, wasOld := redactRecorded(recorded.Old, r.Entity, r.EntityID, field, match)
			fresh, wasNew := redactRecorded(recorded.New, r.Entity, r.EntityID, field, match)
			if !wasOld && !wasNew {
				continue
			}
			r.Changes[field] = FieldChange{Old: old, New: fresh}
			rewritten = true
		}
		// Nothing matched, so nothing is written: an UPDATE that changed no
		// value would still be a valid redaction as far as the trigger is
		// concerned, and counting it would make a second erasure report work
		// it did not do.
		if !rewritten {
			continue
		}

		payload, err := json.Marshal(r.Changes)
		if err != nil {
			return 0, fmt.Errorf("change_log: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE change_log SET changes = $2::jsonb WHERE id = $1`, r.ID, payload); err != nil {
			return 0, fmt.Errorf("change_log: %w", err)
		}
		n++
	}
	return n, nil
}

// redactRecorded replaces one recorded value with the tombstone when it is the
// subject's, and reports whether it did.
//
// Only a JSON string is a candidate, tested on the raw bytes rather than by
// decoding: a side that recorded nothing is null, a list of co-sponsors is an
// array, the money columns are numbers, and `null` decodes into a string
// without complaining. Leaving the rest alone is what keeps an amount out of
// reach of a matcher looking for a name, and it is also the rule the trigger
// enforces from its side, where a null that became the tombstone would turn a
// create entry into something that never happened.
func redactRecorded(raw json.RawMessage, entity string, id *uuid.UUID, field string, match logMatch) (json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return raw, false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw, false
	}
	if value == Tombstone || !match(entity, id, field, value) {
		return raw, false
	}
	return tombstoneValue, true
}
