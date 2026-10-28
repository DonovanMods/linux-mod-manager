package thunderstore_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
)

// sourceUnderTest bundles a Source with the server, cache root and clock it
// was built over, so a test can reach any of them without a five-value
// return at every call site.
type sourceUnderTest struct {
	t        *testing.T
	src      *thunderstore.Source
	srv      *indexServer
	cacheDir string
	clock    *testClock
}
