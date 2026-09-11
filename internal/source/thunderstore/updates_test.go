package thunderstore_test

// UPDATE CHECKS (#409, design §3.5).
//
// The whole claim is that this costs NOTHING upstream. Every package's
// newest version_number is already in the local index, so an update check
// is a map lookup per installed mod and zero requests - which is why
// `lmm update` against a Thunderstore game is microseconds rather than one
// round trip per mod.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installedAt builds the InstalledMod shape core hands a source: the
// GameID is the COMMUNITY, already translated.
func installedAt(id, version string) domain.InstalledMod {
	return domain.InstalledMod{Mod: domain.Mod{
		ID: id, SourceID: "thunderstore", Name: id, Version: version, GameID: testCommunity,
	}}
}

// TestCheckUpdatesIsEntirelyLocal is the headline.
func TestCheckUpdatesIsEntirelyLocal(t *testing.T) {
	s := searchable(t)
	before, _, _, _ := s.srv.counts()

	updates, err := s.src.CheckUpdates(t.Context(), []domain.InstalledMod{
		installedAt("RugbugRedfern-Skinwalkers", "2.1.0"),   // behind
		installedAt("tinyhoot-ShipLoot", "1.1.0"),           // current
		installedAt("notnotnotswipez-MoreCompany", "1.8.0"), // behind
	})
	require.NoError(t, err)

	require.Len(t, updates, 2)
	assert.Equal(t, "RugbugRedfern-Skinwalkers", updates[0].InstalledMod.ID)
	assert.Equal(t, "3.0.2", updates[0].NewVersion)
	assert.Equal(t, "notnotnotswipez-MoreCompany", updates[1].InstalledMod.ID)
	assert.Equal(t, "1.9.0", updates[1].NewVersion)

	after, _, _, _ := s.srv.counts()
	assert.Equal(t, before, after, "an update check must make no request at all")
}

// TestCheckUpdatesComparesStringsNotSemver. versions[0] is authoritative
// about what "newest" means - it is PUBLISH order - so a republished older
// version is something lmm offers rather than hides behind a ">" test.
func TestCheckUpdatesComparesStringsNotSemver(t *testing.T) {
	s := searchable(t)

	updates, err := s.src.CheckUpdates(t.Context(), []domain.InstalledMod{
		installedAt("RugbugRedfern-Skinwalkers", "9.9.9"),
	})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "3.0.2", updates[0].NewVersion,
		"a version the package does not publish is not 'ahead', it is different")
}

// TestCheckUpdatesReportsAGonePackageAsNoUpdate, never as an error: a
// package that has left the community is unavailable, and "delisted" and
// "out of date" are different facts. One dead mod must not blind the rest
// of the batch either.
func TestCheckUpdatesReportsAGonePackageAsNoUpdate(t *testing.T) {
	s := searchable(t)

	updates, err := s.src.CheckUpdates(t.Context(), []domain.InstalledMod{
		installedAt("Nobody-LeftTheIndex", "1.0.0"),
		installedAt("notnotnotswipez-MoreCompany", "1.8.0"),
	})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "notnotnotswipez-MoreCompany", updates[0].InstalledMod.ID)
}

// TestCheckUpdatesRefreshingForcesThroughTheTTL is `lmm update --refresh`:
// the index is inside its TTL, so an ordinary check asks upstream nothing;
// with refresh set it sends the conditional GET, which upstream answers
// 304 in zero bytes.
func TestCheckUpdatesRefreshingForcesThroughTheTTL(t *testing.T) {
	s := searchable(t)
	refresher, ok := any(s.src).(source.RefreshingUpdateChecker)
	require.True(t, ok, "`lmm update --refresh` must reach the index")

	installed := []domain.InstalledMod{installedAt("notnotnotswipez-MoreCompany", "1.8.0")}

	_, err := refresher.CheckUpdatesRefreshing(t.Context(), installed, false, nil)
	require.NoError(t, err)
	_, conditionalsBefore, _, _ := s.srv.counts()
	assert.Zero(t, conditionalsBefore, "a warm index asks nothing")

	updates, err := refresher.CheckUpdatesRefreshing(t.Context(), installed, true, nil)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	_, conditionals, _, served304 := s.srv.counts()
	assert.Equal(t, 1, conditionals, "--refresh sends If-Modified-Since")
	assert.Equal(t, 1, served304, "and an unchanged document costs zero bytes")
}

// TestCheckUpdatesRefreshingSeesANewlyPublishedVersion closes the loop: a
// forced refresh that DOES find a changed document reports the update the
// stale index could not have.
func TestCheckUpdatesRefreshingSeesANewlyPublishedVersion(t *testing.T) {
	s := searchable(t)
	refresher := any(s.src).(source.RefreshingUpdateChecker)
	installed := []domain.InstalledMod{installedAt("tinyhoot-ShipLoot", "1.1.0")}

	updates, err := refresher.CheckUpdatesRefreshing(t.Context(), installed, false, nil)
	require.NoError(t, err)
	assert.Empty(t, updates)

	s.srv.publish(withNewShipLootVersion(t), "Thu, 11 Sep 2026 12:00:00 GMT")

	updates, err = refresher.CheckUpdatesRefreshing(t.Context(), installed, true, nil)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "1.2.0", updates[0].NewVersion)
}

// TestCheckUpdatesReportsProgress: the check is microseconds, but the
// progress line has to behave here exactly as it does everywhere else or
// `lmm update`'s counter develops a hole for this source.
func TestCheckUpdatesReportsProgress(t *testing.T) {
	s := searchable(t)
	reporter, ok := any(s.src).(source.UpdateProgressReporter)
	require.True(t, ok)

	type tick struct {
		n, total int
		name     string
	}
	var ticks []tick
	_, err := reporter.CheckUpdatesWithProgress(t.Context(), []domain.InstalledMod{
		installedAt("tinyhoot-ShipLoot", "1.1.0"),
		installedAt("Quiet-QuietTerminal", "1.0.0"),
	}, func(n, total int, name string) {
		ticks = append(ticks, tick{n, total, name})
	})
	require.NoError(t, err)
	assert.Equal(t, []tick{
		{1, 2, "tinyhoot-ShipLoot"},
		{2, 2, "Quiet-QuietTerminal"},
	}, ticks)
}

// TestCheckUpdatesWithNothingInstalledAsksNothing keeps the empty batch
// from building an index for a game with no Thunderstore mods in it.
func TestCheckUpdatesWithNothingInstalledAsksNothing(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, _, _ := newSource(t, srv)

	updates, err := src.CheckUpdates(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, updates)
	requests, _, _, _ := srv.counts()
	assert.Zero(t, requests, "an empty batch must not build an index")
}

// TestUpdatesCapabilityIsDeclared.
func TestUpdatesCapabilityIsDeclared(t *testing.T) {
	s := searchable(t)
	assert.True(t, s.src.Capabilities().Updates)
}
