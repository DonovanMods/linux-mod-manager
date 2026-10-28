package notices

import (
	"fmt"
	"os"
	"path/filepath"
)

// Vendored is one third-party package vendored into the SPA under
// internal/serve/vendor. They have no go.mod entry, so go list cannot see
// them; this table is their registry. A test holds it in lock-step with the
// "Version:" and "License:" lines in each vendored file's header, so a
// version bump or a new vendored file fails until the table is updated.
type Vendored struct {
	// Name is the package name as the notices list it.
	Name string
	// Version is the pinned version, matching the vendored files' headers.
	Version string
	// SPDX is the package's SPDX license id.
	SPDX string
	// Source is the pinned upstream repository tag.
	Source string
	// LicenseURL is where LicenseFile was fetched from (raw text at the tag).
	LicenseURL string
	// LicenseFile is the upstream license text, checked in beside the
	// vendored files, relative to the repository root.
	LicenseFile string
	// Files are the vendored files (base names under internal/serve/vendor)
	// this package accounts for.
	Files []string
}

// VendorDir is the vendored SPA libraries' directory, relative to the repo root.
const VendorDir = "internal/serve/vendor"

// VendoredSPA is the registry of vendored SPA packages. preact and its hooks
// entry point are one package at one version, so they share an entry.
var VendoredSPA = []Vendored{
	{
		Name:        "preact (with preact/hooks)",
		Version:     "10.29.8",
		SPDX:        "MIT",
		Source:      "https://github.com/preactjs/preact/tree/10.29.8",
		LicenseURL:  "https://raw.githubusercontent.com/preactjs/preact/10.29.8/LICENSE",
		LicenseFile: VendorDir + "/LICENSE.preact",
		Files:       []string{"preact.module.js", "hooks.module.js"},
	},
	{
		Name:        "htm",
		Version:     "3.1.1",
		SPDX:        "Apache-2.0",
		Source:      "https://github.com/developit/htm/tree/3.1.1",
		LicenseURL:  "https://raw.githubusercontent.com/developit/htm/3.1.1/LICENSE",
		LicenseFile: VendorDir + "/LICENSE.htm",
		Files:       []string{"htm.module.js"},
	},
}

// Entry reads the package's checked-in license text into a notice entry.
// The text's detected license must agree with SPDX, so the table cannot
// claim a license the file does not carry.
func (v Vendored) Entry(repoRoot string) (Entry, error) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(v.LicenseFile)))
	if err != nil {
		return Entry{}, fmt.Errorf("vendored %s: %w", v.Name, err)
	}
	text := normalizeText(string(raw))
	detected := DetectSPDX(text)
	if len(detected) != 1 || detected[0] != v.SPDX {
		return Entry{}, fmt.Errorf("vendored %s: %s detects as %v, table says %s", v.Name, v.LicenseFile, detected, v.SPDX)
	}
	return Entry{
		Name:    v.Name,
		Version: v.Version,
		SPDX:    v.SPDX,
		Source:  v.Source + " (license text: " + v.LicenseURL + ")",
		Files:   []File{{Name: filepath.Base(v.LicenseFile), Text: text}},
	}, nil
}
