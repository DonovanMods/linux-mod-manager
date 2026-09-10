// This file guards how a test outside the steamworkshop package is allowed
// to build the REAL Steam Workshop source.
//
// steamworkshop.Options.BaseURL empty means Valve's production Web API, so
// a test that forgets it does not fail - it silently calls Valve. The
// source's own package guards that with a scan of its test files, but W3
// put two more constructions in cmd/lmm and internal/serve, where that scan
// cannot see them. WorkshopOptions is the one door, and it refuses an empty
// BaseURL; steamworkshop's own client_test.go ratchets that every test file
// outside its package comes through it.
package testutil

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
)

// WorkshopOptions builds the Options a test may construct the real Steam
// Workshop source with: an httptest base URL that MUST be given, plus a
// throwaway cache root and a throwaway Steam library so neither the
// metadata cache, steamcmd's isolated home, nor Steam-installation
// discovery can reach anything the user owns.
func WorkshopOptions(t *testing.T, baseURL string) steamworkshop.Options {
	t.Helper()
	if baseURL == "" {
		t.Fatal("WorkshopOptions: BaseURL is empty, which is Valve's production Web API - point it at an httptest server")
	}
	return steamworkshop.Options{
		BaseURL:    baseURL,
		CacheDir:   t.TempDir(),
		SteamRoots: []string{t.TempDir()},
	}
}
