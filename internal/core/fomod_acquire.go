package core

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// fomodInputFor is what a FOMOD installer for sourceID/modID is installed
// with in profileName of game (#614): the answers this run was given for
// the mod (answers, keyed by domain.ModKey - an Apply's Options.Fomod),
// else the choices recorded on its reference (a replay), else none. The
// file states are the profile's (fileStatesFor).
func (s *Service) fomodInputFor(ctx context.Context, game *domain.Game, profileName string, ref *domain.ModReference, sourceID, modID string, answers map[string]domain.FomodChoices) fomodInput {
	in := fomodInput{files: s.fileStatesFor(ctx, game, profileName)}
	if a, ok := answers[domain.ModKey(sourceID, modID)]; ok && !a.IsZero() {
		in.choices, in.answered = a, true
		return in
	}
	if ref != nil {
		in.choices = ref.Fomod
	}
	return in
}

// recordedRef is profileName's reference to sourceID/modID, or nil when
// the profile cannot be read or does not list the mod.
func (s *Service) recordedRef(ctx context.Context, gameID, profileName, sourceID, modID string) *domain.ModReference {
	if profileName == "" {
		return nil
	}
	p, err := s.NewProfileManager().Get(ctx, gameID, profileName)
	if err != nil {
		return nil
	}
	if ref := p.FindRef(sourceID, modID); ref != nil {
		cp := *ref
		return &cp
	}
	return nil
}

// replayInput is fomodInputFor with no answers: what a flow that rebuilds
// or converges an installed mod (deploy's self-heal, verify --fix, a
// switch, an apply, the update batch) installs a FOMOD with - only ever the
// recorded choices, never a question it could answer.
func (s *Service) replayInput(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) fomodInput {
	return s.fomodInputFor(ctx, game, profileName, s.recordedRef(ctx, game.ID, profileName, sourceID, modID), sourceID, modID, nil)
}

// unifyFomod gives one mod's acquired files the single cache entry its
// FOMOD installer resolved to (#614): every file of a mod is one entry, so
// a variant the installer's choices produced is the variant of all of
// them. mod.Variant becomes it, and the outcome is returned for the flow to
// record; nil when no file was an installer. More than one installer among
// one mod's files is refused: which one's choices name the entry is not
// knowable.
func unifyFomod(mod *domain.Mod, files []*acquiredFile) (*fomodResolved, error) {
	var found *fomodResolved
	for _, a := range files {
		if a.fomod == nil {
			continue
		}
		if found != nil {
			return nil, &FomodInstallError{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name,
				Reason: "more than one of its files is a FOMOD installer, which lmm installs only one at a time"}
		}
		found = a.fomod
	}
	if found == nil {
		return nil, nil
	}
	for _, a := range files {
		a.mod.Variant = found.variant
	}
	mod.Variant = found.variant
	return found, nil
}

// withFomod sets ref's recorded choices from a resolve outcome; a nil one
// (not an installer) leaves ref as it is.
func withFomod(ref domain.ModReference, r *fomodResolved) domain.ModReference {
	if r != nil {
		ref.Fomod = r.choices
	}
	return ref
}

// fomodDrift reports that ref records FOMOD choices whose tree is not the
// one row installed (#614): the profile is desired state, so a converge
// flow reinstalls the mod with the recorded choices, as it does for a
// version drift. A reference that records no choices never drifts: a row
// installed with choices the profile does not carry (an older lmm dropped
// the key on a rewrite) keeps its tree rather than ask a question no
// converge flow can.
func fomodDrift(ref domain.ModReference, row *domain.InstalledMod) bool {
	return row != nil && !ref.Fomod.IsZero() && row.Variant != fomodVariant(ref.Fomod)
}
