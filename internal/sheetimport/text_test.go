package sheetimport

import "testing"

func TestNormalizeStatusReadsTheInterfaceLanguages(t *testing.T) {
	// A task list in Dutch or Indonesian is an ordinary task list to the
	// person who wrote it, and every word it uses would otherwise import as
	// not-started with a warning beside it.
	for raw, want := range map[string]string{
		"klaar": "done", "afgerond": "done", "selesai": "done", "lunas": "done",
		"bezig": "in-progress", "loopt": "in-progress", "sedang berjalan": "in-progress",
		"nog niet gestart": "not-started", "te doen": "not-started", "belum": "not-started",
	} {
		got, ok := normalizeStatus(raw)
		if !ok {
			t.Errorf("normalizeStatus(%q) reported the word as unknown; want %q", raw, want)
			continue
		}
		if got != want {
			t.Errorf("normalizeStatus(%q) = %q; want %q", raw, got, want)
		}
	}

	// And a word that is none of the three is still reported rather than
	// guessed at: a cancelled task is not a finished one in any language.
	if _, ok := normalizeStatus("geannuleerd"); ok {
		t.Error("normalizeStatus mapped a cancelled task onto one of the three states")
	}
}
