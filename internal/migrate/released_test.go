package migrate

import "testing"

// releasedChecksums pins the bytes of every migration that has shipped in a
// tag, because the runner's own check only fires against a database that has
// already applied one. Every test here starts from an empty database and so
// agrees with whatever the files happen to contain; the deployments that would
// refuse the release are somewhere else entirely.
//
// Add a line when a migration is released. Never change one: a file that has
// shipped is frozen, and updating the hash to match an edit only moves the
// failure from this test to somebody's startup.
var releasedChecksums = map[string]string{
	"0001_users.sql":                        "4fe1b21503eabd54e0ea4d5d0d1457c8da71e9211d222a2a7739e320d03434b7",
	"0002_settings.sql":                     "f3136908540ea12ed9385a1ad4b5cb394bf5fb3c35892e9838116ec4accfb56a",
	"0003_plan.sql":                         "932387a2df61c35cba659942ad876cf58260500755e19662a762af011306e14d",
	"0004_user_ui_prefs.sql":                "61c306485f07a1fc013d99838962b3639e0ea2d36892552387494a07fde76939",
	"0005_programme_entries.sql":            "b5a7f287dcecb5cf1a6460d165bb1a7598f935eac8eb23995283f934acdef747",
	"0006_reminders_sent.sql":               "e6f45a2e89aea069f0ed9fc0402781f494e86437189516875b4bf0c6d95aefa9",
	"0007_audit.sql":                        "afca63222ceb7bab23129cefb7addb296467399c94692b8d311828b05b71a37e",
	"0008_sessions_and_password_tokens.sql": "4b398aa2d10cdd79bbe88ed5ac0b3f4ddd0b93121984e88e60ca6656c03d7cae",
	"0009_phase_revision.sql":               "45e298146a70c5d64ce44162fcdd9d3559c4afa3cd0bd3212554fbc5931e269a",
	"0010_push_subscriptions.sql":           "4e7786cbff40d368d78f0b40dbac6b3ae9292dbe1dd6a07586e5af78fb276324",
	"0011_passkeys.sql":                     "2098c39a7c6001d2ea51c67a89e8968722b905e577076578a5f3e76a2238bd15",
	"0012_attachments.sql":                  "e79e48b0b6542cee5b1227c323a5767f20e267f36957cdd395a0fef1148b9a1c",
	"0013_change_log_redaction.sql":         "e91f8a54a2656b605a381764b929de10b3e341eb09f1c1c94f3d30c042372de7",
}

// TestReleasedMigrationsAreUnchanged is the guard behind the house rule, and
// it needs no database, so it runs in the everyday -short loop. Without it the
// most innocent change imaginable — a typo fixed in a comment, a line ending
// flipped by a checkout — passes every test here and then refuses to start
// against every database that already has the old bytes recorded.
func TestReleasedMigrationsAreUnchanged(t *testing.T) {
	ms, err := load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	embedded := make(map[string]string, len(ms))
	for _, m := range ms {
		embedded[m.filename] = m.checksum
	}

	for filename, want := range releasedChecksums {
		got, ok := embedded[filename]
		if !ok {
			t.Errorf("%s has shipped but is no longer embedded; a migration that has been applied somewhere cannot be taken back", filename)
			continue
		}
		if got != want {
			t.Errorf("%s was modified after it shipped (released %s, file %s); comments and whitespace are part of the checksum, so correct a stale one with COMMENT ON in a new migration, never in place", filename, want, got)
		}
	}

	for filename := range embedded {
		if _, ok := releasedChecksums[filename]; !ok {
			t.Errorf("%s is not pinned; add it to releasedChecksums in the pull request that adds the migration", filename)
		}
	}
}
