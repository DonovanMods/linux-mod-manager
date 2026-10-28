package core_test

// #441 review F6/F10: what a profile save did beyond writing the document -
// the copy it kept of a layout it had to rewrite, a write it could not make
// atomically - is printed on the Service's warning channel, naming the
// files, whichever flow made the save.

import (
	"context"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileSave_SaysWhereItKeptTheOriginal(t *testing.T) {
	f := newBackfillFixture(t)
	path := f.profilePath("b")
	original := "# mine\nname: b\ngame_id: g1\nmods: [{source_id: src, mod_id: x}]\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	require.NoError(t, f.svc.NewProfileManager().AddMod(context.Background(), f.game.ID, "b",
		domain.ModReference{SourceID: "src", ModID: "y"}))

	notice := f.warnings.String()
	assert.Contains(t, notice, "warning: rewrote "+path+" whole")
	assert.Contains(t, notice, path+".bak")
	kept, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	assert.Equal(t, original, string(kept))
}
