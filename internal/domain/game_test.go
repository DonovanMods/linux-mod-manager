package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLinkMethod_String(t *testing.T) {
	tests := []struct {
		name   string
		method LinkMethod
		want   string
	}{
		{"symlink", LinkSymlink, "symlink"},
		{"hardlink", LinkHardlink, "hardlink"},
		{"copy", LinkCopy, "copy"},
		{"unknown value", LinkMethod(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.method.String())
		})
	}
}

// TestParseLinkMethod pins the fail-loud contract from #172: empty keeps
// today's default, everything else must be an exact recognized name or the
// parse is rejected (ok=false) instead of silently defaulting. Inverted from
// the pre-#172 version of this test, which asserted "bogus"/mismatched-case
// input silently fell back to LinkSymlink.
func TestParseLinkMethod(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantMode LinkMethod
		wantOK   bool
	}{
		{"hardlink", "hardlink", LinkHardlink, true},
		{"copy", "copy", LinkCopy, true},
		{"symlink explicit", "symlink", LinkSymlink, true},
		{"empty defaults to symlink", "", LinkSymlink, true},
		{"unknown is rejected", "bogus", LinkSymlink, false},
		// ParseLinkMethod compares against exact lowercase literals, so any
		// other casing is rejected rather than being case-normalized.
		{"case sensitive - rejected", "Hardlink", LinkSymlink, false},
		{"case sensitive - upper rejected", "COPY", LinkSymlink, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseLinkMethod(tt.input)
			assert.Equal(t, tt.wantMode, got)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func TestDeployMode_String(t *testing.T) {
	tests := []struct {
		name string
		mode DeployMode
		want string
	}{
		{"extract", DeployExtract, "extract"},
		{"copy", DeployCopy, "copy"},
		{"compile", DeployCompile, "compile"},
		// Unlike LinkMethod.String, the default branch here also returns
		// "extract" rather than "unknown" for out-of-range values.
		{"unknown value falls back to extract", DeployMode(99), "extract"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.mode.String())
		})
	}
}

// TestParseDeployMode mirrors TestParseLinkMethod's fail-loud contract.
// Inverted from the pre-#172 version, which asserted "bogus"/mismatched-case
// input silently fell back to DeployExtract.
func TestParseDeployMode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantMode DeployMode
		wantOK   bool
	}{
		{"copy", "copy", DeployCopy, true},
		{"compile", "compile", DeployCompile, true},
		{"extract explicit", "extract", DeployExtract, true},
		{"empty defaults to extract", "", DeployExtract, true},
		{"unknown is rejected", "bogus", DeployExtract, false},
		// Same exact-match, no-case-folding behavior as ParseLinkMethod.
		{"case sensitive - rejected", "Copy", DeployExtract, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseDeployMode(tt.input)
			assert.Equal(t, tt.wantMode, got)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}
