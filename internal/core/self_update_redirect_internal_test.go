package core

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A release download redirects from github.com to GitHub's object storage.
// releaseRedirectPolicy follows that, over https, to GitHub's own hosts,
// and refuses everything else - an https->http downgrade included.
func TestReleaseRedirectPolicy(t *testing.T) {
	via := func(raw string) []*http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		require.NoError(t, err)
		return []*http.Request{req}
	}
	from := via("https://github.com/DonovanMods/linux-mod-manager/releases/download/v2.2.0/checksums.txt")
	for _, ok := range []string{
		"https://github.com/x",
		"https://api.github.com/x",
		"https://objects.githubusercontent.com/github-production-release-asset/x",
		"https://release-assets.githubusercontent.com/x",
		"https://GitHub.com/x",
		"https://github.com:443/x",
	} {
		req, err := http.NewRequest(http.MethodGet, ok, nil)
		require.NoError(t, err)
		assert.NoError(t, releaseRedirectPolicy(req, from), ok)
	}
	for _, bad := range []string{
		"http://objects.githubusercontent.com/x",
		"http://github.com/x",
		"https://example.com/x",
		"https://github.com.evil.example/x",
		"https://evilgithub.com/x",
		"https://evilgithubusercontent.com/x",
		"https://githubusercontent.com.evil/x",
		"https://github.com:8443/x",
		"https://user@evil.example/x",
		"ftp://github.com/x",
	} {
		req, err := http.NewRequest(http.MethodGet, bad, nil)
		require.NoError(t, err)
		assert.Error(t, releaseRedirectPolicy(req, from), bad)
	}

	req, err := http.NewRequest(http.MethodGet, "https://github.com/x", nil)
	require.NoError(t, err)
	long := make([]*http.Request, 10)
	for i := range long {
		long[i] = from[0]
	}
	assert.Error(t, releaseRedirectPolicy(req, long), "at most ten redirects, as net/http's default")
}

// Every client NewSelfUpdater builds carries the policy, the caller's own
// included: a caller's client is copied, never changed.
func TestNewSelfUpdater_InstallsTheRedirectPolicy(t *testing.T) {
	opts := SelfUpdateOptions{APIBase: "http://127.0.0.1:1", DownloadBase: "http://127.0.0.1:1", DataDir: t.TempDir(), Version: "2.1.0"}
	u := NewSelfUpdater(opts)
	require.NotNil(t, u.httpClient.CheckRedirect)

	own := &http.Client{}
	opts.HTTPClient = own
	u = NewSelfUpdater(opts)
	require.NotNil(t, u.httpClient.CheckRedirect)
	assert.Nil(t, own.CheckRedirect, "the caller's client is not modified")
}
