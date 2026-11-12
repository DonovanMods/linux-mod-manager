// collection_export.go is `lmm profile export`'s version-1 writer (#621;
// design §12, spec docs/collection-format.md): a profile, as a shareable
// collection document.
//
// What it writes, by the owner's rulings of 2026-11-12: the profile's mods
// (each with its display name, for a recipient without the source), its
// hooks (as the version-0 export did) and overrides, its collection
// identity and origin, the portable game identity BESIDE the author's
// game_id (so an older lmm still imports the document by it), and the
// features the content requires. What it never writes: the profile's link
// method and its default flag, which are this machine's preferences.
package core

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// CollectionExportOptions is the author's metadata for one export. A field
// left zero keeps what the profile records (the identity of the collection
// it was imported from, or of its last export); ID is the stable
// collection id, Version the author's number for this publish.
type CollectionExportOptions struct {
	ID                  string
	Version             int
	Title               string
	Author              string
	Description         string
	InstallInstructions string
}

// CollectionExport is one exported collection: the version-1 YAML
// document, and the notes the export has for its author.
type CollectionExport struct {
	// Document is the version-1 YAML document - what `lmm profile export`
	// writes to stdout, and `-o <file> --plain` to a file.
	Document []byte
	// Notes are the export's one-time remarks for the author: that
	// republishing someone else's collection is their call, and that the
	// document carries hooks.
	Notes []string
}

// Container returns the export as a `.lmmc` container - what `-o <file>`
// writes by default, and the web UI downloads.
func (e *CollectionExport) Container() ([]byte, error) {
	return writeCollectionContainer(e.Document)
}

// ExportCollection exports gameID's profile profileName as a version-1
// collection document, from the profile, its installed rows (for file ids
// and display names) and the game.
//
// It writes once per profile at most (owner ruling, 2026-11-12): a profile
// with no collection id yet gets one - opts.ID, or a minted random UUID -
// recorded in its `collection:` block under the op lock with the
// text-preserving profile save, so every later export keeps the same id. A
// failure to record it fails the export: a document carrying an id the
// profile does not remember would start a second identity on the next
// export.
func (s *Service) ExportCollection(ctx context.Context, gameID, profileName string, opts CollectionExportOptions) (*CollectionExport, error) {
	game, err := s.GetGame(gameID)
	if err != nil {
		return nil, err
	}
	profile, err := s.NewProfileManager().loadForExport(ctx, gameID, profileName)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string)
	if rows, err := s.GetInstalledMods(ctx, gameID, profileName); err == nil {
		for _, row := range rows {
			names[domain.ModKey(row.SourceID, row.ID)] = row.Name
		}
	}

	if profile.Collection == nil || profile.Collection.ID == "" {
		id := opts.ID
		if id == "" {
			id = newCollectionID()
		}
		recorded, err := s.recordCollectionID(ctx, gameID, profileName, id)
		if err != nil {
			return nil, fmt.Errorf("recording the collection id in profile %s: %w", profileName, err)
		}
		if profile.Collection == nil {
			profile.Collection = &domain.CollectionInfo{}
		}
		profile.Collection.ID = recorded
	}
	info := collectionInfoForExport(profile.Collection, opts)
	doc := &domain.CollectionDocument{
		FormatVersion: domain.CollectionFormatVersion,
		Name:          profile.Name,
		GameID:        profile.GameID,
		Info:          info,
		Game:          s.portableGame(game),
		External:      profile.External,
		Hooks:         profile.Hooks,
		HooksExplicit: profile.HooksExplicit,
		Items:         make([]domain.CollectionItem, len(profile.Mods)),
	}
	for i, ref := range profile.Mods {
		doc.Items[i] = domain.CollectionItem{Ref: ref, Name: names[domain.ModKey(ref.SourceID, ref.ModID)]}
	}
	if len(profile.Overrides) > 0 {
		doc.Overrides = make(map[string]string, len(profile.Overrides))
		for path, content := range profile.Overrides {
			doc.Overrides[path] = string(content)
		}
	}
	doc.Requires = deriveCollectionRequires(doc)

	data, err := config.MarshalCollection(doc)
	if err != nil {
		return nil, err
	}
	return &CollectionExport{Document: data, Notes: exportNotes(doc)}, nil
}

// recordCollectionID records id as gameID/profileName's collection id,
// under the op lock, and returns the id the profile then holds: one another
// export recorded first wins, so two concurrent first exports agree.
func (s *Service) recordCollectionID(ctx context.Context, gameID, profileName, id string) (string, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	pm := s.NewProfileManager()
	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return "", err
	}
	if profile.Collection != nil && profile.Collection.ID != "" {
		return profile.Collection.ID, nil
	}
	if profile.Collection == nil {
		profile.Collection = &domain.CollectionInfo{}
	}
	profile.Collection.ID = id
	if err := pm.save(profile); err != nil {
		return "", err
	}
	return id, nil
}

// collectionInfoForExport is the identity an export writes: what the
// profile records, with each metadata field opts sets taking its place.
// Version starts at 1.
func collectionInfoForExport(stored *domain.CollectionInfo, opts CollectionExportOptions) domain.CollectionInfo {
	var info domain.CollectionInfo
	if stored != nil {
		info = *stored
		if stored.Origin != nil {
			origin := *stored.Origin
			info.Origin = &origin
		}
	}
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&info.ID, opts.ID)
	set(&info.Title, opts.Title)
	set(&info.Author, opts.Author)
	set(&info.Description, opts.Description)
	set(&info.InstallInstructions, opts.InstallInstructions)
	if opts.Version > 0 {
		info.Version = opts.Version
	}
	if info.Version < 1 {
		info.Version = 1
	}
	return info
}

// portableGame is game's portable identity: its name, its Steam app id
// when lmm knows it, and its id on every source it maps.
func (s *Service) portableGame(game *domain.Game) domain.CollectionGame {
	portable := domain.CollectionGame{Name: game.Name, SteamAppID: s.steamAppIDOf(game)}
	if len(game.SourceIDs) > 0 {
		portable.Sources = make(map[string]string, len(game.SourceIDs))
		for sourceID, id := range game.SourceIDs {
			if id != "" {
				portable.Sources[sourceID] = id
			}
		}
	}
	return portable
}

// steamAppIDOf is game's Steam app id, found the way a launch finds it:
// launch.steam_app_id, else the steamworkshop source's numeric mapping,
// else the Steam library scan (when the composition root registered one).
// Empty when none says.
func (s *Service) steamAppIDOf(game *domain.Game) string {
	if l := s.launcher(); l != nil {
		return l.steamApp(game).AppID
	}
	return (&Launcher{}).steamApp(game).AppID
}

// exportNotes is what an export has to tell its author, once.
func exportNotes(doc *domain.CollectionDocument) []string {
	var notes []string
	if origin := doc.Info.Origin; origin != nil && !origin.IsZero() {
		by := origin.Author
		if by == "" {
			by = "its curator"
		}
		where := origin.URL
		if where == "" && origin.Slug != "" {
			where = origin.SourceID + " collection " + origin.Slug
		}
		note := "This profile came from a collection curated by " + by
		if where != "" {
			note += " (" + where + ")"
		}
		notes = append(notes, note+". The export keeps that credit in its origin; whether to republish someone else's collection is your call.")
	}
	if n := countHooks(doc.Hooks, doc.HooksExplicit); n > 0 {
		notes = append(notes, fmt.Sprintf("The document carries this profile's %d hook(s). A recipient imports them only from a local file, and only with --with-hooks.", n))
	}
	return notes
}

// countHooks counts the explicitly set hooks of a profile - each is a
// script lmm would run.
func countHooks(hooks domain.GameHooks, explicit domain.GameHooksExplicit) int {
	return len(listHooks(hooks, explicit))
}

// newCollectionID mints a collection id: a random (version 4) UUID.
func newCollectionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
