package notices

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRoot is the repository root, relative to this package.
const repoRoot = "../.."

const mitText = `The MIT License (MIT)

Copyright (c) 2024 Someone

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction.`

const bsd3Text = `Copyright (c) 2009 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

Redistributions in binary form must reproduce the above copyright notice.
Neither the name of Google Inc. nor the names of its contributors may be used
to endorse or promote products derived from this software.`

const bsd2Text = `Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice.
2. Redistributions in binary form must reproduce the above copyright notice.`

const apacheFullText = `                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/`

// TestNotices_MatchesCheckedInFile is the drift test (the man-page test's
// sibling): regenerating the notices in memory must reproduce the
// checked-in THIRD_PARTY_NOTICES byte for byte, so a dependency bump that
// changes the shipped set fails until `make notices` is run (#522).
//
// It is offline: ShippedModules runs `go list` with GOPROXY=off and reads
// only the module cache. CI's `go test` already populates that cache by
// compiling every dependency of cmd/lmm, which is exactly the module set
// read here.
func TestNotices_MatchesCheckedInFile(t *testing.T) {
	want, err := os.ReadFile(filepath.Join(repoRoot, FileName))
	require.NoError(t, err, "%s is missing: run `make notices`", FileName)

	got, err := Generate(context.Background(), repoRoot)
	require.NoError(t, err)

	// Not assert.Equal: a failure would dump both ~2000-line documents.
	if string(want) != string(got) {
		t.Fatalf("%s is out of date with go.mod or the vendored SPA files (first difference at line %d): run `make notices`",
			FileName, firstDiffLine(string(want), string(got)))
	}
}

// firstDiffLine returns the 1-based line where a and b first differ.
func firstDiffLine(a, b string) int {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return i + 1
		}
	}
	return min(len(al), len(bl)) + 1
}

// TestShippedModules_ExcludesTestOnlyAndMainModules proves the generated set
// is what links into cmd/lmm, not everything in go.mod: the main module and
// the test-only dependencies (testify, chromedp) are absent (#522).
func TestShippedModules_ExcludesTestOnlyAndMainModules(t *testing.T) {
	mods, err := ShippedModules(context.Background(), repoRoot)
	require.NoError(t, err)

	paths := map[string]bool{}
	for _, m := range mods {
		paths[m.Path] = true
		assert.NotEmpty(t, m.Version, m.Path)
		assert.NotEmpty(t, m.Dir, m.Path)
	}
	assert.True(t, paths["github.com/spf13/cobra"], "cobra links into lmm")
	assert.True(t, paths["modernc.org/sqlite"], "sqlite links into lmm")
	assert.False(t, paths["github.com/DonovanMods/linux-mod-manager/v2"], "the main module is lmm itself")
	assert.False(t, paths["github.com/stretchr/testify"], "testify is test-only")
	assert.False(t, paths["github.com/chromedp/chromedp"], "chromedp is test-only")
}

func TestDetectSPDX(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"mit", mitText, []string{"MIT"}},
		{"mit with CRLF and extra spacing", strings.ReplaceAll(mitText, "\n", "\r\n  "), []string{"MIT"}},
		{"bsd-3-clause", bsd3Text, []string{"BSD-3-Clause"}},
		{"bsd-2-clause", bsd2Text, []string{"BSD-2-Clause"}},
		{"apache-2.0 full text", apacheFullText, []string{"Apache-2.0"}},
		{"apache-2.0 boilerplate beside MIT", mitText + "\n\nLicensed under the Apache License, Version 2.0 (the \"License\");", []string{"Apache-2.0", "MIT"}},
		{"public domain", "SQLite Is Public Domain\n\nAll of the code has been dedicated to the public domain.", []string{"LicenseRef-PublicDomain"}},
		{"unknown", "All rights reserved. Do not copy.", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DetectSPDX(tt.text))
		})
	}
}

// writeModule makes a fake module directory holding the given files.
func writeModule(t *testing.T, files map[string]string) Module {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	return Module{Path: "example.com/dep", Version: "v1.2.3", Dir: dir}
}

// TestCollectModule_NoLicenseFileFailsLoudly: a shipped module with no
// license text must stop the generator, never be skipped (#522).
func TestCollectModule_NoLicenseFileFailsLoudly(t *testing.T) {
	m := writeModule(t, map[string]string{"main.go": "package dep", "README.md": "hello"})

	_, err := CollectModule(m)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "example.com/dep@v1.2.3")
	assert.Contains(t, err.Error(), "no license file")
}

func TestCollectModule_UnrecognisedLicenseFailsLoudly(t *testing.T) {
	m := writeModule(t, map[string]string{"LICENSE": "All rights reserved."})

	_, err := CollectModule(m)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no known license in LICENSE")
}

// TestCollectModule_OnlyNoticeFileIsNotALicense: a NOTICE or PATENTS file
// accompanies a license; alone it must not satisfy the "has a license" rule.
func TestCollectModule_OnlyNoticeFileIsNotALicense(t *testing.T) {
	m := writeModule(t, map[string]string{"NOTICE": "Copyright Someone"})

	_, err := CollectModule(m)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no license file")
}

func TestCollectModule_ReproducesEveryLicenseFileAndNotice(t *testing.T) {
	m := writeModule(t, map[string]string{
		"LICENSE": mitText + "   \r\n",
		"NOTICE":  "Notice text\n\n",
		"PATENTS": "Patent grant",
		"go.mod":  "module example.com/dep",
	})

	e, err := CollectModule(m)

	require.NoError(t, err)
	assert.Equal(t, "MIT", e.SPDX)
	var names []string
	for _, f := range e.Files {
		names = append(names, f.Name)
		assert.NotContains(t, f.Text, "\r")
		assert.False(t, strings.HasSuffix(f.Text, "\n"), f.Name)
	}
	assert.Equal(t, []string{"LICENSE", "NOTICE", "PATENTS"}, names)
}

func TestCollectModule_MultipleLicensesJoinSorted(t *testing.T) {
	m := writeModule(t, map[string]string{"LICENSE": bsd3Text, "LICENSE-VEC": mitText})

	e, err := CollectModule(m)

	require.NoError(t, err)
	assert.Equal(t, "BSD-3-Clause AND MIT", e.SPDX)
}

func TestSkippedFilesAreExplicit(t *testing.T) {
	for k, reason := range skippedFiles {
		assert.NotEmpty(t, reason, k)
	}
	for k, v := range spdxOverrides {
		assert.NotEmpty(t, v, k)
	}
}

func TestRender_IsSortedAndCarriesFullText(t *testing.T) {
	doc := string(Render([]Entry{
		{Name: "a.example/one", Version: "v1", SPDX: "MIT", Source: "https://a", Files: []File{{Name: "LICENSE", Text: mitText}}},
	}))

	assert.Contains(t, doc, "a.example/one v1 - MIT")
	assert.Contains(t, doc, "Permission is hereby granted")
	assert.True(t, strings.HasSuffix(doc, "\n") && !strings.HasSuffix(doc, "\n\n"))
}

// TestVendoredTableMatchesFileHeaders holds vendored.go in lock-step with the
// vendored files: every file under internal/serve/vendor is accounted for,
// carries the table's version, and names the table's license and copyright
// holder in its header (#522).
func TestVendoredTableMatchesFileHeaders(t *testing.T) {
	accounted := map[string]Vendored{}
	for _, v := range VendoredSPA {
		for _, f := range v.Files {
			accounted[f] = v
		}
	}

	dir := filepath.Join(repoRoot, filepath.FromSlash(VendorDir))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, de := range entries {
		if !strings.HasSuffix(de.Name(), ".js") {
			continue
		}
		v, ok := accounted[de.Name()]
		require.True(t, ok, "%s is vendored but has no entry in VendoredSPA", de.Name())

		raw, err := os.ReadFile(filepath.Join(dir, de.Name()))
		require.NoError(t, err)
		var header strings.Builder // the leading // comment block only
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(line, "//") {
				break
			}
			header.WriteString(line + "\n")
		}
		assert.Contains(t, header.String(), "Version:  "+v.Version, de.Name())
		assert.Contains(t, header.String(), "License:  "+v.SPDX+", Copyright", de.Name(), "the header names the license and copyright holder")
		assert.Contains(t, header.String(), filepath.Base(v.LicenseFile), de.Name(), "the header points at the license file")
		assert.Contains(t, header.String(), FileName, de.Name())
	}
	for f := range accounted {
		_, err := os.Stat(filepath.Join(dir, f))
		assert.NoError(t, err, "VendoredSPA names %s but it is not vendored", f)
	}
}

// TestGoreleaserShipsNotices keeps every packaging channel in step with the
// generated file: the archive, the nfpm contents entry and the AUR
// package() all carry THIRD_PARTY_NOTICES, and the AUR license list names
// every SPDX id the notices contain (#522).
func TestGoreleaserShipsNotices(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".goreleaser.yaml"))
	require.NoError(t, err)
	cfg := string(raw)

	assert.Contains(t, cfg, "      - "+FileName+"\n", "archives files:")
	assert.Contains(t, cfg, "- src: "+FileName+"\n        dst: /usr/share/doc/lmm/"+FileName, "nfpms contents:")
	assert.Contains(t, cfg, `"./`+FileName+`" "${pkgdir}/usr/share/licenses/lmm-bin/`+FileName+`"`, "aur package()")

	entries, err := Collect(context.Background(), repoRoot)
	require.NoError(t, err)
	ids := map[string]bool{"MIT": true} // lmm's own license
	for _, id := range SPDXIDs(entries) {
		ids[id] = true
	}
	assert.Equal(t, joinIDs(ids), aurLicenseValue(t, cfg),
		"the aurs license: must be one SPDX expression of lmm's MIT plus every id in %s", FileName)
}

// aurLicenseValue returns the aurs section's `license:` value.
func aurLicenseValue(t *testing.T, cfg string) string {
	t.Helper()
	_, aur, ok := strings.Cut(cfg, "\naurs:\n")
	require.True(t, ok, "no aurs: section")
	for _, line := range strings.Split(aur, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "license:"); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatal("no license: in the aurs section")
	return ""
}
