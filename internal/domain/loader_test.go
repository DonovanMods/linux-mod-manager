package domain_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseLoaderRuntime follows ParseDeployMode's fail-loud contract: an
// empty string is "not yet answered" (detection fills it in later) and any
// other unrecognised value is ok=false so the caller can name the field.
func TestParseLoaderRuntime(t *testing.T) {
	for in, want := range map[string]domain.LoaderRuntime{
		"":       domain.LoaderRuntimeUnknown,
		"mono":   domain.LoaderRuntimeMono,
		"il2cpp": domain.LoaderRuntimeIL2CPP,
	} {
		got, ok := domain.ParseLoaderRuntime(in)
		assert.True(t, ok, "%q", in)
		assert.Equal(t, want, got, "%q", in)
	}
	_, ok := domain.ParseLoaderRuntime("coreclr")
	assert.False(t, ok, "an unrecognised runtime must fail loud, not default")
	assert.Equal(t, "mono, il2cpp", domain.ValidLoaderRuntimes)
}

// TestParseLoaderBootstrap is the same contract for the Linux bootstrap
// mode, which is the difference between run_bepinex.sh and a winhttp proxy
// DLL - two genuinely different mechanisms (spike §1.2), not a preference.
func TestParseLoaderBootstrap(t *testing.T) {
	for in, want := range map[string]domain.LoaderBootstrap{
		"":       domain.LoaderBootstrapUnknown,
		"native": domain.LoaderBootstrapNative,
		"proton": domain.LoaderBootstrapProton,
	} {
		got, ok := domain.ParseLoaderBootstrap(in)
		assert.True(t, ok, "%q", in)
		assert.Equal(t, want, got, "%q", in)
	}
	_, ok := domain.ParseLoaderBootstrap("wine")
	assert.False(t, ok)
	assert.Equal(t, "native, proton", domain.ValidLoaderBootstraps)
}

// TestLoaderRuntimeTextRoundTrip: both enums marshal as their wire NAME, so
// a games.yaml value, a --json document and an /api/v1 response all say
// "il2cpp" rather than "2".
func TestLoaderRuntimeTextRoundTrip(t *testing.T) {
	for _, r := range []domain.LoaderRuntime{domain.LoaderRuntimeUnknown, domain.LoaderRuntimeMono, domain.LoaderRuntimeIL2CPP} {
		b, err := r.MarshalText()
		require.NoError(t, err)
		var back domain.LoaderRuntime
		require.NoError(t, back.UnmarshalText(b))
		assert.Equal(t, r, back)
	}
	var bad domain.LoaderRuntime
	assert.Error(t, bad.UnmarshalText([]byte("coreclr")))

	for _, b := range []domain.LoaderBootstrap{domain.LoaderBootstrapUnknown, domain.LoaderBootstrapNative, domain.LoaderBootstrapProton} {
		text, err := b.MarshalText()
		require.NoError(t, err)
		var back domain.LoaderBootstrap
		require.NoError(t, back.UnmarshalText(text))
		assert.Equal(t, b, back)
	}
}

// TestGameLoaderIsBepInEx: kind is a free string so a second loader
// (MelonLoader is the obvious one) costs nothing to add, and the comparison
// is case-insensitive because it is a value a user types into games.yaml.
func TestGameLoaderIsBepInEx(t *testing.T) {
	assert.True(t, (&domain.GameLoader{Kind: "bepinex"}).IsBepInEx())
	assert.True(t, (&domain.GameLoader{Kind: "BepInEx"}).IsBepInEx())
	assert.False(t, (&domain.GameLoader{Kind: "melonloader"}).IsBepInEx())

	var none *domain.GameLoader
	assert.False(t, none.IsBepInEx(), "a game with no loader block declares no loader")
}

// TestGameDeclaresBepInEx is the question every BepInEx rule asks of a game,
// nil-safe on the pointer so no caller has to branch twice.
func TestGameDeclaresBepInEx(t *testing.T) {
	assert.False(t, (&domain.Game{ID: "g"}).DeclaresBepInEx())
	assert.True(t, (&domain.Game{ID: "g", Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx}}).DeclaresBepInEx())
}
