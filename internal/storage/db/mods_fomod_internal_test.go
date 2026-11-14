package db

import (
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPreviousFomod_PerFileReadsAsNoneToTheSingleForm (#634): the per-file
// encoding is an object an older lmm - which decodes the column straight
// into the single form - reads as no recorded choices, not as an error,
// so it needs no schema migration.
func TestPreviousFomod_PerFileReadsAsNoneToTheSingleForm(t *testing.T) {
	raw, err := encodeFomod(domain.FomodRecord{Files: domain.FomodFiles{"f1": {ConfigSHA256: "aa"}}})
	require.NoError(t, err)
	var old domain.FomodChoices
	require.NoError(t, json.Unmarshal([]byte(raw), &old))
	assert.True(t, old.IsZero(), raw)

	single, err := encodeFomod(domain.FomodRecord{Choices: domain.FomodChoices{ConfigSHA256: "aa", Steps: []fomod.StepChoice{}}})
	require.NoError(t, err)
	assert.Equal(t, `{"config_sha256":"aa","steps":[]}`, single, "the single form is #614's encoding, byte for byte")
}
