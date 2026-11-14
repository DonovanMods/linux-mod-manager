package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"

	"github.com/stretchr/testify/assert"
)

func perFilePick(option string) domain.FomodChoices {
	return domain.FomodChoices{ConfigSHA256: "ab", Steps: []fomod.StepChoice{{Name: "S", Groups: []fomod.GroupChoice{{Name: "G", Choices: []fomod.OptionChoice{{Name: option}}}}}}}
}

// TestFomodFilesVariant (#634): the per-file variant covers every file's
// tree AND the order they layer in; one file's record is the single
// variant, unchanged; none is "".
func TestFomodFilesVariant(t *testing.T) {
	a, b := perFilePick("A"), perFilePick("B")
	order := []string{"1", "2"}
	two := fomodFilesVariant(domain.FomodFiles{"1": a, "2": b}, order)
	assert.Len(t, two, 12)
	assert.Equal(t, two, fomodFilesVariant(domain.FomodFiles{"2": b, "1": a}, order), "independent of map order")
	assert.NotEqual(t, two, fomodFilesVariant(domain.FomodFiles{"1": a, "2": b}, []string{"2", "1"}), "the layer order is part of the tree")
	assert.NotEqual(t, two, fomodFilesVariant(domain.FomodFiles{"1": b, "2": a}, order), "which file chose what matters")
	assert.NotEqual(t, two, fomodFilesVariant(domain.FomodFiles{"1": a, "3": b}, []string{"1", "3"}), "so do the file ids")
	assert.NotEqual(t, two, fomodFilesVariant(domain.FomodFiles{"1": a, "2": b}, []string{"1", "2", "9"}), "and a plain file layered among them")
	assert.Equal(t, fomodVariant(a), fomodFilesVariant(domain.FomodFiles{"1": a}, order), "one file is the single tree")
	assert.Equal(t, fomodVariant(a), fomodFilesVariant(domain.FomodFiles{"1": a, "2": {}}, order))
	assert.Empty(t, fomodFilesVariant(nil, nil))

	// A record's variant: by file id, unless its hint orders the files.
	rec := domain.FomodRecord{Files: domain.FomodFiles{"1": a, "2": b}}
	assert.Equal(t, two, recordVariant(rec, []string{"2", "1"}), "the order the files are listed in does not matter")
	rec.Order = []string{"2"}
	assert.Equal(t, fomodFilesVariant(rec.Files, []string{"2", "1"}), recordVariant(rec, []string{"1", "2"}), "the hint does")
	assert.Equal(t, fomodVariant(b), recordVariant(domain.FomodRecord{Choices: b}, nil))
}

// TestCanonicalLayerOrder (#634): the hint's files first, in its order,
// then the rest by file id - one order, whatever order the files came in.
func TestCanonicalLayerOrder(t *testing.T) {
	assert.Equal(t, []string{"10", "9", "a"}, canonicalLayerOrder([]string{"a", "9", "10"}, nil))
	assert.Equal(t, []string{"a", "10", "9"}, canonicalLayerOrder([]string{"9", "10", "a"}, []string{"a", "x", "a"}), "hint first; a hint file not installed is skipped")
	assert.Equal(t, []string{"1"}, canonicalLayerOrder([]string{"1", "1"}, nil))
}

// TestFomodInput_ForFile (#634): each file installs with its own answer,
// else the mod's, else its own recording - none when the mod records per
// file but not this file - else the mod's recording.
func TestFomodInput_ForFile(t *testing.T) {
	a, b, c := perFilePick("A"), perFilePick("B"), perFilePick("C")
	in := fomodInput{fileChoices: domain.FomodFiles{"1": a, "2": b}, fileAnswers: domain.FomodFiles{"2": c}}
	assert.True(t, a.Equal(in.forFile("1").choices))
	assert.False(t, in.forFile("1").answered)
	got := in.forFile("2")
	assert.True(t, c.Equal(got.choices))
	assert.True(t, got.answered)
	assert.True(t, b.Equal(got.recorded), "a re-asked question preselects the file's own recording")
	assert.True(t, in.forFile("3").choices.IsZero(), "a file the record does not name asks")

	single := fomodInput{choices: a}
	assert.True(t, a.Equal(single.forFile("9").choices), "a mod's single recording replays on every file, as before")

	ask := fomodInput{ask: true, askFile: "2", fileChoices: domain.FomodFiles{"1": a, "2": b}}
	assert.False(t, ask.forFile("1").ask, "a reconfigure of one file replays the other")
	assert.True(t, ask.forFile("2").ask)
	all := fomodInput{ask: true, fileChoices: domain.FomodFiles{"1": a, "2": b}, fileAnswers: domain.FomodFiles{"1": c}}
	assert.False(t, all.forFile("1").ask, "an answered file never asks")
	assert.True(t, all.forFile("2").ask)

	assert.Equal(t, fomodFilesVariant(domain.FomodFiles{"1": a, "2": c}, []string{"1", "2"}), in.expectedVariant(), "answers over the recording")
	assert.Equal(t, fomodVariant(a), single.expectedVariant())
}

// TestPairUpdateFiles (#634): an update pairs the old files with the new
// by the source's replacements, a kept id, a one-for-one swap, or a unique
// name or category - never by position - and leaves the rest unpaired.
func TestPairUpdateFiles(t *testing.T) {
	file := func(id, name, category string) domain.DownloadableFile {
		return domain.DownloadableFile{ID: id, Name: name, Category: category}
	}
	ptrs := func(files ...domain.DownloadableFile) []*domain.DownloadableFile {
		out := make([]*domain.DownloadableFile, len(files))
		for i := range files {
			out[i] = &files[i]
		}
		return out
	}
	pool := []domain.DownloadableFile{
		file("m1", "Bugs", "MAIN"), file("p1", "Bugs - Patches", "OPTIONAL"),
		file("m2", "Bugs", "MAIN"), file("p2", "Bugs - Patches", "OPTIONAL"),
	}
	// The source lists the new files in another order than the row's.
	newFiles := ptrs(pool[3], pool[2])
	assert.Equal(t, map[string]string{"m1": "m2", "p1": "p2"}, pairUpdateFiles([]string{"m1", "p1"}, pool, newFiles, nil), "by name")
	assert.Equal(t, map[string]string{"m1": "p2", "p1": "m2"}, pairUpdateFiles([]string{"m1", "p1"}, pool, newFiles, map[string]string{"m1": "p2", "p1": "m2"}), "the source's replacements first")
	assert.Equal(t, map[string]string{"p1": "p1", "m1": "m2"}, pairUpdateFiles([]string{"m1", "p1"}, pool, ptrs(pool[1], pool[2]), nil), "a kept file is its own pair")

	renamed := []domain.DownloadableFile{
		file("m1", "Bugs 1.0", "MAIN"), file("p1", "Bugs Patches 1.0", "OPTIONAL"),
		file("m2", "Bugs 1.1", "MAIN"), file("p2", "Bugs Patches 1.1", "OPTIONAL"),
	}
	assert.Equal(t, map[string]string{"m1": "m2", "p1": "p2"}, pairUpdateFiles([]string{"m1", "p1"}, renamed, ptrs(renamed[3], renamed[2]), nil), "else by category")

	same := []domain.DownloadableFile{
		file("a1", "Patch 1.0", "OPTIONAL"), file("b1", "Other 1.0", "OPTIONAL"),
		file("a2", "Patch 1.1", "OPTIONAL"), file("b2", "Other 1.1", "OPTIONAL"),
	}
	assert.Empty(t, pairUpdateFiles([]string{"a1", "b1"}, same, ptrs(same[2], same[3]), nil), "no confident pair: each asks")
	assert.Equal(t, map[string]string{"a1": "x"}, pairUpdateFiles([]string{"a1"}, nil, ptrs(file("x", "", "")), nil), "one for one")
	assert.Empty(t, pairUpdateFiles([]string{"gone1", "gone2"}, nil, ptrs(file("x", "X", ""), file("y", "Y", "")), nil), "old files the source no longer lists: nothing to match")
}

// TestRemapFomodFiles (#634): each old file's choices go to its pair; an
// unpaired one is dropped, so that installer asks.
func TestRemapFomodFiles(t *testing.T) {
	a, b := perFilePick("A"), perFilePick("B")
	rec := domain.FomodFiles{"m1": a, "p1": b}
	got := remapFomodFiles(rec, map[string]string{"m1": "m2", "p1": "p2"})
	assert.True(t, domain.FomodFiles{"m2": a, "p2": b}.Equal(got))
	got = remapFomodFiles(rec, map[string]string{"m1": "m2"})
	assert.True(t, domain.FomodFiles{"m2": a}.Equal(got))
	assert.Nil(t, remapFomodFiles(nil, map[string]string{"m1": "m2"}))
}

// TestRemapFileOrder (#634): the layer-order hint follows the pairs.
func TestRemapFileOrder(t *testing.T) {
	assert.Equal(t, []string{"p2", "m2"}, remapFileOrder([]string{"p1", "m1"}, map[string]string{"m1": "m2", "p1": "p2"}))
	assert.Nil(t, remapFileOrder([]string{"p1", "m1"}, map[string]string{"m1": "m2"}),
		"one paired file is no order: kept, it would put main-2 first, reversing the hint")
	assert.Equal(t, []string{"p2", "m2"}, remapFileOrder([]string{"x1", "p1", "m1"}, map[string]string{"m1": "m2", "p1": "p2"}),
		"two still paired keep their order")
	assert.Nil(t, remapFileOrder(nil, nil))
}
