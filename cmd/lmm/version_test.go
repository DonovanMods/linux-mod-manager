package main

import "testing"

// TestDisplayVersion pins the dev-provenance display logic (#208): a build
// exactly on the release tag (or one made without the Makefile's ldflags,
// where buildDescribe is empty) shows the plain static version; anything
// else appends the git-describe provenance, dirty suffix included verbatim.
func TestDisplayVersion(t *testing.T) {
	tests := []struct {
		name          string
		version       string
		buildDescribe string
		want          string
	}{
		{
			name:          "empty buildDescribe (go build/go test without ldflags)",
			version:       "1.28.0",
			buildDescribe: "",
			want:          "1.28.0",
		},
		{
			name:          "clean tag build matches v+version exactly",
			version:       "1.28.0",
			buildDescribe: "v1.28.0",
			want:          "1.28.0",
		},
		{
			name:          "ahead of the tag",
			version:       "1.28.0",
			buildDescribe: "v1.28.0-2-g140e3c6",
			want:          "1.28.0 (dev: v1.28.0-2-g140e3c6)",
		},
		{
			name:          "dirty worktree on the tag itself",
			version:       "1.28.0",
			buildDescribe: "v1.28.0-dirty",
			want:          "1.28.0 (dev: v1.28.0-dirty)",
		},
		{
			name:          "ahead of the tag and dirty",
			version:       "1.28.0",
			buildDescribe: "v1.28.0-2-g140e3c6-dirty",
			want:          "1.28.0 (dev: v1.28.0-2-g140e3c6-dirty)",
		},
		{
			name:          "tag-less clone falls back to a bare hash (git describe --always)",
			version:       "1.28.0",
			buildDescribe: "140e3c6",
			want:          "1.28.0 (dev: 140e3c6)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeDisplayVersion(tt.version, tt.buildDescribe)
			if got != tt.want {
				t.Errorf("computeDisplayVersion(%q, %q) = %q, want %q", tt.version, tt.buildDescribe, got, tt.want)
			}
		})
	}
}
