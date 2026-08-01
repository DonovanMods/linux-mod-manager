package icarus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// defaultDumpTreeURL is the community per-week unpack of Icarus's data.pak:
// https://github.com/GODOFMINECRAFT4/IcarusData. The tree is committed as
// loose JSON at the repo root, one commit per game week, with the week
// recorded only in the commit message — there are no tags or releases. This
// URL is HEAD; a specific week is addressed by substituting its commit SHA.
const defaultDumpTreeURL = "https://codeload.github.com/GODOFMINECRAFT4/IcarusData/tar.gz/refs/heads/master"

// maxDumpBytes caps the download. The real tree is ~36 MB; this leaves room to
// grow while refusing to stream an unbounded body into memory.
const maxDumpBytes = 256 << 20

// Build identifies the installed game, read from Icarus/Config/version.json.
// Note this carries no week number — nothing in the install does. Week
// agreement is established by content comparison, not by this value.
type Build struct {
	Major, Minor, Patch int
	Changelist          int
	DataChangelist      int
	FeatureLevel        string
}

func (b Build) String() string {
	return fmt.Sprintf("%d.%d.%d.%d", b.Major, b.Minor, b.Patch, b.Changelist)
}

// detectBuild reads <installRoot>/Icarus/Config/version.json.
func detectBuild(installRoot string) (Build, error) {
	p := filepath.Join(installRoot, "Icarus", "Config", "version.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return Build{}, fmt.Errorf("icarus: reading game version from %s: %w", p, err)
	}
	var doc struct {
		Version struct {
			Major, Minor, Patch int
			Changelist          int
			FeatureLevel        string
		}
		Data struct{ Changelist int }
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Build{}, fmt.Errorf("icarus: parsing %s: %w", p, err)
	}
	return Build{
		Major: doc.Version.Major, Minor: doc.Version.Minor, Patch: doc.Version.Patch,
		Changelist:     doc.Version.Changelist,
		DataChangelist: doc.Data.Changelist,
		FeatureLevel:   doc.Version.FeatureLevel,
	}, nil
}

// Dump is a fetched set of base data tables, keyed by mount-relative path
// (e.g. "Factions/D_Factions.json") with values already converted back to the
// game's CRLF line endings.
type Dump struct {
	tables map[string][]byte
}

// Table returns one table's shipped bytes.
func (d *Dump) Table(rel string) ([]byte, bool) {
	b, ok := d.tables[rel]
	return b, ok
}

// DumpStore fetches and caches base-table dumps.
type DumpStore struct {
	cacheDir   string
	httpClient *http.Client
	treeURL    string // overridable in tests
}

func newDumpStore(cacheDir string, httpClient *http.Client) *DumpStore {
	return &DumpStore{cacheDir: cacheDir, httpClient: httpClient, treeURL: defaultDumpTreeURL}
}

// DumpForBuild loads the base data tables and returns them only if they match
// the installed game, proven by byte-comparing every table basePakPath stores
// uncompressed. A mismatch means the tables are for a different game week:
// that is a hard error naming the offending tables, never a silent
// best-effort.
//
// localDumpDir, when non-empty, is a user-supplied directory holding an
// unpacked data.pak JSON tree (QuickBMS output and the like); it replaces the
// network fetch entirely. Validation is the same either way — a local
// directory from the wrong week is rejected exactly like a stale hosted dump.
func (s *DumpStore) DumpForBuild(ctx context.Context, basePakPath, localDumpDir string) (*Dump, error) {
	var (
		dump *Dump
		err  error
	)
	if localDumpDir != "" {
		dump, err = loadLocalDump(localDumpDir)
	} else {
		dump, err = s.fetchTree(ctx, s.treeURL)
	}
	if err != nil {
		return nil, err
	}
	if err := validateDump(dump, basePakPath); err != nil {
		if localDumpDir != "" {
			return nil, fmt.Errorf("%w (tables were read from the configured data_dump_path %s)", err, localDumpDir)
		}
		return nil, err
	}
	return dump, nil
}

// loadLocalDump reads an unpacked data.pak JSON tree from disk. The layout is
// the same one the hosted dump ships — table paths relative to the directory
// root, e.g. "Factions/D_Factions.json" — so a user can point this at QuickBMS
// output without rearranging anything.
func loadLocalDump(dir string) (*Dump, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("icarus: reading the configured data_dump_path %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("icarus: the configured data_dump_path %s is not a directory", dir)
	}

	dump := &Dump{tables: make(map[string][]byte)}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dump.tables[filepath.ToSlash(rel)] = toCRLF(body)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("icarus: scanning the configured data_dump_path %s: %w", dir, err)
	}
	if len(dump.tables) == 0 {
		return nil, fmt.Errorf("icarus: the configured data_dump_path %s contains no JSON tables "+
			"(expected an unpacked data.pak tree, e.g. Factions/D_Factions.json)", dir)
	}
	return dump, nil
}

// fetchTree downloads a dump tarball and ingests its JSON tables, restoring
// the CRLF line endings the game ships (the repo stores LF).
func (s *DumpStore) fetchTree(ctx context.Context, url string) (*Dump, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("icarus: building dump request: %w", err)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("icarus: fetching base-table dump: %w "+
			"(compiling Icarus mods requires network access — see the plan's Global Constraints)", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("icarus: fetching base-table dump from %s: HTTP %d", url, resp.StatusCode)
	}

	zr, err := gzip.NewReader(io.LimitReader(resp.Body, maxDumpBytes))
	if err != nil {
		return nil, fmt.Errorf("icarus: base-table dump is not valid gzip: %w", err)
	}
	defer zr.Close() //nolint:errcheck

	dump := &Dump{tables: make(map[string][]byte)}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("icarus: reading base-table dump: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, ".json") {
			continue
		}
		// Strip the archive's single top-level directory (e.g.
		// "IcarusData-<sha>/") to get the mount-relative table path.
		rel := hdr.Name
		if i := strings.Index(rel, "/"); i >= 0 {
			rel = rel[i+1:]
		}
		// The repo also carries a stale "data/" copy of the tree; the
		// authoritative tables are the root-level ones.
		if rel == "" || strings.HasPrefix(rel, "data/") {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("icarus: reading %s from base-table dump: %w", rel, err)
		}
		dump.tables[path.Clean(rel)] = toCRLF(body)
	}
	if len(dump.tables) == 0 {
		return nil, fmt.Errorf("icarus: base-table dump from %s contained no JSON tables", url)
	}
	return dump, nil
}

// toCRLF restores the game's line endings. The dump repo stores LF (committed
// with autocrlf); the shipped pak stores CRLF, and the two are otherwise
// byte-identical. Existing CRLFs are left alone so the conversion is
// idempotent.
func toCRLF(b []byte) []byte {
	return []byte(strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n", "\r\n"))
}

// validateDump proves a dump belongs to the installed game.
//
// Only the tables data.pak stores *uncompressed* can be checked — the rest are
// Oodle-compressed and unreadable here, which is the whole reason the dump
// exists. That is enough: a dump built from a different week's data.pak
// disagrees on some of them, and in practice it disagrees loudly (the spike saw
// 3 differing stored tables and 6 missing tables across a 7-week gap).
func validateDump(dump *Dump, basePakPath string) error {
	pak, err := unrealpak.Open(basePakPath)
	if err != nil {
		return fmt.Errorf("icarus: opening base pak %s for dump validation: %w", basePakPath, err)
	}
	defer pak.Close() //nolint:errcheck

	var missing, differing []string
	checked := 0
	for _, f := range pak.Files() {
		shipped, err := pak.ReadFile(f.Path)
		if err != nil {
			if errors.Is(err, unrealpak.ErrUnsupportedFormat) {
				continue // Oodle-compressed (or similar): not readable here, and not our gate
			}
			// Any other ReadFile failure — corruption, a truncated payload, an
			// I/O error — is not an expected skip. Silently excluding it here
			// would quietly narrow what this gate actually verified, exactly
			// the "no silent fallbacks" failure this function exists to prevent.
			return fmt.Errorf("icarus: validating base pak %s: reading %s: %w", basePakPath, f.Path, err)
		}
		checked++
		got, ok := dump.Table(f.Path)
		if !ok {
			missing = append(missing, f.Path)
			continue
		}
		if !bytes.Equal(got, shipped) {
			differing = append(differing, f.Path)
		}
	}
	if checked == 0 {
		return fmt.Errorf("icarus: %s exposed no uncompressed tables to validate the dump against", basePakPath)
	}
	if len(missing) == 0 && len(differing) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(differing)
	return fmt.Errorf(
		"icarus: the available base-table dump does not match the installed game "+
			"(%d/%d uncompressed tables disagree: %s). The dump is for a different game week. "+
			"Wait for the dump to be updated for your game version, or roll the game back to a "+
			"matching week; compiling against a mismatched week would silently corrupt mod data",
		len(missing)+len(differing), checked, summarize(append(differing, missing...)))
}

func summarize(paths []string) string {
	const max = 3
	if len(paths) <= max {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:max], ", "), len(paths)-max)
}
