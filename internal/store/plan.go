package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Plan is everything the browser needs to draw the page.
type Plan struct {
	Settings    Settings
	Phases      []Phase
	Sponsors    []Sponsor
	BudgetItems []BudgetItem
	Programme   []ProgrammeEntry
	Tasks       []Task
	Notes       []Note
	// Confirmed files only, across both kinds of parent. A collection of its
	// own rather than a list on each row: a budget line is sent back whole on
	// an edit, the API refuses fields it does not know, and a line that carried
	// its files would have to be stripped of them before every write.
	Attachments []Attachment
	// Whether the plan has ever been written to. An empty plan is two
	// different things, one nobody has filled in yet and one somebody
	// emptied, and the lists above cannot tell them apart.
	Pristine bool
}

// LoadPlan reads the whole plan.
//
// One round trip from the browser matters more than granularity when the
// origin is one place and the users are not, so the API reads everything at
// once — and this is the query behind it: one statement per table, never one
// per row. The sponsor attributions are stitched onto their items in Go from a
// single read of the join table.
//
// All of it runs in one repeatable-read transaction. Without that, a second
// person deleting a sponsor between the sponsors query and the attributions
// query hands the browser an attribution pointing at somebody who is no longer
// in the list — a dangling reference reintroduced by the reader after the
// schema went to such lengths to make it impossible.
func (s *Store) LoadPlan(ctx context.Context) (Plan, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return Plan{}, fmt.Errorf("begin plan read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var plan Plan

	if plan.Settings, err = queryOne[Settings](ctx, tx, "settings",
		`SELECT `+settingsColumns+` FROM settings WHERE id = true`); err != nil {
		return Plan{}, err
	}
	if plan.Phases, err = queryAll[Phase](ctx, tx, "phases",
		`SELECT `+phaseColumns+` FROM phases ORDER BY position, id`); err != nil {
		return Plan{}, err
	}
	if plan.Sponsors, err = queryAll[Sponsor](ctx, tx, "sponsors",
		`SELECT `+sponsorColumns+` FROM sponsors ORDER BY position, id`); err != nil {
		return Plan{}, err
	}
	if plan.BudgetItems, err = queryAll[BudgetItem](ctx, tx, "budget_items",
		`SELECT `+budgetItemColumns+` FROM budget_items ORDER BY position, id`); err != nil {
		return Plan{}, err
	}
	if plan.Programme, err = queryAll[ProgrammeEntry](ctx, tx, "programme_entries",
		`SELECT `+programmeColumns+` FROM programme_entries ORDER BY position, id`); err != nil {
		return Plan{}, err
	}
	if plan.Tasks, err = queryAll[Task](ctx, tx, "tasks",
		`SELECT `+taskColumns+` FROM tasks ORDER BY position, id`); err != nil {
		return Plan{}, err
	}
	if plan.Notes, err = queryAll[Note](ctx, tx, "notes",
		`SELECT `+noteColumns+` FROM notes ORDER BY position, id`); err != nil {
		return Plan{}, err
	}

	if plan.Attachments, err = readyAttachments(ctx, tx); err != nil {
		return Plan{}, err
	}

	// Whether the plan has ever been written to, which the lists above cannot
	// say: a plan somebody emptied looks exactly like one nobody has filled in
	// yet, and the browser treats the two very differently.
	//
	// Asked of the change log rather than of the tables, because the question
	// is about the past and the tables only know the present. The log refuses
	// every DELETE by trigger, so a plan that was emptied row by row and a
	// database purged for retention both go on answering that they were
	// written to.
	//
	// The plan's own tables and no others. `settings` is written by the first
	// ceiling anybody types and `users` by the bootstrap admin, and neither
	// means a plan existed. Counting them would cost the browser holding the
	// only planner in existence its copy, which is what this narrows rather
	// than reverses.
	if err := tx.QueryRow(ctx,
		`SELECT NOT EXISTS (SELECT 1 FROM change_log WHERE entity = ANY($1))`,
		[]string{
			EntityPhases, EntitySponsors, EntityBudgetItems,
			EntityProgrammeEntries, EntityTasks, EntityNotes,
		}).Scan(&plan.Pristine); err != nil {
		return Plan{}, fmt.Errorf("change_log: %w", err)
	}

	links, err := sponsorLinks(ctx, tx)
	if err != nil {
		return Plan{}, fmt.Errorf("budget_item_sponsors: %w", err)
	}
	for i := range plan.BudgetItems {
		plan.BudgetItems[i].SponsorIDs = links[plan.BudgetItems[i].ID]
	}

	if err := tx.Commit(ctx); err != nil {
		return Plan{}, fmt.Errorf("commit plan read: %w", err)
	}
	return plan, nil
}
