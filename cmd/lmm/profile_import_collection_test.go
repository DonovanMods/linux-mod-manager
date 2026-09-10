package main

import (
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImportRefLine is #365 on the CLI side: `lmm profile import`'s two
// bucket listings must never print a Workshop content id where a version
// goes. The rule itself lives in displayModVersion; this pins that this
// call site uses it, and how it words the result.
func TestImportRefLine(t *testing.T) {
	revision := time.Unix(1764767935, 0).UTC() // 2025-12-03

	tests := []struct {
		name string
		ref  domain.ModReference
		want string
	}{
		{
			"an ordinary ref keeps the v prefix",
			domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.2.3"},
			"nexusmods:42 v1.2.3",
		},
		{
			"a ref with no version at all prints none",
			domain.ModReference{SourceID: "nexusmods", ModID: "42"},
			"nexusmods:42",
		},
		{
			"an external ref prints its revision date, not the content id",
			domain.ModReference{
				SourceID: "steamworkshop", ModID: "3617086610",
				Version: testContentID, External: true, UpdatedAt: revision,
			},
			"steamworkshop:3617086610 (2025-12-03)",
		},
		{
			"an external ref with no recorded date prints no version",
			domain.ModReference{
				SourceID: "steamworkshop", ModID: "3617086610",
				Version: testContentID, External: true,
			},
			"steamworkshop:3617086610",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planRefLine(tt.ref)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, testContentID)
		})
	}
}

// TestProfileImportInputsAreMutuallyExclusive pins the three ways `lmm
// profile import`'s two inputs can be combined wrongly. Each is refused
// BEFORE any service is opened, so none of them touches disk.
func TestProfileImportInputsAreMutuallyExclusive(t *testing.T) {
	t.Run("a collection and a file together", func(t *testing.T) {
		restore := setImportFlags(t, "2500900001", "")
		defer restore()
		err := runProfileImport(profileImportCmd, []string{"survival.yaml"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot also take a file path")
	})

	t.Run("neither a collection nor a file", func(t *testing.T) {
		restore := setImportFlags(t, "", "")
		defer restore()
		err := runProfileImport(profileImportCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--workshop-collection")
	})

	t.Run("--as without a collection", func(t *testing.T) {
		restore := setImportFlags(t, "", "ships")
		defer restore()
		err := runProfileImport(profileImportCmd, []string{"survival.yaml"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "a profile FILE names its own profile")
	})
}

// setImportFlags sets the two package-level flag variables the command
// reads and restores them, the convention every other command test here
// follows for cobra's singleton flag vars.
func setImportFlags(t *testing.T, collection, as string) func() {
	t.Helper()
	oldCollection, oldAs := profileImportCollection, profileImportAs
	profileImportCollection, profileImportAs = collection, as
	return func() { profileImportCollection, profileImportAs = oldCollection, oldAs }
}
