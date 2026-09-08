package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loginFixtureSource is an auth-capable double with NO live validator -
// the "stored, validated on first use" shape a custom source has.
type loginFixtureSource struct {
	id   string
	auth bool
}

func (s *loginFixtureSource) ID() string      { return s.id }
func (s *loginFixtureSource) Name() string    { return "Fixture " + s.id }
func (s *loginFixtureSource) AuthURL() string { return "" }
func (s *loginFixtureSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}
func (s *loginFixtureSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (s *loginFixtureSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, source.ErrNotSupported
}
func (s *loginFixtureSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *loginFixtureSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (s *loginFixtureSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", source.ErrNotSupported
}
func (s *loginFixtureSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}
func (s *loginFixtureSource) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Auth: s.auth}
}

// loginValidatingSource adds the live check, with a verdict the test sets.
type loginValidatingSource struct {
	loginFixtureSource
	err error
}

func (s *loginValidatingSource) ValidateKey(context.Context, string) error { return s.err }

func newLoginService(t *testing.T, srcs ...source.ModSource) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	for _, src := range srcs {
		svc.RegisterSource(src)
	}
	return svc
}

// TestIsAuthCapableSource pins the bool `lmm serve` asks with, since it may
// not name source.CapabilitiesOf itself (its boundary ratchet).
func TestIsAuthCapableSource(t *testing.T) {
	svc := newLoginService(t,
		&loginFixtureSource{id: "acme", auth: true},
		&loginFixtureSource{id: "authless"},
	)

	assert.True(t, app.IsAuthCapableSource(svc, "acme"))
	assert.False(t, app.IsAuthCapableSource(svc, "authless"), "a source declaring no auth is not auth-capable")
	assert.False(t, app.IsAuthCapableSource(svc, "nope"), "an unregistered id is not auth-capable")
}

// TestHasKeyValidator pins the question a frontend must answer BEFORE it
// has a key: does this source actually check one?
func TestHasKeyValidator(t *testing.T) {
	svc := newLoginService(t,
		&loginFixtureSource{id: "acme", auth: true},
		&loginValidatingSource{loginFixtureSource: loginFixtureSource{id: "picky", auth: true}},
	)

	assert.False(t, app.HasKeyValidator(svc, "acme"))
	assert.True(t, app.HasKeyValidator(svc, "picky"))
	assert.False(t, app.HasKeyValidator(svc, "nope"))
}

// TestValidateSourceKey pins all four outcomes the two frontends branch on.
func TestValidateSourceKey(t *testing.T) {
	rejected := errors.New("key rejected by the source")
	svc := newLoginService(t,
		&loginFixtureSource{id: "acme", auth: true},
		&loginFixtureSource{id: "authless"},
		&loginValidatingSource{loginFixtureSource: loginFixtureSource{id: "picky", auth: true}},
		&loginValidatingSource{loginFixtureSource: loginFixtureSource{id: "grumpy", auth: true}, err: rejected},
	)

	t.Run("no validator: stored unvalidated, not an error", func(t *testing.T) {
		validated, err := app.ValidateSourceKey(context.Background(), svc, "acme", "any-key")
		require.NoError(t, err)
		assert.False(t, validated, "nothing was actually checked, and saying otherwise would be a fabricated result")
	})

	t.Run("validator accepts", func(t *testing.T) {
		validated, err := app.ValidateSourceKey(context.Background(), svc, "picky", "good-key")
		require.NoError(t, err)
		assert.True(t, validated)
	})

	t.Run("validator refuses: the source's own error, unwrapped", func(t *testing.T) {
		validated, err := app.ValidateSourceKey(context.Background(), svc, "grumpy", "bad-key")
		require.ErrorIs(t, err, rejected, "the caller classifies and renders the validator's own error")
		assert.False(t, validated)
	})

	t.Run("not auth-capable is typed", func(t *testing.T) {
		for _, id := range []string{"authless", "nope"} {
			_, err := app.ValidateSourceKey(context.Background(), svc, id, "k")
			require.ErrorIs(t, err, app.ErrSourceNotAuthCapable, "id %q", id)
		}
	})
}

// TestValidateSourceKey_NeverEchoesTheKey pins the secret rule at the seam
// itself: whatever a caller does with the result, this function's own error
// must never carry the key.
func TestValidateSourceKey_NeverEchoesTheKey(t *testing.T) {
	const secret = "sk-live-CANARY-9f3b2a71"
	svc := newLoginService(t,
		&loginValidatingSource{
			loginFixtureSource: loginFixtureSource{id: "grumpy", auth: true},
			err:                errors.New("key rejected"),
		},
		&loginFixtureSource{id: "authless"},
	)

	for _, id := range []string{"grumpy", "authless", "nope"} {
		_, err := app.ValidateSourceKey(context.Background(), svc, id, secret)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret, "source %q's error must never carry the key", id)
	}
}
