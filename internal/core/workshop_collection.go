// workshop_collection.go is the collection half of Steam Workshop Tier 2
// (#269 W2): `lmm profile import --workshop-collection <id|url>`, and the
// SPA's equivalent.
//
// A Workshop COLLECTION is a curated list of items, which is exactly what
// an lmm profile is. So this flow adds no new plan type and no new apply
// engine: it resolves the collection, writes the ids into an ordinary
// exported-profile document, and hands that to the existing PlanImport /
// ApplyImport pair. Everything downstream - the staleness snapshot, the
// three buckets, the save, the event stream - is the import flow lmm
// already had.
//
// What it does NOT do is download anything. Items the user is already
// subscribed to were adopted by Tier 1 and are simply present; the rest
// need Tier 3's download path, which lands in its own unit (#347). Until
// then a collection import saves the profile and says, per item, what the
// user has to do in Steam - which is the honest answer, and a good deal
// more useful than a stack of "operation not supported by this source"
// failures.
package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// WorkshopCollection is the document describing one resolved collection and
// what importing it would mean for this machine - the `--json` shape of
// `lmm profile import --workshop-collection`, and what the SPA's import
// modal renders above the ordinary import preview.
//
// It rides on ImportPlan (ImportPlan.WorkshopCollection) rather than being
// returned separately, so a frontend that holds a plan holds the whole
// story, and the serve plan/apply round trip needs no second document.
type WorkshopCollection struct {
	// SourceID is the registry id the refs are keyed under
	// ("steamworkshop"), never assumed by a renderer.
	SourceID string `json:"source_id"`
	// CollectionID is the published-file id of the collection itself, and
	// URL its own page - the one click that shows a user what they are
	// importing. Name is best-effort: a collection lmm can list but not
	// name is still fully importable.
	CollectionID string `json:"collection_id"`
	Name         string `json:"name,omitempty"`
	URL          string `json:"url,omitempty"`
	// GameID and ProfileName are the game the collection was resolved for
	// and the profile the import would create.
	GameID      string `json:"game_id"`
	ProfileName string `json:"profile_name"`
	// Items is every child of the collection, in the author's own order -
	// which becomes the imported profile's load order.
	Items []WorkshopCollectionItem `json:"items"`
	// Tracked and NotSubscribed count Items by what lmm can do with them
	// today: Tracked are already adopted from the user's Steam
	// subscriptions, NotSubscribed are the rest. They are a SUMMARY of
	// Items, carried so a renderer can lead with the one number that
	// decides whether there is anything for the user to do.
	Tracked       int `json:"tracked"`
	NotSubscribed int `json:"not_subscribed"`
}

// WorkshopCollectionItem is one child of a collection.
//
// Tracked means lmm already has a row for it - the user is subscribed and
// `lmm import --workshop` has adopted it, so the imported profile simply
// references what is already there. Note explains what to do about an item
// that is not, in one sentence; it is empty for a tracked one, because
// there is nothing to do.
type WorkshopCollectionItem struct {
	FileID  string `json:"file_id"`
	Name    string `json:"name,omitempty"`
	URL     string `json:"url,omitempty"`
	Tracked bool   `json:"tracked,omitzero"`
	Note    string `json:"note,omitempty"`
}

// subscribeNote is the one sentence an un-subscribed collection item gets.
// It names the Steam action and the lmm command that follows it, because
// those two steps are the whole remedy and splitting them across two
// surfaces is how a user ends up subscribing and then wondering why lmm
// still shows nothing.
const subscribeNote = "subscribe in Steam and re-run `lmm import --workshop`"

// PlanWorkshopCollectionImport resolves ref (a collection id or a URL
// carrying one) against game's workshop source and plans the import of the
// profile it describes.
//
// profileName names the profile to create; empty derives one from the
// collection's own title, falling back to its id. The returned plan is an
// ordinary ImportPlan - PlanImport computed it from a document this
// function assembled - with WorkshopCollection attached.
//
// Nothing is written and nothing is downloaded. The only network calls are
// the source's own: resolving the collection, and one batch metadata
// lookup for the item names (best-effort - an unreachable API costs the
// display names, not the import).
func (s *Service) PlanWorkshopCollectionImport(ctx context.Context, game *domain.Game, profileName, ref string) (*ImportPlan, error) {
	scanner, sourceID, _, err := s.workshopSourceFor(game)
	if err != nil {
		return nil, err
	}
	resolver, ok := scanner.(source.CollectionResolver)
	if !ok {
		return nil, fmt.Errorf("source %q: %w: it does not resolve collections", sourceID, source.ErrNotSupported)
	}

	collection, err := resolver.ResolveCollection(ctx, ref)
	if err != nil {
		return nil, err
	}
	if len(collection.ItemIDs) == 0 {
		return nil, fmt.Errorf("collection %s: %w: it lists no items", collection.ID, source.ErrInvalidReference)
	}

	name := strings.TrimSpace(profileName)
	if name == "" {
		name = collectionProfileName(collection)
	}

	profile := &domain.Profile{Name: name, GameID: game.ID}
	for _, id := range collection.ItemIDs {
		profile.Mods = append(profile.Mods, domain.ModReference{SourceID: sourceID, ModID: id})
	}
	// Round-tripped through the REAL exporter rather than hand-marshalled:
	// the document PlanImport parses is then, by construction, exactly what
	// `lmm profile export` writes, so a collection import can never drift
	// into a shape the importer treats differently.
	data, err := config.ExportProfile(profile)
	if err != nil {
		return nil, fmt.Errorf("building collection profile: %w", err)
	}

	plan, err := s.PlanImport(ctx, game, data)
	if err != nil {
		return nil, err
	}
	plan.WorkshopCollection = s.describeCollection(ctx, game, collection, sourceID, name, plan)
	return plan, nil
}

// describeCollection classifies every item of the collection against the
// plan the import produced, and fills in the display names.
//
// The classification is DERIVED from the plan's own buckets rather than
// re-read from the DB: PlanImport has already decided which refs it has
// rows for, and asking the database a second question that could disagree
// with the first is how a preview ends up contradicting what it is
// previewing.
func (s *Service) describeCollection(ctx context.Context, game *domain.Game, collection source.Collection, sourceID, profileName string, plan *ImportPlan) *WorkshopCollection {
	tracked := make(map[string]bool, len(plan.Installed))
	for _, ref := range plan.Installed {
		tracked[ref.ModID] = true
	}

	doc := &WorkshopCollection{
		SourceID:     sourceID,
		CollectionID: collection.ID,
		Name:         collection.Name,
		URL:          collection.URL,
		GameID:       game.ID,
		ProfileName:  profileName,
		Items:        make([]WorkshopCollectionItem, 0, len(collection.ItemIDs)),
	}
	described := s.describeCollectionItems(ctx, game, sourceID, collection.ItemIDs)
	for _, id := range collection.ItemIDs {
		item := WorkshopCollectionItem{
			FileID:  id,
			Name:    described[id].Name,
			URL:     described[id].SourceURL,
			Tracked: tracked[id],
		}
		if item.Tracked {
			doc.Tracked++
		} else {
			doc.NotSubscribed++
			item.Note = subscribeNote
		}
		doc.Items = append(doc.Items, item)
	}
	return doc
}

// describeCollectionItems resolves display names and item URLs for the
// collection's children in ONE round trip, when the source can describe a
// batch (Steam's endpoint takes a hundred ids per request).
//
// Best-effort throughout: an item lmm could not describe renders as the
// bare file id, which is still enough to find it on Steam. The URL comes
// from the source's own document rather than being built here - core has no
// business knowing what a Workshop item's page looks like.
func (s *Service) describeCollectionItems(ctx context.Context, game *domain.Game, sourceID string, ids []string) map[string]domain.Mod {
	out := make(map[string]domain.Mod, len(ids))
	src, err := s.GetSource(sourceID)
	if err != nil {
		return out
	}
	describer, ok := src.(source.BatchModDescriber)
	if !ok {
		return out
	}
	described, err := describer.DescribeMods(ctx, game.SourceIDs[sourceID], ids, false)
	if err != nil {
		return out
	}
	for _, d := range described {
		if d.Unavailable {
			continue
		}
		out[d.ModID] = d.Mod
	}
	return out
}

// ApplyWorkshopCollectionImport executes a collection plan: it saves the
// profile and installs NOTHING.
//
// The forced NoInstall is the rule this unit is built around, and it lives
// here rather than in either frontend so both inherit it. Until Tier 3
// (#347) lands the download path, lmm cannot fetch a Workshop item it is
// not already subscribed to - so letting the ordinary install loop try
// would spend a round trip per item to produce a stack of "operation not
// supported by this source" failures, when the plan already knows the
// answer and has a sentence for it (WorkshopCollectionItem.Note).
//
// opts is otherwise honoured verbatim - Force in particular, which is what
// re-importing an updated collection over its own profile needs.
func (s *Service) ApplyWorkshopCollectionImport(ctx context.Context, game *domain.Game, plan *ImportPlan, opts ProfileImportOptions, sink EventSink) (*ProfileImportResult, error) {
	opts.Install = false
	opts.NoInstall = true

	result, err := s.ApplyImport(ctx, game, plan, opts, sink)
	if err != nil {
		return result, err
	}
	if c := plan.WorkshopCollection; c != nil && c.NotSubscribed > 0 {
		result.Notes = append(result.Notes,
			fmt.Sprintf("%d item(s) are not subscribed: %s", c.NotSubscribed, subscribeNote))
	}
	return result, nil
}

// collectionProfileName derives a profile name from a collection: its own
// title as a slug, or the collection id when it has no usable title.
//
// A profile name reaches the filesystem (config.validateProfilePath), so
// this keeps to lowercase letters, digits and single dashes rather than
// trusting a title a stranger on the internet chose.
func collectionProfileName(collection source.Collection) string {
	if slug := slugify(collection.Name); slug != "" {
		return slug
	}
	return slugify("workshop-collection-" + collection.ID)
}

// maxProfileSlug bounds a derived profile name. A title is chosen by a
// stranger on the internet and becomes a FILE NAME under
// $XDG_CONFIG_HOME; without a cap a 300-character title fails at the
// filesystem ("file name too long") instead of with lmm's own message, and
// a 4 KiB one would be a 4 KiB path component (W2 review, Minor 10). 64 is
// comfortably inside every filesystem's per-component limit once the
// ".yaml" suffix is added, and long enough that no reasonable title is
// truncated at all.
const maxProfileSlug = 64

// slugify lowercases text and replaces every run of characters that are not
// ASCII letters or digits with a single dash, trimming dashes at both ends.
// Returns "" for text with none at all - a collection titled entirely in a
// non-Latin script falls back to its id rather than to an empty name.
//
// Deliberately ASCII-only, not unicode.IsLetter: the result becomes a
// DIRECTORY name (config.validateProfilePath), and a title chosen by a
// stranger on the internet is not something to hand a filesystem verbatim.
func slugify(text string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(text) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	return capSlug(b.String())
}

// capSlug bounds slug at maxProfileSlug, trimming back to a dash boundary
// so the result ends on a whole word rather than mid-word - unless the cut
// already landed on one, or there is no dash to fall back to (one
// unbroken run of characters is simply cut).
func capSlug(slug string) string {
	if len(slug) <= maxProfileSlug {
		return slug
	}
	clean := slug[maxProfileSlug] == '-'
	slug = slug[:maxProfileSlug]
	if !clean {
		if i := strings.LastIndexByte(slug, '-'); i > 0 {
			slug = slug[:i]
		}
	}
	return strings.Trim(slug, "-")
}

// IsBadCollectionRef reports whether err is a collection import failing
// because of the CALLER's input rather than the server's state: a reference
// the source does not recognise, a collection Valve will not describe, a
// game with no Steam Workshop mapping, or a source that cannot resolve
// collections at all.
//
// It exists so a frontend can answer 400 without importing internal/source,
// which internal/serve's own boundary ratchet forbids - the same shape
// IsNoWorkshopSource already has.
func IsBadCollectionRef(err error) bool {
	return errors.Is(err, source.ErrInvalidReference) ||
		errors.Is(err, source.ErrNotSupported) ||
		IsNoWorkshopSource(err)
}

// stampRefDisplay fills in the two DISPLAY fields a bare
// domain.ModReference carries for a plan renderer's benefit (#365): whether
// the mod is EXTERNAL, and the revision timestamp that is shown in place of
// its unreadable version.
//
// Two sources of truth, in order. An installed ROW answers both exactly, so
// it wins. With no row - an imported profile naming a mod this machine has
// never seen, the exact case the two leaking renderers hit - the ref is
// still external if its SOURCE is the workshop-capable one, by the same
// capability test everything else in core uses (source.WorkshopScanner, so
// core never names the concrete package). That leaves no timestamp, which
// the renderers already handle: displayVersion falls back to an em dash,
// which says "no date recorded" rather than printing 19 digits.
//
// installedRows is keyed by domain.ModKey and may be nil.
func (s *Service) stampRefDisplay(refs []domain.ModReference, installedRows map[string]domain.InstalledMod) {
	if len(refs) == 0 {
		return
	}
	external := make(map[string]bool, 2)
	for i := range refs {
		ref := &refs[i]
		if im, ok := installedRows[domain.ModKey(ref.SourceID, ref.ModID)]; ok {
			ref.External = im.External
			if im.External {
				ref.UpdatedAt = im.UpdatedAt
			}
			continue
		}
		isExternal, known := external[ref.SourceID]
		if !known {
			isExternal = s.sourceIsWorkshop(ref.SourceID)
			external[ref.SourceID] = isExternal
		}
		ref.External = isExternal
	}
}

// sourceIsWorkshop reports whether sourceID's registered source is the
// workshop-capable one - the same source.WorkshopScanner test
// workshopSourceFor applies, asked about one source id rather than about a
// game. An unregistered source is not one.
func (s *Service) sourceIsWorkshop(sourceID string) bool {
	src, err := s.GetSource(sourceID)
	if err != nil {
		return false
	}
	_, ok := src.(source.WorkshopScanner)
	return ok
}
