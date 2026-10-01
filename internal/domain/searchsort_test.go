package domain_test

import (
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSearchSort(t *testing.T) {
	for in, want := range map[string]domain.SearchSort{
		"":           domain.SortRelevance,
		"relevance":  domain.SortRelevance,
		"updated":    domain.SortUpdated,
		"downloads":  domain.SortDownloads,
		"popular":    domain.SortPopular,
		"  Updated ": domain.SortUpdated,
		"POPULAR":    domain.SortPopular,
	} {
		got, err := domain.ParseSearchSort(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}

	_, err := domain.ParseSearchSort("newest")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidSearchSort))
	assert.Contains(t, err.Error(), `"newest"`)
	for _, name := range []string{"relevance", "updated", "downloads", "popular"} {
		assert.Contains(t, err.Error(), name, "the refusal names every valid value")
	}
}

func TestSearchSortsIsTheClosedSetInDisplayOrder(t *testing.T) {
	assert.Equal(t,
		[]domain.SearchSort{domain.SortRelevance, domain.SortUpdated, domain.SortDownloads, domain.SortPopular},
		domain.SearchSorts())
	// A caller mutating the returned slice must not corrupt the set.
	domain.SearchSorts()[0] = "junk"
	assert.Equal(t, domain.SortRelevance, domain.SearchSorts()[0])
}
