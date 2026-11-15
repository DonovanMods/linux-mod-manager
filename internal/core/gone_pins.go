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
// the mod's current main file instead, or skip the mod. Until every one is
// decided, Apply refuses (*CollectionGonePinsError). The choices are plan
// options, as opting in an optional item is, because they change what the
// plan installs. A substituted entry records the pin it stands in for
// (domain.ModReference.SubstitutedFor); a skipped one is recorded in the
// profile's collection baseline, so a collection update neither undoes the
// substitute nor re-adds the mod silently.
package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The choices for a gone pin (CollectionGonePin.Choice).
const (
	// GonePinUseCurrent installs the mod's current main file instead.
	GonePinUseCurrent = "use_current"
	// GonePinSkip leaves the mod out.
	GonePinSkip = "skip"
)

// GonePinChoices is what the user decided for the pinned files the source
// no longer offers (#657), by domain.ModKey: UseCurrent installs each
// named mod's current main file instead, UseCurrentAll every gone pin's
// that has one, and Skip leaves each named mod out. A key naming no gone
// pin is ignored; one named both ways is the caller's error
// (ErrBadGonePinChoice). Skip wins over UseCurrentAll.
type GonePinChoices struct {
	UseCurrent    []string
	UseCurrentAll bool
	Skip          []string
}

// ErrBadGonePinChoice refuses choices that contradict themselves: a mod
// named both to use its current file and to be skipped.
var ErrBadGonePinChoice = errors.New("invalid choice for a pinned file the source no longer offers")

// IsBadGonePinChoice reports whether err is ErrBadGonePinChoice.
func IsBadGonePinChoice(err error) bool { return errors.Is(err, ErrBadGonePinChoice) }

// validate refuses a key named both ways.
func (c GonePinChoices) validate() error {
	for _, key := range c.UseCurrent {
		if slices.Contains(c.Skip, key) {
			return fmt.Errorf("%w: %s is named both to use its current file and to be skipped", ErrBadGonePinChoice, key)
		}
	}
	return nil
}

// choiceFor is the choice for the gone pin key, "" when undecided. A pin
// with no current file can only be skipped.
func (c GonePinChoices) choiceFor(key string, hasCurrent bool) string {
	switch {
	case slices.Contains(c.Skip, key):
		return GonePinSkip
	case hasCurrent && (c.UseCurrentAll || slices.Contains(c.UseCurrent, key)):
		return GonePinUseCurrent
	}
	return ""
}

// echo sets conv's echo of the choices, so a frontend re-plans with them.
func (c GonePinChoices) echo(conv *ConvertedCollection) {
	conv.UseCurrentAll = c.UseCurrentAll
	conv.UseCurrent = sortedUnique(c.UseCurrent)
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

// CollectionGonePin is a mod whose pinned file the source no longer offers
// (#657): what the collection pins, the mod's current main file, and what
// the user chose.
type CollectionGonePin struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Name is the collection's name for the mod.
	Name string `json:"name,omitempty"`
	// Version is the version the collection pins, and FileIDs its pinned
	// files the source no longer offers.
	Version string   `json:"version,omitempty"`
	FileIDs []string `json:"file_ids"`
	// Current is the mod's current main file, which "use the current file"
	// installs; nil when the source offers none, and then the mod can only
	// be skipped.
	Current *CollectionCurrentFile `json:"current,omitempty"`
	// Choice is what the import does: GonePinUseCurrent, GonePinSkip, or
	// empty while undecided - and Apply refuses until every pin is decided.
	Choice string `json:"choice,omitempty"`

	// pin is the collection's whole pin of the mod, which a substitute
	// records; md5 the curator's archive md5 of its first gone file.
	pin domain.CollectionPin
	md5 string
}

// Key is the pin's domain.ModKey, which GonePinChoices names.
func (p CollectionGonePin) Key() string { return domain.ModKey(p.SourceID, p.ModID) }

// describe is the pin as a sentence names it.
func (p CollectionGonePin) describe() string {
	what := p.Name
	if what == "" {
		what = p.Key()
	}
	pinned := "file " + strings.Join(p.FileIDs, ", ")
	if p.Version != "" {
		pinned += " (version " + p.Version + ")"
	}
	return fmt.Sprintf("%s: the collection pins %s", what, pinned)
}

// CollectionCurrentFile is a mod's current main file on its source.
type CollectionCurrentFile struct {
	FileID  string `json:"file_id"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// CollectionGonePinsError refuses an import or update with a pinned file
// the source no longer offers that the user has not decided about (#657):
// install the mod's current file instead, or skip it. Nothing is written.
type CollectionGonePinsError struct {
	// Pins are the undecided pins, in the collection's order.
	Pins []CollectionGonePin `json:"pins"`
}

// Error implements error.
func (e *CollectionGonePinsError) Error() string {
	lines := make([]string, len(e.Pins))
	for i, p := range e.Pins {
		lines[i] = p.describe()
		if p.Current == nil {
			lines[i] += " (no current file: skip it)"
		}
	}
	return fmt.Sprintf("the collection pins %d file(s) the source no longer offers - %s; choose for each: --use-current <mod> (or --use-current-all) installs the mod's current main file instead, --skip <mod> leaves it out",
		len(e.Pins), strings.Join(lines, "; "))
}

// Details implements the --json error envelope's extension point.
func (e *CollectionGonePinsError) Details() any { return e }

// IsCollectionGonePins reports whether err refused undecided gone pins.
func IsCollectionGonePins(err error) bool {
	var target *CollectionGonePinsError
	return errors.As(err, &target)
}

// undecidedGonePins is c's gone pins nobody chose for; nil for a nil c.
func undecidedGonePins(c *ConvertedCollection) []CollectionGonePin {
	if c == nil {
		return nil
	}
	var out []CollectionGonePin
	for _, p := range c.GonePins {
		if p.Choice == "" {
			out = append(out, p)
		}
	}
	return out
}

// isGoneCategory reports whether a source files a file in category as no
// longer offered: removed. An archived file is still offered.
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

// vouched reports whether the published list reports modID's file fileID
// and does not file it as removed, so no listing is needed; a file it
// files as archived is noted. A file the list leaves out, or names
// without its details (NexusMods reports no file record for a deleted
// file), is checked.
func (pc *pinChecker) vouched(modID, fileID string) bool {
	for _, it := range pc.conv.Items {
		if it.ModID == modID && it.FileID == fileID {
			ok := it.listed && !isGoneCategory(it.FileCategory)
			if ok && isArchivedCategory(it.FileCategory) {
				pc.noteArchived(modID, fileID)
			}
			return ok
		}
	}
	return false
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
	offered := map[string]bool{}
	for _, f := range l.files {
		if !isGoneCategory(f.Category) {
			offered[f.ID] = true
		}
		if isArchivedCategory(f.Category) && slices.Contains(ref.FileIDs, f.ID) {
			pc.noteArchived(ref.ModID, f.ID)
		}
	}
	var gone []string
	for _, id := range ref.FileIDs {
		if !offered[id] {
			gone = append(gone, id)
		}
	}
	if len(gone) == 0 {
		return nil, nil
	}
	p := &CollectionGonePin{SourceID: ref.SourceID, ModID: ref.ModID, Name: item.Name, Version: ref.Version, FileIDs: gone,
		pin: domain.CollectionPin{Version: ref.Version, FileIDs: slices.Clone(ref.FileIDs)}}
	for _, f := range item.Files {
		if f.ID == gone[0] {
			p.md5 = strings.ToLower(f.MD5)
		}
	}
	if !l.gone {
		if cur := primaryFile(FilterAndSortFiles(l.files, false)); cur != nil {
			p.Current = &CollectionCurrentFile{FileID: cur.ID, Name: cur.Name, Version: cur.Version}
		}
	}
	return p, nil
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
	// substituted maps each substituted entry's key to the pin it stands
	// in for; skipped are the mods left out.
	substituted map[string]*domain.CollectionPin
	skipped     []domain.CollectionSkip
	// archiveMD5 is, per substituted key, the curator's md5 its recorded
	// installer choices were made against, by the current file's id: so
	// a replay against the current archive asks (#609).
	archiveMD5 map[string]map[string]string
}

// decideGonePins sets each pin's choice from choices and applies it to
// conv: a substitute's entry pins the current file, a skipped entry
// leaves the document, and each item says so. have, when not nil, is the
// profile's own entry for a key (a collection update): skipping a mod the
// profile already has keeps that entry as it is, rather than dropping it.
func decideGonePins(conv *ConvertedCollection, pins []CollectionGonePin, choices GonePinChoices, have map[string]domain.ModReference) gonePinOutcome {
	out := gonePinOutcome{substituted: map[string]*domain.CollectionPin{}, archiveMD5: map[string]map[string]string{}}
	for i := range pins {
		p := &pins[i]
		p.Choice = choices.choiceFor(p.Key(), p.Current != nil)
		note := fmt.Sprintf("%s no longer offers the file the collection pins", conv.sourceName())
		switch p.Choice {
		case GonePinUseCurrent:
			note += fmt.Sprintf("; its current main file %s installs instead", describeCurrent(p.Current))
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
			if p.Current != nil {
				note += fmt.Sprintf(": choose to install its current main file %s instead, or to skip it", describeCurrent(p.Current))
			} else {
				note += ", and it offers no other: skip it to import the rest"
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

// substituteEntry points p's document entry at its current file: the gone
// files are replaced by the current one, their exact file lists, patches
// and per-file installer choices dropped (they were made for the
// curator's archive), and the version is the current file's. The mod's
// own recorded installer choices stay only when the curator's md5 is
// known, so a replay against the new archive asks rather than replays
// unchecked (#609). It reports whether the entry changed.
func substituteEntry(doc *domain.CollectionDocument, p *CollectionGonePin, out *gonePinOutcome) bool {
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
				id = p.Current.FileID
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		ref.FileIDs = ids
		if p.Current.Version != "" {
			ref.Version = p.Current.Version
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
		for j, id := range ref.FomodOrder {
			if gone(id) {
				ref.FomodOrder[j] = p.Current.FileID
			}
		}
		ref.FomodOrder = slices.Compact(ref.FomodOrder)
		if len(ref.FomodFiles) == 0 {
			ref.FomodOrder = nil
		}
		if !ref.Fomod.IsZero() {
			if p.md5 == "" {
				ref.Fomod = domain.FomodChoices{}
			} else {
				out.archiveMD5[p.Key()] = map[string]string{p.Current.FileID: p.md5}
			}
		}
		item.Files = slices.DeleteFunc(item.Files, func(f domain.CollectionFileRef) bool { return gone(f.ID) })
		if !slices.ContainsFunc(item.Files, func(f domain.CollectionFileRef) bool { return f.ID == p.Current.FileID }) {
			item.Files = append(item.Files, domain.CollectionFileRef{ID: p.Current.FileID})
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
				keepSkipped(item, rec, "skipped when its pinned file was gone, and the collection still pins that file")
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
			plan.skips = append(plan.skips, rec)
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
