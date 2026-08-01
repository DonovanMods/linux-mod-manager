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

func TestParseLinkMethod(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  LinkMethod
	}{
		{"hardlink", "hardlink", LinkHardlink},
		{"copy", "copy", LinkCopy},
		{"symlink explicit", "symlink", LinkSymlink},
		{"empty defaults to symlink", "", LinkSymlink},
		{"unknown defaults to symlink", "bogus", LinkSymlink},
		// ParseLinkMethod compares against exact lowercase literals, so any
		// other casing falls through to the default rather than being
		// case-normalized.
		{"case sensitive - not matched", "Hardlink", LinkSymlink},
		{"case sensitive - upper not matched", "COPY", LinkSymlink},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ParseLinkMethod(tt.input))
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

func TestParseDeployMode(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  DeployMode
	}{
		{"copy", "copy", DeployCopy},
		{"compile", "compile", DeployCompile},
		{"extract explicit", "extract", DeployExtract},
		{"empty defaults to extract", "", DeployExtract},
		{"unknown defaults to extract", "bogus", DeployExtract},
		// Same exact-match, no-case-folding behavior as ParseLinkMethod.
		{"case sensitive - not matched", "Copy", DeployExtract},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ParseDeployMode(tt.input))
		})
	}
}
