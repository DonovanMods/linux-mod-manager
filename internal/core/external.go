// Package core: this file holds the EXTERNAL-mod rules (#269).
//
// An external mod (domain.InstalledMod.External) is one lmm tracks but
// never deploys: another agent - today, the Steam client for a Workshop
// item - owns its files where they sit, and the game loads them from
// there. lmm never links, copies, downloads, moves or removes such a mod's
// content.
//
// Every flow that could otherwise touch those files consults this file:
// the partition helpers that keep external mods out of a deploy or purge
// set, and the one typed refusal every flow that CANNOT act on such a mod
// returns. Keeping all of it here is what makes "which flows changed for
// #269" answerable by reading one file plus its callers, rather than by
// grepping for a bool.
package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ExternalModError is the ONE typed refusal for every operation lmm cannot
// perform on an external mod - deploy of a named mod, enable, disable,
// update apply, rollback, relink, `mod set-update auto`, and installing a
// second copy of something already tracked.
//
// One type rather than one per flow (the LockedRefError precedent) keeps
// cmd/lmm's details_coverage ratchet honest with a single entry and gives
// the product one wording for one kind of refusal: Op names what was
// refused, Reason says what the user should do instead.
type ExternalModError struct {
	// Op is the refused operation, in the CLI's own vocabulary
	// ("deploy", "enable", "update", ...).
	Op string
	// Mod identifies the mod refused.
	Mod domain.ModReference
	// ModName is the display name, for a message that names the mod rather
	// than its id.
	ModName string
	// Reason is the user-facing explanation, already phrased as advice.
	Reason string
}

// Error renders "cannot <op> <mod>: <reason>".
func (e *ExternalModError) Error() string {
	name := e.ModName
	if name == "" {
		name = e.Mod.ModID
	}
	return fmt.Sprintf("cannot %s %s: %s", e.Op, name, e.Reason)
}

// Unwrap makes errors.Is(err, domain.ErrExternalMod) true, so a caller that
// only cares that the refusal happened needs no type assertion.
func (e *ExternalModError) Unwrap() error { return domain.ErrExternalMod }

// Details returns the payload the --json error envelope attaches under
// "details" (Ruling 3). The returned type is unexported deliberately: it is
// a wire shape for the envelope, not a core contract type of its own.
func (e *ExternalModError) Details() any {
	return externalModErrorDetails{Op: e.Op, Mod: e.Mod, ModName: e.ModName, Reason: e.Reason}
}

type externalModErrorDetails struct {
	Op      string              `json:"op"`
	Mod     domain.ModReference `json:"mod"`
	ModName string              `json:"mod_name,omitempty"`
	Reason  string              `json:"reason"`
}

// The refusal wordings. They live here, not at each call site, so the CLI,
// the web UI and the --json envelope say the same thing about the same
// fact, and so a change of wording is one edit.
const (
	// ReasonExternalNoDeploy explains why lmm will not deploy an item Steam
	// already has in place.
	ReasonExternalNoDeploy = "this is a Steam Workshop item - Steam keeps its files where the game reads them, and lmm never deploys them"
	// ReasonExternalNoToggle explains why enable/disable is refused. A
	// bookkeeping-only "disabled" flag on a mod the game still loads is a
	// lie, which is why lmm refuses rather than pretending.
	ReasonExternalNoToggle = "lmm cannot disable a Steam Workshop item - unsubscribe it in Steam, or use the game's own mod menu"
	// ReasonExternalNoUpdate explains why lmm cannot APPLY an update it can
	// perfectly well report.
	ReasonExternalNoUpdate = "Steam applies Workshop updates itself the next time you launch the game (or use Steam's \"Verify integrity of game files\") - lmm reports them but cannot apply them"
	// ReasonExternalNoRollback explains why there is nothing to roll back
	// to: lmm never held a previous copy.
	ReasonExternalNoRollback = "lmm has no copy of this Steam Workshop item to roll back to - Steam owns its files"
	// ReasonExternalNoRelink explains why relinking is meaningless: there is
	// no link.
	ReasonExternalNoRelink = "a Steam Workshop item is not deployed by lmm, so there is nothing to relink"
	// ReasonExternalNoAutoUpdate explains why the auto policy is refused
	// while notify and pinned are allowed.
	ReasonExternalNoAutoUpdate = "lmm cannot apply a Steam Workshop update, so \"auto\" would never do anything - \"notify\" (the default) or \"pinned\" are the meaningful policies"
	// ReasonExternalAlreadyTracked is Q2's answer: installing an lmm-managed
	// copy of an item Steam already loads would put the mod in the game
	// twice, with a conflict report that cannot explain itself.
	ReasonExternalAlreadyTracked = "already tracked from your Steam subscription - uninstall it first if you want lmm to manage its own copy"
)

// NoteExternalProfileScope is the advisory a profile switch/apply/sync
// emits when the profiles it moves between differ in external mods.
//
// A Workshop item is game-global; lmm profiles are not. Making a switch
// change what Steam has on disk would mean unsubscribing on the user's
// behalf, which needs a Steam client session and is a documented NO-GO -
// so lmm says plainly what it did not do rather than silently doing
// nothing.
const NoteExternalProfileScope = "%d Steam Workshop item(s) stay active regardless of profile - manage subscriptions in the Steam client."

// UninstallExternalNote is the confirmation wording an uninstall of an
// external mod must carry: the operation removes lmm's tracking and
// nothing else.
const UninstallExternalNote = "this only stops lmm tracking it; the item stays subscribed in Steam - unsubscribe in the Steam client to remove it"

// externalRefusal builds an ExternalModError for one installed mod.
func externalRefusal(op string, mod *domain.InstalledMod, reason string) *ExternalModError {
	return &ExternalModError{
		Op:      op,
		Mod:     domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version},
		ModName: mod.Name,
		Reason:  reason,
	}
}

// refuseExternal returns an ExternalModError when mod is external, and nil
// otherwise - the one-line gate every refusing flow opens with.
func refuseExternal(op string, mod *domain.InstalledMod, reason string) error {
	if mod == nil || !mod.External {
		return nil
	}
	return externalRefusal(op, mod, reason)
}

// partitionExternal splits mods into the ones lmm may deploy or purge and
// the display names of the ones it may not touch. Order is preserved in
// both halves, so a caller's load order survives the split.
//
// This is the ONE place a deploy- or purge-direction mod set is filtered.
// Every such flow calls it rather than testing the field itself, so "which
// mods does lmm actually write files for" has a single answer.
func partitionExternal(mods []domain.InstalledMod) (managed []domain.InstalledMod, external []string) {
	for _, m := range mods {
		if m.External {
			external = append(external, externalDisplayName(m))
			continue
		}
		managed = append(managed, m)
	}
	return managed, external
}

// partitionExternalPtrs is partitionExternal for the pointer slices the
// deploy loop passes around.
func partitionExternalPtrs(mods []*domain.InstalledMod) (managed []*domain.InstalledMod, external []*domain.InstalledMod) {
	for _, m := range mods {
		if m != nil && m.External {
			external = append(external, m)
			continue
		}
		managed = append(managed, m)
	}
	return managed, external
}

// externalDisplayName is what a plan lists an untouched mod as: its name,
// falling back to its id for a row whose metadata never resolved.
func externalDisplayName(m domain.InstalledMod) string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
}

// countExternal reports how many of mods are external - the number
// `lmm status`, the update summary and Mission Control's deploy card all
// report separately from the deployable count.
func countExternal(mods []domain.InstalledMod) int {
	n := 0
	for _, m := range mods {
		if m.External {
			n++
		}
	}
	return n
}

// externalKeys is the set of "sourceID:modID" keys for the external mods in
// mods, for a caller comparing two profiles' external sets.
func externalKeys(mods []domain.InstalledMod) map[string]bool {
	keys := make(map[string]bool)
	for _, m := range mods {
		if m.External {
			keys[domain.ModKey(m.SourceID, m.ID)] = true
		}
	}
	return keys
}

// CheckExternalInstallExclusivity implements Q2 (#269): `lmm install
// steamworkshop:<fileid>` on an item the user is already subscribed to -
// and which lmm therefore already tracks as external - is refused.
//
// Silently creating a second, lmm-deployed copy alongside the one Steam
// already loads is how you get a mod that appears twice in-game and a
// conflict report that cannot explain itself. External and managed are
// mutually exclusive per install; uninstalling the tracking first is the
// documented way to switch.
//
// Not-installed and not-external both pass. A DB read failure passes too:
// this gate only ever ADDS a refusal, and the install flow that follows
// reports a broken database in its own words.
//
// Called by PlanInstall and by PlanInstallMany (per entry), both BEFORE any
// source read, so the ruled wording is what the user sees rather than
// whatever a Tier-1 workshop source says about files it cannot serve.
func (s *Service) CheckExternalInstallExclusivity(ctx context.Context, gameID, profileName, sourceID, modID string) error {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, gameID, profileName)
	if err != nil {
		return nil
	}
	return refuseExternal("install", mod, ReasonExternalAlreadyTracked)
}
