package core

import (
	"math"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- #27: adopt matching scores candidates instead of taking the first
// search hit. The rule and its constants live in adopt_score.go; these pin
// the behaviour those constants are chosen for.

func TestAdoptNameKey(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"lowercases", "SkyUI", "skyui"},
		{"drops separators and spaces", "Sky UI - SE", "skyuise"},
		{"drops punctuation but keeps digits", "SkyUI 5.2 (SE)", "skyui52se"},
		{"already normalised is unchanged", "skyui", "skyui"},
		{"empty stays empty", "  -- ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, adoptNameKey(tc.in))
		})
	}
}

func TestAdoptNameSimilarity(t *testing.T) {
	tests := []struct {
		name        string
		a, b        string
		wantAtLeast float64
		wantAtMost  float64
	}{
		{"identical after normalisation", "SkyUI", "sky-ui", 1, 1},
		{"a plural is nearly identical", "Bigger Backpack", "Bigger Backpacks", 0.9, 1},
		{"the #27 overlap case is far apart", "SkyUI", "SkyUI Flashlite", 0, 0.5},
		{"a short key with a suffix is refused outright", "SkyUI", "SkyUI SE", 0, 0},
		{"a long key survives one edit", "Winter Overhaul", "Winter Overhauls", 0.9, 1},
		{"a long key with an extra word sits just under the bar", "Winter Overhaul", "Winter Overhaul Redux", 0.7, adoptMatchThreshold},
		{"a sequel digit is refused however close the rest is", "Sim Settlements 2", "Sim Settlements 3", 0, 0},
		{"unrelated names score low", "SkyUI", "Realistic Needs", 0, 0.3},
		{"an empty name never matches", "", "SkyUI", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := adoptNameSimilarity(tc.a, tc.b)
			assert.GreaterOrEqual(t, got, tc.wantAtLeast)
			assert.LessOrEqual(t, got, tc.wantAtMost)
		})
	}
}

// TestAdoptCandidateScore covers the whole rule: name similarity, plus the
// version-agreement bonus that can lift a near-miss over the threshold but
// cannot rescue a genuinely different name.
func TestAdoptCandidateScore(t *testing.T) {
	tests := []struct {
		name                            string
		scannedName, scannedVersion     string
		candidateName, candidateVersion string
		wantAccepted                    bool
		wantClass                       AdoptMatchClass
	}{
		{
			name:        "an exact name is an exact match",
			scannedName: "SkyUI", candidateName: "SkyUI",
			wantAccepted: true, wantClass: AdoptMatchExact,
		},
		{
			name:        "normalisation alone makes it exact",
			scannedName: "Sky-UI", candidateName: "SkyUI",
			wantAccepted: true, wantClass: AdoptMatchExact,
		},
		{
			name:        "a plural difference is a strong match",
			scannedName: "Bigger Backpack", candidateName: "Bigger Backpacks",
			wantAccepted: true, wantClass: AdoptMatchStrong,
		},
		{
			name:        "skyui must not be adopted as skyui-flashlite (#27)",
			scannedName: "SkyUI", candidateName: "SkyUI Flashlite",
			wantAccepted: false,
		},
		{
			name:        "a matching version cannot rescue a different mod",
			scannedName: "SkyUI", scannedVersion: "5.2",
			candidateName: "SkyUI Flashlite", candidateVersion: "5.2",
			wantAccepted: false,
		},
		{
			// The pair is long enough for the ratio to be meaningful
			// (0.737) and still under the bar, so the bonus decides it.
			name:        "a matching version lifts a near-miss over the bar",
			scannedName: "Winter Overhaul", scannedVersion: "5.2",
			candidateName: "Winter Overhaul Redux", candidateVersion: "5.2",
			wantAccepted: true, wantClass: AdoptMatchProbable,
		},
		{
			name:        "the same near-miss without a version stays untracked",
			scannedName: "Winter Overhaul", candidateName: "Winter Overhaul Redux",
			wantAccepted: false,
		},
		{
			name:        "a version that disagrees adds nothing",
			scannedName: "Winter Overhaul", scannedVersion: "5.2",
			candidateName: "Winter Overhaul Redux", candidateVersion: "1.0",
			wantAccepted: false,
		},
		{
			name:        "a short name and a short suffix are different mods",
			scannedName: "SkyUI", scannedVersion: "5.2",
			candidateName: "SkyUI SE", candidateVersion: "5.2",
			wantAccepted: false,
		},
		{
			name:        "an elided subtitle is a probable match, never a strong one",
			scannedName: "Ordinator", candidateName: "Ordinator - Perks of Skyrim",
			wantAccepted: true, wantClass: AdoptMatchProbable,
		},
		{
			name:        "a sequel number is never adopted as its predecessor",
			scannedName: "Sim Settlements 2", scannedVersion: "5.2",
			candidateName: "Sim Settlements 3", candidateVersion: "5.2",
			wantAccepted: false,
		},
		{
			name:        "an unrelated name is never a match",
			scannedName: "SkyUI", candidateName: "Realistic Needs and Diseases",
			wantAccepted: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := domain.Mod{Name: tc.candidateName, Version: tc.candidateVersion}
			score := adoptCandidateScore(tc.scannedName, tc.scannedVersion, candidate)
			assert.Equal(t, tc.wantAccepted, score >= adoptMatchThreshold,
				"score %.3f against threshold %.2f", score, adoptMatchThreshold)
			if tc.wantAccepted {
				assert.Equal(t, tc.wantClass, adoptMatchClass(score))
			}
		})
	}
}

// TestAdoptBestCandidateIsDeterministic pins the tie-break order: a higher
// score first, then version agreement, then source ID, then mod ID - so two
// runs over the same catalogue never disagree.
func TestAdoptBestCandidateIsDeterministic(t *testing.T) {
	candidates := []domain.Mod{
		{ID: "9", SourceID: "zeta", Name: "Bigger Backpack"},
		{ID: "2", SourceID: "alpha", Name: "Bigger Backpack"},
		{ID: "1", SourceID: "alpha", Name: "Bigger Backpack"},
	}
	best, score := adoptBestCandidate("Bigger Backpack", "", candidates)
	require.NotNil(t, best)
	assert.InDelta(t, 1.0, score, 1e-9)
	assert.Equal(t, "alpha", best.SourceID, "the lowest source ID breaks a score tie")
	assert.Equal(t, "1", best.ID, "the lowest mod ID breaks a same-source tie")

	// Same inputs in a different order must produce the same winner.
	reordered := []domain.Mod{candidates[1], candidates[0], candidates[2]}
	best2, score2 := adoptBestCandidate("Bigger Backpack", "", reordered)
	require.NotNil(t, best2)
	assert.Equal(t, best.SourceID+"/"+best.ID, best2.SourceID+"/"+best2.ID)
	assert.Less(t, math.Abs(score-score2), 1e-9)
}

// TestAdoptBestCandidatePrefersVersionAgreement: with two equally-named
// candidates, the one whose version matches the scanned archive wins.
func TestAdoptBestCandidatePrefersVersionAgreement(t *testing.T) {
	candidates := []domain.Mod{
		{ID: "1", SourceID: "alpha", Name: "Bigger Backpack", Version: "1.0"},
		{ID: "2", SourceID: "beta", Name: "Bigger Backpack", Version: "5.2"},
	}
	best, _ := adoptBestCandidate("Bigger Backpack", "5.2", candidates)
	require.NotNil(t, best)
	assert.Equal(t, "2", best.ID, "version agreement outranks the source-ID tie-break")
}

// TestAdoptBestCandidateRejectsEveryWeakHit is the "no confident match"
// case: a catalogue full of plausible-looking near-misses yields nothing,
// so the entry stays untracked instead of being adopted as the wrong mod.
func TestAdoptBestCandidateRejectsEveryWeakHit(t *testing.T) {
	candidates := []domain.Mod{
		{ID: "1", SourceID: "alpha", Name: "SkyUI Flashlite"},
		{ID: "2", SourceID: "alpha", Name: "SkyUI Weapons Pack"},
		{ID: "3", SourceID: "beta", Name: "Skyrim Unbound"},
	}
	best, score := adoptBestCandidate("SkyUI", "", candidates)
	assert.Nil(t, best, "no candidate clears the threshold, so nothing is adopted")
	assert.Less(t, score, adoptMatchThreshold)
}

// TestAdoptBestCandidateRefusesAHeadSegmentTie is the Track C re-review's
// N5: a scanned "Alternate Start" matches the HEAD of two different real
// mods, both capped at adoptHeadSegmentCap, so the pair ties exactly and
// the source-ID/mod-ID tie-break would silently pick one of them. Two real
// mods that are equally good matches are precisely the ambiguity #27 exists
// to decline, so the entry stays untracked.
func TestAdoptBestCandidateRefusesAHeadSegmentTie(t *testing.T) {
	candidates := []domain.Mod{
		{ID: "1", SourceID: "alpha", Name: "Alternate Start - Live Another Life"},
		{ID: "2", SourceID: "alpha", Name: "Alternate Start - Realm of Lorkhan"},
	}
	best, score := adoptBestCandidate("Alternate Start", "", candidates)
	assert.Nil(t, best, "two head-segment matches tied exactly must not be resolved by mod ID")
	assert.InDelta(t, adoptHeadSegmentCap, score, 1e-9, "the nearest-miss score is still reported")

	// The same catalogue with only ONE of the two still adopts: the refusal
	// is about the ambiguity, not about head-segment matches as such.
	best, _ = adoptBestCandidate("Alternate Start", "", candidates[:1])
	require.NotNil(t, best)
	assert.Equal(t, "1", best.ID)
}

// TestAdoptBestCandidateBreaksANonHeadTie keeps the ordinary tie-break in
// place: two candidates carrying the SAME full name are the same mod listed
// twice (or on two sources), which the deterministic order resolves.
func TestAdoptBestCandidateBreaksANonHeadTie(t *testing.T) {
	candidates := []domain.Mod{
		{ID: "2", SourceID: "alpha", Name: "Bigger Backpack"},
		{ID: "1", SourceID: "alpha", Name: "Bigger Backpack"},
	}
	best, _ := adoptBestCandidate("Bigger Backpack", "", candidates)
	require.NotNil(t, best)
	assert.Equal(t, "1", best.ID)
}

// TestAdoptScoreRefusesNearNames is the first of the two realistic tables
// the Track C review supplied (review finding 2). Every row is a pair of
// DIFFERENT mods whose names are one sequel digit or one letter apart - the
// single most common near-name shape in modding - and every one of them was
// accepted by the first cut of the rule, the "Mod 2"/"Mod 3" row on the
// exact example adoptNameKey's own doc comment claimed was refused.
func TestAdoptScoreRefusesNearNames(t *testing.T) {
	tests := []struct{ scanned, candidate string }{
		{"Mod 2", "Mod 3"},
		{"Sim Settlements 2", "Sim Settlements 3"},
		{"Sim Settlements", "Sim Settlements 2"},
		{"Mod Organizer 2", "Mod Organizer 3"},
		{"XP32 Maximum Skeleton", "XP33 Maximum Skeleton"},
		{"Frostfall", "Frostfall 3"},
		{"Weapon Mod 1", "Weapon Mod 7"},
		{"Nordic UI", "Nordic UX"},
		{"Vortex", "Vertex"},
		{"Campfire", "Campsite"},
	}
	for _, tc := range tests {
		t.Run(tc.scanned+" is not "+tc.candidate, func(t *testing.T) {
			score := adoptCandidateScore(tc.scanned, "", domain.Mod{Name: tc.candidate})
			assert.Less(t, score, adoptMatchThreshold,
				"%q must not be adopted as %q (scored %.3f)", tc.scanned, tc.candidate, score)
		})
	}
}

// TestAdoptScoreSubtitledCatalogueNames is the review's second table
// (finding 3): an archive named after the short name a mod is known by,
// against the subtitled name its catalogue page actually carries. The
// head-segment rule accepts the rows whose subtitle is set off by
// punctuation and CANNOT accept the rest - a candidate that merely has more
// words in it is exactly #27's SkyUI/SkyUI Flashlite shape - so the table
// pins both outcomes rather than only the happy ones.
func TestAdoptScoreSubtitledCatalogueNames(t *testing.T) {
	tests := []struct {
		scanned, candidate string
		wantAccepted       bool
	}{
		{"iNeed", "iNeed - Food, Water and Sleep", true},
		{"HDT-SMP", "HDT-SMP (Skinned Mesh Physics)", true},
		{"Ordinator", "Ordinator - Perks of Skyrim", true},
		{"Alternate Start", "Alternate Start - Live Another Life", true},
		{"A Quality World Map", "A Quality World Map - Classic", true},
		{"Immersive Citizens", "Immersive Citizens: AI Overhaul", true},
		// No separator: the extra words are part of the name itself, and
		// nothing distinguishes these from "SkyUI"/"SkyUI Flashlite".
		{"RaceMenu", "RaceMenu Special Edition", false},
		{"Cathedral Weathers", "Cathedral Weathers and Seasons", false},
		{"Unofficial Skyrim Patch", "Unofficial Skyrim Special Edition Patch", false},
		// #27's own case must survive the new rule untouched.
		{"SkyUI", "SkyUI Flashlite", false},
		{"SkyUI", "SkyUI Weapons Pack", false},
	}
	for _, tc := range tests {
		t.Run(tc.scanned+" / "+tc.candidate, func(t *testing.T) {
			score := adoptCandidateScore(tc.scanned, "", domain.Mod{Name: tc.candidate})
			accepted := score >= adoptMatchThreshold
			assert.Equal(t, tc.wantAccepted, accepted, "scored %.3f", score)
			if accepted {
				assert.Equal(t, AdoptMatchProbable, adoptMatchClass(score),
					"an elided subtitle is never better than a probable match")
			}
		})
	}
}
