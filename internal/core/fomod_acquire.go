package core

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
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

// rebuildInput is what a flow that REBUILDS an installed row's own cache
// entry (deploy's self-heal, verify --fix's redownload) installs a FOMOD
// with: the recorded choices, which must name the row's variant again. A
// row installed before FOMOD support - the plain entry and no recorded
// choices - is rebuilt raw, the archive's own tree exactly as it was
// installed, never re-resolved into a question the flow cannot ask or a
// variant the row does not name (#614).
func (s *Service) rebuildInput(ctx context.Context, game *domain.Game, row *domain.InstalledMod) fomodInput {
	in := s.replayInput(ctx, game, row.ProfileName, row.SourceID, row.ID)
	if row.Variant == "" && in.choices.IsZero() {
		in.raw = true
	}
	return in
}

// unifyFomod gives one mod's acquired files the single cache entry its
// FOMOD installer resolved to (#614): every file of a mod is one entry, so
// a variant the installer's choices produced is the variant of all of
// them, and the first file commits UNSEEDED - the resolved tree replaces
// the entry rather than layering onto whatever an earlier resolve left
// there. mod.Variant becomes the variant. More than one installer among
// one mod's files is refused: which one's choices name the entry is not
// knowable.
//
// The outcome is for the flow to record on the ref: nil when nothing was
// acquired (the ref's choices are kept), plainTree when the files were
// acquired and none was an installer (the ref's choices are cleared - they
// would name a tree the row does not have), else the resolved choices.
func unifyFomod(mod *domain.Mod, files []*acquiredFile) (*fomodResolved, error) {
	if len(files) == 0 {
		return nil, nil
	}
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
		found = plainTree()
	}
	for _, a := range files {
		a.mod.Variant = found.variant
	}
	if found.variant != "" {
		files[0].unseeded = true
	}
	mod.Variant = found.variant
	return found, nil
}

// plainTree is the outcome of a mod whose files were acquired and none was
// a FOMOD installer: the plain entry, and no choices.
func plainTree() *fomodResolved { return &fomodResolved{} }

// fomodOp is what a profile write does with a ref's recorded choices.
type fomodOp int

const (
	// fomodKeep leaves the recorded choices as they are.
	fomodKeep fomodOp = iota
	// fomodSet records new choices.
	fomodSet
	// fomodClear removes them.
	fomodClear
)

// fomodRecord is a profile write's say over a ref's recorded choices.
type fomodRecord struct {
	op      fomodOp
	choices domain.FomodChoices
}

// fomodRecordOf is the profile write a resolve outcome calls for: keep for
// none (nothing acquired), clear for a plain tree, set for an installer's
// choices.
func fomodRecordOf(r *fomodResolved) fomodRecord {
	switch {
	case r == nil:
		return fomodRecord{op: fomodKeep}
	case r.choices.IsZero():
		return fomodRecord{op: fomodClear}
	default:
		return fomodRecord{op: fomodSet, choices: r.choices}
	}
}

// modFileHooks are a per-file flow's own events around downloadModFiles'
// acquire of each file. Every hook may be nil.
type modFileHooks struct {
	// sink is the file's download event sink.
	sink func(i int, f *domain.DownloadableFile) EventSink
	// before runs before the file's download starts.
	before func(i int, f *domain.DownloadableFile)
	// after runs once it ends: checksum is what the download computed
	// (empty on failure), err its failure.
	after func(i int, f *domain.DownloadableFile, checksum string, err error)
	// checkCtx stops before each file's acquire once ctx is done, for a
	// flow whose old per-file loop had that check at its head.
	checkCtx bool
}

// downloadModFiles fetches a mod's files into gameCache the way an
// install-shaped flow does for one item (#614): every file is acquired
// first, the FOMOD outcome unified over them (unifyFomod), and only then
// committed - so a mod whose installer is not its first file still lands
// in ONE entry, the resolved variant's. mod.Variant becomes that variant.
// The first failure stops it: nothing is committed, and the acquisitions
// are released (a refusal the user answers has already kept its download).
func (s *Service) downloadModFiles(ctx context.Context, gameCache *cache.Cache, sourceID string, game *domain.Game, mod *domain.Mod, files []*domain.DownloadableFile, fx fomodInput, hooks modFileHooks) ([]*DownloadModResult, *fomodResolved, error) {
	acquired := make([]*acquiredFile, 0, len(files))
	defer func() {
		for _, a := range acquired {
			a.release()
		}
	}()
	for i, f := range files {
		if hooks.checkCtx {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
		}
		if hooks.before != nil {
			hooks.before(i, f)
		}
		var sink EventSink
		if hooks.sink != nil {
			sink = hooks.sink(i, f)
		}
		a, err := s.acquireFile(ctx, gameCache, sourceID, game, mod, f, fx, sink)
		if hooks.after != nil {
			checksum := ""
			if a != nil {
				checksum = a.checksum
			}
			hooks.after(i, f, checksum, err)
		}
		if err != nil {
			return nil, nil, err
		}
		acquired = append(acquired, a)
	}
	resolved, err := unifyFomod(mod, acquired)
	if err != nil {
		return nil, nil, err
	}
	results := make([]*DownloadModResult, 0, len(acquired))
	for _, a := range acquired {
		res, err := s.commitAcquired(ctx, a)
		if err != nil {
			return nil, nil, err
		}
		results = append(results, res)
	}
	return results, resolved, nil
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

// cmpFomod is an ingest's outcome for the profile write: the installer's
// choices, or plainTree when the archive was not an installer - an ingest
// always reads the archive, so "not an installer" is known, and the ref's
// choices are cleared.
func cmpFomod(r *fomodResolved) *fomodResolved {
	if r == nil {
		return plainTree()
	}
	return r
}
