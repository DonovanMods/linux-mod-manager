package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func TestFileLists_ZeroCloneEqual(t *testing.T) {
	var none domain.FileLists
	assert.True(t, none.IsZero())
	assert.True(t, domain.FileLists{"1": nil}.IsZero(), "an empty list is no list")
	assert.Nil(t, domain.FileLists{"1": {}}.Clone())

	f := domain.FileLists{"1": {{Path: `a\b.txt`, MD5: "ab"}}, "2": {}}
	c := f.Clone()
	assert.Equal(t, domain.FileLists{"1": {{Path: `a\b.txt`, MD5: "ab"}}}, c, "an empty list is dropped")
	c["1"][0].Path = "changed"
	assert.Equal(t, `a\b.txt`, f["1"][0].Path, "a clone's lists are its own")
	assert.True(t, f.Equal(domain.FileLists{"1": {{Path: `a\b.txt`, MD5: "ab"}}}))
	assert.False(t, f.Equal(domain.FileLists{"1": {{Path: `a/b.txt`, MD5: "ab"}}}), "a path is compared as written")
	assert.False(t, f.Equal(nil))
}

func TestFilePatches_ZeroCloneEqualCount(t *testing.T) {
	p := domain.FilePatches{"1": {{Path: "a", CRC32: "FF5EF52D", SHA256: "x"}, {Path: "b", CRC32: "00000000", SHA256: "y"}}, "2": nil}
	assert.False(t, p.IsZero())
	assert.Equal(t, 2, p.Count())
	assert.True(t, p.Equal(p.Clone()))
	assert.False(t, p.Equal(domain.FilePatches{"1": p["1"][:1]}))
	assert.True(t, domain.FilePatches{"9": {}}.IsZero())
}

func TestModReference_TreeInputs(t *testing.T) {
	ref := domain.ModReference{FileIDs: []string{"2"},
		FileList: domain.FileLists{"1": {{Path: "a", MD5: "ab"}}},
		Patches:  domain.FilePatches{"2": {{Path: "b", CRC32: "00000000", SHA256: "x"}}}}
	assert.True(t, ref.HasTreeInputs())
	assert.True(t, ref.TreeInputsFor("1").Patches == nil)
	assert.Len(t, ref.TreeInputsFor("2").Patches, 1)

	ref.Patches = nil
	assert.False(t, ref.HasTreeInputs(), "a list for a file the ref does not pin is inert")
	ref.FileIDs = nil
	assert.True(t, ref.HasTreeInputs(), "a ref that pins no file is installed from whichever file, so every list counts")
}

func TestSplitTreePath(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c.json"}, domain.SplitTreePath(`a\b/c.json`))
	assert.Equal(t, []string{"a", "", "b"}, domain.SplitTreePath(`a\\b`), "an empty segment is kept, for the importer to refuse")
}
