package core_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// namingSource is a mockSource whose authors are opaque ids with cached
// display names (source.AuthorNameCache), counting how often it is asked.
type namingSource struct {
	*mockSource
	names map[string]string
	asked int
}

type resolvingNamingSource struct {
	*namingSource
	resolved map[string]string
	calls    int
}

func (s *resolvingNamingSource) ResolveAuthorNames(_ context.Context, mods []*domain.Mod) {
	s.calls++
	for _, mod := range mods {
		if mod != nil {
			mod.AuthorName = s.resolved[mod.Author]
		}
	}
}

func (s *namingSource) CachedAuthorNames(authors []string) map[string]string {
	s.asked++
	out := map[string]string{}
	for _, a := range authors {
		if n, ok := s.names[a]; ok {
			out[a] = n
		}
	}
	return out
}

// TestListMods_StampsCachedAuthorNames is #420's listing half: an installed
// row whose source keeps display names for its opaque author ids carries
// author_name beside the id, from the source's cache alone; a row the
// cache has no name for, and a row from a source with no such cache, keeps
// its author as it is.
func TestListMods_StampsCachedAuthorNames(t *testing.T) {
	svc, game, plain := newModDetailTestService(t)
	named := &namingSource{mockSource: newMockSource("named"), names: map[string]string{"7656": "Cargo Captain"}}
	svc.RegisterSource(named)
	ctx := context.Background()
	for _, m := range []domain.Mod{
		{ID: "a", SourceID: "named", Author: "7656"},
		{ID: "b", SourceID: "named", Author: "7657"},
		{ID: "c", SourceID: plain.ID(), Author: "7656"},
	} {
		m.GameID, m.Name, m.Version = game.ID, "Mod "+m.ID, "1.0"
		require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{Mod: m, ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true}))
	}

	rows, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	got := map[string][2]string{}
	for _, r := range rows {
		got[r.ID] = [2]string{r.Author, r.AuthorName}
	}
	assert.Equal(t, map[string][2]string{
		"a": {"7656", "Cargo Captain"},
		"b": {"7657", ""},
		"c": {"7656", ""},
	}, got)
	assert.Equal(t, 1, named.asked, "one cache read per source, not per row")

	row, err := svc.GetInstalledMod(ctx, "named", "a", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "Cargo Captain", row.AuthorName)
}

// TestModDetail_ResolvesAuthorNamesAfterMutationSafeGetMod proves #491's
// split: every core flow that calls GetMod gets only an already-cached name,
// while the detail flow is the one place that pays a live resolver.
func TestModDetail_ResolvesAuthorNamesAfterMutationSafeGetMod(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	named := &resolvingNamingSource{
		namingSource: &namingSource{mockSource: newMockSource("named"), names: map[string]string{"7656": "Cached Name"}},
		resolved:     map[string]string{"7656": "Live Name"},
	}
	named.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "named", GameID: game.ID, Name: "Mod A", Author: "7656"})
	svc.RegisterSource(named)

	mod, err := svc.GetMod(context.Background(), "named", game.ID, "a")
	require.NoError(t, err)
	assert.Equal(t, "Cached Name", mod.AuthorName)
	assert.Zero(t, named.calls, "the shared GetMod path must not resolve live names")

	detail, err := svc.ModDetail(context.Background(), game, "default", "named", "a")
	require.NoError(t, err)
	assert.Equal(t, "Live Name", detail.Mod.AuthorName)
	assert.Equal(t, 1, named.calls)
}

// TestAuthorText is the one author rule every surface reads (#420, #459):
// the resolved name, else the author field, else nothing at all.
func TestAuthorText(t *testing.T) {
	assert.Equal(t, "Cargo Captain", core.AuthorText(&domain.Mod{Author: "7656", AuthorName: "Cargo Captain"}))
	assert.Equal(t, "someone", core.AuthorText(&domain.Mod{Author: "someone"}))
	assert.Equal(t, "", core.AuthorText(&domain.Mod{}))
	assert.Equal(t, "", core.AuthorText(nil))
}
