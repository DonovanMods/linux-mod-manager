package config_test

// #668: a profile's `known_conflicts:` list - the mod pairs whose file
// conflict it marks as intended - and its collection baseline's copy. A
// top-level, omitempty key: a profile without one is unchanged, and an
// in-place save adds or removes only that key.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

func knownConflictsFixture() domain.KnownConflicts {
	return domain.KnownConflicts{
		{Winner: "nexusmods:2", Loser: "nexusmods:1", Origin: domain.KnownConflictCollection},
		{Winner: "nexusmods:3", Loser: "nexusmods:2", Origin: domain.KnownConflictUser},
	}
}

func TestSaveProfile_KnownConflictsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	profile := &domain.Profile{Name: "p", GameID: "tg", KnownConflicts: knownConflictsFixture(),
		CollectionBaseline: &domain.CollectionBaseline{KnownConflicts: knownConflictsFixture()[:1]}}
	require.NoError(t, config.SaveProfile(dir, profile))
	got := readFile(t, dir+"/games/tg/profiles/p.yaml")
	require.Contains(t, got, "known_conflicts:\n    - winner: nexusmods:2\n      loser: nexusmods:1\n      origin: collection\n")

	reloaded, err := config.LoadProfile(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, profile.KnownConflicts, reloaded.KnownConflicts)
	require.Equal(t, profile.CollectionBaseline, reloaded.CollectionBaseline)
}

func TestSaveProfile_KnownConflictsAreEditedInPlace(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)

	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Nil(t, profile.KnownConflicts, "a file without the key marks nothing known")

	profile.KnownConflicts = knownConflictsFixture()
	require.NoError(t, config.SaveProfile(dir, profile))
	got := readFile(t, path)
	require.True(t, strings.HasPrefix(got, handEdited), "the author's text is kept, the key appended:\n%s", got)

	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Equal(t, profile.KnownConflicts, reloaded.KnownConflicts)

	reloaded.KnownConflicts = nil
	require.NoError(t, config.SaveProfile(dir, reloaded))
	require.Equal(t, handEdited, readFile(t, path), "clearing the list restores the file")
}

func TestSnapshotProfileValue_CarriesKnownConflicts(t *testing.T) {
	profile := &domain.Profile{Name: "p", GameID: "tg", KnownConflicts: knownConflictsFixture()}
	exported := config.SnapshotProfileValue(profile)
	require.Equal(t, profile.KnownConflicts, exported.KnownConflicts)
	exported.KnownConflicts[0].Winner = "changed"
	require.Equal(t, "nexusmods:2", profile.KnownConflicts[0].Winner, "the snapshot holds its own copy")
	exported.KnownConflicts[0].Winner = "nexusmods:2"
	back := config.ProfileFromExported(exported)
	require.Equal(t, profile.KnownConflicts, back.KnownConflicts)
	require.Equal(t, profile.KnownConflicts, config.ExportProfileValue(profile).KnownConflicts, "profile export --json carries them too")
}
