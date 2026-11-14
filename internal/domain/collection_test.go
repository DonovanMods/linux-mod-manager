package domain_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestCollectionInfo_IsZeroAndEqual(t *testing.T) {
	var none *domain.CollectionInfo
	require.True(t, none.IsZero())
	require.True(t, (&domain.CollectionInfo{Origin: &domain.CollectionOrigin{}}).IsZero(), "an empty origin records nothing")
	require.True(t, none.Equal(&domain.CollectionInfo{}), "nil and zero are the same identity")

	a := &domain.CollectionInfo{ID: "x", Version: 1, Origin: &domain.CollectionOrigin{Slug: "s"}}
	b := &domain.CollectionInfo{ID: "x", Version: 1, Origin: &domain.CollectionOrigin{Slug: "s"}}
	require.True(t, a.Equal(b), "origins compare by value, not pointer")
	b.Origin.Revision = 2
	require.False(t, a.Equal(b))
	require.False(t, a.Equal(&domain.CollectionInfo{ID: "x", Version: 1}))
	require.False(t, a.Equal(nil))
}

func TestCollectionOrigin_ImportURLHelpers(t *testing.T) {
	var none *domain.CollectionOrigin
	require.Nil(t, none.WithoutImportURL())
	require.Nil(t, none.Redacted())

	onlyURL := &domain.CollectionOrigin{ImportURL: "https://example.com/c.lmmc?token=x"}
	require.Nil(t, onlyURL.WithoutImportURL(), "an origin that only records the import address exports as none")
	require.Equal(t, "https://example.com/c.lmmc?…", onlyURL.Redacted().ImportURL)
	require.Equal(t, "https://example.com/c.lmmc?token=x", onlyURL.ImportURL, "Redacted copies")

	credit := &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc", Author: "curator", ImportURL: "https://example.com/c.lmmc"}
	require.Equal(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc", Author: "curator"}, credit.WithoutImportURL(),
		"the curator's credit is kept")
	plain := &domain.CollectionOrigin{SourceID: "nexusmods"}
	require.Same(t, plain, plain.Redacted(), "nothing to redact")
}
