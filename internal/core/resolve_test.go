package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveVersionFiles(t *testing.T) {
	f := func(id, version, category string, primary bool) domain.DownloadableFile {
		return domain.DownloadableFile{ID: id, Version: version, Category: category, IsPrimary: primary}
	}

	tests := []struct {
		name    string
		files   []domain.DownloadableFile
		version string
		wantIDs []string
		wantErr error // sentinel matched with errors.Is; nil = success
	}{
		{
			name:    "exact match returns the matching file",
			files:   []domain.DownloadableFile{f("10", "1.5", "MAIN", true), f("9", "1.0", "OLD_VERSION", false)},
			version: "1.0",
			wantIDs: []string{"9"},
		},
		{
			name:    "archived files are eligible - no filtering",
			files:   []domain.DownloadableFile{f("10", "1.5", "MAIN", true), f("9", "1.0", "ARCHIVED", false)},
			version: "1.0",
			wantIDs: []string{"9"},
		},
		{
			name: "multiple files of one version all returned, category-sorted MAIN first",
			files: []domain.DownloadableFile{
				f("11", "1.0", "OPTIONAL", false),
				f("10", "1.0", "MAIN", true),
				f("12", "1.5", "MAIN", false),
			},
			version: "1.0",
			wantIDs: []string{"10", "11"},
		},
		{
			name:    "no match is ErrVersionNotFound",
			files:   []domain.DownloadableFile{f("10", "1.5", "MAIN", true), f("9", "1.0", "MAIN", false)},
			version: "2.0",
			wantErr: core.ErrVersionNotFound,
		},
		{
			name:    "version-less list is ErrNotSupported",
			files:   []domain.DownloadableFile{f("main", "", "", true)},
			version: "1.0",
			wantErr: source.ErrNotSupported,
		},
		{
			name:    "empty list is ErrNotSupported",
			files:   nil,
			version: "1.0",
			wantErr: source.ErrNotSupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := core.ResolveVersionFiles("src", tt.files, tt.version)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			ids := make([]string, len(got))
			for i, g := range got {
				ids[i] = g.ID
			}
			assert.Equal(t, tt.wantIDs, ids)
		})
	}
}

func TestResolveVersionFiles_NotFoundListsAvailableVersions(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "10", Version: "1.5"},
		{ID: "9", Version: "1.0"},
		{ID: "8", Version: "1.5"}, // duplicate version - listed once
		{ID: "7"},                 // version-less file - not listed
	}
	_, err := core.ResolveVersionFiles("src", files, "2.0")
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrVersionNotFound))
	assert.Contains(t, err.Error(), `version "2.0"`)
	assert.Contains(t, err.Error(), "available: 1.5, 1.0")
}

// multiVersionSource embeds mockSource and returns multiple versioned files
type multiVersionSource struct{ *mockSource }

func (s *multiVersionSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "10", Name: "Main", FileName: mod.ID + ".zip", Version: "1.5", IsPrimary: true, Category: "MAIN"},
		{ID: "9", Name: "Old", FileName: mod.ID + "-old.zip", Version: "1.0", Category: "ARCHIVED"},
	}, nil
}

func TestServiceResolveModVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	mock := &multiVersionSource{newMockSource("src")}
	svc.RegisterSource(mock)
	mod := &domain.Mod{ID: "mod1", SourceID: "src", GameID: "testgame", Name: "Mod One", Version: "1.5"}

	files, err := svc.ResolveModVersion(context.Background(), "src", mod, "1.0")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "9", files[0].ID)

	_, err = svc.ResolveModVersion(context.Background(), "src", mod, "9.9")
	assert.ErrorIs(t, err, core.ErrVersionNotFound)
}

// TestService_AvailableModVersions covers AvailableModVersions' data
// source: distinct per-file versions in first-seen order, and the
// ErrNotSupported degrade when the source's file list carries no version
// info at all (#97).
func TestService_AvailableModVersions(t *testing.T) {
	svc := newFlowsTestService(t)
	mock := &multiVersionSource{newMockSource("src")}
	svc.RegisterSource(mock)
	mod := &domain.Mod{ID: "mod1", SourceID: "src", GameID: "testgame", Name: "Mod One", Version: "1.5"}

	versions, err := svc.AvailableModVersions(context.Background(), "src", mod)
	require.NoError(t, err)
	assert.Equal(t, []string{"1.5", "1.0"}, versions)
}

func TestService_AvailableModVersions_NoVersionInfo(t *testing.T) {
	svc := newFlowsTestService(t)
	mock := newMockSource("src") // GetModFiles returns a version-less file
	svc.RegisterSource(mock)
	mod := &domain.Mod{ID: "mod1", SourceID: "src", GameID: "testgame", Name: "Mod One"}

	_, err := svc.AvailableModVersions(context.Background(), "src", mod)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrNotSupported)
}

// TestService_SourceCapabilities covers the static lock-gating accessor:
// it reports whatever the registered source's CapabilityReporter declares,
// reached by sourceID alone (#97).
func TestService_SourceCapabilities(t *testing.T) {
	svc := newFlowsTestService(t)
	caps := source.Capabilities{Search: true, Versions: true}
	mock := &capsStubSource{&searchStubSource{id: "src", caps: &caps}}
	svc.RegisterSource(mock)

	got, err := svc.SourceCapabilities("src")
	require.NoError(t, err)
	assert.Equal(t, caps, got)
}

// TestService_SourceCapabilities_UnknownSource covers the not-found path.
func TestService_SourceCapabilities_UnknownSource(t *testing.T) {
	svc := newFlowsTestService(t)

	_, err := svc.SourceCapabilities("nope")
	require.Error(t, err)
}
