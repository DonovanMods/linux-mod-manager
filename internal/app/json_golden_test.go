package app

// The app-side twin of internal/core's json_golden_test.go: app.SourceInfo
// is a wire-contract type like every core query type (it is what `lmm source
// list --json` emits from Unit O on), so it gets the same frozen golden
// treatment even though it lives outside core - the source DEFINITIONS and
// their construction errors, which only app can see, are half of what it
// reports (see SourceInfos' doc comment).

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// goldenStamp is the fixed instant every credential timestamp in these
// goldens carries (#79 gave AuthSourceStatus created_at/updated_at). A
// literal keeps the files byte-stable; the real values come from the
// database, which no golden can pin.
var goldenStamp = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

// The fingerprints these goldens carry, derived rather than typed. The
// field is documented as the first 8 hex of a key's SHA-256, and the
// literals here were arbitrary - one of them was in fact the first 8 hex of
// MD5("hello"), which a maintainer reading the file could reasonably take
// for a real example (review, Minor 10). db.TokenFingerprint is what
// produces the real thing, so these ARE real, and stay byte-stable because
// the inputs are fixed.
var (
	goldenStoredFingerprint = db.TokenFingerprint("golden-nexusmods-stored-key")
	goldenEnvFingerprint    = db.TokenFingerprint("golden-my-repo-env-key")
	goldenOrphanFingerprint = db.TokenFingerprint("golden-ghost-repo-orphan-key")
)

// updateAppJSONGoldens re-records internal/app's JSON contract goldens. Run
// ONCE:
//
//	go test ./internal/app/ -run TestAppJSONGoldens -update-app-json-goldens
//
// After that the files are frozen: they pin the exact wire shape any future
// JSON frontend depends on, so a field/tag change shows up as a diff here.
// Named -update-app-json-goldens for the same reason core's flag is named
// -update-json-goldens: one distinct flag name per test binary.
var updateAppJSONGoldens = flag.Bool("update-app-json-goldens", false, "rewrite internal/app/testdata/json/*.golden")

func TestAppJSONGoldens(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{
			// A registered, in-use source: no error, so `error` is dropped
			// and only `in_use` survives from the two omitempty keys.
			"source_info",
			SourceInfo{
				ID: "nexusmods", Name: "NexusMods", Type: "built-in",
				Auth:         AuthAuthenticated,
				Capabilities: []string{"search", "deps", "updates", "auth", "versions"},
				InUse:        true,
			},
		},
		{
			// An error row: Err never reaches the wire (json:"-"), its
			// message does. Keyed by the definition's own id, not a
			// filename - this is the "id already in use" COLLISION kind
			// (final review, Minor #6 / #301: the old fixture paired a
			// filename key with a collision message, mixing the two error
			// kinds SourceInfos actually produces).
			"source_info_error",
			newSourceInfoError("nexusmods", errors.New("id already in use")),
		},
		{
			"source_probe_result",
			SourceProbeResult{OK: true, Summary: "ok — 3 mod(s) visible"},
		},
		{
			// `lmm source validate --probe <file> --json`'s document (#309):
			// a valid, probed definition - the fullest populated shape (an
			// invalid file's report is a subset with no id/type/probe).
			"source_validation_report",
			SourceValidationReport{
				Path: "/config/sources/my-mods.yaml", ID: "my-mods", Type: "directory", Valid: true,
				Probe: &SourceProbeResult{OK: true, Summary: "ok — 3 mod(s) visible"},
			},
		},
		{
			"auth_source_status",
			AuthSourceStatus{ID: "nexusmods", Name: "NexusMods", Authenticated: true, Via: "stored", KeyFingerprint: goldenStoredFingerprint, CreatedAt: goldenStamp, UpdatedAt: goldenStamp},
		},
		{
			"orphaned_token",
			OrphanedToken{ID: "ghost-repo", Reason: "not_registered", KeyFingerprint: goldenOrphanFingerprint},
		},
		{
			// `lmm auth status --json`'s document (#309): one authenticated
			// row via a stored token, one via env, one never authenticated,
			// plus both OrphanedToken reasons. RestartRequired is set here
			// (#334) to pin its key's wire shape; the empty report below
			// pins that an unset one carries no key at all.
			"auth_status_report",
			AuthStatusReport{
				RestartRequired: true,
				Sources: []AuthSourceStatus{
					{ID: "keyless-repo", Name: "Keyless"},
					{ID: "my-repo", Name: "My Repo", Authenticated: true, Via: "env", EnvVar: "LMM_MY_REPO_API_KEY", KeyMasked: "sup...789", KeyFingerprint: goldenEnvFingerprint},
					{ID: "nexusmods", Name: "NexusMods", Authenticated: true, Via: "stored", KeyFingerprint: goldenStoredFingerprint, CreatedAt: goldenStamp, UpdatedAt: goldenStamp},
				},
				Orphaned: []OrphanedToken{
					{ID: "ghost-repo", Reason: "not_registered", KeyFingerprint: goldenOrphanFingerprint},
					{ID: "local-mods", Reason: "auth_not_declared", Unreadable: true},
				},
			},
		},
		{
			// Sources/Orphaned deliberately left nil (no `omitempty` on
			// either tag, task A review round 1, Minor 6) to pin that a
			// report with nothing to say marshals both as "[]", not "null".
			"auth_status_report_empty",
			AuthStatusReport{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.value, json.Deterministic(true), jsontext.WithIndent("  "))
			require.NoError(t, err)

			path := filepath.Join("testdata", "json", tt.name+".golden")
			if *updateAppJSONGoldens {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, append(b, '\n'), 0o644))
				return
			}

			want, err := os.ReadFile(path)
			require.NoError(t, err, "golden %s missing - record it with -update-app-json-goldens BEFORE relying on this test", path)
			require.Equal(t, string(want), string(b)+"\n", "%s JSON shape drifted from the recorded golden", tt.name)
		})
	}
}
