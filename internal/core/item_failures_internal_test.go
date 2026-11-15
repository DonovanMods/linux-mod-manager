package core

// #655: a refused install-shaped Apply lists every refused item, name and
// reason, so a frontend can show one line per mod rather than the joined
// engine text.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAcquireRefusedError_ItemFailuresListsEveryRefusal(t *testing.T) {
	err := newAcquireRefusedError([]AcquireRefusal{
		{SourceID: "nexusmods", ModID: "266", Name: "Skyrim Upscaled", Err: errors.New("stored file no longer available upstream")},
		{SourceID: "nexusmods", ModID: "57339", Err: errors.New("zip: unsupported compression algorithm")},
	})
	assert.Equal(t, []ItemFailure{
		{SourceID: "nexusmods", ModID: "266", Name: "Skyrim Upscaled", Reason: "stored file no longer available upstream", Refusal: RefusalOther},
		{SourceID: "nexusmods", ModID: "57339", Reason: "zip: unsupported compression algorithm", Refusal: RefusalOther},
	}, err.ItemFailures())
	assert.Contains(t, err.Error(), "nexusmods:266: stored file", "the joined text is unchanged")
}

func TestAcquireRefusedError_ItemFailuresCarriesTheRefusalCode(t *testing.T) {
	err := newAcquireRefusedError([]AcquireRefusal{
		{SourceID: "s", ModID: "1", Name: "One", Err: &LoaderRequiredError{GameID: "g", Kind: "bepinex", ModName: "One"}},
	})
	got := err.ItemFailures()
	if assert.Len(t, got, 1) {
		assert.Equal(t, RefusalLoaderRequired, got[0].Refusal)
		assert.Equal(t, "One", got[0].Name)
	}
}

func TestProfileImportIncompleteError_ItemFailuresAreTheResultsFailures(t *testing.T) {
	failures := []ItemFailure{{SourceID: "s", ModID: "1", Name: "One", Reason: "no downloadable files"}}
	err := &ProfileImportIncompleteError{Profile: "p", Result: &ProfileImportResult{Failed: 1, Failures: failures}}
	assert.Equal(t, failures, err.ItemFailures())
	assert.Nil(t, (&ProfileImportIncompleteError{Profile: "p"}).ItemFailures(), "no result, no items")
}

func TestItemFailuresOf(t *testing.T) {
	refused := newAcquireRefusedError([]AcquireRefusal{
		{SourceID: "s", ModID: "1", Name: "One", Err: errors.New("gone")},
		{SourceID: "s", ModID: "2", Name: "Two", Err: errors.New("bad zip")},
	})
	assert.Len(t, ItemFailuresOf(fmt.Errorf("wrapped: %w", refused)), 2, "found through a wrap")
	incomplete := &ProfileImportIncompleteError{Profile: "p", Result: &ProfileImportResult{Refused: true,
		Failures: []ItemFailure{{SourceID: "s", ModID: "1", Reason: "gone"}}}, refusal: refused}
	assert.Len(t, ItemFailuresOf(incomplete), 1, "the outermost error's own list wins")
	assert.Nil(t, ItemFailuresOf(errors.New("plain")))
	assert.Nil(t, ItemFailuresOf(nil))
	assert.Nil(t, ItemFailuresOf(fmt.Errorf("%w", ErrStalePlan)))
}
