package httpd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Yornik/soiree/internal/store"
)

// PATCH /api/v1/settings — the plan-wide knobs: the spending ceiling, the
// inflation buffer, the fx rate and the split-evenly toggle.
//
// Its own file and its own handler because `settings` is the one table the
// descriptors in api_entities.go cannot express. It is a singleton — one row
// behind a boolean primary key — so there is no {id} to route on, nothing to
// POST and nothing to DELETE, and registering it as an entity would mean
// inventing two verbs that do not exist.
//
// Everything a client can observe is the same as every other patch: the
// revision travels in the body, an omitted field is left alone, a stale
// revision is a 409 carrying the row as it now stands, and money crosses the
// boundary in major units as a decimal string. The route was skipped when the
// collection handlers were written, which left these four fields readable from
// GET /plan and writable nowhere.

// Column bounds, checked here so a figure the column cannot hold is a 400
// naming the limit rather than a 500 carrying Postgres' numeric_field_overflow
// — the same reasoning as the task status check in api_entities.go, which is
// also enforced twice for the same reason.
//
// inflation_pct is numeric(5,2) and fx_rate numeric(18,6); the fx bound is the
// whole-number part alone, which is both exactly representable as a float64
// and eleven orders of magnitude past any real rate. The scales of the same two
// columns are here for the same reason, and see setRate for why a decimal past
// them is the worse half of this.
const (
	maxInflationPct      = 999.99
	inflationPctDecimals = 2
	maxFxRate            = 999999999999
	fxRateDecimals       = 6
)

// settingsBody is a partial write of the singleton.
type settingsBody struct {
	// The read-only fields a client echoes back when it patches the row it is
	// already holding — accepted and ignored, as `echoed` is elsewhere.
	//
	// Two rather than four: the singleton has no id and the table has no
	// updated_by column, so neither appears in a response. A body carrying
	// them is as wrong as a misspelt field and gets the same 400.
	Revision  json.RawMessage `json:"revision"`
	UpdatedAt json.RawMessage `json:"updatedAt"`

	Ceiling      optional[moneyText] `json:"ceiling"`
	InflationPct optional[float64]   `json:"inflationPct"`
	FxRate       optional[float64]   `json:"fxRate"`
	SplitEvenly  optional[bool]      `json:"splitEvenly"`
}

func (b settingsBody) apply(currency string, row store.Settings) (store.Settings, error) {
	var f fieldErrs
	setMoney(&f, "ceiling", currency, b.Ceiling, &row.Ceiling)
	setRate(&f, "inflationPct", b.InflationPct, maxInflationPct, inflationPctDecimals, &row.InflationPct)
	setRate(&f, "fxRate", b.FxRate, maxFxRate, fxRateDecimals, &row.FxRate)
	setValue(&f, "splitEvenly", b.SplitEvenly, &row.SplitEvenly)
	return row, f.err
}

// setRate applies a bounded numeric field: setValue plus the range and the
// scale of the column behind it, so a rate the column cannot hold is refused
// before it reaches a constraint the API cannot report as anything but an
// internal error.
//
// The scale is the half a caller never sees coming. Past the range the column
// complains; past the scale it rounds and says nothing, so the answer carries a
// rate the client did not send, and a client comparing what it holds with what
// it sent finds a difference no further write can close. It patches again on
// every pass for as long as the page is open, and each of those writes bumps
// the revision, which hands every other open page a conflict. A plan in euros
// pricing a second currency in rupiah needs about 0.0000571, a seventh decimal,
// so this is a rate somebody types rather than a contrived one. setQty holds
// the one figure here that is neither money nor a rate to the same rule.
func setRate(f *fieldErrs, field string, o optional[float64], limit float64, decimals int, dst *float64) {
	if !o.set {
		return
	}
	if o.value == nil {
		f.fail(field, "must not be null")
		return
	}
	v := *o.value
	if v < -limit || v > limit {
		f.fail(field, fmt.Sprintf("must be between -%s and %s",
			strconv.FormatFloat(limit, 'f', -1, 64), strconv.FormatFloat(limit, 'f', -1, 64)))
		return
	}
	// The shortest text that reproduces this float exactly: if that carries
	// more decimals than the column keeps, then nothing the column can hold is
	// equal to what was sent, whatever it rounds to.
	//
	// Counted rather than checked by multiplying by the scale the way setQty
	// can, because maxFxRate times a million is past 2^53, and up there that
	// arithmetic refuses rates the column holds perfectly well, maxFxRate
	// itself among them.
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if i := strings.IndexByte(s, '.'); i >= 0 && len(s)-i-1 > decimals {
		f.fail(field, fmt.Sprintf("must have at most %d decimal places", decimals))
		return
	}
	*dst = v
}

// settingsEntity is a descriptor built for exactly one of entity's fields.
//
// writeStoreError needs an encoder to render the row inside a 409, and taking
// it from an entity[T] is what keeps that body byte-identical to every other
// collection's. The routing fields stay zero deliberately: this descriptor
// never reaches register, because the singleton has no id-addressed verbs to
// register.
func settingsEntity(currency string) entity[store.Settings] {
	return entity[store.Settings]{encode: func(s store.Settings) any { return encodeSettings(currency, s) }}
}

// patchSettings is handlePatch without the id: read the row, merge the fields
// the body actually carried, write it back guarded by the revision the caller
// sent.
//
// The read in the middle is not a race, for the same reason it is not one
// there — the UPDATE still names the caller's revision, so a write that
// slipped in between makes this one match no rows and come back as a conflict.
func patchSettings(st *store.Store, currency string) http.HandlerFunc {
	e := settingsEntity(currency)
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		revision, ok := revisionFromBody(body)
		if !ok {
			writeError(w, http.StatusBadRequest, errBadRequest,
				"a patch must carry the revision it is editing, or it would overwrite whatever is there now")
			return
		}

		current, err := st.Settings(r.Context())
		if err != nil {
			writeStoreError(w, e, err)
			return
		}
		row, err := decodeBody[settingsBody, store.Settings](body, currency, current)
		if err != nil {
			writeError(w, http.StatusBadRequest, errBadRequest, err.Error())
			return
		}
		// The caller's revision, not the one just read. That is the conflict
		// check.
		row.Revision = revision

		out, err := st.UpdateSettings(r.Context(), row)
		if err != nil {
			writeStoreError(w, e, err)
			return
		}
		writeJSON(w, http.StatusOK, e.encode(out))
	}
}
