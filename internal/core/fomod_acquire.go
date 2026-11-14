package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// fomodInputFor is what a FOMOD installer for sourceID/modID is installed
// with in profileName of game (#614): the answers this run was given for
// the mod (answers, keyed by domain.ModKey - an Apply's Options.Fomod),
// else the choices recorded on its reference (a replay), else none. The
// file states are the profile's (fileStatesForWith).
//
// A mod whose files each carry an installer (#634) is answered per file,
// under domain.FomodFileKey, and records its choices per file
// (ModReference.FomodFiles): both ride on the input, and acquireFile picks
// out each file's (fomodInput.forFile).
func (s *Service) fomodInputFor(ctx context.Context, game *domain.Game, profileName string, ref *domain.ModReference, sourceID, modID string, answers map[string]domain.FomodChoices) fomodInput {
	return s.fomodInputForWith(ctx, game, profileName, ref, sourceID, modID, answers, fileStatesOpts{})
}

// fomodInputForWith is fomodInputFor with its file states under states
// (fileStatesForWith): an import's, which answer from its document and,
// for a collection, by install phase (#609).
func (s *Service) fomodInputForWith(ctx context.Context, game *domain.Game, profileName string, ref *domain.ModReference, sourceID, modID string, answers map[string]domain.FomodChoices, states fileStatesOpts) fomodInput {
	in := fomodInput{files: s.fileStatesForWith(ctx, game, profileName, domain.ModKey(sourceID, modID), states),
		recipeMD5: s.recipeMD5For(ctx, game, profileName, sourceID, modID)}
	if ref != nil {
		in.fileChoices = ref.FomodFiles.Clone()
		in.orderHint, in.fileIDs = slices.Clone(ref.FomodOrder), slices.Clone(ref.FileIDs)
		// #610: the file lists and patches the files install with.
		in.fileLists, in.patches = ref.FileList.Clone(), ref.Patches.Clone()
	}
	prefix := domain.FomodFileKey(sourceID, modID, "")
	for key, a := range answers {
		if fileID, ok := strings.CutPrefix(key, prefix); ok && fileID != "" && !a.IsZero() {
			if in.fileAnswers == nil {
				in.fileAnswers = domain.FomodFiles{}
			}
			in.fileAnswers[fileID] = a
		}
	}
	if a, ok := answers[domain.ModKey(sourceID, modID)]; ok && !a.IsZero() {
		in.choices, in.answered = a, true
		if ref != nil {
			// #615: a re-asked question preselects what the recording
			// chose for the groups these answers have not reached.
			in.recorded = ref.Fomod
		}
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
//
// A rebuild reproduces the row's own tree, so the installer's file
// conditions are answered as they were when it was installed - the
// recorded file_states, laid over the current ones for any path the
// recording does not name - not as the profile's files stand now.
func (s *Service) rebuildInput(ctx context.Context, game *domain.Game, row *domain.InstalledMod) fomodInput {
	return asRebuild(s.replayInput(ctx, game, row.ProfileName, row.SourceID, row.ID), row)
}

// asRebuild turns a replay input into a rebuild of row's own tree: raw for
// a row installed before FOMOD support, else answered by the recorded file
// states first (rebuildInput) - each file's own, for a mod recorded per
// file (#634; applied by forFile).
func asRebuild(in fomodInput, row *domain.InstalledMod) fomodInput {
	if row.Variant == "" && in.choices.IsZero() && in.fileChoices.IsZero() {
		in.raw = true
		return in
	}
	in.rebuild = true
	return in
}

// recordedFileStates answers a path the recording names with its recorded
// state, and any other through inner.
type recordedFileStates struct {
	recorded map[string]string
	inner    fomod.FileStates
}

// State implements fomod.FileStates.
func (r recordedFileStates) State(path string) fomod.FileState {
	if st, ok := r.recorded[path]; ok {
		for _, s := range []fomod.FileState{fomod.FileMissing, fomod.FileInactive, fomod.FileActive} {
			if s.String() == st {
				return s
			}
		}
	}
	if r.inner == nil {
		return fomod.FileMissing
	}
	return r.inner.State(path)
}

// unifyFomod gives one mod's acquired files the single cache entry its
// FOMOD installers resolved to (#614): every file of a mod is one entry, so
// a variant the installers' choices produced is the variant of all of
// them, and the first file commits UNSEEDED - the resolved tree replaces
// the entry rather than layering onto whatever an earlier resolve left
// there. mod.Variant becomes the variant.
//
// One installer among the files names the entry by its choices, and the
// files commit in the order the flow acquired them, as before #634.
// Several (#634: a collection pinning a mod's main file and a patch, each a
// FOMOD) name it by their choices together and by the ONE order every flow
// layers them in (canonicalLayerOrder over hint, the ref's fomod_order):
// the files come back in that order, and the caller commits them so.
//
// The outcome is for the flow to record on the ref: nil when nothing was
// acquired (the ref's choices are kept), plainTree when the files were
// acquired and none was an installer (the ref's choices are cleared - they
// would name a tree the row does not have), else the resolved choices.
func unifyFomod(mod *domain.Mod, files []*acquiredFile, hint []string) ([]*acquiredFile, *fomodResolved, error) {
	if len(files) == 0 {
		return files, nil, nil
	}
	var installers []*acquiredFile
	for _, a := range files {
		if a.fomod != nil {
			installers = append(installers, a)
		}
	}
	var found *fomodResolved
	switch len(installers) {
	case 0:
		found = plainTree()
	case 1:
		found = installers[0].fomod
	default:
		ids := make([]string, len(files))
		for i, a := range files {
			ids[i] = a.file.ID
		}
		order := canonicalLayerOrder(ids, hint)
		files = slices.Clone(files)
		slices.SortStableFunc(files, func(a, b *acquiredFile) int {
			return slices.Index(order, a.file.ID) - slices.Index(order, b.file.ID)
		})
		perFile := domain.FomodFiles{}
		for _, a := range installers {
			perFile[a.file.ID] = a.fomod.choices
		}
		var kept []string
		for _, id := range hint {
			if slices.Contains(ids, id) && !slices.Contains(kept, id) {
				kept = append(kept, id)
			}
		}
		found = &fomodResolved{variant: fomodFilesVariant(perFile, order), files: perFile, order: kept}
	}
	// #610: the files' lists and patches name the tree too.
	inputs := map[string]domain.TreeInputs{}
	for _, a := range files {
		if !a.treeInputs.IsZero() {
			inputs[a.file.ID] = a.treeInputs
		}
	}
	if variant := treeInputsVariant(found.variant, inputs); variant != found.variant {
		withInputs := *found
		withInputs.variant = variant
		found = &withInputs
	}
	for _, a := range files {
		a.mod.Variant = found.variant
	}
	if found.variant != "" {
		files[0].unseeded = true
	}
	mod.Variant = found.variant
	return files, found, nil
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
	op fomodOp
	// rec is what fomodSet records: one installer's choices, or one per
	// file (#634).
	rec domain.FomodRecord
}

// fomodRecordOf is the profile write a resolve outcome calls for: keep for
// none (nothing acquired), clear for a plain tree, set for an installer's
// choices (or several installers', per file).
func fomodRecordOf(r *fomodResolved) fomodRecord {
	switch {
	case r == nil:
		return fomodRecord{op: fomodKeep}
	case r.record().IsZero():
		return fomodRecord{op: fomodClear}
	default:
		return fomodRecord{op: fomodSet, rec: r.record()}
	}
}

// resolvedFromRef is the outcome a cached entry of ref's recorded choices
// stands for: their tree, already built (#614, per file #634).
func resolvedFromRef(ref *domain.ModReference, variant string) *fomodResolved {
	rec := ref.FomodRecord()
	if rec.PerFile() {
		return &fomodResolved{variant: variant, files: rec.Files.Clone(), order: slices.Clone(rec.Order)}
	}
	return &fomodResolved{variant: variant, choices: rec.Choices}
}

// modFomodAsks collects the FOMOD questions one mod's files raise while
// they are acquired (#634), so a mod whose files each carry an installer
// asks every one in ONE round, each labelled with its file - and a mod
// with one installer asks exactly as before, keyed by the mod.
type modFomodAsks struct {
	asks []fileAsk
}

// fileAsk is one file's question.
type fileAsk struct {
	file *domain.DownloadableFile
	q    *FomodChoicesRequiredError
}

// isFomodQuestion reports whether err is a FOMOD question.
func isFomodQuestion(err error) bool {
	var q *FomodChoicesRequiredError
	return err != nil && asFomodQuestion(err, &q)
}

// take keeps err when it is a FOMOD question, and reports whether it was:
// the flow then goes on to the mod's next file rather than stop.
func (m *modFomodAsks) take(err error, file *domain.DownloadableFile) bool {
	var q *FomodChoicesRequiredError
	if !asFomodQuestion(err, &q) {
		return false
	}
	m.asks = append(m.asks, fileAsk{file: file, q: q})
	return true
}

// finish is the mod's refusal once every file is acquired: one
// *FomodChoicesRequiredError over every question, labelled per file when
// the mod has more than one installer (the asked files and the acquired
// files that resolved one); ErrNotFomodInstalled for a reconfigure (fx.ask)
// none of whose files is an installer; else nil.
func (m *modFomodAsks) finish(mod *domain.Mod, fx fomodInput, acquired []*acquiredFile) error {
	installers := len(m.asks)
	for _, a := range acquired {
		if a.fomod != nil {
			installers++
		}
	}
	if installers > 1 && fx.answered {
		// #634: one answer for the mod cannot be meant for every one of its
		// installers; each is answered under its own file key.
		var unanswered []string
		for _, a := range m.asks {
			if fx.fileAnswers[a.file.ID].IsZero() {
				unanswered = append(unanswered, a.file.ID)
			}
		}
		for _, a := range acquired {
			if a.fomod != nil && fx.fileAnswers[a.file.ID].IsZero() {
				unanswered = append(unanswered, a.file.ID)
			}
		}
		if len(unanswered) > 0 {
			keys := make([]string, len(unanswered))
			for i, id := range unanswered {
				keys[i] = domain.FomodFileKey(mod.SourceID, mod.ID, id)
			}
			return &FomodInstallError{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name,
				Reason: fmt.Sprintf("it has an installer in each of %d files, so one answer for the mod (%s) cannot answer them; answer each file's installer under its own key (%s)",
					installers, domain.ModKey(mod.SourceID, mod.ID), strings.Join(keys, ", "))}
		}
	}
	if len(m.asks) == 0 {
		if fx.ask && fx.softAsk && installers == 0 {
			return &FomodInstallError{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name,
				Reason: "its archive is not a FOMOD installer, so there are no installer choices to change", err: ErrNotFomodInstalled}
		}
		return nil
	}
	merged := &FomodChoicesRequiredError{}
	for _, a := range m.asks {
		for _, q := range a.q.Mods {
			if installers > 1 {
				q.FileID, q.FileName = a.file.ID, fileLabel(a.file)
			}
			merged.Mods = append(merged.Mods, q)
		}
	}
	return merged
}

// fileLabel is a file's display name for a question: its name, else its
// file name.
func fileLabel(f *domain.DownloadableFile) string {
	if f.Name != "" {
		return f.Name
	}
	return f.FileName
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
	// requireVariant, when set, is the only tree a rebuild may commit: a
	// resolve to any other variant is refused with ReasonFomodReanswer
	// BEFORE anything is committed, so no unused entry is left behind.
	requireVariant *string
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
	var asks modFomodAsks
	fx.softAsk = fx.ask
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
			// #634: the mod's question is one failure, whichever of its
			// files ask: the first reports it, as a lone installer always
			// has, and a later file's question is no failure of its own.
			afterErr := err
			if isFomodQuestion(err) && len(asks.asks) > 0 {
				afterErr = nil
			}
			hooks.after(i, f, checksum, afterErr)
		}
		if err != nil {
			if asks.take(err, f) {
				// #634: the mod's other files may ask too; ask them all.
				continue
			}
			return nil, nil, err
		}
		acquired = append(acquired, a)
	}
	if err := asks.finish(mod, fx, acquired); err != nil {
		return nil, nil, err
	}
	// #634: the files commit in their layer order; the results stay in the
	// caller's order.
	position := make(map[*acquiredFile]int, len(acquired))
	for i, a := range acquired {
		position[a] = i
	}
	committed, resolved, err := unifyFomod(mod, acquired, fx.orderHint)
	if err != nil {
		return nil, nil, err
	}
	if hooks.requireVariant != nil && mod.Variant != *hooks.requireVariant {
		return nil, nil, &FomodInstallError{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name, Reason: ReasonFomodReanswer}
	}
	results := make([]*DownloadModResult, len(acquired))
	for _, a := range committed {
		res, err := s.commitAcquired(ctx, a)
		if err != nil {
			return nil, nil, err
		}
		results[position[a]] = res
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
//
// A reference whose files have a file list or patches (#610) drifts the
// same way: they name the tree too.
func fomodDrift(ref domain.ModReference, row *domain.InstalledMod) bool {
	return row != nil && (!ref.FomodRecord().IsZero() || ref.HasTreeInputs()) && row.Variant != refVariant(&ref)
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

// IsLegacyFomodInstall reports whether row is a FOMOD mod installed before
// lmm ran FOMOD installers (#614): its plain cache entry (no variant) still
// holds the archive's own fomod/ModuleConfig.xml - the raw archive, every
// option's files at once, was what got deployed. Such an install keeps
// working exactly as before (deploy's self-heal and verify --fix rebuild it
// raw); reconfiguring it runs its installer and installs the chosen tree.
// A row with a variant, an external one, or one whose entry is not cached
// is not one.
func (s *Service) IsLegacyFomodInstall(game *domain.Game, row *domain.InstalledMod) bool {
	if game == nil || row == nil || row.Variant != "" || row.External {
		return false
	}
	files, err := s.GetGameCache(game).ListFiles(modCacheKey(game.ID, row.Mod))
	if err != nil {
		return false
	}
	inst, err := fomod.Locate("", slashMembers(files))
	return err == nil && inst != nil
}

// holdsFomodInstaller reports whether the cache entry k holds a FOMOD
// installer's files (fomod/ModuleConfig.xml, or one Locate refuses): a
// plain entry that does is a legacy raw install's, never the resolved tree
// of a fresh one (#614).
func holdsFomodInstaller(c *cache.Cache, k cache.Key) bool {
	files, err := c.ListFiles(k)
	if err != nil {
		return false
	}
	inst, err := fomod.Locate("", slashMembers(files))
	return err != nil || inst != nil
}
