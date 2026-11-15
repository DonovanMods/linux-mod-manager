// Package core: this file is a collection's to-do list (#660, owner
// rulings 2026-11-15). A collection import is never all or nothing: it
// installs every item it can, and each item it could not stays in the
// profile, in its place, with a to-do record (domain.ModReference.Todo):
// why, and the one action that resolves it. The list is what both
// frontends show as "Finish collection"; collection_finish.go resolves it.
//
// An item is on the list when its collection profile lists it, switched
// on, with no install under the profile: with its record when it has one,
// else as TodoNotInstalled (an older lmm rewrote the profile and dropped
// the record, or the import was interrupted before writing it). An item
// that has an install is resolved, whatever record it still carries.
package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// The one action that resolves a to-do (CollectionTodoItem.Action). Every
// item can also be skipped.
const (
	// TodoActionAnswerInstaller: answer the item's FOMOD installer; the
	// answers install it.
	TodoActionAnswerInstaller = "answer_installer"
	// TodoActionChooseFile: the pinned file is gone - use the mod's current
	// file, leave the gone file out (CanSkipFile), or skip the mod.
	TodoActionChooseFile = "choose_file"
	// TodoActionRetry: try the install again (once the tool the remedy
	// names is installed, for an unsupported archive).
	TodoActionRetry = "retry"
	// TodoActionDownload: download the file by hand and install it from
	// the downloads folder (`lmm profile fill`).
	TodoActionDownload = "download"
)

// CollectionTodoItem is one item of a collection profile's to-do list:
// the mod, why it is not installed, and what resolves it.
type CollectionTodoItem struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Name is the mod's name, when the import knew it.
	Name string `json:"name,omitempty"`
	// Reason is a domain.Todo* reason; Detail the failure's own text.
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
	// FomodReason is, for an installer's questions, why it asks
	// (FomodReasonUnanswered, FomodReasonStale, FomodReasonArchiveMismatch).
	FomodReason string `json:"fomod_reason,omitempty"`
	// Action is the one TodoAction* that resolves it, and Remedy the
	// sentence that says so.
	Action string `json:"action"`
	Remedy string `json:"remedy"`
	// FileIDs and CanSkipFile are a gone pin's: the files the source no
	// longer offers, and whether leaving them out installs the rest.
	FileIDs     []string `json:"file_ids,omitempty"`
	CanSkipFile bool     `json:"can_skip_file,omitzero"`
}

// Key is the item's domain.ModKey, which a finish request names it by.
func (t CollectionTodoItem) Key() string { return domain.ModKey(t.SourceID, t.ModID) }

// label is the item as a sentence names it.
func (t CollectionTodoItem) label() string {
	if t.Name != "" {
		return t.Name
	}
	return t.Key()
}

// CollectionTodoReport is a collection profile's to-do list (#660): what
// `lmm profile finish` lists and the web UI's Finish collection panel
// shows. Total counts the items the profile lists, Todos the ones that
// need the user, in the profile's load order.
type CollectionTodoReport struct {
	Profile string `json:"profile"`
	// Collection is the collection's name, empty for a profile with none.
	Collection string               `json:"collection,omitempty"`
	Total      int                  `json:"total"`
	Todos      []CollectionTodoItem `json:"todos"`
}

// CollectionIncomplete is what a deploy, switch or apply plan of an
// unfinished collection profile says about it (#660 ruling 3): the plan
// may run, and the game may not behave as the curator intended. Items are
// the unresolved to-dos, which the plan leaves alone - they are finished
// with `lmm profile finish` (Finish collection), never retried
// mid-converge.
type CollectionIncomplete struct {
	Warning string               `json:"warning"`
	Items   []CollectionTodoItem `json:"items"`
}

// incompleteOf is the plan's readout of todos; nil for none.
func incompleteOf(todos []CollectionTodoItem) *CollectionIncomplete {
	if len(todos) == 0 {
		return nil
	}
	return &CollectionIncomplete{Warning: incompleteWarning(todos), Items: todos}
}

// recordedTodos are profile's to-dos that carry a record: the items a
// converge leaves alone (an item with no record converges as any listed
// mod does). installed names the profile's rows by domain.ModKey.
func recordedTodos(profile *domain.Profile, installed map[string]bool) []CollectionTodoItem {
	var out []CollectionTodoItem
	for _, t := range collectionTodos(profile, installed) {
		if t.Reason != domain.TodoNotInstalled {
			out = append(out, t)
		}
	}
	return out
}

// incompleteWarning is the warning's sentence, naming the items.
func incompleteWarning(todos []CollectionTodoItem) string {
	if len(todos) == 0 {
		return ""
	}
	names := make([]string, 0, len(todos))
	for _, t := range todos {
		names = append(names, t.label())
	}
	return fmt.Sprintf("collection incomplete: %d item(s) unresolved (%s); the game may not behave as the curator intended - finish it with `lmm profile finish`",
		len(todos), strings.Join(names, ", "))
}

// todoItem is ref's to-do as the list shows it; name is used when the
// record has none.
func todoItem(ref domain.ModReference, todo *domain.CollectionTodo) CollectionTodoItem {
	it := CollectionTodoItem{SourceID: ref.SourceID, ModID: ref.ModID, Name: todo.Name, Reason: todo.Reason,
		Detail: todo.Detail, FomodReason: todo.FomodReason, FileIDs: todo.FileIDs, CanSkipFile: todo.CanSkipFile}
	it.Action, it.Remedy = todoRemedy(it)
	return it
}

// todoRemedy is an item's one action and the sentence that says so - a
// collection item's own remedy, never an installed mod's ("reinstall the
// mod or run 'lmm update'", which is wrong for a mod that was never
// installed).
func todoRemedy(t CollectionTodoItem) (string, string) {
	name := t.label()
	switch t.Reason {
	case domain.TodoInstallerQuestions:
		switch t.FomodReason {
		case FomodReasonStale:
			return TodoActionAnswerInstaller, fmt.Sprintf("%s's installer changed since the curator recorded their choices; answer it to continue", name)
		case FomodReasonArchiveMismatch:
			return TodoActionAnswerInstaller, fmt.Sprintf("%s's download is not the archive the curator recorded their choices against; answer its installer to continue", name)
		}
		return TodoActionAnswerInstaller, fmt.Sprintf("%s's installer has choices the collection does not record; answer it to continue", name)
	case domain.TodoGonePin:
		text := fmt.Sprintf("the source no longer offers the file the collection pins for %s: use the mod's current file, or skip it", name)
		if t.CanSkipFile {
			text = fmt.Sprintf("the source no longer offers a file the collection pins for %s: leave that file out and install the rest, use the mod's current file, or skip it", name)
		}
		return TodoActionChooseFile, text
	case domain.TodoManualDownload:
		return TodoActionDownload, fmt.Sprintf("download %s by hand from its page, then install it from your downloads folder", name)
	case domain.TodoUnsupportedArchive:
		if strings.Contains(t.Detail, ErrSevenZipMissing.Error()) {
			return TodoActionRetry, fmt.Sprintf("install 7-Zip (the 7z command, package p7zip) so lmm can extract %s, then retry", name)
		}
		return TodoActionRetry, fmt.Sprintf("lmm cannot extract %s's archive; retry once a tool that can is installed, or skip it", name)
	case domain.TodoDownloadFailed:
		return TodoActionRetry, fmt.Sprintf("the download of %s failed; retry it", name)
	case domain.TodoNotInstalled:
		return TodoActionRetry, fmt.Sprintf("%s is not installed; retry to install it", name)
	}
	return TodoActionRetry, fmt.Sprintf("%s was refused; fix what the reason names, then retry, or skip it", name)
}

// sourceFetchError marks a failure to ask the source about a mod (its
// description, its file list): a download failure for the to-do, which a
// retry may well get past. Its text is the cause's.
type sourceFetchError struct{ err error }

func (e *sourceFetchError) Error() string { return e.err.Error() }
func (e *sourceFetchError) Unwrap() error { return e.err }

// todoFor is the to-do one failed collection item records: reason is the
// failure's text, err the error behind it (nil for a failure at commit -
// a deploy or a save - which records TodoRefused).
func todoFor(name, reason string, err error) *domain.CollectionTodo {
	t := &domain.CollectionTodo{Reason: domain.TodoRefused, Name: name, Detail: reason}
	var question *FomodChoicesRequiredError
	var gone *StoredFilesUnavailableError
	var dl *DownloadError
	var fetch *sourceFetchError
	switch {
	case err == nil:
	case asFomodQuestion(err, &question):
		t.Reason, t.Detail = domain.TodoInstallerQuestions, ""
		for _, q := range question.Mods {
			if t.FomodReason == "" || q.Reason == FomodReasonStale {
				t.FomodReason = q.Reason
			}
		}
	case errors.As(err, &gone):
		t.Reason, t.Detail, t.FileIDs = domain.TodoGonePin, gone.Bare(), gone.FileIDs
	case errors.Is(err, ErrStoredFilesUnavailable), errors.Is(err, ErrNoDownloadableFiles):
		t.Reason = domain.TodoGonePin
	case errors.Is(err, source.ErrManualDownload), errors.Is(err, source.ErrAccountRestricted),
		errors.As(err, &dl) && dl.ManualDownload:
		t.Reason = domain.TodoManualDownload
	case errors.Is(err, ErrSevenZipMissing), errors.Is(err, ErrUnsupportedArchive):
		t.Reason = domain.TodoUnsupportedArchive
	case errors.As(err, new(*LoaderRequiredError)), errors.Is(err, ErrTreeInput), errors.Is(err, ErrCodePatch):
		// A refusal of its own, with its own setup steps.
	case errors.As(err, &dl), errors.As(err, &fetch):
		t.Reason = domain.TodoDownloadFailed
	}
	return t
}

// collectionTodos is profile's to-do list against its installed rows
// (installed: by domain.ModKey). Empty for a profile with no collection.
func collectionTodos(profile *domain.Profile, installed map[string]bool) []CollectionTodoItem {
	if !isCollectionProfile(profile) {
		return nil
	}
	var out []CollectionTodoItem
	seen := map[string]bool{}
	for _, ref := range profile.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if seen[key] || ref.External || ref.Disabled || installed[key] {
			continue
		}
		seen[key] = true
		todo := ref.Todo
		if todo == nil {
			if !ref.FromCollection {
				continue
			}
			todo = &domain.CollectionTodo{Reason: domain.TodoNotInstalled}
		}
		out = append(out, todoItem(ref, todo))
	}
	return out
}

// profileTodos is the to-do list of gameID's profile, read from its
// profile file and installed rows: nil for a profile that is not a
// collection's.
func (s *Service) profileTodos(ctx context.Context, gameID string, profile *domain.Profile) ([]CollectionTodoItem, error) {
	if !isCollectionProfile(profile) {
		return nil, nil
	}
	rows, err := s.GetInstalledMods(ctx, gameID, profile.Name)
	if err != nil {
		return nil, err
	}
	installed := make(map[string]bool, len(rows))
	for _, r := range rows {
		installed[domain.ModKey(r.SourceID, r.ID)] = true
	}
	return collectionTodos(profile, installed), nil
}

// CollectionTodos is profileName's to-do list (#660): the items its
// collection lists that are not installed, each with why and the action
// that resolves it. A profile with no collection has none. Read-only.
func (s *Service) CollectionTodos(ctx context.Context, game *domain.Game, profileName string) (*CollectionTodoReport, error) {
	profile, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	todos, err := s.profileTodos(ctx, game.ID, profile)
	if err != nil {
		return nil, err
	}
	report := &CollectionTodoReport{Profile: profile.Name, Todos: todos}
	if report.Todos == nil {
		report.Todos = []CollectionTodoItem{}
	}
	if profile.Collection != nil {
		report.Collection = profile.Collection.Title
	}
	if isCollectionProfile(profile) {
		report.Total = countListed(profile)
	}
	return report, nil
}

// isCollectionProfile reports whether profile is a collection's: it
// records the collection, or a collection brought in any of its entries (a
// document whose collection identity is empty records none).
func isCollectionProfile(profile *domain.Profile) bool {
	if profile == nil {
		return false
	}
	if profile.Collection != nil {
		return true
	}
	for _, ref := range profile.Mods {
		if ref.FromCollection || ref.Todo != nil {
			return true
		}
	}
	return false
}

// countListed counts the mods profile lists switched on, each once.
func countListed(profile *domain.Profile) int {
	seen := map[string]bool{}
	for _, ref := range profile.Mods {
		if !ref.Disabled {
			seen[domain.ModKey(ref.SourceID, ref.ModID)] = true
		}
	}
	return len(seen)
}

// writeTodos records todos on profileName's references in one write (by
// domain.ModKey; a nil value clears a record). A reference the profile no
// longer has is left alone.
func (pm *ProfileManager) writeTodos(ctx context.Context, gameID, profileName string, todos map[string]*domain.CollectionTodo) error {
	if len(todos) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	profile, err := pm.Get(ctx, gameID, profileName)
	if err != nil {
		return err
	}
	changed := false
	for i := range profile.Mods {
		ref := &profile.Mods[i]
		todo, ok := todos[domain.ModKey(ref.SourceID, ref.ModID)]
		if !ok || ref.Todo.Equal(todo) {
			continue
		}
		ref.Todo = todo.Clone()
		changed = true
	}
	if !changed {
		return nil
	}
	return pm.save(profile)
}
