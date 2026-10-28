// Package notices builds THIRD_PARTY_NOTICES, the file that carries every
// shipped third-party license text beside lmm's own LICENSE (#522).
//
// MIT, BSD and Apache-2.0 all require their notice to accompany a binary
// distribution, and lmm's binary compiles in Go modules and embeds a few
// vendored browser libraries. The set is therefore derived, never typed:
// the Go modules from `go list -deps ./cmd/lmm` (what actually links into
// the shipped binary, so test-only dependencies are out of scope by
// construction), each module's license files read from the module cache,
// and the vendored SPA packages from the small table in vendored.go.
//
// The generator is dev-time only. It lives behind `make notices`
// (tools/notices), imports nothing outside the standard library, and is
// never linked into lmm. A drift test regenerates the file in memory and
// compares it with the checked-in copy, so a dependency bump that changes
// the set fails until `make notices` is run. A module whose license file
// is missing or unrecognisable fails the generator; nothing is skipped
// silently.
package notices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// FileName is the generated file's name, at the repository root.
const FileName = "THIRD_PARTY_NOTICES"

// Module is one Go module compiled into lmm.
type Module struct {
	// Path is the module path, e.g. "github.com/spf13/cobra".
	Path string
	// Version is the resolved module version, e.g. "v1.10.2".
	Version string
	// Dir is the module's directory in the module cache.
	Dir string
}

// File is one license-bearing file reproduced in the notices.
type File struct {
	// Name is the file's base name, e.g. "LICENSE" or "NOTICE".
	Name string
	// Text is the file's content, normalised by normalizeText.
	Text string
}

// Entry is one third-party component in the notices.
type Entry struct {
	// Name is the component's display name (a Go module path or a package).
	Name string
	// Version is the component's version.
	Version string
	// SPDX is the SPDX license expression for the component.
	SPDX string
	// Source is a URL for the component's source.
	Source string
	// Files are the license texts, in the order they are reproduced.
	Files []File
}

// goListPackage is the slice of `go list -json` output ShippedModules reads.
type goListPackage struct {
	ImportPath string
	Standard   bool
	Module     *struct {
		Path    string
		Version string
		Main    bool
		Dir     string
		Replace *struct {
			Path    string
			Version string
			Dir     string
		}
	}
}

// ShippedModules returns the third-party Go modules that link into
// ./cmd/lmm, sorted by path. It runs `go list -deps` from repoRoot for the
// release target (linux, CGO off, the same environment .goreleaser.yaml
// builds with) and with the network off: it reads only what `go mod
// download` already put in the module cache, so it works in offline CI.
func ShippedModules(ctx context.Context, repoRoot string) ([]Module, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-json=ImportPath,Standard,Module", "./cmd/lmm")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0",
		"GOFLAGS=-mod=readonly", "GOPROXY=off",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps ./cmd/lmm: %w\n%s\n(a module missing from the cache? run `go mod download`)", err, strings.TrimSpace(stderr.String()))
	}
	return parseModules(out)
}

// parseModules reads the concatenated JSON objects `go list -json` prints
// and returns the distinct non-main, non-standard modules, sorted by path.
func parseModules(listJSON []byte) ([]Module, error) {
	byPath := map[string]Module{}
	dec := json.NewDecoder(bytes.NewReader(listJSON))
	for {
		var p goListPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decoding go list output: %w", err)
		}
		if p.Standard || p.Module == nil || p.Module.Main {
			continue
		}
		dir := p.Module.Dir
		if r := p.Module.Replace; r != nil && r.Dir != "" {
			dir = r.Dir
		}
		if dir == "" {
			return nil, fmt.Errorf("module %s@%s has no directory in the module cache (run `go mod download`)", p.Module.Path, p.Module.Version)
		}
		byPath[p.Module.Path] = Module{Path: p.Module.Path, Version: p.Module.Version, Dir: dir}
	}
	mods := make([]Module, 0, len(byPath))
	for _, m := range byPath {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

// licenseFilePrefixes are the upper-cased base-name prefixes a module's
// license-bearing top-level files start with. Every such file is reproduced.
var licenseFilePrefixes = []string{"LICENSE", "LICENCE", "COPYING", "UNLICENSE", "NOTICE", "PATENTS"}

// supplementaryPrefixes mark files that accompany a license rather than
// being one (Apache NOTICE files, the Go PATENTS grant). They are
// reproduced but need no SPDX detection of their own.
var supplementaryPrefixes = []string{"NOTICE", "PATENTS"}

// skippedFiles lists license-named files that are deliberately NOT
// reproduced, each with the reason. Adding to this list is a reviewed
// decision; the generator never skips a file on its own.
var skippedFiles = map[string]string{
	"modernc.org/memory/LICENSE-LOGO": "licenses the project's logo image (a Wikimedia Commons URL), which is not part of the compiled module",
}

// spdxOverrides replaces the detected SPDX expression for a module whose
// license files cannot be classified mechanically, each with the reason.
var spdxOverrides = map[string]string{
	"modernc.org/libc": "BSD-2-Clause AND BSD-3-Clause AND MIT AND LicenseRef-PublicDomain",
}

// hasPrefixAny reports whether name starts with any of prefixes.
func hasPrefixAny(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// CollectModule reads a module's license files into an Entry. A module with
// no license file, or whose license text no detection rule recognises, is an
// error: the file would otherwise ship incomplete without anyone noticing.
func CollectModule(m Module) (Entry, error) {
	dirEntries, err := os.ReadDir(m.Dir)
	if err != nil {
		return Entry{}, fmt.Errorf("module %s@%s: %w", m.Path, m.Version, err)
	}
	var files []File
	ids := map[string]bool{}
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		upper := strings.ToUpper(de.Name())
		if !hasPrefixAny(upper, licenseFilePrefixes) {
			continue
		}
		if _, skip := skippedFiles[m.Path+"/"+de.Name()]; skip {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(m.Dir, de.Name()))
		if err != nil {
			return Entry{}, fmt.Errorf("module %s@%s: %w", m.Path, m.Version, err)
		}
		text := normalizeText(string(raw))
		if text == "" {
			return Entry{}, fmt.Errorf("module %s@%s: %s is empty", m.Path, m.Version, de.Name())
		}
		if !hasPrefixAny(upper, supplementaryPrefixes) {
			detected := DetectSPDX(text)
			if len(detected) == 0 {
				return Entry{}, fmt.Errorf("module %s@%s: no known license in %s; extend DetectSPDX or spdxOverrides", m.Path, m.Version, de.Name())
			}
			for _, id := range detected {
				ids[id] = true
			}
		}
		files = append(files, File{Name: de.Name(), Text: text})
	}
	if len(ids) == 0 {
		return Entry{}, fmt.Errorf("module %s@%s: no license file found in %s; refusing to ship it without a notice", m.Path, m.Version, m.Dir)
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	spdx, ok := spdxOverrides[m.Path]
	if !ok {
		spdx = joinIDs(ids)
	}
	return Entry{
		Name:    m.Path,
		Version: m.Version,
		SPDX:    spdx,
		Source:  "https://pkg.go.dev/" + m.Path + "@" + m.Version,
		Files:   files,
	}, nil
}

// joinIDs renders a set of SPDX ids as a sorted "A AND B" expression.
func joinIDs(ids map[string]bool) string {
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)
	return strings.Join(list, " AND ")
}

// normalizeText makes license text deterministic across checkouts: LF line
// endings, no trailing whitespace, no leading or trailing blank lines.
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// Generate builds the full THIRD_PARTY_NOTICES document for the checkout at
// repoRoot: every shipped Go module plus the vendored SPA packages.
func Generate(ctx context.Context, repoRoot string) ([]byte, error) {
	entries, err := Collect(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	return Render(entries), nil
}

// Collect returns every notice entry, sorted by name.
func Collect(ctx context.Context, repoRoot string) ([]Entry, error) {
	mods, err := ShippedModules(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	if len(mods) == 0 {
		return nil, errors.New("go list reported no third-party modules for ./cmd/lmm; refusing to write an empty notices file")
	}
	var entries []Entry
	for _, m := range mods {
		e, err := CollectModule(m)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	for _, v := range VendoredSPA {
		e, err := v.Entry(repoRoot)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// SPDXIDs returns the distinct SPDX license ids named by entries (the
// operands of every expression), sorted. The AUR package's license list is
// checked against it.
func SPDXIDs(entries []Entry) []string {
	set := map[string]bool{}
	for _, e := range entries {
		for _, f := range strings.Fields(e.SPDX) {
			if f != "AND" && f != "OR" {
				set[f] = true
			}
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

const rule = "================================================================================"

// Render lays entries out as the THIRD_PARTY_NOTICES document.
func Render(entries []Entry) []byte {
	var b strings.Builder
	b.WriteString("THIRD-PARTY NOTICES\n\n")
	b.WriteString("lmm is licensed under the MIT License (see LICENSE). The software listed below is\n")
	b.WriteString("compiled into, or embedded in, the lmm binary and is distributed under its own\n")
	b.WriteString("license. Each component's license text follows, reproduced as that license requires.\n\n")
	b.WriteString("GENERATED FILE - DO NOT EDIT. Run `make notices` (go run ./tools/notices) to\n")
	b.WriteString("regenerate it from `go list -deps ./cmd/lmm` and internal/notices/vendored.go.\n\n")
	b.WriteString("Contents\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "  %s %s - %s\n", e.Name, e.Version, e.SPDX)
	}
	b.WriteString("\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "%s\n%s %s\nLicense: %s\nSource:  %s\n%s\n", rule, e.Name, e.Version, e.SPDX, e.Source, rule)
		for _, f := range e.Files {
			fmt.Fprintf(&b, "\n--- %s ---\n\n%s\n", f.Name, f.Text)
		}
		b.WriteString("\n")
	}
	return []byte(strings.TrimRight(b.String(), "\n") + "\n")
}
