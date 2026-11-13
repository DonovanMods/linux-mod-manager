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
