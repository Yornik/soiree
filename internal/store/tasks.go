package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TaskStatus is where a task has got to. The same three values the browser
// already uses, mirrored by a CHECK constraint so a typo cannot be stored.
type TaskStatus string

const (
	TaskNotStarted TaskStatus = "not-started"
	TaskInProgress TaskStatus = "in-progress"
	TaskDone       TaskStatus = "done"
)

// Task is something somebody has to do. Owner is free text rather than a
// reference to a user: plenty of tasks belong to the caterer, to somebody's
// brother, or to nobody with an account here.
type Task struct {
	ID     uuid.UUID  `db:"id"`
	Name   string     `db:"name"`
	Owner  string     `db:"owner"`
	Due    *time.Time `db:"due"`
	Status TaskStatus `db:"status"`

	Position  int32      `db:"position"`
	Revision  int64      `db:"revision"`
	UpdatedAt time.Time  `db:"updated_at"`
	UpdatedBy *uuid.UUID `db:"updated_by"`
}

const taskColumns = `id, name, owner, due, status, position, revision, updated_at, updated_by`

// CreateTask inserts a task. An empty Status takes the column default.
func (s *Store) CreateTask(ctx context.Context, in Task, actor *uuid.UUID) (Task, error) {
	who := resolveActor(ctx, actor)
	return createAudited(ctx, s, EntityTasks, who, func(tx pgx.Tx) (Task, error) {
		return queryOne[Task](ctx, tx, "tasks",
			`INSERT INTO tasks (id, name, owner, due, status, position, updated_by)
			 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, COALESCE($5::text, 'not-started'), $6, $7)
			 RETURNING `+taskColumns,
			newID(in.ID), in.Name, in.Owner, in.Due, nullString(string(in.Status)), in.Position, who.ID)
	})
}

// Task reads one task.
func (s *Store) Task(ctx context.Context, id uuid.UUID) (Task, error) {
	return queryOne[Task](ctx, s.pool, "tasks",
		`SELECT `+taskColumns+` FROM tasks WHERE id = $1`, id)
}

// Tasks lists tasks in display order.
func (s *Store) Tasks(ctx context.Context) ([]Task, error) {
	return queryAll[Task](ctx, s.pool, "tasks",
		`SELECT `+taskColumns+` FROM tasks ORDER BY position, id`)
}

// UpdateTask writes every mutable field, refusing the write if in.Revision is
// no longer current.
func (s *Store) UpdateTask(ctx context.Context, in Task, actor *uuid.UUID) (Task, error) {
	who := resolveActor(ctx, actor)
	out, err := updateAudited(ctx, s, EntityTasks, taskColumns, in.ID, who,
		func(tx pgx.Tx, _ Task) (Task, error) {
			return queryOne[Task](ctx, tx, "tasks",
				`UPDATE tasks
				    SET name = $1, owner = $2, due = $3, status = $4, position = $5,
				        revision = revision + 1, updated_at = now(), updated_by = $6
				  WHERE id = $7 AND revision = $8
				RETURNING `+taskColumns,
				in.Name, in.Owner, in.Due, in.Status, in.Position, who.ID, in.ID, in.Revision)
		})
	if err == nil {
		return out, nil
	}
	if !isNotFound(err) {
		return Task{}, err
	}

	current, err := s.Task(ctx, in.ID)
	return Task{}, conflict("tasks", in.ID, in.Revision, current, err)
}

// DeleteTask removes a task, refusing if revision is no longer current. Its
// history stays.
func (s *Store) DeleteTask(ctx context.Context, id uuid.UUID, revision int64) error {
	// Not deleteAudited, which every other single-row delete uses: a task can
	// have files, they go with it by cascade, and the change log only knows
	// about that if it is told here. See lockAttachmentsOf.
	actor := resolveActor(ctx, nil)
	_, err := inTx(ctx, s, func(tx pgx.Tx) (struct{}, error) {
		before, err := lockRow[Task](ctx, tx, EntityTasks, taskColumns, id)
		if err != nil {
			return struct{}{}, err
		}
		files, err := lockAttachmentsOf(ctx, tx, attachmentsOfTasks, []uuid.UUID{id})
		if err != nil {
			return struct{}{}, err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM tasks WHERE id = $1 AND revision = $2`, id, revision)
		if err != nil {
			return struct{}{}, fmt.Errorf("tasks: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return struct{}{}, notFoundErr(EntityTasks)
		}
		if err := recordDelete(ctx, tx, EntityTasks, id, revisionOf(before), before, actor); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, recordAttachmentsLost(ctx, tx, files, actor)
	})
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}

	current, err := s.Task(ctx, id)
	return conflict("tasks", id, revision, current, err)
}
