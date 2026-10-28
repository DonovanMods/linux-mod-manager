package adapter_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubAdapter is a minimal GameAdapter with no optional capabilities - the
// shape the built-in adapter.Generic has, restated here so this package's
// tests never depend on a concrete adapter.
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
}

func (c capableAdapter) RouteFile(*domain.Game, string) adapter.FileRoute { return c.route }
func (c capableAdapter) CheckPreconditions(*domain.Game, []domain.InstalledMod) error {
	return c.precond
}
func (c capableAdapter) Verify(context.Context, adapter.VerifyRequest) ([]adapter.Finding, error) {
	return c.findings, nil
}

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
		}
		assert.Equal(t, adapter.RouteCopyOnce, adapter.Route(a, game, "BepInEx/config/x.cfg"))
		assert.ErrorIs(t, adapter.CheckPreconditions(a, game, nil), boom)

		findings, err := adapter.Verify(context.Background(), a, adapter.VerifyRequest{Game: game})
		require.NoError(t, err)
		require.Len(t, findings, 1)
		assert.Equal(t, "loader_missing", findings[0].Status)
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

// claimingAdapter claims an archive whose members include the name it was
// built with - the shape of a real ArchiveClaimer, without any one
// adapter's rules.
type claimingAdapter struct {
	stubAdapter
	marker string
	refuse error
}

func (c claimingAdapter) ClaimArchive(members []string) (adapter.Claim, error) {
	if c.refuse != nil {
		return adapter.Claim{}, c.refuse
	}
	for _, m := range members {
		if strings.HasPrefix(m, c.marker+"/") {
			return adapter.Claim{Evidence: c.marker + " root", Requires: c.marker}, nil
		}
	}
	return adapter.Claim{}, nil
}

// TestRegistryClaimArchive is the loader precondition's core-side half
// (#359 through #413's seam): core asks every registered adapter EXCEPT the
// game's own whether an archive is unmistakably theirs.
func TestRegistryClaimArchive(t *testing.T) {
	newRegistry := func() *adapter.Registry {
		r := adapter.NewRegistry()
		r.Register(claimingAdapter{stubAdapter: stubAdapter{id: "alpha"}, marker: "alpha"})
		r.Register(claimingAdapter{stubAdapter: stubAdapter{id: "beta"}, marker: "beta"})
		return r
	}

	t.Run("a foreign archive is claimed, with its evidence", func(t *testing.T) {
		who, claim, err := newRegistry().ClaimArchive(adapter.GenericID, []string{"alpha/plugin.dll"})
		require.NoError(t, err)
		require.NotNil(t, who)
		assert.Equal(t, "alpha", who.ID())
		assert.Equal(t, "alpha root", claim.Evidence)
		assert.Equal(t, "alpha", claim.Requires)
		assert.True(t, claim.Claimed())
	})

	t.Run("the game's OWN adapter is never asked", func(t *testing.T) {
		who, claim, err := newRegistry().ClaimArchive("alpha", []string{"alpha/plugin.dll"})
		require.NoError(t, err)
		assert.Nil(t, who, "it has already had its full say through NormalizeArchive")
		assert.False(t, claim.Claimed())
	})

	t.Run("an archive nobody claims is nobody's problem", func(t *testing.T) {
		who, _, err := newRegistry().ClaimArchive(adapter.GenericID, []string{"Mods/MyMod/MyMod.dll"})
		require.NoError(t, err)
		assert.Nil(t, who)
	})

	t.Run("an adapter with no ArchiveClaimer is skipped", func(t *testing.T) {
		r := adapter.NewRegistry()
		r.Register(stubAdapter{id: "plain"})
		who, _, err := r.ClaimArchive(adapter.GenericID, []string{"alpha/plugin.dll"})
		require.NoError(t, err)
		assert.Nil(t, who)
	})

	// A refusal wins over a claim WHEREVER the two sit in registered-name
	// order (#413 review F1). The original case named its refuser "alpha"
	// and its claimer "beta", so a scan that returned the first answer it
	// met passed by alphabetical accident; the refuser is named on both
	// sides of the claimer here, and only a scan that actually prefers the
	// refusal passes both.
	for _, tc := range []struct {
		name      string
		refuserID string
	}{
		{name: "the refuser sorts before the claimer", refuserID: "aardvark"},
		{name: "the refuser sorts after the claimer", refuserID: "zebra"},
	} {
		t.Run("a refusal is surfaced ahead of any claim: "+tc.name, func(t *testing.T) {
			r := adapter.NewRegistry()
			refusal := fmt.Errorf("%w: it is the framework itself", adapter.ErrNotAMod)
			r.Register(claimingAdapter{stubAdapter: stubAdapter{id: tc.refuserID}, refuse: refusal})
			r.Register(claimingAdapter{stubAdapter: stubAdapter{id: "beta"}, marker: "beta"})

			who, claim, err := r.ClaimArchive(adapter.GenericID, []string{"beta/plugin.dll"})
			require.ErrorIs(t, err, adapter.ErrNotAMod)
			require.NotNil(t, who)
			assert.Equal(t, tc.refuserID, who.ID(), "the refusing adapter is the one reported")
			assert.False(t, claim.Claimed(),
				`"this archive is the framework itself" is a better thing to say than "your game needs that framework"`)
		})
	}

	t.Run("of two claims, the first in registered-name order wins", func(t *testing.T) {
		r := adapter.NewRegistry()
		r.Register(claimingAdapter{stubAdapter: stubAdapter{id: "zebra"}, marker: "shared"})
		r.Register(claimingAdapter{stubAdapter: stubAdapter{id: "alpha"}, marker: "shared"})

		who, claim, err := r.ClaimArchive(adapter.GenericID, []string{"shared/plugin.dll"})
		require.NoError(t, err)
		require.NotNil(t, who)
		assert.Equal(t, "alpha", who.ID(), "a build shipping two claimers answers deterministically")
		assert.True(t, claim.Claimed())
	})
}

// TestSeverityIsIssueByDefault pins the zero value, which is what an
// adapter that says nothing about a finding's weight gets: a report an
// adapter bothered to make is a problem, and core counts it as one.
func TestSeverityIsIssueByDefault(t *testing.T) {
	assert.Equal(t, adapter.SeverityIssue, adapter.Finding{}.Severity)
	assert.Equal(t, "issue", adapter.SeverityIssue.String())
	assert.Equal(t, "warning", adapter.SeverityWarning.String())
	assert.Equal(t, "note", adapter.SeverityNote.String())
}
