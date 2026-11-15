// gone_pins.go finds the files a source collection pins that its source no
// longer offers, and applies the user's choice for each (#657).
//
// A curator tests a collection with exact files, and mod authors remove
// old files on every update (the Unofficial Skyrim Special Edition Patch
// does), so a collection soon pins a file nobody can download. Found only
// at acquire, after every other download, that failed the whole import. A
// plan now finds it first, and bounded: the published list the plan
// already resolved reports each file it still has; only a mod with a file
// the list does not vouch for costs one listing call, made at most once
// per mod per plan. A pin is gone when the listing leaves its file out (or
// files it as removed). An ARCHIVED file is not gone (owner ruling,
// 2026-11-15): NexusMods still serves it, and the curator's exact file is
// the point of a collection, so it is installed, with a plan note saying it
// is archived.
//
// Each gone pin is the user's to decide (owner ruling, 2026-11-15): install
// the mod's current main file instead, or skip the mod. A pin with a
// current file to stand in for it defaults to that file (owner ruling,
// #660), the plan saying so - and saying when the curator pinned the
// version exactly, which Vortex would not substitute. A pin nothing stands
// in for no longer refuses the import: its item is left a gone_pin to-do
// (collection_todo.go). The choices are plan options, as opting in an
// optional item is, because they change what the plan installs. A substituted entry records the pin it stands in for
// (domain.ModReference.SubstitutedFor); a skipped one is recorded in the
// profile's collection baseline, so a collection update neither undoes the
// substitute nor re-adds the mod silently.
package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The choices for a gone pin (CollectionGonePin.Choice).
const (
	// GonePinUseCurrent installs, for each gone file, the current file of
	// the same role (CollectionGoneFile.Replacement).
	GonePinUseCurrent = "use_current"
	// GonePinSkipFile leaves the gone files out and installs the rest of
	// the mod's pinned files: offered only when every gone file is a
	// secondary one (CollectionGonePin.CanSkipFile).
	GonePinSkipFile = "skip_file"
	// GonePinSkip leaves the whole mod out.
	GonePinSkip = "skip"
)

// The role a gone file played in its mod's pin (CollectionGoneFile.Role).
const (
	// GoneFileMain is the mod's main file: its stand-in is the mod's
	// current main file.
	GoneFileMain = "main"
	// GoneFileSecondary is an optional, update or other file beside a main
	// file that is still offered: its stand-in is a current file of the
	// same category whose name matches, when exactly one does.
	GoneFileSecondary = "secondary"
)

// GonePinChoices is what the user decided for the pinned files the source
// no longer offers (#657), by domain.ModKey: UseCurrent installs each
// named mod's current files of the same role instead, UseCurrentAll every
// gone pin's that has them, SkipFile leaves out each named mod's gone
// secondary files and installs the rest, and Skip leaves each named mod
// out. A key naming no gone pin, one named twice, and a choice the pin
// does not offer are the caller's error (ErrBadGonePinChoice). A named
// choice wins over UseCurrentAll.
type GonePinChoices struct {
	UseCurrent    []string
	UseCurrentAll bool
	SkipFile      []string
	Skip          []string
}

// ErrBadGonePinChoice refuses choices for the pinned files the source no
// longer offers that cannot apply: a mod named two ways, a mod with no
// gone pin, or a choice its pin does not offer.
var ErrBadGonePinChoice = errors.New("invalid choice for a pinned file the source no longer offers")

// IsBadGonePinChoice reports whether err is ErrBadGonePinChoice.
func IsBadGonePinChoice(err error) bool { return errors.Is(err, ErrBadGonePinChoice) }

// named is every key c names, with the flag that named it.
func (c GonePinChoices) named() map[string][]string {
	out := map[string][]string{}
	for flag, keys := range map[string][]string{"--use-current": c.UseCurrent, "--skip-file": c.SkipFile, "--skip": c.Skip} {
		for _, key := range keys {
			if !slices.Contains(out[key], flag) {
				out[key] = append(out[key], flag)
			}
		}
	}
	return out
}

// validate refuses a key named two ways.
func (c GonePinChoices) validate() error {
	named := c.named()
	for _, key := range slices.Sorted(maps.Keys(named)) {
		if flags := named[key]; len(flags) > 1 {
			slices.Sort(flags)
			return fmt.Errorf("%w: %s is named by %s; choose one", ErrBadGonePinChoice, key, strings.Join(flags, " and "))
		}
	}
	return nil
}

// check refuses a key that names no pin of pins, and a choice its pin
// does not offer, by name - as an unknown --optional id is refused (#654).
func (c GonePinChoices) check(pins []CollectionGonePin) error {
	byKey := map[string]CollectionGonePin{}
	var keys []string
	for _, p := range pins {
		byKey[p.Key()] = p
		keys = append(keys, p.Key())
	}
	var unknown []string
	for _, key := range slices.Sorted(maps.Keys(c.named())) {
		if _, ok := byKey[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		tail := "; this plan has none"
		if len(keys) > 0 {
			tail = "; the plan's are " + strings.Join(keys, ", ")
		}
		return fmt.Errorf("%w: %s names no mod whose pinned file is gone%s", ErrBadGonePinChoice, strings.Join(unknown, ", "), tail)
	}
	for _, key := range c.UseCurrent {
		if !byKey[key].CanUseCurrent() {
			return fmt.Errorf("%w: %s has no current file to install in place of its gone one; --skip-file or --skip it", ErrBadGonePinChoice, key)
		}
	}
	for _, key := range c.SkipFile {
		if !byKey[key].CanSkipFile {
			return fmt.Errorf("%w: %s's gone file is its main file, so it cannot be left out alone; --use-current or --skip it", ErrBadGonePinChoice, key)
		}
	}
	return nil
}

// choiceFor is p's choice, "" when undecided.
func (c GonePinChoices) choiceFor(p CollectionGonePin) string {
	key := p.Key()
	switch {
	case slices.Contains(c.Skip, key):
		return GonePinSkip
	case slices.Contains(c.SkipFile, key) && p.CanSkipFile:
		return GonePinSkipFile
	case p.CanUseCurrent() && (c.UseCurrentAll || slices.Contains(c.UseCurrent, key)):
		return GonePinUseCurrent
	}
	return ""
}

// echo sets conv's echo of the choices, so a frontend re-plans with them.
func (c GonePinChoices) echo(conv *ConvertedCollection) {
	conv.UseCurrentAll = c.UseCurrentAll
	conv.UseCurrent = sortedUnique(c.UseCurrent)
	conv.SkipFile = sortedUnique(c.SkipFile)
	conv.Skip = sortedUnique(c.Skip)
}

// sortedUnique is list sorted with its repeats dropped; nil when empty.
func sortedUnique(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	out := slices.Clone(list)
	slices.Sort(out)
	return slices.Compact(out)
}

// CollectionGonePin is a mod whose pinned files the source no longer
// offers (#657): what the collection pins, each gone file with its role
// and stand-in, and what the user chose.
type CollectionGonePin struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Name is the collection's name for the mod.
	Name string `json:"name,omitempty"`
	// Version is the version the collection pins, and FileIDs its pinned
	// files the source no longer offers.
	Version string   `json:"version,omitempty"`
	FileIDs []string `json:"file_ids"`
	// Current is the main file's stand-in when the main file is gone (the
	// mod's current main file), else the stand-in of the first gone file;
	// nil when "use the current file" is not on offer. Files has every
	// gone file's.
	Current *CollectionCurrentFile `json:"current,omitempty"`
	// Files are the gone files, each with its role and stand-in.
	Files []CollectionGoneFile `json:"files,omitempty"`
	// NotReplaced are the gone files "use the current file" leaves out:
	// secondary files beside a gone main file, which the current main file
	// does not stand in for, and a secondary file no current file matches.
	NotReplaced []string `json:"not_replaced,omitempty"`
	// CanSkipFile says the gone files can be left out alone, the rest of
	// the mod installed: every gone file is a secondary one.
	CanSkipFile bool `json:"can_skip_file,omitzero"`
	// Conflict marks a collection update's pin of an entry the user owns
	// (a conflict, CollectionUpdatePlan.Conflicts): the user's version is
	// kept unless the apply overrides the conflict, and only then must the
	// pin be decided.
	Conflict bool `json:"conflict,omitzero"`
	// Choice is what the import does: GonePinUseCurrent, GonePinSkipFile,
	// GonePinSkip, or empty when nothing can be done but skip - and the
	// import then leaves the item a gone_pin to-do (#660) rather than
	// refusing.
	Choice string `json:"choice,omitempty"`
	// Default says Choice is the default, not the user's (#660 owner
	// ruling): a pin with a current file to stand in for it installs that
	// file unless the user skips it.
	Default bool `json:"default,omitzero"`
	// CuratorExact says the curator pinned this version exactly (the
	// collection's "exact" update policy) - so the current file is one the
	// curator did not test. Vortex installs no newer file for an exact pin;
	// lmm does by default, and says so (#660 owner ruling).
	CuratorExact bool `json:"curator_exact,omitzero"`

	// pin is the collection's whole pin of the mod, which a stand-in
	// records; md5 the curator's archive md5 of each gone file.
	pin domain.CollectionPin
	md5 map[string]string
}

// CollectionGoneFile is one pinned file the source no longer offers.
type CollectionGoneFile struct {
	FileID string `json:"file_id"`
	// Name is the file's own name, as the collection records it.
	Name string `json:"name,omitempty"`
	// Role is GoneFileMain or GoneFileSecondary.
	Role string `json:"role"`
	// Replacement is the current file that stands in for it under "use
	// the current file"; nil when none does.
	Replacement *CollectionCurrentFile `json:"replacement,omitempty"`
}

// Key is the pin's domain.ModKey, which GonePinChoices names.
func (p CollectionGonePin) Key() string { return domain.ModKey(p.SourceID, p.ModID) }

// CanUseCurrent reports whether "use the current file" is on offer: the
// gone main file has its stand-in, or, with the main file still offered,
// every gone secondary file has one.
func (p CollectionGonePin) CanUseCurrent() bool { return p.Current != nil }

// describe is the pin as a sentence names it.
func (p CollectionGonePin) describe() string {
	what := p.Name
	if what == "" {
		what = p.Key()
	}
	return fmt.Sprintf("%s: the collection pins %s", what, describePin(p.Version, p.FileIDs))
}

// CollectionCurrentFile is a file the source offers now.
type CollectionCurrentFile struct {
	FileID  string `json:"file_id"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// undecidedGonePins is c's gone pins nobody chose for; nil for a nil c. A
// conflict's pin counts only when overridden reports the user takes the
// collection's side of it.
func undecidedGonePins(c *ConvertedCollection, overridden func(key string) bool) []CollectionGonePin {
	if c == nil {
		return nil
	}
	var out []CollectionGonePin
	for _, p := range c.GonePins {
		if p.Choice == "" && (!p.Conflict || (overridden != nil && overridden(p.Key()))) {
			out = append(out, p)
		}
	}
	return out
}

// availableCategories are the file categories that say a source still
// offers a file. A published list's file in any other (or none) is looked
// up in the mod's listing.
var availableCategories = []string{"MAIN", "UPDATE", "OPTIONAL", "OLD_VERSION", "MISCELLANEOUS", "ARCHIVED"}

// isAvailableCategory reports whether category is one of
// availableCategories.
func isAvailableCategory(category string) bool {
	return slices.Contains(availableCategories, strings.ToUpper(category))
}

// isGoneCategory reports whether a source's listing files a file in
// category as no longer offered: removed. An archived file is still
// offered, and so is a listed file whose category the source does not say.
func isGoneCategory(category string) bool {
	switch strings.ToUpper(category) {
	case "REMOVED", "DELETED":
		return true
	}
	return false
}

// isArchivedCategory reports whether category is an archived file's: still
// offered, but no longer on the mod's page.
func isArchivedCategory(category string) bool { return strings.EqualFold(category, "ARCHIVED") }

// isMainLikeCategory reports whether category is a main file's: the
// current main, or a main file the author moved to old versions.
func isMainLikeCategory(category string) bool {
	switch strings.ToUpper(category) {
	case "MAIN", "OLD_VERSION":
		return true
	}
	return false
}

// isSecondaryCategory reports whether category is a file beside a main
// one: an optional, update or miscellaneous file.
func isSecondaryCategory(category string) bool {
	switch strings.ToUpper(category) {
	case "OPTIONAL", "UPDATE", "MISCELLANEOUS":
		return true
	}
	return false
}

// noteArchived says on modID's item for fileID that the file the
// collection pins is archived, and is installed all the same.
func (pc *pinChecker) noteArchived(modID, fileID string) {
	note := fmt.Sprintf("archived on %s; installing the curator's file", pc.conv.sourceName())
	for i := range pc.conv.Items {
		it := &pc.conv.Items[i]
		if it.ModID != modID || it.FileID != fileID || strings.Contains(it.Note, note) {
			continue
		}
		if it.Note == "" {
			it.Note = note
		} else {
			it.Note += "; " + note
		}
	}
}

// pinListing is one mod's file listing, fetched at most once per plan.
type pinListing struct {
	files []domain.DownloadableFile
	// gone: the source has no record of the mod at all.
	gone bool
	err  error
}

// pinChecker finds gone pins for one plan, each mod's listing fetched at
// most once.
type pinChecker struct {
	s        *Service
	game     *domain.Game
	conv     *ConvertedCollection
	listings map[string]*pinListing
	// warnings are the mods lmm could not check.
	warnings []string
}

func (s *Service) newPinChecker(game *domain.Game, conv *ConvertedCollection) *pinChecker {
	return &pinChecker{s: s, game: game, conv: conv, listings: map[string]*pinListing{}}
}

// listing is modID's listing, fetched on first use.
func (pc *pinChecker) listing(ctx context.Context, modID string) *pinListing {
	key := domain.ModKey(pc.conv.SourceID, modID)
	if l, ok := pc.listings[key]; ok {
		return l
	}
	l := &pinListing{}
	mod := &domain.Mod{ID: modID, SourceID: pc.conv.SourceID, GameID: pc.game.SourceIDs[pc.conv.SourceID]}
	files, err := pc.s.GetModFiles(ctx, pc.game, pc.conv.SourceID, mod)
	switch {
	case err == nil:
		l.files = files
	case errors.Is(err, domain.ErrModNotFound):
		l.gone = true
	default:
		l.err = err
	}
	pc.listings[key] = l
	return l
}

// listItem is modID's file fileID as the published list reports it; nil
// when the conversion has no item for it.
func (pc *pinChecker) listItem(modID, fileID string) *ConvertedCollectionItem {
	for i := range pc.conv.Items {
		if it := &pc.conv.Items[i]; it.ModID == modID && it.FileID == fileID {
			return it
		}
	}
	return nil
}

// vouched reports whether the published list reports modID's file fileID
// in a category that says it is offered (availableCategories), so no
// listing is needed; a file it files as archived is noted. A file the
// list leaves out, names without its details (NexusMods reports no file
// record for a deleted file), or files in any other category is checked.
func (pc *pinChecker) vouched(modID, fileID string) bool {
	it := pc.listItem(modID, fileID)
	if it == nil || !it.listed || !isAvailableCategory(it.FileCategory) {
		return false
	}
	if isArchivedCategory(it.FileCategory) {
		pc.noteArchived(modID, fileID)
	}
	return true
}

// check returns item's gone pin, or nil when every file it pins is
// offered (or could not be checked, which a warning says). Only an error
// of ctx itself is returned.
func (pc *pinChecker) check(ctx context.Context, item domain.CollectionItem) (*CollectionGonePin, error) {
	ref := item.Ref
	if ref.SourceID != pc.conv.SourceID || ref.ModID == "" || len(ref.FileIDs) == 0 {
		return nil, nil
	}
	unvouched := false
	for _, id := range ref.FileIDs {
		if !pc.vouched(ref.ModID, id) {
			unvouched = true
		}
	}
	if !unvouched {
		return nil, nil
	}
	l := pc.listing(ctx, ref.ModID)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if l.err != nil {
		pc.warnings = append(pc.warnings, fmt.Sprintf("lmm could not check that %s still offers the file %s pins (%v); a file it no longer offers fails the import when it downloads",
			pc.s.sourceDisplayName(ref.SourceID), firstNonEmpty(item.Name, ref.ModID), l.err))
		return nil, nil
	}
	offered := map[string]*domain.DownloadableFile{}
	for i, f := range l.files {
		if !isGoneCategory(f.Category) {
			offered[f.ID] = &l.files[i]
		}
		if isArchivedCategory(f.Category) && slices.Contains(ref.FileIDs, f.ID) {
			pc.noteArchived(ref.ModID, f.ID)
		}
	}
	var gone []string
	for _, id := range ref.FileIDs {
		if offered[id] == nil {
			gone = append(gone, id)
		}
	}
	if len(gone) == 0 {
		return nil, nil
	}
	p := &CollectionGonePin{SourceID: ref.SourceID, ModID: ref.ModID, Name: item.Name, Version: ref.Version, FileIDs: gone,
		pin: domain.CollectionPin{Version: ref.Version, FileIDs: slices.Clone(ref.FileIDs)}, md5: map[string]string{}}
	for _, f := range item.Files {
		if slices.Contains(gone, f.ID) && f.MD5 != "" {
			p.md5[f.ID] = strings.ToLower(f.MD5)
		}
	}
	pc.assignRoles(p, ref.FileIDs, offered, l)
	return p, nil
}

// assignRoles gives each of p's gone files its role and stand-in (owner
// ruling, 2026-11-15): the stand-in must play the gone file's role, and a
// current main file is never put beside a pinned main file.
//
//   - A pinned file still offered as a main file (MAIN, or a main the
//     author moved to OLD_VERSION) keeps the main role, so every gone file
//     is secondary.
//   - Otherwise the gone main file is the one the published list last
//     filed as a main file, else one it did not file as secondary, else -
//     every pinned file gone - the first. Its stand-in is the mod's current
//     main file, and the other gone files are not replaced.
//   - A secondary gone file's stand-in is a current file of the same
//     category (any secondary category, when the list never said) whose
//     name matches the gone file's, when exactly one does. Without one for
//     every gone file, "use the current file" is not on offer: leave the
//     gone files out (skip_file), or skip the mod.
func (pc *pinChecker) assignRoles(p *CollectionGonePin, pinned []string, offered map[string]*domain.DownloadableFile, l *pinListing) {
	category := func(id string) string {
		if it := pc.listItem(p.ModID, id); it != nil && it.listed && isAvailableCategory(it.FileCategory) {
			return strings.ToUpper(it.FileCategory)
		}
		return ""
	}
	name := func(id string) string {
		if it := pc.listItem(p.ModID, id); it != nil {
			return it.fileName
		}
		return ""
	}
	mainOffered := false
	for _, id := range pinned {
		if f := offered[id]; f != nil && isMainLikeCategory(f.Category) {
			mainOffered = true
		}
	}
	main := ""
	if !mainOffered {
		for _, id := range p.FileIDs {
			if isMainLikeCategory(category(id)) {
				main = id
				break
			}
		}
		if main == "" {
			for _, id := range p.FileIDs {
				if !isSecondaryCategory(category(id)) {
					main = id
					break
				}
			}
		}
		if main == "" && len(p.FileIDs) == len(pinned) {
			main = p.FileIDs[0]
		}
	}
	var current *CollectionCurrentFile
	if !l.gone {
		if cur := primaryFile(FilterAndSortFiles(availableFiles(l.files), false)); cur != nil {
			current = &CollectionCurrentFile{FileID: cur.ID, Name: cur.Name, Version: cur.Version}
		}
	}
	allMatched := true
	for _, id := range p.FileIDs {
		gf := CollectionGoneFile{FileID: id, Name: name(id), Role: GoneFileSecondary}
		switch {
		case id == main:
			gf.Role, gf.Replacement = GoneFileMain, current
		case main != "":
			// Beside a gone main: the current main stands in for the mod.
			p.NotReplaced = append(p.NotReplaced, id)
		default:
			gf.Replacement = matchSecondary(gf.Name, category(id), pinned, l.files)
			if gf.Replacement == nil {
				allMatched = false
				p.NotReplaced = append(p.NotReplaced, id)
			}
		}
		p.Files = append(p.Files, gf)
	}
	switch {
	case main != "":
		p.Current = current
	case allMatched:
		p.Current = p.Files[0].Replacement
		p.NotReplaced = nil
	}
	if main == "" {
		p.CanSkipFile = true
	}
	if p.Current == nil {
		p.NotReplaced = nil
	}
}

// availableFiles is files without the ones its source files as removed or
// in a category that does not say it is offered.
func availableFiles(files []domain.DownloadableFile) []domain.DownloadableFile {
	var out []domain.DownloadableFile
	for _, f := range files {
		if isAvailableCategory(f.Category) {
			out = append(out, f)
		}
	}
	return out
}

// versionToken matches a version inside a file name: one after a "v"
// ("v1", "v1.2a") or a dotted one ("1.2", "4.3.8a"). A bare number or a
// number with a letter ("4K", "2K", "1080p", "512", "2048") is a
// resolution or a size - part of what the file is - so it stays in the
// name (#657 re-review: "Textures 4K" must not match "Textures 2K").
var versionToken = regexp.MustCompile(`(?i)\b(?:v\d+(?:[._]\d+)*|\d+(?:[._]\d+)+)[a-z]?\b`)

// normaliseFileName is a file name compared without case, versions or
// punctuation, so "Ten Patch v1.2" and "Ten Patch 1.3" match, and
// "Textures 4K" and "Textures 2K" do not.
func normaliseFileName(name string) string {
	name = versionToken.ReplaceAllString(strings.ToLower(name), " ")
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, name)
	return strings.Join(strings.Fields(name), " ")
}

// matchSecondary is the current file standing in for a gone secondary
// file named name of category (empty when unknown): a file of files the
// mod does not pin, offered in that category (any secondary one when
// unknown), never a main file, whose name matches - when exactly one does.
func matchSecondary(name, category string, pinned []string, files []domain.DownloadableFile) *CollectionCurrentFile {
	want := normaliseFileName(name)
	if want == "" {
		return nil
	}
	var hit *domain.DownloadableFile
	for i, f := range files {
		cat := strings.ToUpper(f.Category)
		if slices.Contains(pinned, f.ID) || !isSecondaryCategory(cat) || (category != "" && isSecondaryCategory(category) && cat != category) {
			continue
		}
		if normaliseFileName(firstNonEmpty(f.Name, f.FileName)) != want {
			continue
		}
		if hit != nil {
			return nil
		}
		hit = &files[i]
	}
	if hit == nil {
		return nil
	}
	return &CollectionCurrentFile{FileID: hit.ID, Name: hit.Name, Version: hit.Version}
}

// findGonePins checks every entry of conv's document that needs (the
// entries the plan downloads), in the document's order.
func (pc *pinChecker) findGonePins(ctx context.Context, needs func(domain.CollectionItem) bool) ([]CollectionGonePin, error) {
	var out []CollectionGonePin
	for _, item := range pc.conv.Document.Items {
		if !needs(item) {
			continue
		}
		p, err := pc.check(ctx, item)
		if err != nil {
			return nil, err
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

// gonePinOutcome is what deciding a plan's gone pins did to its document.
type gonePinOutcome struct {
	// changed: the document changed, so the plan is built again.
	changed bool
	// substituted maps each entry standing in for a gone pin (a current
	// file in its place, or its gone files left out) to that pin; skipped
	// are the mods left out.
	substituted map[string]*domain.CollectionPin
	skipped     []domain.CollectionSkip
	// archiveMD5 is, per substituted key, the curator's md5 each stand-in
	// file's recorded installer choices were made against, by the stand-in
	// file's id: so a replay against the current archive asks (#609).
	archiveMD5 map[string]map[string]string
}

// decideGonePins sets each pin's choice from choices and applies it to
// conv: a stand-in's entry pins the current files, a skip_file entry drops
// its gone files, a skipped entry leaves the document, and each item says
// so. have, when not nil, is the profile's own entry for a key (a
// collection update): skipping a mod the profile already has keeps that
// entry as it is, rather than dropping it.
func decideGonePins(conv *ConvertedCollection, pins []CollectionGonePin, choices GonePinChoices, have map[string]domain.ModReference) gonePinOutcome {
	out := gonePinOutcome{substituted: map[string]*domain.CollectionPin{}, archiveMD5: map[string]map[string]string{}}
	for i := range pins {
		p := &pins[i]
		p.Choice = choices.choiceFor(*p)
		p.CuratorExact = conv.pinnedExactly(*p)
		if p.Choice == "" && p.CanUseCurrent() {
			// A conflict's pin too: it applies only when the user takes
			// the collection's side of the conflict, and then the same
			// default holds (#660 review S5).
			// #660 owner ruling: the current file is the default, and
			// skipping it the alternative.
			p.Choice, p.Default = GonePinUseCurrent, true
		}
		note := fmt.Sprintf("%s no longer offers the file the collection pins", conv.sourceName())
		switch p.Choice {
		case GonePinUseCurrent:
			if p.CuratorExact && p.Version != "" {
				note += fmt.Sprintf("; the curator pinned %s exactly", p.Version)
			}
			note += "; " + describeStandIns(*p)
			if substituteEntry(conv.Document, p, &out) {
				out.changed = true
			}
		case GonePinSkipFile:
			note += "; left out, and the rest of the mod installed"
			if substituteEntry(conv.Document, p, &out) {
				out.changed = true
			}
		case GonePinSkip:
			out.changed = true
			if ref, ok := have[p.Key()]; ok {
				note += "; the profile keeps the version it has"
				keepEntry(conv.Document, ref)
				break
			}
			note += "; skipped"
			conv.Document.Items = slices.DeleteFunc(conv.Document.Items, func(it domain.CollectionItem) bool {
				return domain.ModKey(it.Ref.SourceID, it.Ref.ModID) == p.Key()
			})
			out.skipped = append(out.skipped, domain.CollectionSkip{SourceID: p.SourceID, ModID: p.ModID, Pin: *p.pin.Clone()})
		default:
			switch {
			case p.Conflict:
				note += ": your version is kept; to take the collection's, decide this file too"
			case p.CanUseCurrent():
				note += ": choose to " + describeStandIns(*p) + ", or to skip it"
			case p.CanSkipFile:
				note += ", and no current file matches it: the item is left to finish - leave the file out and install the rest, or skip the mod"
			default:
				note += ", and it offers no other: the item is left to finish - skip it, or retry once the file is offered again"
			}
		}
		for j := range conv.Items {
			it := &conv.Items[j]
			if it.ModID != p.ModID || !slices.Contains(p.FileIDs, it.FileID) {
				continue
			}
			it.Note = note
			if _, kept := have[p.Key()]; p.Choice == GonePinSkip && !kept {
				it.State = CollectionItemSkipped
			}
		}
	}
	conv.GonePins = pins
	conv.recount()
	return out
}

// describeStandIns says what "use the current file" installs for p, and
// what it leaves out.
func describeStandIns(p CollectionGonePin) string {
	var parts []string
	for _, f := range p.Files {
		if f.Replacement == nil {
			continue
		}
		if f.Role == GoneFileMain {
			parts = append(parts, "its current main file "+describeCurrent(f.Replacement))
		} else {
			parts = append(parts, "its current "+describeCurrent(f.Replacement)+" in place of "+firstNonEmpty(f.Name, "file "+f.FileID))
		}
	}
	text := "install " + strings.Join(parts, " and ") + " instead"
	if len(p.NotReplaced) > 0 {
		var names []string
		for _, f := range p.Files {
			if slices.Contains(p.NotReplaced, f.FileID) {
				names = append(names, firstNonEmpty(f.Name, "file "+f.FileID))
			}
		}
		text += " (not replaced: " + strings.Join(names, ", ") + ")"
	}
	return text
}

// keepEntry points doc's entry for ref's mod at ref as the profile has it,
// so the update leaves it unchanged (#657).
func keepEntry(doc *domain.CollectionDocument, ref domain.ModReference) {
	key := domain.ModKey(ref.SourceID, ref.ModID)
	for i := range doc.Items {
		if domain.ModKey(doc.Items[i].Ref.SourceID, doc.Items[i].Ref.ModID) == key {
			ref.FromCollection, ref.SubstitutedFor = false, nil
			doc.Items[i].Ref = ref
			return
		}
	}
}

// describeCurrent names a current file as a plan line does.
func describeCurrent(f *CollectionCurrentFile) string {
	if f == nil {
		return ""
	}
	name := firstNonEmpty(f.Name, "file "+f.FileID)
	if f.Version != "" {
		name += " (version " + f.Version + ")"
	}
	return name
}

// sourceName is the collection's source as a sentence names it.
func (c *ConvertedCollection) sourceName() string {
	if c.SourceID == "nexusmods" {
		return "NexusMods"
	}
	return c.SourceID
}

// substituteEntry points p's document entry at what p.Choice installs:
// under use_current each gone file is replaced by its stand-in (one with
// none is left out), under skip_file every gone file is left out. A gone
// file's exact file list, patches and per-file installer choices go with
// it (they were made for the curator's archive). The version follows the
// main file: a stand-in main file's version, else the pin's. The mod's own
// recorded installer choices stay only when the curator's md5 of every
// replaced file is known, so a replay against a new archive asks rather
// than replays unchecked (#609). It reports whether the entry changed.
func substituteEntry(doc *domain.CollectionDocument, p *CollectionGonePin, out *gonePinOutcome) bool {
	standIn := map[string]*CollectionCurrentFile{}
	if p.Choice == GonePinUseCurrent {
		for _, f := range p.Files {
			if f.Replacement != nil {
				standIn[f.FileID] = f.Replacement
			}
		}
	}
	for i := range doc.Items {
		item := &doc.Items[i]
		if domain.ModKey(item.Ref.SourceID, item.Ref.ModID) != p.Key() {
			continue
		}
		ref := &item.Ref
		gone := func(id string) bool { return slices.Contains(p.FileIDs, id) }
		var ids []string
		for _, id := range ref.FileIDs {
			if gone(id) {
				r := standIn[id]
				if r == nil {
					continue
				}
				id = r.FileID
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		ref.FileIDs = ids
		for _, f := range p.Files {
			if f.Role == GoneFileMain && standIn[f.FileID] != nil && standIn[f.FileID].Version != "" {
				ref.Version = standIn[f.FileID].Version
			}
		}
		for _, id := range p.FileIDs {
			delete(ref.FileList, id)
			delete(ref.Patches, id)
			delete(ref.FomodFiles, id)
		}
		if len(ref.FileList) == 0 {
			ref.FileList = nil
		}
		if len(ref.Patches) == 0 {
			ref.Patches = nil
		}
		if len(ref.FomodFiles) == 0 {
			ref.FomodFiles = nil
		}
		var order []string
		for _, id := range ref.FomodOrder {
			if gone(id) {
				r := standIn[id]
				if r == nil {
					continue
				}
				id = r.FileID
			}
			order = append(order, id)
		}
		ref.FomodOrder = slices.Compact(order)
		if len(ref.FomodFiles) == 0 {
			ref.FomodOrder = nil
		}
		if !ref.Fomod.IsZero() && len(standIn) > 0 {
			sums := map[string]string{}
			for goneID, r := range standIn {
				sum := p.md5[goneID]
				if sum == "" {
					sums = nil
					break
				}
				sums[r.FileID] = sum
			}
			if sums == nil {
				ref.Fomod = domain.FomodChoices{}
			} else {
				out.archiveMD5[p.Key()] = sums
			}
		}
		item.Files = slices.DeleteFunc(item.Files, func(f domain.CollectionFileRef) bool { return gone(f.ID) })
		for _, f := range p.Files {
			r := standIn[f.FileID]
			if r != nil && !slices.ContainsFunc(item.Files, func(f domain.CollectionFileRef) bool { return f.ID == r.FileID }) {
				item.Files = append(item.Files, domain.CollectionFileRef{ID: r.FileID})
			}
		}
		out.substituted[p.Key()] = p.pin.Clone()
		return true
	}
	return false
}

// mergeArchiveMD5 adds extra's md5s to into, which may be nil.
func mergeArchiveMD5(into, extra map[string]map[string]string) map[string]map[string]string {
	for key, files := range extra {
		if into == nil {
			into = map[string]map[string]string{}
		}
		if into[key] == nil {
			into[key] = map[string]string{}
		}
		for id, sum := range files {
			into[key][id] = sum
		}
	}
	return into
}

// reofferedNote is the note on a mod the user skipped that a collection
// update pins anew at an available file (#657): the file is named by
// pinned.
func reofferedNote(pinned string) string {
	return "previously skipped because its pinned file was gone; the collection now pins " + pinned + ", which is available"
}

// describePin names a pin's files and version as a sentence does.
func describePin(version string, fileIDs []string) string {
	out := "file " + strings.Join(fileIDs, ", ")
	if version != "" {
		out += " (version " + version + ")"
	}
	return out
}

// resolveUpdatePins applies what the profile recorded about gone pins to a
// collection update's new version, and finds and decides the new
// version's own (#657). A mod the profile skipped stays out while the new
// version pins the same file (or a file the source does not offer
// either); one it pins anew at an available file comes back, listed for
// the user's attention (CollectionUpdatePlan.Reoffered), unless keep names
// it, which keeps it skipped. An entry standing in for a gone pin stays as
// it is while the new version pins that same file. Every other entry the
// update downloads is checked, as an import's are.
func (s *Service) resolveUpdatePins(ctx context.Context, game *domain.Game, base *domain.Profile, conv *ConvertedCollection, choices GonePinChoices, keep []string, plan *CollectionUpdatePlan) error {
	have := firstRefs(base.Mods)
	records := map[string]domain.CollectionSkip{}
	if b := base.CollectionBaseline; b != nil {
		for _, rec := range b.Skipped {
			records[domain.ModKey(rec.SourceID, rec.ModID)] = rec
		}
	}
	plan.substituted = map[string]*domain.CollectionPin{}
	plan.reoffered = map[string]domain.CollectionSkip{}
	markSkipped := func(key, note string) {
		for j := range conv.Items {
			if it := &conv.Items[j]; domain.ModKey(conv.SourceID, it.ModID) == key && it.State == CollectionItemWillInstall {
				it.State, it.Note = CollectionItemSkipped, note
			}
		}
	}
	keepSkipped := func(item domain.CollectionItem, rec domain.CollectionSkip, note string) {
		plan.Skipped = append(plan.Skipped, CollectionUpdateItem{SourceID: rec.SourceID, ModID: rec.ModID, Name: item.Name,
			Version: rec.Pin.Version, NewVersion: item.Ref.Version, Note: note})
		plan.skips = append(plan.skips, rec)
		markSkipped(domain.ModKey(rec.SourceID, rec.ModID), note)
	}
	doc := conv.Document
	dropItem := func(key string) {
		doc.Items = slices.DeleteFunc(doc.Items, func(it domain.CollectionItem) bool {
			return domain.ModKey(it.Ref.SourceID, it.Ref.ModID) == key
		})
	}
	var items []domain.CollectionItem
	for _, item := range doc.Items {
		key := domain.ModKey(item.Ref.SourceID, item.Ref.ModID)
		ref, ok := have[key]
		if rec, skipped := records[key]; skipped && !ok {
			if rec.Pin.Same(item.Ref.Version, item.Ref.FileIDs) {
				note := "skipped when its pinned file was gone, and the collection still pins that file"
				if rec.Declined {
					note = "you kept it skipped when the collection pinned this file; it is offered again when the collection pins another"
				}
				keepSkipped(item, rec, note)
				continue
			}
			plan.reoffered[key] = rec
		}
		if ok && ref.FromCollection && ref.SubstitutedFor.Same(item.Ref.Version, item.Ref.FileIDs) {
			// The curator still pins the gone file: the substitute stands.
			plan.substituted[key] = ref.SubstitutedFor.Clone()
			kept := ref
			kept.FromCollection, kept.SubstitutedFor = false, nil
			item.Ref = kept
		}
		items = append(items, item)
	}
	doc.Items = items

	checker := s.newPinChecker(game, conv)
	pins, err := checker.findGonePins(ctx, func(item domain.CollectionItem) bool {
		key := domain.ModKey(item.Ref.SourceID, item.Ref.ModID)
		ref, ok := have[key]
		_, reoffered := plan.reoffered[key]
		return reoffered || !ok || ref.Version != item.Ref.Version || !sameFileIDs(ref.FileIDs, item.Ref.FileIDs)
	})
	if err != nil {
		return err
	}
	choices.echo(conv)
	conv.Notes = append(conv.Notes, checker.warnings...)
	var asked []CollectionGonePin
	for _, p := range pins {
		rec, reoffered := plan.reoffered[p.Key()]
		if !reoffered {
			// An entry the user owns keeps their version unless the apply
			// overrides the conflict: only then must its pin be decided.
			if ref, ok := have[p.Key()]; ok && !ref.FromCollection {
				p.Conflict = true
			}
			asked = append(asked, p)
			continue
		}
		// The new pin of a mod the user skipped is gone too: it stays out.
		delete(plan.reoffered, p.Key())
		for _, item := range doc.Items {
			if domain.ModKey(item.Ref.SourceID, item.Ref.ModID) == p.Key() {
				keepSkipped(item, rec, "skipped when its pinned file was gone, and the source does not offer the file the collection now pins either")
				break
			}
		}
		dropItem(p.Key())
	}
	if err := choices.check(asked); err != nil {
		return err
	}
	// A skipped mod pinned anew at an available file comes back, unless
	// the user keeps it skipped.
	plan.KeepSkipped = sortedUnique(keep)
	for _, item := range slices.Clone(doc.Items) {
		key := domain.ModKey(item.Ref.SourceID, item.Ref.ModID)
		rec, ok := plan.reoffered[key]
		if !ok {
			continue
		}
		entry := CollectionUpdateItem{SourceID: rec.SourceID, ModID: rec.ModID, Name: item.Name,
			Version: rec.Pin.Version, NewVersion: item.Ref.Version,
			Note: reofferedNote(describePin(item.Ref.Version, item.Ref.FileIDs))}
		if slices.Contains(keep, key) {
			entry.KeepSkipped = true
			entry.Note += "; kept skipped"
			// The declined pin is recorded, so the next update does not
			// offer it again; a newer pin is offered.
			plan.skips = append(plan.skips, domain.CollectionSkip{SourceID: rec.SourceID, ModID: rec.ModID, Declined: true,
				Pin: domain.CollectionPin{Version: item.Ref.Version, FileIDs: slices.Clone(item.Ref.FileIDs)}})
			markSkipped(key, entry.Note)
			dropItem(key)
			delete(plan.reoffered, key)
		}
		for j := range conv.Items {
			if it := &conv.Items[j]; domain.ModKey(conv.SourceID, it.ModID) == key {
				if !entry.KeepSkipped && it.State == CollectionItemWillInstall {
					it.Note = entry.Note
				}
				if conv.reoffered == nil {
					conv.reoffered = map[string]bool{}
				}
				conv.reoffered[it.ID] = true
			}
		}
		plan.Reoffered = append(plan.Reoffered, entry)
	}
	conv.recount()
	if len(asked) == 0 {
		return nil
	}
	outcome := decideGonePins(conv, asked, choices, have)
	for _, p := range asked {
		// A skipped mod the profile has keeps its entry, and the pin that
		// entry stands in for, if any.
		if ref, ok := have[p.Key()]; ok && p.Choice == GonePinSkip && ref.FromCollection && ref.SubstitutedFor != nil {
			plan.substituted[p.Key()] = ref.SubstitutedFor.Clone()
		}
	}
	maps.Copy(plan.substituted, outcome.substituted)
	plan.skips = append(plan.skips, outcome.skipped...)
	plan.gonePinMD5 = outcome.archiveMD5
	return nil
}

// pinnedExactly reports whether the curator pinned p's files exactly: the
// collection's "exact" update policy (or none, which is exact) on any of
// its gone files.
func (c *ConvertedCollection) pinnedExactly(p CollectionGonePin) bool {
	for _, it := range c.Items {
		if it.ModID == p.ModID && slices.Contains(p.FileIDs, it.FileID) {
			if it.UpdatePolicy == "" || it.UpdatePolicy == "exact" {
				return true
			}
		}
	}
	return false
}
