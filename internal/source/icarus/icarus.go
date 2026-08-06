package icarus

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// gameID is fixed: the Firestore database this source reads is Icarus-only.
const gameID = "icarus"

// Icarus is a ModSource backed by the public, unauthenticated Firestore REST
// API described in docs/plans/2026-07-29-icarus-exmod-pak-research.md.
type Icarus struct {
	firestore *firestoreClient
}

// New constructs an Icarus source. projectID is the Firestore project ID
// (from the Firebase console) — passed explicitly rather than hard-coded so
// tests can point at an httptest server and so the real value lives in one
// place at the call site (Task 9), not buried in this package.
func New(httpClient *http.Client, projectID string) *Icarus {
	return &Icarus{firestore: newFirestoreClient(projectID, httpClient)}
}

var (
	_ source.ModSource          = (*Icarus)(nil)
	_ source.CapabilityReporter = (*Icarus)(nil)
	_ source.MergeCompiler      = (*Icarus)(nil)
)

// ValidateSource implements source.MergeCompiler by delegating to the
// package-level ValidateSource function.
func (s *Icarus) ValidateSource(sourceFilePath string) error {
	return ValidateSource(sourceFilePath)
}

// MergeCompile implements source.MergeCompiler by delegating to the
// package-level MergeCompile function. ctx is unused: merging is pure local
// file I/O against the installed game's own pak (#175/#197), with nothing
// to cancel.
func (s *Icarus) MergeCompile(ctx context.Context, basePakPath string, sources []MergeSource, outputPakPath string) ([]string, []source.MergeFailure, error) {
	return MergeCompile(ctx, basePakPath, sources, outputPakPath)
}

func (s *Icarus) ID() string   { return "icarus" }
func (s *Icarus) Name() string { return "Icarus (Project Daedalus)" }

// AuthURL/ExchangeToken: unsupported — Firestore reads here are public.
func (s *Icarus) AuthURL() string { return "" }
func (s *Icarus) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("source %q: authentication: %w", s.ID(), source.ErrNotSupported)
}

// GetDependencies: the modinfo.json v2 schema has no dependency field.
func (s *Icarus) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, fmt.Errorf("source %q: dependencies: %w", s.ID(), source.ErrNotSupported)
}

func (s *Icarus) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Dependencies: false, Updates: true, Auth: false}
}

func (s *Icarus) TypeLabel() string { return "built-in" }

// Search fetches the whole mods collection and filters client-side — this
// catalog has no server-side query support to speak of, matching
// project_daedalus's own ModsController#find_mods approach.
func (s *Icarus) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	docs, err := s.firestore.listCollection(ctx, "mods")
	if err != nil {
		return source.SearchResult{}, fmt.Errorf("source %q: searching: %w", s.ID(), err)
	}

	var mods []domain.Mod
	q := strings.ToLower(query.Query)
	for _, d := range docs {
		m := mapDoc(d)
		if q == "" || strings.Contains(strings.ToLower(m.Name), q) ||
			strings.Contains(strings.ToLower(m.Author), q) ||
			strings.Contains(strings.ToLower(m.Description), q) {
			mods = append(mods, m)
		}
	}

	// Firestore's listCollection order is not guaranteed stable across runs,
	// so the same page could otherwise return different mods on different
	// requests. Sort deterministically before slicing, matching the custom
	// api/manifest/directory sources' name-based ordering convention
	// (internal/source/custom/search.go), with an ID tiebreak for the rare
	// case of two mods sharing a name.
	sort.SliceStable(mods, func(i, j int) bool {
		if mods[i].Name != mods[j].Name {
			return mods[i].Name < mods[j].Name
		}
		return mods[i].ID < mods[j].ID
	})

	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	page := query.Page
	if page < 0 {
		page = 0
	}
	// Both page*pageSize (below) and start+pageSize (for end, further down)
	// can overflow int for a huge user-supplied Page or PageSize, wrapping
	// negative instead of landing somewhere past len(mods) — the pre-fix
	// "start > len(mods)"/"end > len(mods)" clamps never catch a NEGATIVE
	// value, so a wrapped result panicked the slice below instead of just
	// clamping to an empty page. Both operations are guarded before they
	// run rather than trusted to land in range; pageSize > 0 is guaranteed
	// by the clamp above, so both divisions here are always safe.
	start := len(mods)
	if page <= math.MaxInt/pageSize {
		start = page * pageSize
		if start > len(mods) {
			start = len(mods)
		}
	}
	end := len(mods)
	if pageSize <= math.MaxInt-start {
		end = start + pageSize
		if end > len(mods) {
			end = len(mods)
		}
	}

	return source.SearchResult{Mods: mods[start:end], TotalCount: len(mods), Page: page, PageSize: pageSize}, nil
}

func (s *Icarus) GetMod(ctx context.Context, queryGameID, modID string) (*domain.Mod, error) {
	doc, err := s.firestore.getDocument(ctx, "mods", modID)
	if err != nil {
		return nil, fmt.Errorf("source %q: fetching mod %s: %w", s.ID(), modID, err)
	}
	m := mapDoc(*doc)
	return &m, nil
}

// GetModFiles returns the mod's downloadable files (pak and/or exmodz — see
// modinfo.json v2 schema). When exactly one file exists, it is marked primary.
// When both variants are published, exmodz is marked primary (#211) — the pak
// remains explicitly selectable. All returned files have a Description set:
// "mergeable EXMOD - recommended" for exmodz, "prebuilt PAK" for pak.
func (s *Icarus) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	doc, err := s.firestore.getDocument(ctx, "mods", mod.ID)
	if err != nil {
		return nil, fmt.Errorf("source %q: listing files for %s: %w", s.ID(), mod.ID, err)
	}
	filesField, _ := doc.Fields["files"].(map[string]any)
	var out []domain.DownloadableFile
	for _, kind := range []string{"pak", "exmodz"} {
		rawURL, ok := filesField[kind].(string)
		if !ok || rawURL == "" {
			continue
		}
		description := "prebuilt PAK"
		if kind == "exmodz" {
			description = "mergeable EXMOD - recommended"
		}
		out = append(out, domain.DownloadableFile{
			ID:          kind,
			Name:        kind,
			FileName:    fileNameFromURL(rawURL, kind),
			Category:    strings.ToUpper(kind),
			Description: description,
		})
	}
	if len(out) == 1 {
		out[0].IsPrimary = true
	} else {
		// #211: with both variants published, the mergeable exmodz is the
		// default everywhere IsPrimary is honored (CLI default mark, --yes,
		// TUI plan, batch, profile apply, selectDeployFiles) - the prebuilt
		// pak stays explicitly selectable.
		for i := range out {
			if out[i].ID == "exmodz" {
				out[i].IsPrimary = true
			}
		}
	}
	return out, nil
}

// GetDownloadURL re-fetches the mod document and returns the stored URL for
// fileID ("pak" or "exmodz") directly — no signing, matching a static-URL
// catalog rather than an OAuth-gated one.
func (s *Icarus) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	doc, err := s.firestore.getDocument(ctx, "mods", mod.ID)
	if err != nil {
		return "", fmt.Errorf("source %q: download URL for %s: %w", s.ID(), fileID, err)
	}
	filesField, _ := doc.Fields["files"].(map[string]any)
	rawURL, ok := filesField[fileID].(string)
	if !ok || rawURL == "" {
		return "", fmt.Errorf("source %q: file %s: no download URL", s.ID(), fileID)
	}
	return rawURL, nil
}

// CheckUpdates compares each installed mod's stored version against the
// catalog's current version string (semantic-ish, per modinfo.json's
// "recommended" versioning note — not guaranteed strictly semver, so this
// uses domain.IsNewerVersion the same way custom.API does).
func (s *Icarus) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var updates []domain.Update
	var errs []error
	for _, inst := range installed {
		select {
		case <-ctx.Done():
			return updates, ctx.Err()
		default:
		}
		current, err := s.GetMod(ctx, gameID, inst.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if domain.IsNewerVersion(inst.Version, current.Version) {
			updates = append(updates, domain.Update{InstalledMod: inst, NewVersion: current.Version})
		}
	}
	if len(errs) > 0 {
		return updates, fmt.Errorf("source %q: %d update check(s) failed: %v", s.ID(), len(errs), errs[0])
	}
	return updates, nil
}

// mapDoc converts a decoded Firestore document into domain.Mod per the
// modinfo.json v2 schema (docs/plans/2026-07-29-icarus-exmod-pak-research.md).
func mapDoc(d firestoreDoc) domain.Mod {
	str := func(key string) string {
		s, _ := d.Fields[key].(string)
		return s
	}
	return domain.Mod{
		ID:          d.ID,
		SourceID:    "icarus",
		GameID:      gameID,
		Name:        str("name"),
		Author:      str("author"),
		Version:     str("version"),
		Category:    str("compatibility"), // Icarus week-build string, e.g. "w57"
		Description: str("description"),
		PictureURL:  str("imageURL"),
		SourceURL:   str("readmeURL"),
	}
}

// fileNameFromURL derives a download's file name from its URL, falling back
// to a synthesized "mod.<fallbackExt>" name (never a bare, dot-less
// fallbackExt) when the URL yields nothing usable. A dot-less fallback would
// silently defeat isExmodzFile's case-insensitive ".exmodz" suffix check —
// a downloaded file named e.g. "exmodz" would never route through the
// DeployCompile ingest branch (validate+retain, #197). A parsed basename
// that exists but carries no extension of its own gets fallbackExt
// appended rather than being discarded outright, preserving whatever real
// name the URL offered.
func fileNameFromURL(rawURL, fallbackExt string) string {
	fallback := "mod." + fallbackExt
	u, err := url.Parse(rawURL)
	if err != nil || u.Path == "" {
		return fallback
	}
	base := path.Base(u.Path)
	if base == "." || base == "/" || base == ".." {
		return fallback
	}
	if path.Ext(base) == "" {
		return base + "." + fallbackExt
	}
	return base
}
