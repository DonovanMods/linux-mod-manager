package custom

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom/metadata"
)

// Directory is a ModSource backed by a local directory: each subdirectory (or
// .zip/.jar archive) is one mod. The directory is rescanned on each operation
// so edits show up without restarting lmm; scans are local and cheap.
type Directory struct {
	id   string
	name string
	path string // absolute, verified at construction
}

// NewDirectory constructs a directory source from a validated definition.
// The configured path must exist and be a directory.
func NewDirectory(def SourceDefinition) (*Directory, error) {
	path := def.Directory.Path
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("expanding %q: %w", path, err)
		}
		path = filepath.Join(home, path[2:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("directory source path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("directory source path %s is not a directory", abs)
	}

	return &Directory{id: def.ID, name: def.Name, path: abs}, nil
}

// ID implements source.ModSource.
func (d *Directory) ID() string { return d.id }

// Name implements source.ModSource.
func (d *Directory) Name() string { return d.name }

// AuthURL implements source.ModSource; directory sources need no auth.
func (d *Directory) AuthURL() string { return "" }

// ExchangeToken implements source.ModSource.
func (d *Directory) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("source %q: authentication: %w", d.id, source.ErrNotSupported)
}

// Capabilities implements source.CapabilityReporter.
func (d *Directory) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Updates: true}
}

// dirMod pairs a scanned mod with its filesystem location.
type dirMod struct {
	mod       domain.Mod
	path      string // absolute path to the mod directory or archive
	isArchive bool
	size      int64 // archive size in bytes; 0 for directories
}

// scan reads the source directory. Subdirectories are directory mods;
// .zip/.jar files are archive mods; everything else is ignored.
func (d *Directory) scan() ([]dirMod, error) {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return nil, fmt.Errorf("source %q: scanning %s: %w", d.id, d.path, err)
	}

	var mods []dirMod
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue // hidden entries (.git, .DS_Store, dotfiles, ...) are never mods
		}
		entryPath := filepath.Join(d.path, entry.Name())

		if entry.IsDir() {
			mods = append(mods, d.scanDir(entry.Name(), entryPath))
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".zip" && ext != ".jar" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("source %q: stat %s: %w", d.id, entryPath, err)
		}
		base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		mod := domain.Mod{ID: base, SourceID: d.id}
		if meta := metadata.ResolveArchive(entryPath); meta != nil {
			applyMetadata(&mod, meta)
		} else {
			mod.Name, mod.Version = nameAndVersionFrom(base)
		}
		mods = append(mods, dirMod{
			mod:       mod,
			path:      entryPath,
			isArchive: true,
			size:      info.Size(),
		})
	}

	return mods, nil
}

// scanDir builds a dirMod for a mod directory, preferring well-known metadata
// files over dirname parsing.
func (d *Directory) scanDir(dirName, dirPath string) dirMod {
	mod := domain.Mod{ID: dirName, SourceID: d.id}

	if info := metadata.Resolve(dirPath); info != nil {
		applyMetadata(&mod, info)
	} else {
		mod.Name, mod.Version = nameAndVersionFrom(dirName)
	}

	return dirMod{mod: mod, path: dirPath}
}

// applyMetadata copies well-known metadata fields onto mod, used by both
// directory mods (metadata.Resolve) and archive mods (metadata.ResolveArchive)
// so they share the same precedence: DisplayName wins, falling back to Name,
// and metadata always wins over any filename-derived guess.
func applyMetadata(mod *domain.Mod, info *metadata.Info) {
	mod.Name = info.DisplayName
	if mod.Name == "" {
		mod.Name = info.Name
	}
	mod.Version = info.Version
	mod.Summary = info.Summary
	mod.Description = info.Summary
	mod.Author = info.Author
}

// nameAndVersionFrom splits a directory/file base name into a display name and
// version ("PlainMod-0.5" -> "PlainMod", "0.5").
func nameAndVersionFrom(base string) (string, string) {
	version := domain.ExtractVersionFromName(base)
	name := base
	if version != "" {
		if idx := strings.LastIndex(base, version); idx > 0 {
			name = strings.TrimRight(base[:idx], "-_ vV")
		}
	}
	return name, version
}

// Search implements source.ModSource with client-side matching: case-insensitive
// substring on name and summary; name matches rank first, then alphabetical.
// GameID is accepted from any game (a directory source applies to any game that
// maps it — it does not filter by GameID) but is echoed onto every returned mod
// so downstream installs are attributed to the correct game.
func (d *Directory) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	scanned, err := d.scan()
	if err != nil {
		return source.SearchResult{}, err
	}

	q := strings.ToLower(query.Query)
	type ranked struct {
		mod       domain.Mod
		nameMatch bool
	}
	var matches []ranked
	for _, dm := range scanned {
		nameMatch := q == "" || strings.Contains(strings.ToLower(dm.mod.Name), q) || strings.Contains(strings.ToLower(dm.mod.ID), q)
		summaryMatch := strings.Contains(strings.ToLower(dm.mod.Summary), q)
		if !nameMatch && !summaryMatch {
			continue
		}
		matches = append(matches, ranked{mod: dm.mod, nameMatch: nameMatch})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].nameMatch != matches[j].nameMatch {
			return matches[i].nameMatch
		}
		return matches[i].mod.Name < matches[j].mod.Name
	})

	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	start := query.Page * pageSize
	if start < 0 {
		start = 0
	}
	end := min(start+pageSize, len(matches))
	if start > len(matches) {
		start = len(matches)
	}

	mods := make([]domain.Mod, 0, end-start)
	for _, m := range matches[start:end] {
		mod := m.mod
		mod.GameID = query.GameID
		mods = append(mods, mod)
	}

	return source.SearchResult{
		Mods:       mods,
		TotalCount: len(matches),
		Page:       query.Page,
		PageSize:   pageSize,
	}, nil
}

// GetMod implements source.ModSource. gameID is accepted from any game and not
// used to look up the mod (see Search); it is echoed onto the returned mod so
// downstream installs are attributed to the correct game.
func (d *Directory) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	dm, err := d.find(modID)
	if err != nil {
		return nil, err
	}
	mod := dm.mod
	mod.GameID = gameID
	return &mod, nil
}

// GetDependencies implements source.ModSource; directory mods declare none.
func (d *Directory) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, fmt.Errorf("source %q: dependencies: %w", d.id, source.ErrNotSupported)
}

// GetModFiles implements source.ModSource: every mod has exactly one synthetic
// file ("main") representing its directory or archive.
func (d *Directory) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	dm, err := d.find(mod.ID)
	if err != nil {
		return nil, err
	}
	return []domain.DownloadableFile{{
		ID:        "main",
		Name:      dm.mod.Name,
		FileName:  filepath.Base(dm.path),
		Version:   dm.mod.Version,
		Size:      dm.size,
		IsPrimary: true,
	}}, nil
}

// GetDownloadURL implements source.ModSource, returning a file:// URL that
// Service.DownloadModToCache ingests by local copy instead of HTTP download.
func (d *Directory) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	dm, err := d.find(mod.ID)
	if err != nil {
		return "", err
	}
	return "file://" + dm.path, nil
}

// CheckUpdates implements source.ModSource by comparing installed versions to
// the current scan.
func (d *Directory) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	scanned, err := d.scan()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]domain.Mod, len(scanned))
	for _, dm := range scanned {
		byID[dm.mod.ID] = dm.mod
	}

	var updates []domain.Update
	for _, inst := range installed {
		select {
		case <-ctx.Done():
			return updates, ctx.Err()
		default:
		}
		current, ok := byID[inst.ID]
		if !ok {
			continue // mod removed from the directory; nothing to offer
		}
		if domain.IsNewerVersion(inst.Version, current.Version) {
			updates = append(updates, domain.Update{
				InstalledMod: inst,
				NewVersion:   current.Version,
			})
		}
	}
	return updates, nil
}

// find scans and returns the mod with the given ID.
func (d *Directory) find(modID string) (dirMod, error) {
	scanned, err := d.scan()
	if err != nil {
		return dirMod{}, err
	}
	for _, dm := range scanned {
		if dm.mod.ID == modID {
			return dm, nil
		}
	}
	return dirMod{}, fmt.Errorf("source %q: mod not found: %s", d.id, modID)
}
