package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Settings is the singleton row of plan-wide knobs. One deployment serves one
// event, so there is exactly one of these and it has no id worth exposing.
type Settings struct {
	Ceiling int64 `db:"ceiling"` // minor units
	// Percentages and rates are float64 rather than minor units because they
	// are not money: nothing is paid in them and they are never summed. The
	// column precisions (5,2) and (18,6) stay well inside what a float64
	// represents exactly, with the caveat that a fx_rate using all 18 digits
	// would not — no real rate comes close.
	InflationPct float64   `db:"inflation_pct"`
	FxRate       float64   `db:"fx_rate"`
	SplitEvenly  bool      `db:"split_evenly"`
	Revision     int64     `db:"revision"`
	UpdatedAt    time.Time `db:"updated_at"`
}

const settingsColumns = `ceiling, inflation_pct, fx_rate, split_evenly, revision, updated_at`

// Settings reads the singleton. The row is created by the migrations, so a
// missing one means the schema was tampered with rather than that the event
// has no settings yet.
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	return queryOne[Settings](ctx, s.pool, "settings",
		`SELECT `+settingsColumns+` FROM settings WHERE id = true`)
}

// UpdateSettings writes the singleton, refusing the write if in.Revision is
// no longer current.
//
// The ceiling is money and gets the same history as every other figure: "who
// raised the budget, and when" is one of the questions that starts the
// argument. The singleton has no id, so its history is recorded under a null
// entity_id and read back with uuid.Nil.
func (s *Store) UpdateSettings(ctx context.Context, in Settings) (Settings, error) {
	actor := resolveActor(ctx, nil)
	out, err := inTx(ctx, s, func(tx pgx.Tx) (Settings, error) {
		before, err := queryOne[Settings](ctx, tx, "settings",
			`SELECT `+settingsColumns+` FROM settings WHERE id = true FOR UPDATE`)
		if err != nil {
			return Settings{}, err
		}
		after, err := queryOne[Settings](ctx, tx, "settings",
			`UPDATE settings
			    SET ceiling = $1, inflation_pct = $2, fx_rate = $3, split_evenly = $4,
			        revision = revision + 1, updated_at = now()
			  WHERE id = true AND revision = $5
			RETURNING `+settingsColumns,
			in.Ceiling, in.InflationPct, in.FxRate, in.SplitEvenly, in.Revision)
		if err != nil {
			return Settings{}, err
		}
		if err := recordUpdate(ctx, tx, EntitySettings, uuid.Nil, &after.Revision, before, after, actor); err != nil {
			return Settings{}, err
		}
		return after, nil
	})
	if err == nil {
		return out, nil
	}
	if !isNotFound(err) {
		return Settings{}, err
	}

	current, err := s.Settings(ctx)
	// uuid.Nil: the singleton has no id, and inventing one would put a
	// meaningless value in the 409 body.
	return Settings{}, conflict("settings", uuid.Nil, in.Revision, current, err)
}
