package sheetimport

import (
	"strings"
	"testing"
)

func TestLoadConfigRejectsMistakes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// A key that does nothing is a typo, and a typo in a mapping file
			// is a column silently left out of the import.
			name: "unknown key",
			in:   `{"tables":[{"sheet":"Plan","colums":{"item":"A"}}]}`,
			want: "unknown field",
		},
		{
			name: "unknown field name",
			in:   `{"tables":[{"sheet":"Plan","columns":{"prince":"A"}}]}`,
			want: `unknown field "prince"`,
		},
		{
			name: "field from the wrong kind",
			in:   `{"tables":[{"kind":"tasks","columns":{"name":"A","paid":"B"}}]}`,
			want: `unknown field "paid"`,
		},
		{
			name: "unknown kind",
			in:   `{"tables":[{"kind":"budjet","columns":{"item":"A"}}]}`,
			want: "unknown kind",
		},
		{
			name: "two fields on one column",
			in:   `{"tables":[{"columns":{"item":"A","vendor":"A"}}]}`,
			want: "mapped to both",
		},
		{
			// Both mapped would leave the line total ambiguous, and an
			// ambiguous budget is the thing being fixed here.
			name: "unit and total together",
			in:   `{"tables":[{"columns":{"item":"A","unit":"B","total":"C"}}]}`,
			want: "not both",
		},
		{
			name: "backwards row range",
			in:   `{"tables":[{"firstRow":30,"lastRow":10,"columns":{"item":"A"}}]}`,
			want: "before firstRow",
		},
		{
			name: "no tables",
			in:   `{"decimal":"dot"}`,
			want: "no tables",
		},
		{
			name: "bad decimal mode",
			in:   `{"decimal":"point","tables":[{"columns":{"item":"A"}}]}`,
			want: "unknown decimal mode",
		},
		{
			name: "parent item on a task table",
			in:   `{"tables":[{"kind":"tasks","parentItem":"Catering","columns":{"name":"A"}}]}`,
			want: "only applies to a budget table",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadConfig(strings.NewReader(c.in))
			if err == nil {
				t.Fatalf("LoadConfig(%s) accepted a broken mapping", c.in)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v; want it to mention %q", err, c.want)
			}
		})
	}
}

func TestLoadConfigRoundTrip(t *testing.T) {
	in := `{
	  "decimal": "comma",
	  "totalKeywords": ["total", "totaal"],
	  "ceiling": 50000000,
	  "tables": [
	    {
	      "name": "costs",
	      "sheet": "Plan",
	      "headerRow": 12,
	      "firstRow": 13,
	      "lastRow": 20,
	      "columns": { "item": "A", "qty": "B", "total": "C", "paid": "D" }
	    }
	  ]
	}`
	cfg, err := LoadConfig(strings.NewReader(in))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Ceiling != 50000000 || len(cfg.Tables) != 1 {
		t.Fatalf("parsed %+v", cfg)
	}
	if got := cfg.Tables[0].totalKeywords(cfg); len(got) != 2 {
		t.Errorf("keywords = %v; want the file's own list", got)
	}

	var out strings.Builder
	if err := cfg.WriteJSON(&out); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	// What -detect prints has to be loadable again without editing.
	if _, err := LoadConfig(strings.NewReader(out.String())); err != nil {
		t.Fatalf("a written mapping must load again: %v\n%s", err, out.String())
	}
}

func TestColumnLettersAndNumbersAreEquivalent(t *testing.T) {
	letters := &Config{Tables: []Table{{Columns: map[string]string{"item": "A", "total": "C"}}}}
	numbers := &Config{Tables: []Table{{Columns: map[string]string{"item": "1", "total": "3"}}}}
	for _, cfg := range []*Config{letters, numbers} {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		col, ok := cfg.Tables[0].column("total")
		if !ok || col != 3 {
			t.Errorf("total column = %d, %v; want 3", col, ok)
		}
	}
}
