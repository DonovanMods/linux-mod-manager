package adapter_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubAdapter is a minimal GameAdapter with no optional capabilities - the
// shape internal/adapter/generic has, restated here so this package's tests
// never depend on a concrete adapter package.
type stubAdapter struct {
	id     string
	layout adapter.Layout
	err    error
}

func (s stubAdapter) ID() string    { return s.id }
func (s stubAdapter) Label() string { return s.id }
func (s stubAdapter) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return s.layout, s.err
}

// capableAdapter implements every optional capability, so the dispatch
// helpers can be shown to REACH an implementation as well as to skip a
// missing one.
type capableAdapter struct {
	stubAdapter
	route    adapter.FileRoute
	precond  error
	findings []adapter.Finding
	notes    []adapter.GuidanceNote
}

func (c capableAdapter) RouteFile(*domain.Game, string) adapter.FileRoute { return c.route }
func (c capableAdapter) CheckPreconditions(*domain.Game, []domain.InstalledMod) error {
	return c.precond
}
func (c capableAdapter) Verify(context.Context, adapter.VerifyRequest) ([]adapter.Finding, error) {
	return c.findings, nil
}
func (c capableAdapter) Guidance(*domain.Game) []adapter.GuidanceNote { return c.notes }

func TestRegistryResolve(t *testing.T) {
	t.Run("an empty name resolves to the generic default", func(t *testing.T) {
		r := adapter.NewRegistry()
		gen := stubAdapter{id: adapter.GenericID}
		r.Register(gen)

		got, err := r.Resolve("")
		require.NoError(t, err)
		assert.Equal(t, adapter.GenericID, got.ID())
	})

	t.Run("a fresh registry already answers the default with the built-in identity", func(t *testing.T) {
		// This is what keeps a Service built without the composition root
		// - every core test - resolving the SAME identity the product
		// does, and what makes "Resolve never returns nil" true.
		got, err := adapter.NewRegistry().Resolve("")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, adapter.GenericID, got.ID())

		byName, err := adapter.NewRegistry().Resolve(adapter.GenericID)
		require.NoError(t, err)
		assert.Equal(t, adapter.GenericID, byName.ID())
	})

	t.Run("an unregistered explicit name fails loud, naming the registered set", func(t *testing.T) {
		r := adapter.NewRegistry()
		r.Register(stubAdapter{id: adapter.GenericID})
		r.Register(stubAdapter{id: "icarus"})

		_, err := r.Resolve("bepinex")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown adapter "bepinex"`)
		assert.Contains(t, err.Error(), "generic-files, icarus")
	})

	t.Run("Names is sorted and Has answers registration", func(t *testing.T) {
		r := adapter.NewRegistry()
		r.Register(stubAdapter{id: "icarus"})

		assert.Equal(t, []string{"generic-files", "icarus"}, r.Names())
		assert.True(t, r.Has("icarus"))
		assert.True(t, r.Has(adapter.GenericID), "the identity is always registered")
		assert.False(t, r.Has("bepinex"))
	})
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"generic-files", "icarus", "bepinex", "unity-loader-2"} {
		assert.True(t, adapter.ValidName(ok), "%q should be a valid adapter name", ok)
	}
	for _, bad := range []string{"", "Icarus", "generic files", "-icarus", "icarus-", "generic--files", "icarus/1"} {
		assert.False(t, adapter.ValidName(bad), "%q should not be a valid adapter name", bad)
	}
}

func TestZeroLayoutIsTheIdentity(t *testing.T) {
	var zero adapter.Layout
	assert.False(t, zero.Applies())
	for _, m := range []string{"a.txt", "nested/dir/b.pak", ""} {
		got, keep := zero.Rewrite(m)
		assert.True(t, keep)
		assert.Equal(t, m, got)
	}
}

func TestNewLayoutRewritesAndDrops(t *testing.T) {
	l := adapter.NewLayout("game-root-relative", map[string]string{
		"plugins/mod.dll": "BepInEx/plugins/mod.dll",
		"README.md":       "", // dropped
	})
	assert.True(t, l.Applies())
	assert.Equal(t, "game-root-relative", l.Kind)

	got, keep := l.Rewrite("plugins/mod.dll")
	assert.True(t, keep)
	assert.Equal(t, "BepInEx/plugins/mod.dll", got)

	_, keep = l.Rewrite("README.md")
	assert.False(t, keep, "a member mapped to the empty string is dropped")

	// A member the table says nothing about keeps its own name, so an
	// adapter's table only ever lists what it MOVES.
	got, keep = l.Rewrite("untouched/file.cfg")
	assert.True(t, keep)
	assert.Equal(t, "untouched/file.cfg", got)

	// An empty-but-non-nil table still counts as an opinion.
	assert.True(t, adapter.NewLayout("empty", map[string]string{}).Applies())
	assert.False(t, adapter.NewLayout("none", nil).Applies(), "a nil table is the identity")
}

func TestCapabilityDispatchIsNilSafe(t *testing.T) {
	game := &domain.Game{ID: "g"}

	t.Run("an adapter with no optional capability takes every identity branch", func(t *testing.T) {
		a := stubAdapter{id: "plain"}
		assert.Equal(t, adapter.RouteLink, adapter.Route(a, game, "a"))
		require.NoError(t, adapter.CheckPreconditions(a, game, nil))

		findings, err := adapter.Verify(context.Background(), a, adapter.VerifyRequest{Game: game})
		require.NoError(t, err)
		assert.Empty(t, findings)

		assert.Empty(t, adapter.Guidance(a, game))
		_, ok := adapter.Compiler(a)
		assert.False(t, ok)
	})

	t.Run("an implemented capability is reached", func(t *testing.T) {
		boom := errors.New("install the loader first")
		a := capableAdapter{
			stubAdapter: stubAdapter{id: "capable"},
			route:       adapter.RouteCopyOnce,
			precond:     boom,
			findings:    []adapter.Finding{{Status: "loader_missing", Note: "no preloader"}},
			notes:       []adapter.GuidanceNote{{Title: "Launch", Body: "run it once"}},
		}
		assert.Equal(t, adapter.RouteCopyOnce, adapter.Route(a, game, "BepInEx/config/x.cfg"))
		assert.ErrorIs(t, adapter.CheckPreconditions(a, game, nil), boom)

		findings, err := adapter.Verify(context.Background(), a, adapter.VerifyRequest{Game: game})
		require.NoError(t, err)
		require.Len(t, findings, 1)
		assert.Equal(t, "loader_missing", findings[0].Status)

		require.Len(t, adapter.Guidance(a, game), 1)
	})
}

func TestFileRouteString(t *testing.T) {
	assert.Equal(t, "link", adapter.RouteLink.String())
	assert.Equal(t, "copy-once", adapter.RouteCopyOnce.String())
	assert.Equal(t, "skip", adapter.RouteSkip.String())
	assert.Equal(t, "unknown", adapter.FileRoute(99).String())
}

func TestSentinelsAreDistinct(t *testing.T) {
	assert.False(t, errors.Is(adapter.ErrNotAMod, adapter.ErrPreconditionUnmet))
	wrapped := errors.Join(adapter.ErrNotAMod)
	assert.ErrorIs(t, wrapped, adapter.ErrNotAMod)
}
