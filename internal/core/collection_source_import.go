// collection_source_import.go is the import of a source's own collection
// (#607 C2, #609 C4; design docs/plans/2026-11-10-collections-design.md §1,
// §3, §6, §12): `lmm profile import --nexus-collection <ref>` and the web
// UI's equivalent. The collection is converted (ConvertSourceCollection)
// into lmm's version-1 collection document and imported through L1's
// pipeline (PlanCollectionImport / ApplyCollectionImport), so it plans,
// gates and applies as every collection does; the conversion rides on the
// plan (CollectionImport.Source) for the frontends to render.
package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// SourceCollectionImportRequest is what PlanSourceCollectionImport plans.
type SourceCollectionImportRequest struct {
	// SourceID is the source the collection lives on ("nexusmods").
	SourceID string
	// Ref is the collection as the user gave it: a slug, slug@revision or
	// the collection's page URL.
	Ref string
	// ProfileName names the profile to create (`--as`). Empty uses the
	// collection's own name.
	ProfileName string
	// WithOptional opts every optional item in; Optional opts in the items
	// it names (ConvertedCollectionItem.ID). Optional items are off by
	// default (owner ruling, 2026-11-10).
	WithOptional bool
	Optional     []string
}

// PlanSourceCollectionImport resolves req's collection on its source for
// game, converts it into an lmm collection document and plans importing it
// as a new profile. Nothing is written but the recipe's own cache entry. The
// plan's Collection.Source carries every item with its state - and, for an
// account the source serves no downloads to, every item to install is a
// manual download, said up front rather than discovered at Apply.
//
// A collection needing a feature this lmm lacks still plans; its Apply
// refuses (*CollectionUnsupportedError). A reference the source cannot
// resolve is IsBadCollectionRef.
func (s *Service) PlanSourceCollectionImport(ctx context.Context, game *domain.Game, req SourceCollectionImportRequest) (*ImportPlan, error) {
	conv, err := s.ConvertSourceCollection(ctx, game, req.SourceID, req.Ref,
		CollectionConvertOptions{WithOptional: req.WithOptional, Optional: req.Optional})
	if err != nil {
		return nil, err
	}
	data, err := config.MarshalCollection(conv.Document)
	if err != nil {
		return nil, fmt.Errorf("writing the converted collection: %w", err)
	}
	plan, err := s.PlanCollectionImport(ctx, CollectionImportRequest{
		Data: data, Input: CollectionFromSource, GameID: game.ID, ProfileName: req.ProfileName,
	})
	if err != nil {
		return nil, err
	}
	c := plan.Collection
	c.Source = conv
	plan.phases = conv.phases
	if len(conv.AutoUpdate) > 0 {
		plan.autoUpdate = make(map[string]bool, len(conv.AutoUpdate))
		for _, key := range conv.AutoUpdate {
			plan.autoUpdate[key] = true
		}
	}
	c.Unsupported = mergeUnsupported(c.Unsupported, conv.Unsupported)

	installed := map[string]bool{}
	for _, ref := range plan.Installed {
		installed[domain.ModKey(ref.SourceID, ref.ModID)] = true
	}
	// A mod another profile already has needs no download, so a free
	// account installs it like any other.
	cached := map[string]bool{}
	for _, ref := range plan.AlreadyCached {
		cached[domain.ModKey(ref.SourceID, ref.ModID)] = true
	}
	if tier, ok := s.accountTier(ctx, req.SourceID); ok {
		conv.AccountTier = string(tier)
		conv.ManualDownload = tier == source.TierFree
	}
	pending := 0
	for i := range conv.Items {
		it := &conv.Items[i]
		if it.State != CollectionItemWillInstall {
			continue
		}
		switch {
		case installed[domain.ModKey(req.SourceID, it.ModID)]:
			it.State = CollectionItemInstalled
		case conv.ManualDownload && !cached[domain.ModKey(req.SourceID, it.ModID)]:
			it.State = CollectionItemManualDownload
			pending++
		}
	}
	if pending > 0 {
		conv.Notes = append(conv.Notes, fmt.Sprintf("This %s account is not served downloads through the API, so %d item(s) are manual downloads: the import saves the profile with them pending, and each item links its file's page.",
			s.sourceDisplayName(req.SourceID), pending))
	}
	conv.recount()
	return plan, nil
}

// accountTier is the source account's tier, when the source reports one and
// can say; ok is false otherwise (no key, an unreachable check), and the
// plan then says nothing about it - Apply finds out per item.
func (s *Service) accountTier(ctx context.Context, sourceID string) (source.Tier, bool) {
	src, err := s.GetSource(sourceID)
	if err != nil {
		return "", false
	}
	var tier source.Tier
	if refresher, ok := src.(source.AccountTierRefresher); ok {
		// A plan is the user asking now: an upgrade made minutes ago counts.
		tier, err = refresher.RefreshAccountTier(ctx)
	} else if reporter, ok := src.(source.AccountTierReporter); ok {
		tier, err = reporter.AccountTier(ctx)
	} else {
		return "", false
	}
	if err != nil || tier == "" {
		return "", false
	}
	return tier, true
}

// mergeUnsupported is a's and b's features together, one entry per
// feature with every item either names, in name order.
func mergeUnsupported(a, b []UnsupportedCollectionFeature) []UnsupportedCollectionFeature {
	out := slices.Clone(a)
	for _, f := range b {
		i := slices.IndexFunc(out, func(g UnsupportedCollectionFeature) bool { return g.Feature == f.Feature })
		if i < 0 {
			out = append(out, UnsupportedCollectionFeature{Feature: f.Feature, Items: slices.Clone(f.Items)})
			continue
		}
		for _, item := range f.Items {
			out[i].Items = appendUnique(out[i].Items, item)
		}
	}
	slices.SortFunc(out, func(x, y UnsupportedCollectionFeature) int { return strings.Compare(x.Feature, y.Feature) })
	return out
}

// finishSourceCollection says what a source collection's import left for
// the user once its profile is saved: the items still to download by hand,
// and - by name, since nothing but the collection's recipe records it - the
// ones the curator wants on their newest file, which the import could not
// set to update automatically because they are not installed yet. The
// policy of every item it DID install was written with its row
// (ImportPlan.autoUpdate).
func finishSourceCollection(plan *ImportPlan, c *ConvertedCollection, result *ProfileImportResult, opts ProfileImportOptions) {
	installed := opts.Install && !opts.NoInstall
	pending := map[string]bool{}
	if !installed {
		for _, ref := range slices.Concat(plan.AlreadyCached, plan.NeedsRedownload, plan.Missing) {
			pending[domain.ModKey(ref.SourceID, ref.ModID)] = true
		}
	} else {
		for _, ref := range slices.Concat(plan.NeedsRedownload, plan.Missing) {
			if c.ManualDownload {
				pending[domain.ModKey(ref.SourceID, ref.ModID)] = true
			}
		}
	}
	if len(pending) > 0 {
		result.Notes = append(result.Notes, fmt.Sprintf("The profile records %d item(s) still to download: get each file from its page and install it from the file (lmm install --id <mod-id> --from-file <archive>).", len(pending)))
	}
	// Only the rows this import wrote were set to auto: a mod the profile
	// already had (ImportPlan.Installed) keeps its own policy.
	written := map[string]bool{}
	if installed {
		for _, ref := range slices.Concat(plan.AlreadyCached, plan.NeedsRedownload, plan.Missing) {
			if key := domain.ModKey(ref.SourceID, ref.ModID); !pending[key] {
				written[key] = true
			}
		}
	}
	var autoInstalled int
	var autoPending []string
	for _, it := range c.Items {
		if !it.AutoUpdate {
			continue
		}
		key := domain.ModKey(c.SourceID, it.ModID)
		switch {
		case pending[key]:
			autoPending = append(autoPending, it.Name)
		case written[key]:
			autoInstalled++
		}
	}
	if autoInstalled > 0 {
		result.Notes = append(result.Notes, fmt.Sprintf("%d item(s) the curator wants on their newest file were installed at the tested file and set to update automatically.", autoInstalled))
	}
	if len(autoPending) > 0 {
		result.Notes = append(result.Notes, fmt.Sprintf("The curator wants these on their newest file: %s. Once each is installed, set it to update automatically (lmm mod set-update <mod-id> --auto).",
			strings.Join(autoPending, ", ")))
	}
}

// lapsedAccount is err as ONE refusal when every item the apply refused
// was refused for the account (source.ErrAccountRestricted): the account
// was planned as one the source serves downloads to, and is not one any
// more (#607). The user's next step is to plan again - which marks every
// item a manual download - not to read one refusal per mod. nil otherwise.
func (s *Service) lapsedAccount(sourceID string, err error) error {
	var refused *AcquireRefusedError
	if !errors.As(err, &refused) || len(refused.Refusals) == 0 {
		return nil
	}
	for _, r := range refused.Refusals {
		if !errors.Is(r.Err, source.ErrAccountRestricted) {
			return nil
		}
	}
	return &source.AccountRestrictedError{Reason: fmt.Sprintf(
		"this %s account is no longer served downloads through the API (it is not Premium), so nothing was imported: plan the import again, and every item is listed as a manual download",
		s.sourceDisplayName(sourceID))}
}
