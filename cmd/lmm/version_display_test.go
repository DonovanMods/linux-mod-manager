package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The content id every case below refuses to print: the real manifest of the
// fixture Workshop item, 19 digits, and the item's version IDENTITY (#269).
const testContentID = "7987119735124793734"

var testRevision = time.Unix(1764767935, 0).UTC() // 2025-12-03

// TestDisplayModVersion is the CLI twin of the SPA's own version-helper tests:
// the rule lives in one place, so it is pinned in one place, and the call
// sites only have to be shown to call it.
func TestDisplayModVersion(t *testing.T) {
	tests := []struct {
		name      string
		external  bool
		version   string
		updatedAt time.Time
		want      string
	}{
		{"ordinary mod prints its version", false, "1.2.3", time.Time{}, "1.2.3"},
		{"ordinary mod ignores any date it carries", false, "1.2.3", testRevision, "1.2.3"},
		{"external mod prints the revision date", true, testContentID, testRevision, "2025-12-03"},
		{"external mod with no date prints the table's own dash", true, testContentID, time.Time{}, "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := displayModVersion(tt.external, tt.version, tt.updatedAt)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, testContentID)
		})
	}
}

// TestDisplayUpdateTarget: lmm has no date for a revision it has not seen, so
// "newer" is the whole of what it can truthfully say - and all a user can act
// on, since Steam applies the update itself either way.
func TestDisplayUpdateTarget(t *testing.T) {
	assert.Equal(t, "2.0", displayUpdateTarget(false, "2.0"))
	assert.Equal(t, "newer", displayUpdateTarget(true, "8888888888888888888"))
}

// TestDisplayLockTarget pins the branch the CLI's own `lmm mod lock` cannot
// reach (its capability gate refuses a Versions:false source first) but
// `lmm mod show`, `lmm list -v`, `lmm mod set-update --pin` and `lmm serve`'s
// lock route all can: a lock names ONE revision, and for a Workshop item that
// revision's only name is the content id, so there is nothing to print.
func TestDisplayLockTarget(t *testing.T) {
	assert.Equal(t, "v1.2.3", displayLockTarget(false, "1.2.3"))
	assert.Equal(t, "", displayLockTarget(true, testContentID))
}

// TestDisplayRevision covers the wording `lmm mod show` gives its header and
// its Installed: line - one rule, so the two can never disagree about the
// same instant.
func TestDisplayRevision(t *testing.T) {
	assert.Equal(t, "revision of 2025-12-03", displayRevision(testRevision))
	assert.Equal(t, "revision of unknown", displayRevision(time.Time{}),
		"a row installed before lmm recorded the date")
}
