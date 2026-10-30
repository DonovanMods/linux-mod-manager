package core_test

// #590: an installed mod that left its source's catalog (#539) is often the
// same mod republished under a new ID - same name, same author. The update
// check and Health look in the same source for that successor and suggest
// it, never re-linking on their own; several candidates are listed and none
// picked.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// successorCatalogSource is goneCatalogSource with a searchable catalog: the
// mods a name search can find, and a count of the searches it answered.
type successorCatalogSource struct {
	*goneCatalogSource
	catalog  []domain.Mod
	searches int
}

func (s *successorCatalogSource) Search(_ context.Context, q source.SearchQuery) (source.SearchResult, error) {
	s.searches++
	fold := func(v string) string { return strings.Join(strings.Fields(strings.ToLower(v)), " ") }
	var hits []domain.Mod
	for _, m := range s.catalog {
		if strings.Contains(fold(m.Name), fold(q.Query)) {
			hits = append(hits, m)
		}
	}
	return source.SearchResult{Mods: hits}, nil
}

// GetModFiles answers a gone id the way Icarus does (#539): a wrapped
// domain.ErrModNotFound under the source's own transport chain.
func (s *successorCatalogSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if s.gone[mod.ID] {
		return nil, fmt.Errorf("source %q: listing files for %s: fetching mods/%s: not in the catalog (HTTP 404): %w", s.id, mod.ID, mod.ID, domain.ErrModNotFound)
	}
	return []domain.DownloadableFile{{ID: "f1", FileName: mod.ID + ".zip", Version: "1.0", IsPrimary: true}}, nil
}

// newSuccessorService installs "old" (Mega Points by Donovan, 1.0) and has
// the source's catalog drop it; catalog is what a search can find instead.
func newSuccessorService(t *testing.T, catalog ...domain.Mod) (*core.Service, *domain.Game, *successorCatalogSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	src := &successorCatalogSource{
		goneCatalogSource: &goneCatalogSource{mockSource: newMockSource("gonecat"), gone: map[string]bool{"old": true}, broken: map[string]bool{}, newer: "2.0"},
		catalog:           catalog,
	}
	svc.RegisterSource(src)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "old", SourceID: "gonecat", Name: "Mega Points", Author: "Donovan", Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true, FileIDs: []string{"f1"},
	}))
	seedProfileWithMod(t, svc, game.ID, "default", "gonecat", "old", "1.0")
	return svc, game, src
}

func catalogMod(id, name, author, version string) domain.Mod {
	return domain.Mod{ID: id, SourceID: "gonecat", GameID: "g1", Name: name, Author: author, Version: version}
}

func checkReport(t *testing.T, svc *core.Service, game *domain.Game) *core.UpdateCheckReport {
	t.Helper()
	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	require.Len(t, report.CatalogMissing, 1)
	return report
}

func TestCheckGameUpdateReport_SuggestsTheOneSameNameSameAuthorSuccessor(t *testing.T) {
	svc, game, src := newSuccessorService(t,
		catalogMod("new", "Mega Points", "Donovan", "1.0"),
		catalogMod("other-author", "Mega Points", "Someone Else", "1.0"),
		catalogMod("other-name", "Mega Points Plus", "Donovan", "1.0"),
	)

	report := checkReport(t, svc, game)
	assert.Equal(t, []core.CatalogSuccessor{{ModID: "new", Name: "Mega Points", Author: "Donovan", Version: "1.0"}},
		report.CatalogMissing[0].Successors)
	assert.Equal(t, 1, src.searches, "one search per gone mod")
}

// Names and authors compare folded: case, spacing and punctuation are not
// what makes a mod a different one.
func TestCheckGameUpdateReport_SuccessorNameAndAuthorAreNormalised(t *testing.T) {
	svc, game, _ := newSuccessorService(t, catalogMod("new", "mega  points", "DONOVAN", "1.1"))

	report := checkReport(t, svc, game)
	require.Len(t, report.CatalogMissing[0].Successors, 1)
	assert.Equal(t, "new", report.CatalogMissing[0].Successors[0].ModID)
}

func TestCheckGameUpdateReport_SeveralSuccessorsAreAllListed(t *testing.T) {
	svc, game, _ := newSuccessorService(t,
		catalogMod("new-a", "Mega Points", "Donovan", "1.0"),
		catalogMod("new-b", "Mega Points", "Donovan", "1.2"),
	)

	report := checkReport(t, svc, game)
	ids := []string{}
	for _, s := range report.CatalogMissing[0].Successors {
		ids = append(ids, s.ModID)
	}
	assert.ElementsMatch(t, []string{"new-a", "new-b"}, ids, "ambiguous: every candidate, none picked")
}

// "Ideally the same or a newer version": an older copy is only a candidate
// when nothing same-or-newer is.
func TestCheckGameUpdateReport_AnOlderCandidateYieldsToASameOrNewerOne(t *testing.T) {
	svc, game, _ := newSuccessorService(t,
		catalogMod("older", "Mega Points", "Donovan", "0.9"),
		catalogMod("newer", "Mega Points", "Donovan", "1.1"),
	)

	report := checkReport(t, svc, game)
	require.Len(t, report.CatalogMissing[0].Successors, 1)
	assert.Equal(t, "newer", report.CatalogMissing[0].Successors[0].ModID)
}

func TestCheckGameUpdateReport_NoSuccessorLeavesTheReportAsItWas(t *testing.T) {
	svc, game, _ := newSuccessorService(t, catalogMod("other", "Mega Points", "Someone Else", "1.0"))

	report := checkReport(t, svc, game)
	assert.Equal(t, core.CatalogModRef{SourceID: "gonecat", ModID: "old", Name: "Mega Points"}, report.CatalogMissing[0])
}

// The gone mod's own id is never its successor, even if a stale search
// still lists it.
func TestCheckGameUpdateReport_TheGoneIDIsNotItsOwnSuccessor(t *testing.T) {
	svc, game, _ := newSuccessorService(t, catalogMod("old", "Mega Points", "Donovan", "1.0"))

	report := checkReport(t, svc, game)
	assert.Empty(t, report.CatalogMissing[0].Successors)
}

// Health checks the typed condition, not the error string: a version check
// whose files lookup answered domain.ErrModNotFound is catalog_missing,
// worded as #539 words it, with the same suggestion.
func TestVerify_CatalogMissingModIsNamedWithItsSuccessor(t *testing.T) {
	svc, game, _ := newSuccessorService(t, catalogMod("new", "Mega Points", "Donovan", "1.0"))

	result, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)

	var got *core.VerifyFinding
	for i := range result.Findings {
		if result.Findings[i].ModID == "old" && result.Findings[i].FileID == "" {
			got = &result.Findings[i]
		}
	}
	require.NotNil(t, got)
	assert.Equal(t, core.VerifyStatusCatalogMissing, got.Status)
	assert.Equal(t, "no longer in Gone Catalog's catalog (removed, or republished under a new ID)", got.Note)
	assert.NotContains(t, got.Note, "could not check version")
	assert.Equal(t, "gonecat", got.SourceID, "a frontend re-links from the finding alone")
	assert.Equal(t, []core.CatalogSuccessor{{ModID: "new", Name: "Mega Points", Author: "Donovan", Version: "1.0"}}, got.Successors)
	assert.False(t, got.Fixable)
	assert.GreaterOrEqual(t, result.Warnings, 1)
}

func TestVerify_CatalogMissingWithNoSuccessorStillSaysSo(t *testing.T) {
	svc, game, _ := newSuccessorService(t)

	result, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)
	var statuses []string
	for _, f := range result.Findings {
		if f.ModID == "old" && f.FileID == "" {
			statuses = append(statuses, f.Status)
			assert.Empty(t, f.Successors)
		}
	}
	assert.Equal(t, []string{core.VerifyStatusCatalogMissing}, statuses)
}
