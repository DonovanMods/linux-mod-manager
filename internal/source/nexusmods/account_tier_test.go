package nexusmods

// #601: the account tier and the free-account download refusal.
//
// testdata/v1-account-tier/ holds the v1 REST API's own answers, recorded
// 2026-11-12 from api.nexusmods.com with a free and a Premium key (issue
// #601's comments). The user id, name, email, key and profile URL are
// redacted, and each body is pretty-printed; nothing else is changed:
//
//   - validate-free.json          GET /v1/users/validate.json, free key    -> 200
//   - validate-premium.json       the same, Premium key: identical but for
//     is_premium/is_supporter (and their "?" duplicates), which are true
//   - download-link-free-403.json GET /v1/games/cyberpunk2077/mods/107/
//     files/168615/download_link.json, free key -> 403 (the API's own typo
//     "visting" and embedded indentation included). A Premium key gets 200
//     and the CDN mirrors.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	validatePath = "/v1/users/validate.json"
	linkPath     = "/v1/games/cyberpunk2077/mods/107/files/168615/download_link.json"
)

// tierFixture reads one testdata/v1-account-tier fixture.
func tierFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "v1-account-tier", name))
	require.NoError(t, err)
	return body
}

// tierServer answers path with status and body, counting the requests, and
// returns a NexusMods source fronted by it.
func tierServer(t *testing.T, path string, status int, body []byte) (*NexusMods, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	nm := New(srv.Client(), "testapikey")
	nm.client.SetBaseURL(srv.URL)
	return nm, &hits
}

func TestNexusMods_ImplementsAccountTierReporter(t *testing.T) {
	var _ source.AccountTierReporter = New(nil, "")
}

func TestNexusMods_AccountTier(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    source.Tier
	}{
		{"validate-free.json", source.TierFree},
		{"validate-premium.json", source.TierPremium},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			nm, _ := tierServer(t, validatePath, http.StatusOK, tierFixture(t, tc.fixture))
			tier, err := nm.AccountTier(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.want, tier)
		})
	}
}

// The tier is the account's, so it is asked once per key for the session.
func TestNexusMods_AccountTier_CachedForTheSession(t *testing.T) {
	nm, hits := tierServer(t, validatePath, http.StatusOK, tierFixture(t, "validate-free.json"))
	for range 3 {
		tier, err := nm.AccountTier(context.Background())
		require.NoError(t, err)
		assert.Equal(t, source.TierFree, tier)
	}
	assert.Equal(t, int32(1), hits.Load())

	// A new key may be a different account: asked again.
	nm.SetAPIKey("anotherkey")
	_, err := nm.AccountTier(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(2), hits.Load())
}

// A failed check is not remembered: the next call asks again.
func TestNexusMods_AccountTier_FailureIsNotCached(t *testing.T) {
	nm, hits := tierServer(t, validatePath, http.StatusInternalServerError, []byte(`{"message":"boom"}`))
	_, err := nm.AccountTier(context.Background())
	require.Error(t, err)
	_, err = nm.AccountTier(context.Background())
	require.Error(t, err)
	assert.Equal(t, int32(2), hits.Load())
}

func TestNexusMods_AccountTier_RejectedKeyIsAuthRequired(t *testing.T) {
	nm, _ := tierServer(t, validatePath, http.StatusUnauthorized, []byte(`{"message":"Please provide a valid API Key"}`))
	_, err := nm.AccountTier(context.Background())
	assert.ErrorIs(t, err, domain.ErrAuthRequired)
}

func TestNexusMods_AccountTier_NoKeyIsAuthRequired(t *testing.T) {
	nm, hits := tierServer(t, validatePath, http.StatusOK, tierFixture(t, "validate-free.json"))
	nm.SetAPIKey("")
	_, err := nm.AccountTier(context.Background())
	assert.ErrorIs(t, err, domain.ErrAuthRequired)
	assert.Zero(t, hits.Load(), "no key, no request")
}

// The recorded free-account refusal is an account restriction - and never a
// manual download, which core would record against the mod for everyone.
func TestNexusMods_GetDownloadURL_FreeAccountIsAccountRestricted(t *testing.T) {
	nm, _ := tierServer(t, linkPath, http.StatusForbidden, tierFixture(t, "download-link-free-403.json"))
	_, err := nm.GetDownloadURL(context.Background(), &domain.Mod{ID: "107", GameID: "cyberpunk2077"}, "168615")
	require.Error(t, err)

	assert.ErrorIs(t, err, source.ErrAccountRestricted)
	assert.False(t, errors.Is(err, source.ErrManualDownload))
	var restricted *source.AccountRestrictedError
	require.ErrorAs(t, err, &restricted)
	assert.Contains(t, restricted.Error(), "Premium")
	assert.NotContains(t, err.Error(), "visting", "the API's own garbled sentence is not what the user reads")
}

// Only the premium wording classifies: any other 403 stays what it was.
func TestNexusMods_GetDownloadURL_Other403IsNotAccountRestricted(t *testing.T) {
	for name, body := range map[string]string{
		"other message": `{"code": 403, "message": "This file has been removed"}`,
		"not json":      `Forbidden`,
		"empty":         ``,
	} {
		t.Run(name, func(t *testing.T) {
			nm, _ := tierServer(t, linkPath, http.StatusForbidden, []byte(body))
			_, err := nm.GetDownloadURL(context.Background(), &domain.Mod{ID: "107", GameID: "cyberpunk2077"}, "168615")
			require.Error(t, err)
			assert.False(t, errors.Is(err, source.ErrAccountRestricted))
			assert.True(t, isStatus(err, http.StatusForbidden), "the 403 is kept as data")
		})
	}
}

// The premium wording on any status but 403 is not the refusal either.
func TestNexusMods_GetDownloadURL_PremiumWordingWithout403IsNotAccountRestricted(t *testing.T) {
	nm, _ := tierServer(t, linkPath, http.StatusBadRequest, tierFixture(t, "download-link-free-403.json"))
	_, err := nm.GetDownloadURL(context.Background(), &domain.Mod{ID: "107", GameID: "cyberpunk2077"}, "168615")
	require.Error(t, err)
	assert.False(t, errors.Is(err, source.ErrAccountRestricted))
}

// A valid key is valid whatever the 200's body: only the tier needs it.
func TestNexusMods_ValidateKey_UndecodableBodyIsStillValid(t *testing.T) {
	nm, _ := tierServer(t, validatePath, http.StatusOK, []byte(`not json`))
	require.NoError(t, nm.ValidateKey(context.Background(), "testapikey"))
	_, err := nm.AccountTier(context.Background())
	assert.Error(t, err)
}

// lmm serve re-keys a source live: a re-key racing AccountTier (and every
// other reader of the key) must be safe under -race.
func TestNexusMods_AccountTier_ConcurrentReKeyIsRaceFree(t *testing.T) {
	nm, _ := tierServer(t, validatePath, http.StatusOK, tierFixture(t, "validate-free.json"))
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			nm.SetAPIKey(fmt.Sprintf("key-%d", i))
		}()
		go func() {
			defer wg.Done()
			_, _ = nm.AccountTier(ctx)
			_ = nm.IsAuthenticated()
		}()
	}
	wg.Wait()
	tier, err := nm.AccountTier(ctx)
	require.NoError(t, err)
	assert.Equal(t, source.TierFree, tier)
}
