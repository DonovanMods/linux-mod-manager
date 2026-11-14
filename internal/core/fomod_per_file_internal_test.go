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
// tree; one file's record is the single variant, unchanged; none is "".
func TestFomodFilesVariant(t *testing.T) {
	a, b := perFilePick("A"), perFilePick("B")
	two := fomodFilesVariant(domain.FomodFiles{"1": a, "2": b})
	assert.Len(t, two, 12)
	assert.Equal(t, two, fomodFilesVariant(domain.FomodFiles{"2": b, "1": a}), "independent of map order")
	assert.NotEqual(t, two, fomodFilesVariant(domain.FomodFiles{"1": b, "2": a}), "which file chose what matters")
	assert.NotEqual(t, two, fomodFilesVariant(domain.FomodFiles{"1": a, "3": b}), "so do the file ids")
	assert.Equal(t, fomodVariant(a), fomodFilesVariant(domain.FomodFiles{"1": a}), "one file is the single tree")
	assert.Equal(t, fomodVariant(a), fomodFilesVariant(domain.FomodFiles{"1": a, "2": {}}))
	assert.Empty(t, fomodFilesVariant(nil))
	assert.Equal(t, two, recordVariant(domain.FomodRecord{Choices: b, Files: domain.FomodFiles{"1": a, "2": b}}), "the per-file form is the record")
	assert.Equal(t, fomodVariant(b), recordVariant(domain.FomodRecord{Choices: b}))
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

	assert.Equal(t, fomodFilesVariant(domain.FomodFiles{"1": a, "2": c}), in.expectedVariant(), "answers over the recording")
	assert.Equal(t, fomodVariant(a), single.expectedVariant())
}

// TestRemapFomodFiles (#634): an update hands each old file's choices to
// the file replacing it.
func TestRemapFomodFiles(t *testing.T) {
	a, b := perFilePick("A"), perFilePick("B")
	rec := domain.FomodFiles{"m1": a, "p1": b}
	files := func(ids ...string) []*domain.DownloadableFile {
		out := make([]*domain.DownloadableFile, len(ids))
		for i, id := range ids {
			out[i] = &domain.DownloadableFile{ID: id}
		}
		return out
	}
	got := remapFomodFiles(rec, []string{"m1", "p1"}, files("m2", "p2"), map[string]string{"m1": "m2", "p1": "p2"})
	assert.True(t, domain.FomodFiles{"m2": a, "p2": b}.Equal(got), "by the source's replacements")
	got = remapFomodFiles(rec, []string{"m1", "p1"}, files("m2", "p2"), nil)
	assert.True(t, domain.FomodFiles{"m2": a, "p2": b}.Equal(got), "else in pinned order")
	got = remapFomodFiles(rec, []string{"m1", "p1"}, files("m2", "p1"), map[string]string{"m1": "m2"})
	assert.True(t, domain.FomodFiles{"m2": a, "p1": b}.Equal(got), "a kept file keeps its choices")
	got = remapFomodFiles(rec, []string{"m1", "p1"}, files("m2"), nil)
	assert.True(t, got.IsZero(), "no pairing: each new installer asks")
	assert.Nil(t, remapFomodFiles(nil, []string{"m1"}, files("m2"), nil))
}
