package core_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
)

// TestVersionText is the one version-display rule (#458, #459): core's
// stamped display version first, nothing for a mod with no version - the
// importer's placeholder included - and the version itself otherwise.
func TestVersionText(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  *domain.Mod
		want string
	}{
		{"nil", nil, ""},
		{"ordinary", &domain.Mod{Version: "1.2.3"}, "1.2.3"},
		{"empty", &domain.Mod{}, ""},
		{"the importer's placeholder", &domain.Mod{Version: core.VersionUnknown}, ""},
		{"a Workshop revision", &domain.Mod{Version: "7987119735124793734", DisplayVersion: "2026-08-27"}, "2026-08-27"},
		{"a Workshop item with no date", &domain.Mod{Version: "7987119735124793734", DisplayVersion: core.NoRevisionDate}, core.NoRevisionDate},
		{"a version that merely contains the word", &domain.Mod{Version: "unknown-2"}, "unknown-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, core.VersionText(tc.mod))
		})
	}
}
