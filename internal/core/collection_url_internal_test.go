package core

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSafeCause is #622 review S2: a transport error can quote what the
// far end sent, so its text is cleaned before it reaches a terminal, and
// it still unwraps to the original.
func TestSafeCause(t *testing.T) {
	require.NoError(t, safeCause(nil))
	plain := errors.New("connection refused")
	require.Same(t, plain, safeCause(plain), "a clean error is returned as is")

	dirty := errors.New("bad response \x1b[31mRED\x1b[0m‮")
	got := safeCause(dirty)
	require.NotContains(t, got.Error(), "\x1b")
	require.NotContains(t, got.Error(), "‮")
	require.Contains(t, got.Error(), "bad response")
	require.ErrorIs(t, got, dirty)
}

// TestProfileResultsAreBuiltByNewProfileResult is #622 review S3: every
// ProfileResult a frontend emits shows a URL import's address redacted,
// which holds only while each one is built by NewProfileResult. A
// composite literal anywhere else in core, the CLI or the server fails.
func TestProfileResultsAreBuiltByNewProfileResult(t *testing.T) {
	literal := regexp.MustCompile(`ProfileResult\{`)
	var offenders []string
	for _, dir := range []string{".", "../../cmd/lmm", "../serve"} {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || filepath.Ext(name) != ".go" || regexp.MustCompile(`_test\.go$`).MatchString(name) {
				continue
			}
			data, err := fs.ReadFile(os.DirFS(dir), name)
			require.NoError(t, err)
			for _, loc := range literal.FindAllIndex(data, -1) {
				if dir == "." && name == "profile.go" && isNewProfileResultBody(data, loc[0]) {
					continue
				}
				offenders = append(offenders, filepath.Join(dir, name))
			}
		}
	}
	require.Empty(t, offenders, "build a ProfileResult with NewProfileResult, which redacts origin.import_url")
}

// isNewProfileResultBody reports whether the literal at off is the one
// inside NewProfileResult itself.
func isNewProfileResultBody(data []byte, off int) bool {
	start := regexp.MustCompile(`func NewProfileResult\(`).FindIndex(data)
	return start != nil && off > start[0] && off-start[0] < 300
}
