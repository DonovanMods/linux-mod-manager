// profile_fill.go is `lmm profile fill` (#608 C3; design
// docs/plans/2026-11-10-collections-design.md §6 "Free accounts"): a
// profile imported from a source's collection by an account the source
// serves no downloads to records every item pending, and the user fetches
// the files by hand. PlanProfileFill matches the archives in a directory
// (typically ~/Downloads), or a set of uploaded ones, to the profile's
// pending pins by size and md5 - the facts the collection's recipe records
// for each file - and ApplyProfileFill installs the matches through the
// import's acquire/decide/commit engine (acquire_engine.go), with each
// pin's recorded installer choices and stated mod type, as if downloaded.
//
// The directory is a stranger's: whatever is in ~/Downloads got there from
// anywhere. So:
//
//   - only its top level is read, and a link there is never followed: an
//     entry is opened beneath the directory's own descriptor with
//     O_NOFOLLOW (and O_NONBLOCK, so a FIFO cannot hang the scan), and must
//     still be the regular file the listing named;
//   - a file is hashed only when its SIZE is a pending pin's that is still
//     unmatched, and each size gets a bounded number of wrong guesses
//     (maxFillMismatchesPerSize), so a large Downloads folder costs a
//     directory listing, not a read of every file in it;
//   - the user's files are never moved or deleted: Apply copies each match
//     into staging, checks the copy against the recorded md5 and size
//     again (the file may have changed since the plan), and installs from
//     the copy.
package core

import (
	"cmp"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// maxFillMismatchesPerSize bounds how many files of one pending pin's size
// are hashed without matching it. A pin's own file is found on the first
// try in any ordinary folder; the bound is what keeps a folder holding
// thousands of same-sized files from being read end to end.
const maxFillMismatchesPerSize = 4

// maxFillScanEntries bounds how many directory entries one plan looks at.
const maxFillScanEntries = 50000

// ErrProfileNotFillable is PlanProfileFill's refusal of a profile with no
// source collection behind it: only a collection's recipe records the
// checksums a downloaded file is matched by. Callers branch with errors.Is.
var ErrProfileNotFillable = errors.New("the profile was not imported from a source's collection")

// ProfileFillRequest is what PlanProfileFill plans.
type ProfileFillRequest struct {
	// Profile is the profile whose pending pins to fill.
	Profile string
	// Paths are where to look: a directory's top level is scanned (links
	// never followed), and a file is a candidate itself.
	Paths []string
	// Archives are candidate files with the name to show for each - an
	// upload's own file name rather than its staged path.
	Archives []FillArchive
}

// FillArchive is one candidate file and the name a plan shows for it.
type FillArchive struct {
	Path string
	Name string
}

// FillPendingReason says why a pending pin was not matched.
type FillPendingReason string

const (
	// FillNoArchive: no file found has the pin's size and checksum.
	FillNoArchive FillPendingReason = "no_archive"
	// FillPartial: the mod pins several files and only some were found;
	// it installs once every one is there.
	FillPartial FillPendingReason = "partial"
	// FillNoChecksum: the collection records no checksum and size for the
	// pin's file, so no download can be matched to it.
	FillNoChecksum FillPendingReason = "no_checksum"
	// FillDisabled: the mod is switched off in the profile, and a fill
	// installs only what the profile has on.
	FillDisabled FillPendingReason = "disabled"
)

// FillIgnoredReason says why a file looked at was not used.
type FillIgnoredReason string

const (
	// FillIgnoredNoMatch: no unmatched pending pin has the file's size, so
	// it was not read at all.
	FillIgnoredNoMatch FillIgnoredReason = "no_match"
	// FillIgnoredChecksumMismatch: the size is a pending pin's, the
	// checksum is not.
	FillIgnoredChecksumMismatch FillIgnoredReason = "checksum_mismatch"
	// FillIgnoredDuplicate: another file already matched the same pin.
	FillIgnoredDuplicate FillIgnoredReason = "duplicate"
	// FillIgnoredHashLimit: enough files of this size failed to match
	// that the rest were not read (maxFillMismatchesPerSize).
	FillIgnoredHashLimit FillIgnoredReason = "hash_limit"
	// FillIgnoredSymlink: a link, which a fill never follows.
	FillIgnoredSymlink FillIgnoredReason = "symlink"
	// FillIgnoredNotRegular: neither a regular file nor a directory (a
	// FIFO, a socket, a device).
	FillIgnoredNotRegular FillIgnoredReason = "not_regular_file"
	// FillIgnoredUnreadable: the file could not be read.
	FillIgnoredUnreadable FillIgnoredReason = "unreadable"
)

// FillFile is one pinned file of a fill plan's mod: the source's file id,
// the facts the collection records for it, and the archive matched to it.
type FillFile struct {
	FileID string `json:"file_id"`
	// FileName is the collection's name for the file, when it has one.
	FileName string `json:"file_name,omitempty"`
	Size     int64  `json:"size,omitzero"`
	MD5      string `json:"md5,omitempty"`
	// Archive is the matched file's name; empty while none matched.
	Archive string `json:"archive,omitempty"`
}

// FillMatch is a pending mod every file of which was found: the fill
// installs it.
type FillMatch struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name,omitempty"`
	Version  string `json:"version,omitempty"`
	// ModType is the pin's stated mod type, which the install keeps.
	ModType string     `json:"mod_type,omitempty"`
	Files   []FillFile `json:"files"`
	// Installer reports that the profile records installer (FOMOD) choices
	// for the mod, which the install replays.
	Installer bool `json:"installer,omitzero"`
	// AutoUpdate reports that the curator wants the mod on its newest file,
	// so the install sets it to update automatically.
	AutoUpdate bool `json:"auto_update,omitzero"`
}

// FillPending is a pending mod the fill does not install, and why.
type FillPending struct {
	SourceID string            `json:"source_id"`
	ModID    string            `json:"mod_id"`
	Name     string            `json:"name,omitempty"`
	Version  string            `json:"version,omitempty"`
	Files    []FillFile        `json:"files"`
	Reason   FillPendingReason `json:"reason"`
}

// FillIgnored is a file the fill looked at and does not use.
type FillIgnored struct {
	Name   string            `json:"name"`
	Size   int64             `json:"size,omitzero"`
	Reason FillIgnoredReason `json:"reason"`
}

// ProfileFillPlan is PlanProfileFill's answer: which pending pins the files
// found fill, which stay pending, and every file it did not use. Its Apply
// installs Matched.
type ProfileFillPlan struct {
	Profile string `json:"profile"`
	GameID  string `json:"game_id"`
	// Collection is the collection the profile came from.
	Collection *domain.CollectionOrigin `json:"collection,omitempty"`
	Matched    []FillMatch              `json:"matched"`
	Pending    []FillPending            `json:"pending"`
	Ignored    []FillIgnored            `json:"ignored"`
	// Scanned counts the files looked at, Hashed those read to be checked.
	Scanned int `json:"scanned"`
	Hashed  int `json:"hashed"`
	// RecordedOnly says the profile is not the game's active one
	// (ActiveProfile is), so the install records the mods and deploys
	// nothing, as an import into it would.
	RecordedOnly  bool   `json:"recorded_only,omitzero"`
	ActiveProfile string `json:"active_profile,omitempty"`
	// Notes are what a person should know about the plan.
	Notes []string `json:"notes,omitempty"`

	items      []fillItem
	autoUpdate map[string]bool
	snapshot   installedSnapshot
}

// fillItem is one matched mod as Apply installs it.
type fillItem struct {
	ref      domain.ModReference
	name     string
	supplied map[string]suppliedArchive
}

// suppliedArchive is a user's file matched to one pinned source file: where
// it is (its directory and its name there, opened without following a
// link) and the size and md5 it was matched by.
type suppliedArchive struct {
	dir     string
	name    string
	display string
	size    int64
	md5     string
}

// ProfileFillOptions answers a fill's Apply.
type ProfileFillOptions struct {
	// Fomod answers FOMOD installer questions (#614), keyed by
	// domain.ModKey, as ProfileImportOptions.Fomod does: a pin whose
	// recorded choices no longer fit its installer refuses the fill, with
	// nothing written, until they are answered.
	Fomod map[string]domain.FomodChoices
}

// ProfileFillResult is ApplyProfileFill's outcome.
type ProfileFillResult struct {
	Profile   string `json:"profile"`
	Installed int    `json:"installed"`
	// Recorded counts the mods recorded in a profile that is not the
	// active one (RecordedOnly), installed but not deployed.
	Recorded int `json:"recorded,omitzero"`
	Failed   int `json:"failed"`
	// Remaining counts the profile's pins still pending after the fill.
	Remaining int           `json:"remaining"`
	Warnings  []string      `json:"warnings,omitempty"`
	Notes     []string      `json:"notes,omitempty"`
	Failures  []ItemFailure `json:"failures,omitempty"`
	// RecordedOnly and ActiveProfile are the plan's.
	RecordedOnly  bool   `json:"recorded_only,omitzero"`
	ActiveProfile string `json:"active_profile,omitempty"`
	// Refused says nothing was written: an item needs an answer first
	// (FomodChoicesRequired, LoaderRequired), and every matched item is
	// listed in Failures with its refusal code.
	Refused              bool                       `json:"refused,omitzero"`
	LoaderRequired       *LoaderRequiredError       `json:"loader_required,omitempty"`
	FomodChoicesRequired *FomodChoicesRequiredError `json:"fomod_choices_required,omitempty"`
}

// ProfileFillIncompleteError is ApplyProfileFill's error when a matched mod
// was not installed: one failed on its own (the rest are installed), or the
// fill was refused with nothing written (Result.Refused). It carries the
// whole result for a frontend's error envelope.
type ProfileFillIncompleteError struct {
	Profile string
	Result  *ProfileFillResult
	refusal error
}

// Error names the profile and every mod that was not installed.
func (e *ProfileFillIncompleteError) Error() string {
	if e.Result != nil && e.Result.Refused && e.refusal != nil {
		return fmt.Sprintf("nothing was installed into profile %q - nothing was written: %v", e.Profile, e.refusal)
	}
	return fmt.Sprintf("profile %q was not fully filled - %d mod(s) failed: %s",
		e.Profile, e.Result.Failed, failedImportModsText(e.Result.Failures))
}

// Details returns the whole ProfileFillResult for a frontend's error
// envelope's "details" field.
func (e *ProfileFillIncompleteError) Details() any { return e.Result }

// Unwrap is the refusal behind a Refused fill, nil otherwise.
func (e *ProfileFillIncompleteError) Unwrap() error { return e.refusal }

// recipeFile is what a collection's recipe records about one source file.
type recipeFile struct {
	md5    string
	size   int64
	name   string
	policy string
}

// fillWant is one pending pinned file being looked for.
type fillWant struct {
	key    string
	fileID string
	recipeFile
	match *suppliedArchive
}

// fillCandidate is one file a fill looks at: its directory, its name
// there, and the name shown for it.
type fillCandidate struct {
	dir     string
	name    string
	display string
}

// PlanProfileFill matches req's files to the profile's pending pins - its
// mods with no installed row - by size, then md5, against what the
// collection's recipe records for each pinned file. The recipe is the one
// the profile's collection origin names, from the cache, else fetched again;
// a profile with no source collection behind it is ErrProfileNotFillable.
// Nothing is written but a re-fetched recipe's cache entry.
func (s *Service) PlanProfileFill(ctx context.Context, game *domain.Game, req ProfileFillRequest) (*ProfileFillPlan, error) {
	if err := s.refuseModPathMoved(ctx, game.ID); err != nil {
		return nil, err
	}
	profile, err := s.NewProfileManager().Get(ctx, game.ID, req.Profile)
	if err != nil {
		return nil, err
	}
	var origin *domain.CollectionOrigin
	if profile.Collection != nil {
		origin = profile.Collection.Origin
	}
	if origin == nil || origin.SourceID == "" || origin.Slug == "" || origin.Revision <= 0 {
		return nil, fmt.Errorf("%w: profile %s has no collection recipe recording each file's checksum, so lmm cannot tell which downloads are its files (install each by hand: lmm install --id <mod-id> --from-file <archive>)",
			ErrProfileNotFillable, profile.Name)
	}
	live, recordedOnly, err := s.importScope(ctx, game.ID, profile.Name)
	if err != nil {
		return nil, err
	}
	rows, err := s.GetInstalledMods(ctx, game.ID, profile.Name)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.snapshotOf(game.ID, rows)
	if err != nil {
		return nil, err
	}
	installed := make(map[string]bool, len(rows))
	for _, r := range rows {
		installed[domain.ModKey(r.SourceID, r.ID)] = true
	}

	plan := &ProfileFillPlan{
		Profile: profile.Name, GameID: game.ID, Collection: origin,
		Matched: []FillMatch{}, Pending: []FillPending{}, Ignored: []FillIgnored{},
		RecordedOnly: recordedOnly, ActiveProfile: live, snapshot: snapshot,
	}
	manifest, err := s.collectionRecipeFor(ctx, game, *origin)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		plan.Notes = append(plan.Notes, recipeErrorText(err)+". Without it no download can be matched: plan the fill again once it can be read.")
	}
	recipe, names := recipeFacts(manifest, origin.SourceID)

	// The pending pins, each file a want; a file with no recorded facts
	// cannot be matched, and leaves its mod pending.
	type pendingRef struct {
		ref    domain.ModReference
		name   string
		wants  []*fillWant
		reason FillPendingReason
	}
	var pending []*pendingRef
	bySize := map[int64][]*fillWant{}
	seen := map[string]bool{}
	for _, ref := range profile.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if seen[key] || installed[key] || ref.External {
			continue
		}
		seen[key] = true
		p := &pendingRef{ref: ref, name: names[key]}
		pending = append(pending, p)
		switch {
		case ref.Disabled:
			p.reason = FillDisabled
			continue
		case ref.SourceID != origin.SourceID || len(ref.FileIDs) == 0:
			p.reason = FillNoChecksum
			continue
		}
		for _, id := range ref.FileIDs {
			w := &fillWant{key: key, fileID: id, recipeFile: recipe[recipeKey(ref.ModID, id)]}
			if w.md5 == "" || w.size <= 0 {
				p.reason = FillNoChecksum
			}
			p.wants = append(p.wants, w)
		}
		if p.reason == "" {
			for _, w := range p.wants {
				bySize[w.size] = append(bySize[w.size], w)
			}
		}
	}

	if len(bySize) > 0 {
		s.matchFillFiles(plan, s.fillCandidates(plan, req), bySize)
	}

	plan.autoUpdate = map[string]bool{}
	var lockedLatest []string
	for _, p := range pending {
		files := make([]FillFile, 0, len(p.wants))
		matched := 0
		for _, w := range p.wants {
			f := FillFile{FileID: w.fileID, FileName: w.name, Size: w.size, MD5: w.md5}
			if w.match != nil {
				f.Archive = w.match.display
				matched++
			}
			files = append(files, f)
		}
		if p.reason == "" && matched == len(p.wants) && matched > 0 {
			key := domain.ModKey(p.ref.SourceID, p.ref.ModID)
			auto := latestOnly(manifest, origin.SourceID, p.ref)
			if auto && p.ref.Locked {
				auto = false
				lockedLatest = append(lockedLatest, cmp.Or(p.name, key))
			}
			if auto {
				plan.autoUpdate[key] = true
			}
			plan.Matched = append(plan.Matched, FillMatch{
				SourceID: p.ref.SourceID, ModID: p.ref.ModID, Name: p.name, Version: p.ref.Version,
				ModType: p.ref.ModType, Files: files, Installer: !p.ref.Fomod.IsZero(), AutoUpdate: auto,
			})
			item := fillItem{ref: p.ref, name: p.name, supplied: map[string]suppliedArchive{}}
			for _, w := range p.wants {
				item.supplied[w.fileID] = *w.match
			}
			plan.items = append(plan.items, item)
			continue
		}
		reason := p.reason
		switch {
		case reason != "":
		case matched > 0:
			reason = FillPartial
		default:
			reason = FillNoArchive
		}
		plan.Pending = append(plan.Pending, FillPending{
			SourceID: p.ref.SourceID, ModID: p.ref.ModID, Name: p.name, Version: p.ref.Version,
			Files: files, Reason: reason,
		})
	}
	if len(lockedLatest) > 0 {
		plan.Notes = append(plan.Notes, fmt.Sprintf("The curator wants these on their newest file, but they are locked, so they stay on the installed file: %s.",
			strings.Join(lockedLatest, ", ")))
	}
	return plan, nil
}

// recipeKey keys a recipe file by its mod and file ids.
func recipeKey(modID, fileID string) string { return modID + ":" + fileID }

// recipeFacts is what manifest records about each of sourceID's files, by
// recipeKey, and each mod's name, by domain.ModKey. Empty when there is no
// manifest.
func recipeFacts(m *source.CollectionManifest, sourceID string) (map[string]recipeFile, map[string]string) {
	files := map[string]recipeFile{}
	names := map[string]string{}
	if m == nil {
		return files, names
	}
	for _, mod := range m.Mods {
		if mod.SourceType != "nexus" || mod.ModID == "" || mod.FileID == "" {
			continue
		}
		files[recipeKey(mod.ModID, mod.FileID)] = recipeFile{
			md5: strings.ToLower(mod.MD5), size: mod.SizeBytes, policy: mod.UpdatePolicy,
			name: domain.SanitizeLine(cmp.Or(mod.LogicalFilename, mod.Name)),
		}
		key := domain.ModKey(sourceID, mod.ModID)
		if names[key] == "" {
			names[key] = domain.SanitizeLine(mod.Name)
		}
	}
	return files, names
}

// latestOnly reports whether the curator marked every one of ref's files
// "latest": the mod installs the tested file and then follows the newest
// (owner ruling, 2026-11-13). One file pinned exact, or preferred, keeps
// the whole mod off auto, as the conversion decided it (convertCollection).
func latestOnly(m *source.CollectionManifest, sourceID string, ref domain.ModReference) bool {
	if m == nil || ref.SourceID != sourceID {
		return false
	}
	rank := -1
	for _, mod := range m.Mods {
		if mod.SourceType == "nexus" && mod.ModID == ref.ModID && slices.Contains(ref.FileIDs, mod.FileID) {
			rank = max(rank, policyRank(mod.UpdatePolicy))
		}
	}
	return rank == 0
}

// collectionRecipeFor reads the recipe of the collection origin names: from
// its cache entry, else resolved and fetched again (fetchCollectionRecipe).
func (s *Service) collectionRecipeFor(ctx context.Context, game *domain.Game, origin domain.CollectionOrigin) (*source.CollectionManifest, error) {
	src, err := s.GetSource(origin.SourceID)
	if err != nil {
		return nil, err
	}
	resolver, ok := src.(source.GameCollectionResolver)
	fetcher, ok2 := src.(source.CollectionManifestFetcher)
	if !ok || !ok2 {
		return nil, fmt.Errorf("%s has no collections lmm can read: %w", s.sourceDisplayName(origin.SourceID), source.ErrNotSupported)
	}
	dir, err := s.collectionCacheDir(game, origin.SourceID, origin.Slug, origin.Revision)
	if err != nil {
		return nil, err
	}
	if checkRecipeTree(dir) != nil {
		gameSourceID := game.SourceIDs[origin.SourceID]
		if gameSourceID == "" {
			return nil, fmt.Errorf("game %s has no %s id, so lmm cannot look its collection up there", game.ID, origin.SourceID)
		}
		list, err := resolver.ResolveGameCollection(ctx, gameSourceID, fmt.Sprintf("%s@%d", origin.Slug, origin.Revision))
		if err != nil {
			return nil, err
		}
		if list.ID != origin.Slug || list.Revision != origin.Revision {
			return nil, fmt.Errorf("the source resolved %s revision %d as %s revision %d", origin.Slug, origin.Revision, list.ID, list.Revision)
		}
		if dir, err = s.fetchCollectionRecipe(ctx, game, origin.SourceID, fetcher, list); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	m, err := fetcher.ParseManifest(root.FS())
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// fillCandidates lists the files req names, in a stable order: each
// directory's top level (directories inside it skipped, links and other
// non-regular entries recorded as ignored), each named file, each archive.
func (s *Service) fillCandidates(plan *ProfileFillPlan, req ProfileFillRequest) []fillCandidate {
	var out []fillCandidate
	ignore := func(name string, reason FillIgnoredReason) {
		plan.Scanned++
		plan.Ignored = append(plan.Ignored, FillIgnored{Name: domain.SanitizeLine(name), Reason: reason})
	}
	consider := func(dir, name, display string, mode os.FileMode) {
		switch {
		case mode&os.ModeSymlink != 0:
			ignore(display, FillIgnoredSymlink)
		case mode.IsDir():
		case !mode.IsRegular():
			ignore(display, FillIgnoredNotRegular)
		default:
			out = append(out, fillCandidate{dir: dir, name: name, display: domain.SanitizeLine(display)})
		}
	}
	scanned := 0
	for _, p := range req.Paths {
		info, err := os.Lstat(p)
		if err != nil {
			plan.Notes = append(plan.Notes, fmt.Sprintf("%s could not be read: %v", domain.SanitizeLine(p), err))
			continue
		}
		if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			consider(filepath.Dir(p), filepath.Base(p), filepath.Base(p), info.Mode())
			continue
		}
		// The directory the user named is theirs to name, link or not;
		// nothing inside it is followed.
		entries, err := os.ReadDir(p)
		if err != nil {
			if info.Mode()&os.ModeSymlink != 0 {
				consider(filepath.Dir(p), filepath.Base(p), filepath.Base(p), info.Mode())
				continue
			}
			plan.Notes = append(plan.Notes, fmt.Sprintf("%s could not be read: %v", domain.SanitizeLine(p), err))
			continue
		}
		for _, e := range entries {
			if scanned >= maxFillScanEntries {
				plan.Notes = append(plan.Notes, fmt.Sprintf("%s has more than %d entries; only the first %d were looked at.", domain.SanitizeLine(p), maxFillScanEntries, maxFillScanEntries))
				break
			}
			scanned++
			consider(p, e.Name(), e.Name(), e.Type())
		}
	}
	for _, a := range req.Archives {
		info, err := os.Lstat(a.Path)
		display := cmp.Or(a.Name, filepath.Base(a.Path))
		if err != nil {
			ignore(display, FillIgnoredUnreadable)
			continue
		}
		consider(filepath.Dir(a.Path), filepath.Base(a.Path), display, info.Mode())
	}
	return out
}

// matchFillFiles matches candidates to the wanted files, by size first:
// only a file whose size an unmatched want has is read, and each size gets
// maxFillMismatchesPerSize wrong guesses.
func (s *Service) matchFillFiles(plan *ProfileFillPlan, candidates []fillCandidate, bySize map[int64][]*fillWant) {
	mismatches := map[int64]int{}
	for _, c := range candidates {
		plan.Scanned++
		f, info, err := openNoFollow(c.dir, c.name)
		if err != nil {
			reason := FillIgnoredUnreadable
			switch {
			case errors.Is(err, syscall.ELOOP):
				reason = FillIgnoredSymlink
			case errors.Is(err, errFillNotRegular):
				reason = FillIgnoredNotRegular
			}
			plan.Ignored = append(plan.Ignored, FillIgnored{Name: c.display, Reason: reason})
			continue
		}
		size := info.Size()
		ignored := func(reason FillIgnoredReason) {
			plan.Ignored = append(plan.Ignored, FillIgnored{Name: c.display, Size: size, Reason: reason})
		}
		wants := bySize[size]
		if !slices.ContainsFunc(wants, func(w *fillWant) bool { return w.match == nil }) {
			_ = f.Close()
			ignored(FillIgnoredNoMatch)
			continue
		}
		if mismatches[size] >= maxFillMismatchesPerSize {
			_ = f.Close()
			ignored(FillIgnoredHashLimit)
			continue
		}
		plan.Hashed++
		h := md5.New()
		n, err := io.Copy(h, f)
		_ = f.Close()
		if err != nil || n != size {
			ignored(FillIgnoredUnreadable)
			continue
		}
		sum := hex.EncodeToString(h.Sum(nil))
		sup := &suppliedArchive{dir: c.dir, name: c.name, display: c.display, size: size, md5: sum}
		used, duplicate := false, false
		for _, w := range wants {
			if w.md5 != sum {
				continue
			}
			if w.match == nil {
				w.match, used = sup, true
			} else {
				duplicate = true
			}
		}
		switch {
		case used:
		case duplicate:
			ignored(FillIgnoredDuplicate)
		default:
			mismatches[size]++
			ignored(FillIgnoredChecksumMismatch)
		}
	}
}

// errFillNotRegular is openNoFollow's refusal of anything but a regular
// file.
var errFillNotRegular = errors.New("not a regular file")

// openNoFollow opens the regular file name beneath dir for reading without
// following a link (ELOOP) and without blocking on a FIFO, and refuses
// anything that is not a regular file (errFillNotRegular).
func openNoFollow(dir, name string) (*os.File, os.FileInfo, error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = d.Close() }()
	fd, err := syscall.Openat(int(d.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, &os.PathError{Op: "open", Path: filepath.Join(dir, name), Err: err}
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir, name))
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, errFillNotRegular
	}
	return f, info, nil
}

// copySupplied copies sup into dest, hashing the copy as it is written, and
// refuses one that is no longer the file the plan matched.
func copySupplied(sup suppliedArchive, dest string) (*DownloadResult, error) {
	src, _, err := openNoFollow(sup.dir, sup.name)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", sup.display, err)
	}
	defer func() { _ = src.Close() }()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	m, s := md5.New(), sha256.New()
	n, err := io.Copy(io.MultiWriter(out, m, s), io.LimitReader(src, sup.size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("copying %s: %w", sup.display, err)
	}
	sum := hex.EncodeToString(m.Sum(nil))
	if n != sup.size || sum != sup.md5 {
		return nil, fmt.Errorf("%s changed since the fill was planned (it is no longer the file the collection records): plan the fill again", sup.display)
	}
	return &DownloadResult{Path: dest, Size: n, Checksum: sum, SHA256: hex.EncodeToString(s.Sum(nil))}, nil
}

// acquireSupplied is acquire for one pinned file from the user's own copy
// (a profile fill): the file is copied into staging - never moved - and the
// copy checked against the md5 and size it was matched by, then laid out
// exactly as a download would be (acquireArchive). The copy is not a
// download, so a refused Apply does not retain it: the re-run copies again.
func (s *Service) acquireSupplied(ctx context.Context, gameCache *cache.Cache, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, sup suppliedArchive, fx fomodInput, sink EventSink) (*acquiredFile, error) {
	a, err := s.acquireSuppliedRaw(ctx, gameCache, sourceID, game, mod, file, sup, fx, sink)
	if err != nil {
		return nil, asDownloadError(err, sourceID, mod, file)
	}
	return a, nil
}

func (s *Service) acquireSuppliedRaw(ctx context.Context, gameCache *cache.Cache, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, sup suppliedArchive, fx fomodInput, sink EventSink) (a *acquiredFile, err error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil, fmt.Errorf("getting source: %w", err)
	}
	tempDir, err := newStagingDir(s.stagingRoot(), "lmm-fill-*")
	if err != nil {
		return nil, err
	}
	a = s.newAcquired(gameCache, sourceID, game, mod, file)
	a.owned = append(a.owned, tempDir)
	defer func(pending *acquiredFile) {
		if err != nil {
			pending.release()
		}
	}(a)
	defer releaseOnPanic(a.release)

	// The source's own name for the file carries its true extension (a
	// browser may have renamed the download); the user's is the fallback.
	name := filepath.Base(file.FileName)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = filepath.Base(sup.name)
	}
	archivePath := filepath.Join(tempDir, name)
	result, err := copySupplied(sup, archivePath)
	if err != nil {
		return nil, err
	}
	if err := s.acquireArchive(ctx, a, src, game, mod, file, archivePath, result, false, fx, sink); err != nil {
		return nil, err
	}
	return a, nil
}

// ApplyProfileFill installs plan's matched mods into its profile, through
// the import's acquire/decide/commit engine: every match is copied and
// laid out before anything is written, so a mod whose installer needs an
// answer refuses the whole fill with nothing written
// (*ProfileFillIncompleteError, Result.Refused); otherwise each mod is
// committed - cache, deploy, row, profile ref - with its recorded installer
// choices and stated mod type, and a mod the curator wants on its newest
// file is set to update automatically. A mod that fails on its own is
// counted and the rest continue, and the result is then a
// *ProfileFillIncompleteError too. A plan whose profile moved is
// ErrStalePlan.
func (s *Service) ApplyProfileFill(ctx context.Context, game *domain.Game, plan *ProfileFillPlan, opts ProfileFillOptions, sink EventSink) (*ProfileFillResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &ProfileFillResult{Profile: plan.Profile}, err
	}
	defer release()
	result, err := s.applyProfileFill(ctx, game, plan, opts, sink)
	s.takeCaptureWarnings(game.ID, OpProfileFill, ImportNote, &result.Warnings, nil)
	if err == nil && result.Failed > 0 {
		err = &ProfileFillIncompleteError{Profile: plan.Profile, Result: result}
	}
	return result, err
}

func (s *Service) applyProfileFill(ctx context.Context, game *domain.Game, plan *ProfileFillPlan, opts ProfileFillOptions, sink EventSink) (*ProfileFillResult, error) {
	result := &ProfileFillResult{Profile: plan.Profile, RecordedOnly: plan.RecordedOnly, ActiveProfile: plan.ActiveProfile,
		Remaining: len(plan.Pending) + len(plan.items)}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	if err := s.checkPlanFresh(ctx, game.ID, plan.Profile, plan.snapshot); err != nil {
		return result, err
	}
	if len(plan.items) == 0 {
		return result, nil
	}
	_, recordedOnly, err := s.importScope(ctx, game.ID, plan.Profile)
	if err != nil {
		return result, err
	}
	if recordedOnly != plan.RecordedOnly {
		return result, fmt.Errorf("%w: whether %s is the active profile of %s changed since this fill was planned", ErrStalePlan, plan.Profile, game.ID)
	}
	deploy := !recordedOnly
	pm := s.NewProfileManager()
	profile, err := pm.Get(ctx, game.ID, plan.Profile)
	if err != nil {
		return result, err
	}
	// The pins themselves are the plan's precondition too: a pin moved to
	// another file, or retyped, is not what the files were matched to.
	refs := make([]domain.ModReference, len(plan.items))
	iplan := &ImportPlan{Profile: profile, autoUpdate: plan.autoUpdate,
		supplied: map[string]map[string]suppliedArchive{}, fillNames: map[string]string{}}
	for i, it := range plan.items {
		ref := profile.FindRef(it.ref.SourceID, it.ref.ModID)
		if ref == nil || !slices.Equal(ref.FileIDs, it.ref.FileIDs) || ref.ModType != it.ref.ModType || ref.Disabled {
			return result, fmt.Errorf("%w: %s's pin in profile %s changed since this fill was planned", ErrStalePlan, domain.ModKey(it.ref.SourceID, it.ref.ModID), plan.Profile)
		}
		refs[i] = *ref
		key := domain.ModKey(ref.SourceID, ref.ModID)
		iplan.supplied[key] = it.supplied
		iplan.fillNames[key] = it.name
	}

	ir := &ProfileImportResult{ProfileName: plan.Profile}
	total := len(refs)
	emit(StepEvent{Scope: Scope{Op: OpProfileFill, Total: total}, Phase: ImportInstalling})
	names := make(map[string]string, total)
	failed := make(map[string]string)
	var installer *Installer
	run := acquireRun[*importStaged]{
		policy: acquirePolicy{mode: acquirePhased},
		n:      total,
		key:    func(i int) string { return domain.ModKey(refs[i].SourceID, refs[i].ModID) },
		acquire: func(ctx context.Context, i int) (*importStaged, *acquireFailure) {
			st, failure := s.acquireImportItem(ctx, game, iplan, refs[i], Scope{Op: OpProfileFill, Index: i + 1, Total: total}, opts.Fomod, ir, emit, failed)
			key := domain.ModKey(refs[i].SourceID, refs[i].ModID)
			switch {
			case st != nil:
				names[key] = st.scope.ModName
			case failure != nil:
				names[key] = failure.refusal.Name
			}
			return st, failure
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
	var refused *AcquireRefusedError
	if errors.As(runErr, &refused) {
		_ = refusedImport(iplan, refs, refused, names, failed, ir)
		fillFromImport(result, ir)
		return result, &ProfileFillIncompleteError{Profile: plan.Profile, Result: result, refusal: refused}
	}
	defer fillFromImport(result, ir)
	if runErr != nil {
		return result, runErr
	}

	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profile.Name); syncErr != nil {
		ir.Warnings = append(ir.Warnings, fmt.Sprintf("syncing merged pak: %v", syncErr))
	} else {
		ir.Warnings = append(ir.Warnings, syncWarnings...)
	}
	var auto int
	for _, it := range plan.items {
		key := domain.ModKey(it.ref.SourceID, it.ref.ModID)
		if plan.autoUpdate[key] && failed[key] == "" {
			auto++
		}
	}
	if auto > 0 {
		ir.Notes = append(ir.Notes, fmt.Sprintf("%d mod(s) the curator wants on their newest file were installed at the tested file and set to update automatically.", auto))
	}
	return result, nil
}

// fillFromImport copies what the import engine recorded into the fill's
// result.
func fillFromImport(result *ProfileFillResult, ir *ProfileImportResult) {
	result.Installed, result.Recorded, result.Failed = ir.Installed, ir.Recorded, ir.Failed
	result.Warnings = append(result.Warnings, ir.Warnings...)
	result.Notes = append(result.Notes, ir.Notes...)
	result.Failures = ir.Failures
	result.Refused = ir.Refused
	result.LoaderRequired, result.FomodChoicesRequired = ir.LoaderRequired, ir.FomodChoicesRequired
	result.Remaining -= ir.Installed + ir.Recorded
}
