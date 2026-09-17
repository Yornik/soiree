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
