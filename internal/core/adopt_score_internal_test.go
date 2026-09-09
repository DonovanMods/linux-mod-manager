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
		{"a short suffix sits just under the bar", "SkyUI", "SkyUI SE", 0.6, adoptMatchThreshold},
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
			name:        "a matching version lifts a near-miss over the bar",
			scannedName: "SkyUI", scannedVersion: "5.2",
			candidateName: "SkyUI SE", candidateVersion: "5.2",
			wantAccepted: true, wantClass: AdoptMatchProbable,
		},
		{
			name:        "the same near-miss without a version stays untracked",
			scannedName: "SkyUI", candidateName: "SkyUI SE",
			wantAccepted: false,
		},
		{
			name:        "a version that disagrees adds nothing",
			scannedName: "SkyUI", scannedVersion: "5.2",
			candidateName: "SkyUI SE", candidateVersion: "1.0",
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
