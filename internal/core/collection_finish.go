// Package core: this file is "Finish collection" (#660): resolving a
// collection profile's to-do list (collection_todo.go) - `lmm profile
// finish`, and the web UI's Finish collection panel as a job.
//
// Each item resolves on its own, in any order: retried (and installed
// with the FOMOD answers the Apply's options carry, for an installer's
// questions), installed at the mod's current file in place of a gone pin,
// installed without its gone secondary files, or skipped - which drops it
// from the profile and records the skip in the collection baseline, so a
// collection update neither re-adds it silently nor counts it as the
// user's edit (#657's baseline.skipped). An item installs at its place in
// the profile's load order: its reference is already there.
//
// The install runs the import's engine (acquire_engine.go), as a profile
// fill does, blocking on nothing: an item that fails again keeps (or
// changes) its to-do, and the others install. An archive an earlier
// attempt kept is installed without a second download, and the plan and
// the progress say so.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The ways a finish installs an item (CollectionFinishItem.Choice).
const (
	// FinishRetry installs the item as the profile pins it.
	FinishRetry = "retry"
	// FinishUseCurrent installs a gone pin's mod at its current main file
	// in place of the gone one.
	FinishUseCurrent = "use_current"
	// FinishSkipFile installs a gone pin's mod without its gone secondary
	// files.
	FinishSkipFile = "skip_file"
)

// ErrBadFinishChoice refuses a finish request that names no item on the
// to-do list, names one two ways, or asks for what its item does not offer.
var ErrBadFinishChoice = errors.New("invalid choice for an item to finish")

// IsBadFinishChoice reports whether err is ErrBadFinishChoice.
func IsBadFinishChoice(err error) bool { return errors.Is(err, ErrBadFinishChoice) }

// ErrNotACollectionProfile refuses finishing a profile with no collection
// behind it: only a collection's items are to-dos.
var ErrNotACollectionProfile = errors.New("the profile was not imported from a collection")

// CollectionFinishRequest is what PlanCollectionFinish plans: which items
// to resolve and how, by domain.ModKey. RetryAll retries every item not
// named otherwise (a manual download's included: it installs once the
// account is served downloads).
type CollectionFinishRequest struct {
	Profile    string
	Retry      []string
	RetryAll   bool
	UseCurrent []string
	SkipFile   []string
	Skip       []string
}

// CollectionFinishItem is one item a finish installs, and how.
type CollectionFinishItem struct {
	Todo CollectionTodoItem `json:"todo"`
	// Choice is FinishRetry, FinishUseCurrent or FinishSkipFile.
	Choice string `json:"choice"`
	// Current is, for FinishUseCurrent, the file installed in place of the
	// gone one.
	Current *CollectionCurrentFile `json:"current,omitempty"`
	// Reused counts this item's files an earlier attempt downloaded and
	// kept: installed without a second download.
	Reused int `json:"reused,omitzero"`
}

// CollectionFinishPlan is PlanCollectionFinish's answer: the profile's
// to-do list as it stands, what this finish installs and skips, and how
// many of the downloads it needs an earlier attempt kept.
type CollectionFinishPlan struct {
	Profile string `json:"profile"`
	GameID  string `json:"game_id"`
	// Todos is the whole list before this finish.
	Todos   []CollectionTodoItem   `json:"todos"`
	Install []CollectionFinishItem `json:"install"`
	Skip    []CollectionTodoItem   `json:"skip"`
	// ReusableDownloads counts the archives the install reuses rather
	// than downloading again ("reusing N downloaded files").
	ReusableDownloads int `json:"reusable_downloads,omitzero"`
	// RecordedOnly and ActiveProfile are as a profile import's (#462): a
	// profile that is not the game's active one has its mods recorded,
	// not deployed.
	RecordedOnly  bool   `json:"recorded_only,omitzero"`
	ActiveProfile string `json:"active_profile,omitempty"`
	// NoChanges says the request resolves nothing.
	NoChanges bool `json:"no_changes"`

	snapshot installedSnapshot
	// refs are the references to install, as they will be installed (a
	// use_current or skip_file one rewritten), in the profile's order;
	// base the profile's own references to them, as planned.
	refs []domain.ModReference
	base map[string]domain.ModReference
}

// CollectionFinishOptions are an ApplyCollectionFinish's answers: FOMOD
// installer answers, keyed by domain.ModKey, as InstallOptions.Fomod.
type CollectionFinishOptions struct {
	Fomod map[string]domain.FomodChoices
}

// CollectionFinishResult is what a finish did: how many items it
// installed (or recorded, on a profile that is not the active one), which
// it skipped, the ones that failed again, and the list left.
type CollectionFinishResult struct {
	Profile   string        `json:"profile"`
	Installed int           `json:"installed"`
	Recorded  int           `json:"recorded,omitzero"`
	Skipped   []string      `json:"skipped,omitempty"`
	Failed    int           `json:"failed"`
	Failures  []ItemFailure `json:"failures,omitempty"`
	// Todos is the list left, and Total the items the profile lists.
	Todos           []CollectionTodoItem `json:"todos"`
	Total           int                  `json:"total"`
	ReusedDownloads int                  `json:"reused_downloads,omitzero"`
	Warnings        []string             `json:"warnings,omitempty"`
	// FomodChoicesRequired is the question document of every retried item
	// whose installer asked: answer it and finish again with the answers.
	FomodChoicesRequired *FomodChoicesRequiredError `json:"fomod_choices_required,omitempty"`
	RecordedOnly         bool                       `json:"recorded_only,omitzero"`
	ActiveProfile        string                     `json:"active_profile,omitempty"`
}

// CollectionFinishIncompleteError is a finish in which an item failed
// again: the others were resolved, and the failed one keeps its to-do.
// Details() is the result; it unwraps to the FOMOD question when an
// installer asked, so a frontend answers it as it answers an install's.
type CollectionFinishIncompleteError struct {
	Profile string
	Result  *CollectionFinishResult
}

// Error names how many items are left.
func (e *CollectionFinishIncompleteError) Error() string {
	names := make([]string, 0, len(e.Result.Failures))
	for _, f := range e.Result.Failures {
		names = append(names, cmpOrKey(f.Name, f.SourceID, f.ModID)+": "+f.Reason)
	}
	return fmt.Sprintf("collection %s is not finished - %d item(s) failed again: %s", e.Profile, e.Result.Failed, strings.Join(names, "; "))
}

// Details implements the --json error envelope's extension point.
func (e *CollectionFinishIncompleteError) Details() any { return e.Result }

// Unwrap is the FOMOD question an installer asked, if one did.
func (e *CollectionFinishIncompleteError) Unwrap() error {
	if e.Result.FomodChoicesRequired != nil {
		return e.Result.FomodChoicesRequired
	}
	return nil
}

// cmpOrKey is name, else the mod's key.
func cmpOrKey(name, sourceID, modID string) string {
	if name != "" {
		return name
	}
	return domain.ModKey(sourceID, modID)
}

// PlanCollectionFinish plans resolving req's items of req.Profile's to-do
// list. Nothing is written. A request naming no item plans nothing
// (NoChanges) and still lists the to-dos.
func (s *Service) PlanCollectionFinish(ctx context.Context, game *domain.Game, req CollectionFinishRequest) (*CollectionFinishPlan, error) {
	if err := s.refuseUndeployable(ctx, game.ID); err != nil {
		return nil, err
	}
	profile, err := s.NewProfileManager().Get(ctx, game.ID, req.Profile)
	if err != nil {
		return nil, err
	}
	if !isCollectionProfile(profile) {
		return nil, fmt.Errorf("%w: %s", ErrNotACollectionProfile, profile.Name)
	}
	live, recordedOnly, err := s.importScope(ctx, game.ID, profile.Name)
	if err != nil {
		return nil, err
	}
	rows, err := s.GetInstalledMods(ctx, game.ID, profile.Name)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.snapshotOf(ctx, game.ID, rows)
	if err != nil {
		return nil, err
	}
	installed := make(map[string]bool, len(rows))
	for _, r := range rows {
		installed[domain.ModKey(r.SourceID, r.ID)] = true
	}
	todos := collectionTodos(profile, installed)
	plan := &CollectionFinishPlan{Profile: profile.Name, GameID: game.ID, Todos: todos,
		Install: []CollectionFinishItem{}, Skip: []CollectionTodoItem{},
		RecordedOnly: recordedOnly, ActiveProfile: live, snapshot: snapshot, base: map[string]domain.ModReference{}}
	if plan.Todos == nil {
		plan.Todos = []CollectionTodoItem{}
	}

	choice, err := finishChoices(req, todos)
	if err != nil {
		return nil, err
	}
	for _, todo := range todos {
		how, ok := choice[todo.Key()]
		if !ok {
			continue
		}
		ref := *profile.FindRef(todo.SourceID, todo.ModID)
		plan.base[todo.Key()] = ref
		if how == GonePinSkip {
			plan.Skip = append(plan.Skip, todo)
			continue
		}
		item := CollectionFinishItem{Todo: todo, Choice: how}
		switch how {
		case FinishUseCurrent:
			current, err := s.currentMainFile(ctx, game, ref)
			if err != nil {
				return nil, err
			}
			item.Current = current
			ref = standInRef(ref, todo.FileIDs, current)
		case FinishSkipFile:
			ref = standInRef(ref, todo.FileIDs, nil)
		}
		for _, id := range ref.FileIDs {
			if s.hasRetainedDownload(ref.SourceID, ref.ModID, id, ref.Version) {
				item.Reused++
			}
		}
		plan.ReusableDownloads += item.Reused
		plan.refs = append(plan.refs, ref)
		plan.Install = append(plan.Install, item)
	}
	plan.NoChanges = len(plan.Install) == 0 && len(plan.Skip) == 0
	return plan, nil
}

// finishChoices is how req resolves each item it names, by key: a key that
// names no to-do, one named two ways, and a choice the item does not offer
// are ErrBadFinishChoice.
func finishChoices(req CollectionFinishRequest, todos []CollectionTodoItem) (map[string]string, error) {
	byKey := map[string]CollectionTodoItem{}
	for _, t := range todos {
		byKey[t.Key()] = t
	}
	out := map[string]string{}
	for _, named := range []struct {
		flag, how string
		keys      []string
	}{
		{"--retry", FinishRetry, req.Retry},
		{"--use-current", FinishUseCurrent, req.UseCurrent},
		{"--skip-file", FinishSkipFile, req.SkipFile},
		{"--skip", GonePinSkip, req.Skip},
	} {
		for _, key := range named.keys {
			todo, ok := byKey[key]
			if !ok {
				return nil, fmt.Errorf("%w: %s is not on the profile's list of items to finish", ErrBadFinishChoice, key)
			}
			if prior, dup := out[key]; dup && prior != named.how {
				return nil, fmt.Errorf("%w: %s is named two ways; choose one", ErrBadFinishChoice, key)
			}
			switch {
			case named.how == FinishUseCurrent && (todo.Reason != domain.TodoGonePin || todo.CanSkipFile):
				return nil, fmt.Errorf("%w: %s has no gone main file for a current one to stand in for", ErrBadFinishChoice, key)
			case named.how == FinishSkipFile && !todo.CanSkipFile:
				return nil, fmt.Errorf("%w: %s has no gone secondary file to leave out", ErrBadFinishChoice, key)
			}
			out[key] = named.how
		}
	}
	if req.RetryAll {
		for _, t := range todos {
			if _, named := out[t.Key()]; !named {
				out[t.Key()] = FinishRetry
			}
		}
	}
	return out, nil
}

// currentMainFile is the mod's current main file, for a gone pin's
// stand-in: the source's primary file among the ones it still offers.
func (s *Service) currentMainFile(ctx context.Context, game *domain.Game, ref domain.ModReference) (*CollectionCurrentFile, error) {
	mod, err := s.GetMod(ctx, ref.SourceID, game.ID, ref.ModID)
	if err != nil {
		return nil, fmt.Errorf("looking up %s's current file: %w", domain.ModKey(ref.SourceID, ref.ModID), err)
	}
	files, err := s.GetModFiles(ctx, game, ref.SourceID, mod)
	if err != nil {
		return nil, fmt.Errorf("looking up %s's current file: %w", domain.ModKey(ref.SourceID, ref.ModID), err)
	}
	var mains []domain.DownloadableFile
	for _, f := range availableFiles(files) {
		if isMainLikeCategory(f.Category) || f.Category == "" {
			mains = append(mains, f)
		}
	}
	f := primaryFile(mains)
	if f == nil {
		return nil, fmt.Errorf("%w: %s offers no current main file", ErrBadFinishChoice, domain.ModKey(ref.SourceID, ref.ModID))
	}
	return &CollectionCurrentFile{FileID: f.ID, Name: f.Name, Version: f.Version}, nil
}

// standInRef is ref with its gone files replaced by current (nil: left
// out), recording the pin it stands in for, as a gone pin's substitute at
// an import is (#657). The gone files' file lists, patches and per-file
// installer choices go with them.
func standInRef(ref domain.ModReference, gone []string, current *CollectionCurrentFile) domain.ModReference {
	pin := &domain.CollectionPin{Version: ref.Version, FileIDs: slices.Clone(ref.FileIDs)}
	var ids []string
	for _, id := range ref.FileIDs {
		if slices.Contains(gone, id) {
			continue
		}
		ids = append(ids, id)
	}
	if current != nil {
		if !slices.Contains(ids, current.FileID) {
			ids = append(ids, current.FileID)
		}
		if current.Version != "" {
			ref.Version = current.Version
		}
		// Choices recorded for another archive are not replayed.
		ref.Fomod = domain.FomodChoices{}
	}
	ref.FileIDs = ids
	ref.FileList, ref.Patches, ref.FomodFiles = ref.FileList.Clone(), ref.Patches.Clone(), ref.FomodFiles.Clone()
	for _, id := range gone {
		delete(ref.FileList, id)
		delete(ref.Patches, id)
		delete(ref.FomodFiles, id)
	}
	ref.FomodOrder = slices.DeleteFunc(slices.Clone(ref.FomodOrder), func(id string) bool { return slices.Contains(gone, id) })
	if ref.SubstitutedFor == nil {
		ref.SubstitutedFor = pin
	}
	return ref
}

// hasRetainedDownload reports whether an earlier attempt kept the archive
// of (source, mod, file) at version, for a plan to say it is reused.
func (s *Service) hasRetainedDownload(sourceID, modID, fileID, version string) bool {
	dir := s.retainedDownloadDir(sourceID, modID, fileID)
	if dir == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, retainedDownloadFile))
	if err != nil {
		return false
	}
	var meta retainedDownload
	if json.Unmarshal(data, &meta) != nil || meta.FileName == "" || (version != "" && meta.Version != version) {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, filepath.Base(meta.FileName)))
	return err == nil && info.Mode().IsRegular()
}

// ApplyCollectionFinish executes plan: it skips the items the plan skips,
// rewrites the references a stand-in or a left-out file changes, and
// installs the rest through the import's engine, each in its place in the
// load order. An item that fails again keeps its to-do with the new reason
// (*CollectionFinishIncompleteError); the others are resolved either way.
func (s *Service) ApplyCollectionFinish(ctx context.Context, game *domain.Game, plan *CollectionFinishPlan, opts CollectionFinishOptions, sink EventSink) (*CollectionFinishResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &CollectionFinishResult{Profile: plan.Profile, Todos: plan.Todos}, err
	}
	defer release()
	result, err := s.applyCollectionFinish(ctx, game, plan, opts, sink)
	if err == nil && result.Failed > 0 {
		err = &CollectionFinishIncompleteError{Profile: plan.Profile, Result: result}
	}
	return result, err
}

func (s *Service) applyCollectionFinish(ctx context.Context, game *domain.Game, plan *CollectionFinishPlan, opts CollectionFinishOptions, sink EventSink) (*CollectionFinishResult, error) {
	result := &CollectionFinishResult{Profile: plan.Profile, Todos: plan.Todos,
		RecordedOnly: plan.RecordedOnly, ActiveProfile: plan.ActiveProfile}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	if err := s.checkPlanFresh(ctx, game.ID, plan.Profile, plan.snapshot); err != nil {
		return result, err
	}
	if plan.NoChanges {
		return result, nil
	}
	_, recordedOnly, err := s.importScope(ctx, game.ID, plan.Profile)
	if err != nil {
		return result, err
	}
	if recordedOnly != plan.RecordedOnly {
		return result, fmt.Errorf("%w: whether %s is the active profile of %s changed since this finish was planned", ErrStalePlan, plan.Profile, game.ID)
	}
	pm := s.NewProfileManager()
	profile, err := pm.Get(ctx, game.ID, plan.Profile)
	if err != nil {
		return result, err
	}
	for key, want := range plan.base {
		have := profile.FindRef(want.SourceID, want.ModID)
		if have == nil || !sameFinishRef(*have, want) {
			return result, fmt.Errorf("%w: %s's entry in profile %s changed since this finish was planned", ErrStalePlan, key, plan.Profile)
		}
	}

	// The skips and the rewritten references, in one profile write: what
	// the install below reads.
	changed := false
	for _, todo := range plan.Skip {
		ref := profile.FindRef(todo.SourceID, todo.ModID)
		if profile.CollectionBaseline == nil {
			profile.CollectionBaseline = &domain.CollectionBaseline{}
		}
		profile.CollectionBaseline.Skipped = append(profile.CollectionBaseline.Skipped, domain.CollectionSkip{
			SourceID: ref.SourceID, ModID: ref.ModID, Pin: domain.CollectionPin{Version: ref.Version, FileIDs: slices.Clone(ref.FileIDs)}})
		profile.Mods = slices.DeleteFunc(profile.Mods, func(r domain.ModReference) bool {
			return r.SourceID == todo.SourceID && r.ModID == todo.ModID
		})
		result.Skipped = append(result.Skipped, todo.Key())
		changed = true
	}
	for _, ref := range plan.refs {
		have := profile.FindRef(ref.SourceID, ref.ModID)
		if !slices.Equal(have.FileIDs, ref.FileIDs) || have.Version != ref.Version {
			// #660 review S6: the to-do moves with the pin in this same
			// save. Should the run stop before the install, the item is
			// listed as not installed at its new pin - a retry - and never
			// as the gone pin it no longer is, which a second --use-current
			// would stand in for again.
			name := ""
			if have.Todo != nil {
				name = have.Todo.Name
			}
			*have = ref
			have.Todo = &domain.CollectionTodo{Reason: domain.TodoNotInstalled, Name: name,
				Detail: "its pin moved to the files that stand in for the gone ones; not installed yet"}
			changed = true
		}
	}
	if changed {
		if err := pm.save(profile); err != nil {
			return result, err
		}
	}

	if len(plan.refs) > 0 {
		if err := s.finishInstall(ctx, game, plan, profile, !recordedOnly, opts, result, emit); err != nil {
			return result, err
		}
	}
	saved, err := pm.Get(ctx, game.ID, plan.Profile)
	if err != nil {
		return result, err
	}
	todos, err := s.profileTodos(ctx, game.ID, saved)
	if err != nil {
		return result, err
	}
	result.Todos, result.Total = todos, countListed(saved)
	if result.Todos == nil {
		result.Todos = []CollectionTodoItem{}
	}
	return result, nil
}

// sameFinishRef reports whether have is still the reference a finish was
// planned against: its pin, and its to-do.
func sameFinishRef(have, want domain.ModReference) bool {
	return have.Version == want.Version && slices.Equal(have.FileIDs, want.FileIDs) &&
		have.Disabled == want.Disabled && have.Todo.Equal(want.Todo)
}

// finishInstall installs plan's references through the import's engine,
// blocking on nothing, and records each failure's to-do.
func (s *Service) finishInstall(ctx context.Context, game *domain.Game, plan *CollectionFinishPlan, profile *domain.Profile, deploy bool, opts CollectionFinishOptions, result *CollectionFinishResult, emit func(Event)) error {
	refs := plan.refs
	iplan := &ImportPlan{Profile: profile, archiveMD5: map[string]map[string]string{}, todoMode: true}
	for _, ref := range refs {
		// #609: an installer's recorded choices replay only against the
		// archive the collection recorded them for.
		if have := profile.FindRef(ref.SourceID, ref.ModID); have != nil && have.Todo != nil && len(have.Todo.ArchiveMD5) > 0 {
			iplan.archiveMD5[domain.ModKey(ref.SourceID, ref.ModID)] = have.Todo.ArchiveMD5
		}
	}
	ir := &ProfileImportResult{ProfileName: profile.Name}
	total := len(refs)
	emit(StepEvent{Scope: Scope{Op: OpCollectionFinish, Total: total}, Phase: ImportInstalling})
	failed := map[string]string{}
	failedErrs := map[string]error{}
	pm := s.NewProfileManager()
	var installer *Installer
	run := acquireRun[*importStaged]{
		policy: acquirePolicy{mode: acquirePhased, blockNone: true},
		n:      total,
		key:    func(i int) string { return domain.ModKey(refs[i].SourceID, refs[i].ModID) },
		acquire: func(ctx context.Context, i int) (*importStaged, *acquireFailure) {
			key := domain.ModKey(refs[i].SourceID, refs[i].ModID)
			return s.acquireImportItem(ctx, game, iplan, refs[i], Scope{Op: OpCollectionFinish, Index: i + 1, Total: total},
				opts.Fomod, s.importFileStates(game, iplan, key, nil, nil), ir, emit, failed, failedErrs)
		},
		begin: func(ctx context.Context) error {
			var err error
			installer, err = s.getInstallerForProfile(ctx, game, profile.Name)
			return err
		},
		commit: func(ctx context.Context, _ int, st *importStaged) error {
			return s.commitImportItem(ctx, game, iplan, profile.Name, pm, installer, st, deploy, ir, emit)
		},
		release: func(st *importStaged, blocked bool) { s.releaseAcquired(st.files, blocked) },
	}
	runErr := runAcquire(ctx, run)
	result.Installed, result.Recorded, result.Failed = ir.Installed, ir.Recorded, ir.Failed
	result.Failures, result.ReusedDownloads = ir.Failures, ir.ReusedDownloads
	result.Warnings = append(result.Warnings, ir.Warnings...)

	// Each item that failed again keeps a to-do saying why now, in one
	// write - a cancelled run's included, since what failed before the
	// cancellation is true either way.
	records := map[string]*domain.CollectionTodo{}
	var questions []FomodQuestion
	for _, f := range ir.Failures {
		key := domain.ModKey(f.SourceID, f.ModID)
		records[key] = carryTodo(todoFor(f.Name, f.Reason, failedErrs[key]), plan.base[key].Todo, iplan.archiveMD5[key])
		var q *FomodChoicesRequiredError
		if errors.As(failedErrs[key], &q) {
			questions = append(questions, q.Mods...)
		}
	}
	if len(questions) > 0 {
		result.FomodChoicesRequired = &FomodChoicesRequiredError{Mods: questions}
	}
	if err := pm.writeTodos(context.WithoutCancel(ctx), game.ID, profile.Name, records); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("recording why items failed: %v", err))
	}
	if runErr != nil {
		return runErr
	}
	if warnings, err := s.syncMergedPak(ctx, game, profile.Name); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("syncing merged pak: %v", err))
	} else {
		result.Warnings = append(result.Warnings, warnings...)
	}
	return nil
}
