// Package thunderstore: this file is DEPENDENCIES, and the one entry in
// them that is not a dependency at all (#360 §3.4).
//
// Thunderstore packages declare their dependencies as version-pinned
// strings, "Namespace-Name-Version", which map straight onto
// domain.ModReference and need no change to core.DependencyResolver at all.
// What needs a rule is the BepInExPack entry that 41,045 of the largest
// community's 50,707 packages carry: that is the mod LOADER, and a loader
// is not a profile member. It installs into the game ROOT, its presence is
// a property of the game installation, and resolving it as a mod would
// deploy a framework under lmm's deployed-files bookkeeping - where the
// next uninstall or profile switch tears it out from under every plugin.
//
// So it is dropped from the dependency list and reported through
// source.LoaderRequirer instead, which core turns into the SAME plan-time
// refusal #359 infers from an archive's shape.
package thunderstore

import (
	"context"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// loaderPackName is the package name that IS BepInEx rather than a mod.
// The NAMESPACE varies across the site - BepInEx-BepInExPack (40,526
// packages depend on it), bbepis-BepInExPack (455),
// denikson-BepInExPack_Valheim (38), BepInEx-BepInExPack_H3VR (33) and six
// others - so the name is the reliable signal and the namespace is not
// consulted at all.
const loaderPackName = "BepInExPack"

// isLoaderPack reports whether a package NAME is a BepInEx framework pack:
// exactly "BepInExPack", or a per-game variant "BepInExPack_<Game>".
//
// Case-folded for bepinexDirName's reason: a safety rule that turns on one
// character is not a safety rule. Everything else in the BepInEx namespace
// stays an ordinary mod - BepInEx-MonoMod_Loader and CatsArmy-BepInEx_GUI
// are real packages people install - which is why this matches the whole
// name or the name plus an underscore, and never a mere prefix.
func isLoaderPack(name string) bool {
	if strings.EqualFold(name, loaderPackName) {
		return true
	}
	return len(name) > len(loaderPackName) &&
		strings.EqualFold(name[:len(loaderPackName)+1], loaderPackName+"_")
}

// unversionedLoaderPack reports whether a dependency string SplitDependency
// could not parse is nonetheless a BepInEx framework pack named with no
// version at all - "BepInEx-BepInExPack" rather than
// "BepInEx-BepInExPack-5.4.2100".
//
// Thunderstore's manifest schema pins every dependency, so no such string is
// served today; this exists because the consequence of missing one is bad
// out of proportion to the odds. The pack is a real package in the index, so
// an unrecognised loader entry does not surface as a missing dependency - it
// resolves, downloads and only then hits #358's extract-time refusal, with
// the plan-time precondition never fired at all.
//
// It is deliberately stricter than SplitPackage alone: both halves must
// match the measured namespace/name invariant, so "BepInExPack_Valheim-oops"
// - a name no package can have - is not read as the loader.
func unversionedLoaderPack(dep string) bool {
	ns, name, ok := SplitPackage(dep)
	if !ok || !identifierPattern.MatchString(ns) || !identifierPattern.MatchString(name) {
		return false
	}
	return isLoaderPack(name)
}

// GetDependencies returns the INSTALLED version's dependencies as ordinary
// same-source references, with the loader entry routed out.
//
// The installed version's, not the latest's: a profile pinned to an older
// version must resolve the graph that version declared, or a plan promises
// dependencies the bytes on disk never asked for. An empty or unrecognised
// mod.Version falls back to the newest, which is the pre-install case -
// nothing is installed yet.
//
// A dependency naming a package this community's index does not hold is
// still returned: the resolver's "missing dependency" path says exactly
// that, and it is a plan warning rather than a failure (the package may be
// listed in another community). An UNPARSEABLE string is returned too, as
// the id it claims to be, so it reaches the user through the same warning
// naming precisely what the package declared - silence is the one answer
// that helps nobody, and a ref that resolves to nothing is only ever looked
// up in the index, never joined onto a path.
func (s *Source) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	deps, err := s.declaredDependencies(ctx, mod)
	if err != nil {
		return nil, err
	}
	refs := make([]domain.ModReference, 0, len(deps))
	for _, dep := range deps {
		ns, name, version, ok := SplitDependency(dep)
		if !ok {
			if unversionedLoaderPack(dep) {
				continue // the loader, pinned to nothing - see LoaderRequirement
			}
			refs = append(refs, domain.ModReference{SourceID: sourceID, ModID: dep})
			continue
		}
		if isLoaderPack(name) {
			continue // the loader - see LoaderRequirement
		}
		refs = append(refs, domain.ModReference{SourceID: sourceID, ModID: ns + "-" + name, Version: version})
	}
	return refs, nil
}

// LoaderRequirement implements source.LoaderRequirer: the fact
// GetDependencies drops, kept rather than lost.
//
// It reports the FIRST loader pack the installed version declares, at the
// version that pack was pinned to - which is the version the user is about
// to be told to go and install, and the one piece of information the setup
// steps cannot derive for themselves. A dependency string that pins no
// version at all still reports the requirement, with an empty version.
func (s *Source) LoaderRequirement(ctx context.Context, mod *domain.Mod) (kind, version string, required bool, err error) {
	deps, err := s.declaredDependencies(ctx, mod)
	if err != nil {
		return "", "", false, err
	}
	for _, dep := range deps {
		_, name, pinned, ok := SplitDependency(dep)
		switch {
		case ok && isLoaderPack(name):
			return domain.LoaderKindBepInEx, pinned, true, nil
		case !ok && unversionedLoaderPack(dep):
			// The pack, pinned to nothing: still the requirement, with no
			// version to name. Reported as "" rather than guessed at.
			return domain.LoaderKindBepInEx, "", true, nil
		}
	}
	return "", "", false, nil
}

// declaredDependencies is the raw dependency strings of the version
// mod names, or of the newest version when it names none this package has.
func (s *Source) declaredDependencies(ctx context.Context, mod *domain.Mod) ([]string, error) {
	rec, err := s.recordFor(ctx, mod.GameID, mod.ID)
	if err != nil {
		return nil, err
	}
	if len(rec.Versions) == 0 {
		return nil, nil
	}
	for _, v := range rec.Versions {
		if v.Version == mod.Version {
			return v.Dependencies, nil
		}
	}
	return rec.Versions[0].Dependencies, nil
}
