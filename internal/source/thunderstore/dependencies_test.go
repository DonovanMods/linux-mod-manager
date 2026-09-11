package thunderstore_test

// DEPENDENCIES and the LOADER (#409, design §3.4).
//
// Two rules, and the second is the one that matters. Ordinary dependency
// strings become domain.ModReferences the existing resolver already
// understands. But 41,045 of the largest community's 50,707 packages
// declare a dependency on a BepInExPack, and that is NOT a mod: it is the
// mod LOADER, it lives in the game root, it survives a profile switch, and
// resolving it as a profile member would deploy a framework into the mod
// path and then tear it out from under every plugin at the next uninstall.
// So it is routed out of the dependency list and reported as #359's
// plan-time precondition instead.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetDependenciesRoutesTheLoaderOutAndKeepsTheRest is the headline.
func TestGetDependenciesRoutesTheLoaderOutAndKeepsTheRest(t *testing.T) {
	s := searchable(t)
	mod := s.mod(t, "Evaisa-LethalThings")

	deps, err := s.src.GetDependencies(t.Context(), mod)
	require.NoError(t, err)

	assert.Equal(t, []domain.ModReference{
		{SourceID: "thunderstore", ModID: "Ghost-NotInThisIndex", Version: "1.0.0"},
	}, deps, "BepInEx-BepInExPack is the loader, not a dependency; the other entry survives")
}

// TestGetDependenciesKeepsAnOrdinaryPackageInTheLoadersNAMESPACE is the
// rule's precision. The namespace varies across the site
// (BepInEx-BepInExPack, bbepis-BepInExPack, denikson-BepInExPack_Valheim),
// so the reliable signal is the NAME - and BepInEx-MonoMod_Loader,
// CatsArmy-BepInEx_GUI and friends are real packages that must keep
// resolving as mods.
func TestGetDependenciesKeepsAnOrdinaryPackageInTheLoadersNAMESPACE(t *testing.T) {
	s := searchable(t)

	deps, err := s.src.GetDependencies(t.Context(), s.mod(t, "BepInEx-MonoMod_Loader"))
	require.NoError(t, err)
	assert.Empty(t, deps, "its own BepInExPack dependency is still the loader")

	// And a package that DEPENDS on it keeps it.
	mod := s.mod(t, "RugbugRedfern-Skinwalkers")
	mod.Version = "2.0.0"
	deps, err = s.src.GetDependencies(t.Context(), mod)
	require.NoError(t, err)
	assert.Equal(t, []domain.ModReference{
		{SourceID: "thunderstore", ModID: "BepInEx-MonoMod_Loader", Version: "1.1.0"},
	}, deps)
}

// TestGetDependenciesReadsTheINSTALLEDVersion, not the latest: a profile
// pinned to an old version must resolve the graph that version declared,
// or an install plan promises dependencies the bytes on disk never wanted.
func TestGetDependenciesReadsTheINSTALLEDVersion(t *testing.T) {
	s := searchable(t)
	mod := s.mod(t, "RugbugRedfern-Skinwalkers")

	mod.Version = "2.1.0"
	deps, err := s.src.GetDependencies(t.Context(), mod)
	require.NoError(t, err)
	assert.Equal(t, []domain.ModReference{
		{SourceID: "thunderstore", ModID: "NotAtoms-TerminalApi", Version: "1.5.0"},
	}, deps, "2.1.0's own dependency, not 3.0.2's")

	// An empty or unknown version falls back to the newest, which is what a
	// pre-install plan holds: nothing is installed yet.
	mod.Version = ""
	deps, err = s.src.GetDependencies(t.Context(), mod)
	require.NoError(t, err)
	assert.Empty(t, deps, "3.0.2 declares only the loader")
}

// TestGetDependenciesSurfacesAnUnparseableStringRatherThanDroppingIt: the
// string cannot become a package id, but silence is the one answer that
// helps nobody - passed through as the id it claims to be, it reaches the
// user as the resolver's "missing dependency", naming exactly what the
// package declared.
func TestGetDependenciesSurfacesAnUnparseableStringRatherThanDroppingIt(t *testing.T) {
	s := searchable(t)
	mod := s.mod(t, "Umlaut-Cafe_Mod")

	deps, err := s.src.GetDependencies(t.Context(), mod)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, "NotAPackageString", deps[0].ModID)
	assert.Empty(t, deps[0].Version)
}

// TestLoaderRequirementIsWhatTheDroppedEntryBECOMES. Dropping the loader
// from the dependency list is only half the rule; the other half is that
// the fact does not disappear.
func TestLoaderRequirementIsWhatTheDroppedEntryBECOMES(t *testing.T) {
	s := searchable(t)
	requirer, ok := any(s.src).(source.LoaderRequirer)
	require.True(t, ok, "the source must be able to report a loader requirement")

	kind, version, required, err := requirer.LoaderRequirement(t.Context(), s.mod(t, "RugbugRedfern-Skinwalkers"))
	require.NoError(t, err)
	assert.True(t, required)
	assert.Equal(t, domain.LoaderKindBepInEx, kind)
	assert.Equal(t, "5.4.2100", version)

	// A third-party pack in another namespace is the same loader.
	valheim := s.mod(t, "denikson-BepInExPack_Valheim")
	valheim.Version = "5.4.2202"
	_, _, required, err = requirer.LoaderRequirement(t.Context(), valheim)
	require.NoError(t, err)
	assert.False(t, required, "the pack itself declares no dependency on a pack")

	// A package that declares none needs none.
	_, _, required, err = requirer.LoaderRequirement(t.Context(), s.mod(t, "Umlaut-Cafe_Mod"))
	require.NoError(t, err)
	assert.False(t, required)
}

// TestDependenciesCapabilityIsDeclared: the resolver consults it before
// asking, so it has to become true in the commit that makes it true.
func TestDependenciesCapabilityIsDeclared(t *testing.T) {
	s := searchable(t)
	assert.True(t, s.src.Capabilities().Dependencies)
}
