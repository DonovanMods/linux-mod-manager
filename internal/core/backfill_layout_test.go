package core_test

// #441 review F11: the #431 backfill records its markers with the surgical
// editor, which declines some layouts (a comment inside a flow reference's
// braces, an alias). Before #441 the next lmm save rewrote such a file whole,
// so the retry that followed succeeded; saves now keep the author's layout,
// so the backfill stayed owed and a later switch into the profile turned the
// user's disabled mods back on. The backfill now does what a save does with
// a layout it cannot edit: rewrites the file whole, keeping the original
// beside it, and says so, naming both files.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uneditableA gives profile a's reference to "off" a layout the marker
// editor declines: a comment before the flow reference's closing brace.
func uneditableA(t *testing.T, f *backfillFixture) string {
	t.Helper()
	doc := mustRead(t, f.profilePath("a"))
	start := strings.Index(doc, "- source_id: src")
	end := strings.Index(doc, "version: \"1.0\"\n")
	require.True(t, start >= 0 && end > start, "unexpected profile layout:\n%s", doc)
	end += len("version: \"1.0\"\n")
	doc = doc[:start] + "- {source_id: src, mod_id: \"off\", version: \"1.0\" # kept by hand\n      }\n" + doc[end:]
	require.NoError(t, os.WriteFile(f.profilePath("a"), []byte(doc), 0o644))
	return doc
}

func TestBackfillProfileDisabledMarkers_ALayoutItCannotEditIsRewrittenAndKept(t *testing.T) {
	ctx := context.Background()

	t.Run("rewritten, with the original kept", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		f.row(t, "b", "x", true, false)
		original := uneditableA(t, f)
		require.NoError(t, os.WriteFile(f.profilePath("a")+".bak", []byte("an older backup\n"), 0o600))
		f.owe(t)

		report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
		require.NoError(t, err)
		require.NotNil(t, report)
		assert.Empty(t, report.Skipped, "nothing is left owed")
		require.Len(t, report.Marked, 1)
		assert.Equal(t, "off", report.Marked[0].ModID)
		backup := f.profilePath("a") + ".bak.1"
		assert.Equal(t, []core.ProfileBackfillRewrite{{GameID: "g1", Profile: "a", File: f.profilePath("a"), Backup: backup}}, report.Rewritten)
		assert.Equal(t, original, mustRead(t, backup), "the file as the user wrote it")
		assert.Equal(t, "an older backup\n", mustRead(t, f.profilePath("a")+".bak"), "an existing backup is never replaced")
		assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))

		notice := f.warnings.String()
		assert.Contains(t, notice, "rewrote "+f.profilePath("a")+" whole")
		assert.Contains(t, notice, "kept as "+backup)
		owed, err := f.svc.ProfileDisabledBackfillOwedForTest(ctx)
		require.NoError(t, err)
		assert.Empty(t, owed)

		// What the rewrite is for: switching back into a leaves off off.
		f.switchTo(t, "b")
		f.switchTo(t, "a")
		f.assertOffAndUndeployed(t)
	})

	t.Run("a file it cannot write stays owed, with no backup", func(t *testing.T) {
		skipAsRoot(t)
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		f.row(t, "b", "x", true, false)
		original := uneditableA(t, f)
		require.NoError(t, os.Chmod(f.profilePath("a"), 0o444))
		f.owe(t)

		report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
		require.NoError(t, err)
		require.Len(t, report.Skipped, 1)
		assert.Empty(t, report.Rewritten)
		assert.Equal(t, original, mustRead(t, f.profilePath("a")))
		assert.NoFileExists(t, f.profilePath("a")+".bak")
	})
}
