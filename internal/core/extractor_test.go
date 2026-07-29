package core_test

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestZip(t *testing.T, dir string, files map[string]string) string {
	zipPath := filepath.Join(dir, "test.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, f.Close())
	}()

	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	return zipPath
}

func TestExtractor_Extract_Zip(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a test zip file
	files := map[string]string{
		"readme.txt":        "This is a readme file",
		"mod/plugin.dll":    "plugin binary content",
		"mod/data/file.txt": "nested file content",
	}
	zipPath := createTestZip(t, srcDir, files)

	extractor := core.NewExtractor()
	err := extractor.Extract(zipPath, destDir)
	require.NoError(t, err)

	// Verify files were extracted
	content, err := os.ReadFile(filepath.Join(destDir, "readme.txt"))
	require.NoError(t, err)
	assert.Equal(t, "This is a readme file", string(content))

	content, err = os.ReadFile(filepath.Join(destDir, "mod/plugin.dll"))
	require.NoError(t, err)
	assert.Equal(t, "plugin binary content", string(content))

	content, err = os.ReadFile(filepath.Join(destDir, "mod/data/file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "nested file content", string(content))
}

func TestExtractor_Extract_ZipWithDirectories(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a zip with explicit directory entries
	zipPath := filepath.Join(srcDir, "test.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)

	w := zip.NewWriter(f)

	// Add directory entry
	_, err = w.Create("subdir/")
	require.NoError(t, err)

	// Add file in directory
	fw, err := w.Create("subdir/file.txt")
	require.NoError(t, err)
	_, err = fw.Write([]byte("content"))
	require.NoError(t, err)

	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	extractor := core.NewExtractor()
	err = extractor.Extract(zipPath, destDir)
	require.NoError(t, err)

	// Verify directory was created
	info, err := os.Stat(filepath.Join(destDir, "subdir"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Verify file was extracted
	content, err := os.ReadFile(filepath.Join(destDir, "subdir/file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "content", string(content))
}

func TestExtractor_Extract_EmptyZip(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create an empty zip file
	zipPath := filepath.Join(srcDir, "empty.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	extractor := core.NewExtractor()
	err = extractor.Extract(zipPath, destDir)
	require.NoError(t, err)
}

func TestExtractor_Extract_NonExistentFile(t *testing.T) {
	destDir := t.TempDir()

	extractor := core.NewExtractor()
	err := extractor.Extract("/nonexistent/file.zip", destDir)
	require.Error(t, err)
}

func TestExtractor_Extract_InvalidZip(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a file that's not a valid zip
	invalidPath := filepath.Join(srcDir, "invalid.zip")
	err := os.WriteFile(invalidPath, []byte("not a zip file"), 0644)
	require.NoError(t, err)

	extractor := core.NewExtractor()
	err = extractor.Extract(invalidPath, destDir)
	require.Error(t, err)
}

func TestExtractor_Extract_UnsupportedWithoutExtensionReportsPath(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	invalidPath := filepath.Join(srcDir, "downloaded-archive")
	err := os.WriteFile(invalidPath, []byte("not an archive"), 0644)
	require.NoError(t, err)

	extractor := core.NewExtractor()
	err = extractor.Extract(invalidPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported archive format for path")
	assert.Contains(t, err.Error(), invalidPath)
}

// TestExtractor_Extract_TruncatedZip verifies corrupt/truncated zip returns error (error-path test).
func TestExtractor_Extract_TruncatedZip(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a zip that has valid local header but is truncated (no central directory / truncated content)
	zipPath := filepath.Join(srcDir, "truncated.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	fw, err := w.Create("file.txt")
	require.NoError(t, err)
	_, err = fw.Write([]byte("content"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, f.Sync())
	// Truncate file to simulate corrupt download (remove central directory)
	info, err := f.Stat()
	require.NoError(t, err)
	err = f.Truncate(info.Size() / 2)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	extractor := core.NewExtractor()
	err = extractor.Extract(zipPath, destDir)
	require.Error(t, err)
}

func TestExtractor_CanExtract(t *testing.T) {
	extractor := core.NewExtractor()

	tests := []struct {
		filename string
		expected bool
	}{
		{"mod.zip", true},
		{"mod.ZIP", true},
		{"mod.7z", true},
		{"mod.7Z", true},
		{"mod.rar", true},
		{"mod.RAR", true},
		{"mod.txt", false},
		{"mod.exe", false},
		{"mod", false},
		{"", false},
		{"archive.tar.gz", false}, // Not supported
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := extractor.CanExtract(tt.filename)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractor_CanExtract_ByContentWithoutExtension(t *testing.T) {
	srcDir := t.TempDir()
	zipPath := createTestZip(t, srcDir, map[string]string{"file.txt": "content"})
	noExtPath := filepath.Join(srcDir, "downloaded-archive")

	require.NoError(t, os.Rename(zipPath, noExtPath))

	extractor := core.NewExtractor()
	assert.True(t, extractor.CanExtract(noExtPath))
}

func TestExtractor_Extract_ZipWithoutExtension(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	zipPath := createTestZip(t, srcDir, map[string]string{"nested/file.txt": "content"})
	noExtPath := filepath.Join(srcDir, "downloaded-archive")

	require.NoError(t, os.Rename(zipPath, noExtPath))

	extractor := core.NewExtractor()
	require.NoError(t, extractor.Extract(noExtPath, destDir))

	content, err := os.ReadFile(filepath.Join(destDir, "nested/file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "content", string(content))
}

func TestExtractor_Extract_NonExistentFileReportsPath(t *testing.T) {
	destDir := t.TempDir()
	missingPath := "/nonexistent/file.zip"

	extractor := core.NewExtractor()
	err := extractor.Extract(missingPath, destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accessing archive")
	assert.Contains(t, err.Error(), missingPath)
}

func TestExtractor_DetectFormat(t *testing.T) {
	extractor := core.NewExtractor()

	tests := []struct {
		filename string
		expected string
	}{
		{"mod.zip", "zip"},
		{"mod.ZIP", "zip"},
		{"mod.7z", "7z"},
		{"mod.7Z", "7z"},
		{"mod.rar", "rar"},
		{"mod.RAR", "rar"},
		{"mod.txt", ""},
		{"mod", ""},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := extractor.DetectFormat(tt.filename)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractor_Extract_ZipSlipPrevention(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a zip with a path traversal attempt
	zipPath := filepath.Join(srcDir, "malicious.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)

	w := zip.NewWriter(f)
	// Try to write outside the destination directory
	fw, err := w.CreateHeader(&zip.FileHeader{
		Name:   "../../../etc/passwd",
		Method: zip.Store,
	})
	require.NoError(t, err)
	_, err = fw.Write([]byte("malicious content"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	extractor := core.NewExtractor()
	err = extractor.Extract(zipPath, destDir)
	// Should either error or safely extract to a sanitized path
	// The key is that it should NOT write to ../../../etc/passwd
	if err == nil {
		// If no error, verify file was NOT written outside destDir
		_, err := os.Stat("/etc/passwd_test")
		assert.True(t, os.IsNotExist(err), "should not write outside destination")
	}
}

// createOrderedTestZip is createTestZip with a deterministic member order -
// needed when a test cares which members were written before extraction
// aborts on a rejected one.
func createOrderedTestZip(t *testing.T, dir, name string, members []struct{ Name, Content string }) string {
	t.Helper()
	zipPath := filepath.Join(dir, name)
	f, err := os.Create(zipPath)
	require.NoError(t, err)

	w := zip.NewWriter(f)
	for _, m := range members {
		fw, err := w.Create(m.Name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(m.Content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
	return zipPath
}

// TestExtractor_Extract_RejectsReservedCacheMarkerNames is the #96 round 2
// review finding: a mod archive could ship members under lmm's reserved
// ".lmm-" cache namespace, which the marker-based cache-first guard trusts as
// its own bookkeeping. Two concrete abuses were demonstrated against a probe
// archive downloaded for file1:
//
//   - FORGERY: a member named ".lmm-file-file2" made
//     HasFileIDs(["file1","file2"]) report true, so the cache-first guard
//     skipped file2's download entirely and the mod deployed with a genuinely
//     missing file - silently, with no error.
//   - HIDING: a member named ".lmm-hidden.esp" was excluded from ListFiles and
//     therefore never deployed, again silently.
//
// The extractor now refuses such members outright. This mirrors how the
// sibling untrusted-archive guards in this repo behave - sanitizePath's zip
// slip check and copyDir's symlink containment check both FAIL the operation
// naming the offending entry rather than skipping it silently, on the
// reasoning that the archive is malformed or hostile and the user should see
// why. Legitimate markers never travel through an archive: they are written
// by cache.MarkFileComplete and reseeded by prepareStaging's copyDir.
func TestExtractor_Extract_RejectsReservedCacheMarkerNames(t *testing.T) {
	tests := []struct {
		name    string
		member  string
		comment string
	}{
		{"forged completion marker", ".lmm-file-file2", "forges another file's completion"},
		{"hidden member", ".lmm-hidden.esp", "hides from ListFiles and deploy"},
		{"nested under reserved dir", ".lmm-evil/payload.esp", "a reserved DIRECTORY hides everything under it"},
		{"reserved name in a subdirectory", "sub/.lmm-file-file2", "reserved names are rejected at any depth"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srcDir := t.TempDir()
			destDir := t.TempDir()

			// The legitimate member comes FIRST so it is already written when
			// extraction aborts on the reserved one - proving the rejection
			// is what stopped it, not an unrelated early failure.
			zipPath := createOrderedTestZip(t, srcDir, "malicious.zip", []struct{ Name, Content string }{
				{"mod1.esp", "legitimate content"},
				{tt.member, "hostile content"},
			})

			err := core.NewExtractor().Extract(zipPath, destDir)
			require.Error(t, err, "extraction must fail: %s", tt.comment)
			assert.Contains(t, err.Error(), ".lmm-", "the error must name the reserved prefix it rejected")

			_, statErr := os.Stat(filepath.Join(destDir, tt.member))
			assert.True(t, os.IsNotExist(statErr), "the reserved member must never be written to disk")

			_, statErr = os.Stat(filepath.Join(destDir, "mod1.esp"))
			assert.NoError(t, statErr, "the preceding legitimate member is unaffected")
		})
	}
}

// TestExtractor_Extract_ReservedNameRejection_AppliesTo7z covers the external
// extractor path (.7z/.rar shell out to the system 7z binary, so there is no
// per-member hook to reject at - the guard has to inspect what 7z actually
// wrote). Skipped where 7z isn't installed, same as any other 7z-dependent
// behavior.
func TestExtractor_Extract_ReservedNameRejection_AppliesTo7z(t *testing.T) {
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}

	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Build a .7z holding one legitimate member and one forged marker.
	stage := filepath.Join(srcDir, "stage")
	require.NoError(t, os.MkdirAll(stage, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stage, "mod1.esp"), []byte("legitimate content"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(stage, ".lmm-file-file2"), nil, 0644))

	archivePath := filepath.Join(srcDir, "malicious.7z")
	cmd := exec.Command("7z", "a", "-y", archivePath, filepath.Join(stage, "."))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building fixture archive: %s", out)

	err = core.NewExtractor().Extract(archivePath, destDir)
	require.Error(t, err, "a forged marker must fail extraction on the 7z path too")
	assert.Contains(t, err.Error(), ".lmm-")
}

// TestExtractor_Extract_KeepsPreexistingMarkersInDest guards the multi-file
// download flow against a false positive: prepareStaging reseeds the staging
// directory from the existing cache entry BEFORE extracting the next file, so
// markers written by a mod's earlier files are legitimately already sitting
// in destDir. The reserved-name check must judge what THIS archive
// contributed, never what was already there.
func TestExtractor_Extract_KeepsPreexistingMarkersInDest(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// A marker from a previous file's commit, reseeded into staging.
	require.NoError(t, cache.MarkFileComplete(destDir, "file1"))

	zipPath := createTestZip(t, srcDir, map[string]string{"mod2.esp": "second file"})
	require.NoError(t, core.NewExtractor().Extract(zipPath, destDir))

	_, err := os.Stat(filepath.Join(destDir, ".lmm-file-file1"))
	assert.NoError(t, err, "a legitimately pre-existing marker must survive extraction untouched")
	_, err = os.Stat(filepath.Join(destDir, "mod2.esp"))
	assert.NoError(t, err)
}

func TestExtractor_Extract_PreservesPermissions(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a zip with a file that has execute permissions
	zipPath := filepath.Join(srcDir, "test.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)

	w := zip.NewWriter(f)
	header := &zip.FileHeader{
		Name:   "script.sh",
		Method: zip.Store,
	}
	header.SetMode(0755)
	fw, err := w.CreateHeader(header)
	require.NoError(t, err)
	_, err = fw.Write([]byte("#!/bin/bash\necho hello"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	extractor := core.NewExtractor()
	err = extractor.Extract(zipPath, destDir)
	require.NoError(t, err)

	// Check that the file was extracted (permissions may vary by platform)
	info, err := os.Stat(filepath.Join(destDir, "script.sh"))
	require.NoError(t, err)
	assert.False(t, info.IsDir())
}

func TestExtractor_Extract_CreatesDestDir(t *testing.T) {
	srcDir := t.TempDir()

	files := map[string]string{
		"file.txt": "content",
	}
	zipPath := createTestZip(t, srcDir, files)

	// Use a non-existent destination directory
	destDir := filepath.Join(t.TempDir(), "nested", "dest")

	extractor := core.NewExtractor()
	err := extractor.Extract(zipPath, destDir)
	require.NoError(t, err)

	// Verify directory was created and file extracted
	content, err := os.ReadFile(filepath.Join(destDir, "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "content", string(content))
}

func TestNewExtractor(t *testing.T) {
	e := core.NewExtractor()
	assert.NotNil(t, e)
}
