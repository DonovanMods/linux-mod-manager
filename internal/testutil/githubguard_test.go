package testutil_test

import (
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardGitHub_RefusesBeforeTheNetwork: a request to api.github.com
// through the default transport fails without being sent, and is recorded
// for TestMain to fail the run on (#568).
func TestGuardGitHub_RefusesBeforeTheNetwork(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })

	attempts := testutil.GuardGitHub()
	require.Empty(t, attempts())

	resp, err := http.Get("https://api.github.com/repos/DonovanMods/linux-mod-manager/releases/latest")
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.ErrorIs(t, err, testutil.ErrGitHubRefused)
	assert.Equal(t, []string{"GET https://api.github.com/repos/DonovanMods/linux-mod-manager/releases/latest"}, attempts())
}

// TestGuardGitHub_RefusesReleaseDownloads: `lmm self-update` (#569)
// downloads from github.com, which the guard refuses as it does the API.
func TestGuardGitHub_RefusesReleaseDownloads(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })

	attempts := testutil.GuardGitHub()
	resp, err := http.Get("https://github.com/DonovanMods/linux-mod-manager/releases/download/v2.1.0/checksums.txt")
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.ErrorIs(t, err, testutil.ErrGitHubRefused)
	assert.Len(t, attempts(), 1)
}
