// Package core: this file holds the workshop-adopt flow (#269) -
// ScanWorkshop, PlanWorkshopAdopt and ApplyWorkshopAdopt.
//
// "Adopt" here means what it means in adopt.go - bringing something already
// on disk under lmm's management - but the thing on disk is different in
// kind, which is why this is a SIBLING of PlanAdopt rather than a branch
// inside it. The mod_path adopt is filename-based end to end: it scans the
// game's own mod directory, matches by NAME SEARCH across every searchable
// source, copies into the cache for copy-mode games, and stamps
// ManualDownload. A workshop adopt scans Steam's library tree, matches by
// EXACT published-file id (no search at all), must never write a cache
// entry, and produces EXTERNAL rows lmm will never deploy. Threading four
// conditionals through scanLocal -> matchUntracked -> adoptScannedMod would
// fork that flow's shape inside itself.
//
// What it reuses, so this is a sibling and not a second engine: beginOp,
// installedSnapshot + checkPlanFresh (Ruling 5 staleness), saveInstalledMod,
// the profile-ref upsert, and the Event/Scope/StepEvent vocabulary.
//
// Nothing in this file reads, writes, moves or removes a single byte of the
// items themselves. The Steam client owns them.
package core

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// WorkshopScan is the pure, displayable result of scanning the Steam
// libraries on this machine for the items a game has subscribed: the app
// they belong to, where they were found, and how many lmm already tracks.
type WorkshopScan struct {
	GameID string `json:"game_id"`
	// AppID is the Steam app id the scan ran for - games.yaml's own
	// per-source id under `steamworkshop`.
	AppID string `json:"app_id"`
	// Libraries names every Steam library that actually held a workshop
	// manifest for AppID. Empty means Steam has downloaded nothing for this
	// game (or that the game is not installed through Steam at all), which
	// is an ordinary state, not an error.
	Libraries []string `json:"libraries"`
	// Items is every subscribed item those manifests declare, in file-id
	// order. Tracked and untracked alike: the split is Tracked/Untracked.
	Items []domain.WorkshopItem `json:"items"`
	// Tracked counts the items lmm already has a row for; Untracked counts
	// the ones an adopt would take on. The two always sum to len(Items).
	Tracked   int `json:"tracked"`
	Untracked int `json:"untracked"`
	// Warnings carries the non-fatal diagnostics the scan collected - an
	// unreadable library, a damaged manifest. One bad file never fails the
	// scan.
	Warnings []string `json:"warnings,omitempty"`
}

// WorkshopAdoptEntry is one untracked item an adopt would take on, with
// whatever Steam's API was willing to say about it folded in.
type WorkshopAdoptEntry struct {
	// FileID is the Steam published-file id, which is also the mod id lmm
	// will track the item under.
	FileID string `json:"file_id"`
	// Path is the directory the Steam client owns - what lands in
	// InstalledMod.ExternalPath. lmm records it and never writes to it.
	Path string `json:"path"`
	// SizeOnDisk, Manifest and TimeUpdated are the ACF's own facts.
	// Manifest is the content id lmm records as the item's Version; it is
	// what the update check compares Steam's hcontent_file against.
	SizeOnDisk  int64  `json:"size_on_disk,omitzero"`
	Manifest    string `json:"manifest,omitempty"`
	TimeUpdated int64  `json:"time_updated,omitzero"`
	// Mod is the metadata Steam's keyless API returned - nil when it
	// returned none. An entry with no Mod is still adoptable: the item is
	// on disk and the game loads it, so refusing to track it would help
	// nobody.
	Mod *domain.Mod `json:"mod,omitempty"`
	// Unavailable reports that Steam would not describe this item at all
	// (delisted, deleted or private while the user is still subscribed).
	Unavailable bool `json:"unavailable,omitzero"`
	// Note explains Unavailable, or any other per-entry caveat, in one
	// sentence.
	Note string `json:"note,omitempty"`
}

// WorkshopAdoptPlan is the pure, displayable plan for adopting a game's
// untracked Steam Workshop items. Computing it performs local reads and one
// batched, keyless metadata call; it writes nothing, so a caller may compute
// it speculatively, render it, and discard it.
type WorkshopAdoptPlan struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`

	// Scan is the underlying WorkshopScan.
	Scan *WorkshopScan `json:"scan"`
	// Entries is one per untracked item, in scan order - exactly what
	// ApplyWorkshopAdopt will adopt.
	Entries []WorkshopAdoptEntry `json:"entries"`
	// NoChanges reports that there is nothing to adopt: either Steam has
	// downloaded nothing for this game, or lmm already tracks all of it.
	NoChanges bool `json:"no_changes"`

	// snapshot is Ruling 5's staleness precondition: the installed-mod set
	// this plan was computed from. ApplyWorkshopAdopt re-derives it and
	// returns ErrStalePlan when it no longer matches. Unexported and outside
	// the wire contract - a frontend round-trips the plan through its own
	// store, not through JSON.
	snapshot installedSnapshot `json:"-"`
}

// WorkshopAdoptOptions configures PlanWorkshopAdopt.
type WorkshopAdoptOptions struct {
	// Refresh bypasses the source's metadata cache for this plan, so a
	// freshly-subscribed item's title shows up immediately rather than
	// after the cache's TTL.
	Refresh bool
}

// WorkshopAdoptResult reports ApplyWorkshopAdopt's outcome. Adopted, Skipped
// and Failed count plan entries, and every entry lands in exactly one.
type WorkshopAdoptResult struct {
	Adopted int `json:"adopted"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
	// Warnings carries diagnostics a caller that never watched the event
	// stream still needs to see.
	Warnings []string `json:"warnings,omitempty"`
}

// ErrNoWorkshopSource reports that the game maps no source capable of
// scanning locally-installed Workshop items - in practice, that games.yaml
// has no `steamworkshop: <appid>` entry for it. `lmm game edit --source
// steamworkshop=<appid>` (or re-running `lmm game detect`) is the fix.
var errNoWorkshopSource = fmt.Errorf("no Steam Workshop source is configured for this game")

// workshopSourceFor resolves the game's workshop-capable source, its
// registry id (the id every adopted row is keyed under) and the app id it is
// mapped to. Core stays source-agnostic: it asks the registry for the
// game's sources and takes the first that implements
// source.WorkshopScanner, so internal/core never imports the concrete
// steamworkshop package (the boundary the design's §7 ratchet pins).
func (s *Service) workshopSourceFor(game *domain.Game) (scanner source.WorkshopScanner, sourceID, appID string, err error) {
	// The game's OWN sources map is walked, rather than SourcesForGame's
	// registry lookup by game id: the caller already handed us the game, and
	// making the answer depend on games.yaml having been loaded would make
	// this fail for a game object a frontend built but has not saved yet.
	// Sorted for determinism, exactly as SourcesForGame sorts.
	ids := make([]string, 0, len(game.SourceIDs))
	for id := range game.SourceIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		appID := game.SourceIDs[id]
		if appID == "" {
			continue
		}
		src, err := s.GetSource(id)
		if err != nil {
			continue // unregistered: silently skipped, as SourcesForGame does
		}
		ws, ok := src.(source.WorkshopScanner)
		if !ok {
			continue
		}
		return ws, id, appID, nil
	}
	return nil, "", "", errNoWorkshopSource
}

// ScanWorkshop reports every Steam Workshop item installed for game, split
// by whether lmm already tracks it in profileName.
//
// Pure local reads: Steam's own appworkshop manifests and lmm's own DB. No
// network call, no write, and never a touch of the items' files. It is the
// scan-only query for a frontend that wants to show what is there without
// planning an adopt.
func (s *Service) ScanWorkshop(ctx context.Context, game *domain.Game, profileName string) (*WorkshopScan, error) {
	scan, _, err := s.scanWorkshop(ctx, game, profileName)
	return scan, err
}

// scanWorkshop is ScanWorkshop's internal twin: it ALSO returns the
// installed-mod set the scan was classified against, so PlanWorkshopAdopt
// can build its staleness snapshot from the same read rather than asking the
// DB twice for two views that could disagree (scanLocal's own convention).
func (s *Service) scanWorkshop(ctx context.Context, game *domain.Game, profileName string) (*WorkshopScan, []domain.InstalledMod, error) {
	scanner, sourceID, appID, err := s.workshopSourceFor(game)
	if err != nil {
		return nil, nil, err
	}

	found, err := scanner.ScanWorkshopItems(ctx, appID)
	if err != nil {
		return nil, nil, err
	}

	installed, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, nil, fmt.Errorf("getting installed mods: %w", err)
	}
	tracked := make(map[string]bool, len(installed))
	for _, im := range installed {
		if im.SourceID == sourceID {
			tracked[im.ID] = true
		}
	}

	scan := &WorkshopScan{
		GameID:    game.ID,
		AppID:     appID,
		Libraries: found.Roots,
		Items:     found.Items,
		Warnings:  found.Warnings,
	}
	for _, it := range found.Items {
		if tracked[it.FileID] {
			scan.Tracked++
			continue
		}
		scan.Untracked++
	}
	return scan, installed, nil
}

// PlanWorkshopAdopt scans the Steam libraries for game's subscribed items
// and resolves the untracked ones against Steam's keyless metadata API,
// producing the plan ApplyWorkshopAdopt consumes.
//
// It performs local reads and ONE batched metadata call (a hundred ids per
// request), and writes nothing. A metadata failure is not fatal: every entry
// keeps the identity its ACF record carries, which is enough to track it,
// and the failure is reported as a scan warning.
func (s *Service) PlanWorkshopAdopt(ctx context.Context, game *domain.Game, profileName string, opts WorkshopAdoptOptions) (*WorkshopAdoptPlan, error) {
	scan, installed, err := s.scanWorkshop(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	scanner, sourceID, appID, err := s.workshopSourceFor(game)
	if err != nil {
		return nil, err
	}

	trackedIDs := make(map[string]bool, len(installed))
	for _, im := range installed {
		if im.SourceID == sourceID {
			trackedIDs[im.ID] = true
		}
	}

	plan := &WorkshopAdoptPlan{
		GameID:   game.ID,
		Profile:  profileName,
		Scan:     scan,
		snapshot: snapshotOf(installed),
	}
	var untracked []domain.WorkshopItem
	for _, it := range scan.Items {
		if trackedIDs[it.FileID] {
			continue
		}
		untracked = append(untracked, it)
	}
	if len(untracked) == 0 {
		plan.Entries = []WorkshopAdoptEntry{}
		plan.NoChanges = true
		return plan, nil
	}

	ids := make([]string, 0, len(untracked))
	for _, it := range untracked {
		ids = append(ids, it.FileID)
	}
	described := s.describeWorkshopItems(ctx, scanner, appID, ids, opts.Refresh, scan)

	plan.Entries = make([]WorkshopAdoptEntry, 0, len(untracked))
	for _, it := range untracked {
		entry := WorkshopAdoptEntry{
			FileID:      it.FileID,
			Path:        it.Path,
			SizeOnDisk:  it.SizeOnDisk,
			Manifest:    it.Manifest,
			TimeUpdated: it.TimeUpdated,
		}
		if d, ok := described[it.FileID]; ok {
			if d.Unavailable {
				entry.Unavailable = true
				entry.Note = d.Note
			} else {
				mod := d.Mod
				entry.Mod = &mod
			}
		}
		plan.Entries = append(plan.Entries, entry)
	}
	return plan, nil
}

// describeWorkshopItems resolves ids' metadata through the source's optional
// batch describer, keyed by id. Every failure degrades to "no metadata":
// Steam being unreachable must not stop lmm tracking items that are sitting
// on the user's disk right now, so the failure becomes a scan warning and
// each entry falls back to the identity its ACF record carries.
func (s *Service) describeWorkshopItems(ctx context.Context, scanner source.WorkshopScanner, appID string, ids []string, refresh bool, scan *WorkshopScan) map[string]source.ModDescription {
	describer, ok := scanner.(source.BatchModDescriber)
	if !ok {
		return nil
	}
	described, err := describer.DescribeMods(ctx, appID, ids, refresh)
	if err != nil {
		scan.Warnings = append(scan.Warnings,
			fmt.Sprintf("could not fetch Steam metadata (items are still adoptable): %v", err))
		return nil
	}
	out := make(map[string]source.ModDescription, len(described))
	for _, d := range described {
		out[d.ModID] = d
	}
	return out
}

// ApplyWorkshopAdopt records every plan entry as an EXTERNAL installed mod:
// a DB row and a profile ref, and nothing else. No file is read, written,
// moved or removed - not lmm's, and certainly not Steam's.
//
// Each row is stamped External with the Steam-owned directory as its
// ExternalPath, Deployed true (its files ARE where the game reads them, and
// no flow ever mutates that for an external mod), Enabled true, and
// UpdatePolicy notify - forced, because auto is refused for a mod lmm cannot
// update and pinned is a decision the user has not made yet.
//
// Version is the ACF's manifest (the content id), which is what makes the
// update check exact. An item whose manifest the ACF does not record adopts
// with an empty version and falls back to the timestamp signal.
//
// A per-entry failure is counted and reported, never fatal. Ruling 5: the
// plan is refused with ErrStalePlan when the profile's installed-mod set has
// changed since it was computed. sink may be nil.
func (s *Service) ApplyWorkshopAdopt(ctx context.Context, game *domain.Game, plan *WorkshopAdoptPlan, sink EventSink) (*WorkshopAdoptResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &WorkshopAdoptResult{}, err
	}
	defer release()
	return s.applyWorkshopAdopt(ctx, game, plan, sink)
}

func (s *Service) applyWorkshopAdopt(ctx context.Context, game *domain.Game, plan *WorkshopAdoptPlan, sink EventSink) (*WorkshopAdoptResult, error) {
	result := &WorkshopAdoptResult{}
	if plan == nil {
		return result, fmt.Errorf("workshop adopt plan is nil: call PlanWorkshopAdopt first")
	}
	if err := s.checkPlanFresh(ctx, plan.GameID, plan.Profile, plan.snapshot); err != nil {
		return result, err
	}

	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	_, sourceID, _, err := s.workshopSourceFor(game)
	if err != nil {
		return result, err
	}

	emit(StepEvent{
		Scope:  Scope{Op: OpWorkshopAdopt, Total: len(plan.Entries)},
		Phase:  WorkshopScanned,
		Detail: fmt.Sprintf("%d item(s) in %d Steam librar(y/ies)", len(plan.Scan.Items), len(plan.Scan.Libraries)),
	})

	// The tracked set starts from what is installed now and grows with each
	// adoption, so an item listed twice by two libraries adopts only once.
	current, _ := s.GetInstalledMods(ctx, plan.GameID, plan.Profile)
	tracked := make(map[string]bool, len(current))
	for _, im := range current {
		if im.SourceID == sourceID {
			tracked[im.ID] = true
		}
	}

	pm := s.NewProfileManager()
	total := len(plan.Entries)
	for idx, entry := range plan.Entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		name := workshopEntryName(entry)
		scope := Scope{
			Op: OpWorkshopAdopt, Index: idx + 1, Total: total, ModName: name,
			Mod: &domain.ModReference{SourceID: sourceID, ModID: entry.FileID},
		}

		if tracked[entry.FileID] {
			result.Skipped++
			emit(StepEvent{Scope: scope, Phase: WorkshopSkipped, Detail: "already tracked by lmm"})
			continue
		}
		if entry.Unavailable {
			// Reported, then adopted anyway: the item IS on disk and the
			// game IS loading it, so refusing to track it would leave the
			// user with a mod lmm pretends not to see.
			emit(StepEvent{Scope: scope, Phase: WorkshopUnavailable, Detail: entry.Note})
		}

		mod := workshopEntryMod(entry, sourceID, plan.GameID, name)
		installedMod := &domain.InstalledMod{
			Mod:          mod,
			ProfileName:  plan.Profile,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			Deployed:     true,
			External:     true,
			ExternalPath: entry.Path,
		}
		if err := s.saveInstalledMod(ctx, installedMod); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return result, cerr
			}
			result.Failed++
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", name, err))
			emit(StepEvent{Scope: scope, Phase: WorkshopSkipped, Detail: fmt.Sprintf("could not record: %v", err)})
			continue
		}

		// Ruling 16 (A): the DB row is already saved, so the profile ref
		// that completes it is written even under a cancelled ctx; the
		// cancellation itself is then fatal rather than a warning.
		ref := domain.ModReference{SourceID: sourceID, ModID: entry.FileID, Version: mod.Version}
		if err := completeProfileWrite(ctx, func(ctx context.Context) error {
			return pm.UpsertMod(ctx, plan.GameID, plan.Profile, ref)
		}); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				result.Adopted++
				return result, cerr
			}
			msg := fmt.Sprintf("%s: could not update profile: %v", name, err)
			result.Warnings = append(result.Warnings, msg)
			emit(StepEvent{Scope: scope, Phase: WorkshopSkipped, Detail: msg})
		}

		tracked[entry.FileID] = true
		result.Adopted++
		emit(StepEvent{Scope: scope, Phase: WorkshopAdopted, Detail: entry.Path})
	}

	// A cancellation landing on the LAST entry's own iteration never reaches
	// another loop-top ctx.Err() check above (applyAdopt's own NEW-1 fix).
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

// workshopEntryName is what an adopted row is called: Steam's own title
// where the API gave one, else a plain "Workshop item <fileid>" - honest
// about the fact that lmm does not know the name, rather than leaving a
// blank row.
func workshopEntryName(entry WorkshopAdoptEntry) string {
	if entry.Mod != nil && entry.Mod.Name != "" {
		return entry.Mod.Name
	}
	return "Workshop item " + entry.FileID
}

// workshopEntryMod builds the domain.Mod an adopted row carries: the
// metadata Steam described where there is any, and the ACF's own facts where
// there is not.
//
// Version is ALWAYS the ACF manifest, never the API's hcontent_file, even
// when both are present: the recorded version must describe what is on disk
// right now, and the API describes what Steam has published. The two being
// different is precisely what the update check is for.
func workshopEntryMod(entry WorkshopAdoptEntry, sourceID, gameID, name string) domain.Mod {
	mod := domain.Mod{
		ID:       entry.FileID,
		SourceID: sourceID,
		Name:     name,
		Version:  entry.Manifest,
		// The lmm game id, not the Steam app id: the DB keys rows by it, and
		// Updater translates it through game.SourceIDs per source at check
		// time, exactly as it does for every other source.
		GameID: gameID,
	}
	if entry.Mod != nil {
		mod.Author = entry.Mod.Author
		mod.Summary = entry.Mod.Summary
		mod.Description = entry.Mod.Description
		mod.Category = entry.Mod.Category
		mod.PictureURL = entry.Mod.PictureURL
		mod.SourceURL = entry.Mod.SourceURL
		mod.Downloads = entry.Mod.Downloads
	}
	if entry.TimeUpdated > 0 {
		mod.UpdatedAt = time.Unix(entry.TimeUpdated, 0).UTC()
	} else if entry.Mod != nil {
		mod.UpdatedAt = entry.Mod.UpdatedAt
	}
	return mod
}
