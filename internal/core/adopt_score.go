package core

import (
	"strings"
	"unicode"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The adopt matcher's scoring rule (#27).
//
// `lmm import` scan mode used to adopt the FIRST hit the first searchable
// source returned for an untracked archive's detected name. That is right
// most of the time and badly wrong the rest of it: searching "skyui"
// happily returns "SkyUI Flashlite" first, and the archive would then be
// tracked as a different mod, with a different version history and a
// different update target - a mistake the user has no obvious way to see.
//
// Every candidate is now scored against the scanned name, and a candidate
// that does not clear adoptMatchThreshold is not a match at all: the entry
// stays UNTRACKED and is adopted as a local mod, which is recoverable,
// rather than mis-attributed, which is not. Under-matching is the
// deliberate failure mode.
const (
	// adoptMatchThreshold is the score a candidate must reach to be
	// accepted. 0.75 is chosen against the cases in
	// adopt_score_internal_test.go: it accepts a spelling or plural
	// difference ("Bigger Backpack" / "Bigger Backpacks", 0.93) and refuses
	// a name that has a whole extra word in it ("SkyUI" / "SkyUI
	// Flashlite", 0.36) - #27's own example.
	adoptMatchThreshold = 0.75

	// adoptStrongThreshold separates AdoptMatchStrong from
	// AdoptMatchProbable. Above it the two names differ only in spelling;
	// below it something real differs and the match rests partly on the
	// version agreeing.
	adoptStrongThreshold = 0.9

	// adoptVersionAgreementBonus is how much of the remaining distance to
	// certainty a matching version closes. A scanned archive and a
	// candidate that agree on BOTH a near-identical name and an exact
	// version are much likelier to be the same mod than the name alone
	// suggests, so "SkyUI 5.2" can be adopted as the source's "SkyUI SE"
	// 5.2 (0.71 -> 0.79). Deliberately small: a quarter of the gap cannot
	// lift a genuinely different name over the bar - "SkyUI" against "SkyUI
	// Flashlite" 5.2 only reaches 0.52 and is still refused.
	adoptVersionAgreementBonus = 0.25
)

// AdoptMatchClass is how confident an adopt match is - the score band a
// frontend renders instead of a bare number (#27). Empty on an AdoptMatch
// with no Mod: nothing was matched, so there is no confidence to report.
type AdoptMatchClass string

// The adopt match confidence bands. Every accepted match carries exactly
// one; see adoptMatchClass for their boundaries.
const (
	// AdoptMatchExact means the two names are identical once normalised -
	// case, spacing and punctuation aside, the same name.
	AdoptMatchExact AdoptMatchClass = "exact"
	// AdoptMatchStrong means the names differ only in spelling (a plural, a
	// typo): adoptStrongThreshold or better.
	AdoptMatchStrong AdoptMatchClass = "strong"
	// AdoptMatchProbable means the match cleared adoptMatchThreshold but
	// not adoptStrongThreshold - accepted, and the one band worth showing a
	// user before they confirm an adopt.
	AdoptMatchProbable AdoptMatchClass = "probable"
)

// adoptMatchClass names the band score falls in. Only called for a score
// that already cleared adoptMatchThreshold.
func adoptMatchClass(score float64) AdoptMatchClass {
	switch {
	case score >= 1:
		return AdoptMatchExact
	case score >= adoptStrongThreshold:
		return AdoptMatchStrong
	default:
		return AdoptMatchProbable
	}
}

// adoptNameKey normalises a mod name for comparison: lowercased, with every
// rune that is not a letter or a digit removed, so "SkyUI 5.2 (SE)",
// "skyui-5.2-se" and "Sky UI 5.2 SE" all compare identically. Digits are
// KEPT: "Mod 2" and "Mod 3" are different mods, and the whole rule leans
// towards refusing an uncertain match.
func adoptNameKey(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// adoptNameSimilarity scores two mod names from 0 (nothing in common) to 1
// (identical), as 1 - levenshtein(a, b)/max(len(a), len(b)) over their
// normalised keys. Edit distance rather than token overlap because the
// differences that should still match are spelling-sized (a plural, a
// hyphen, a capital) while the differences that should not are whole extra
// words - and a ratio over the LONGER name is what makes an extra word
// expensive. An empty key on either side scores 0: there is nothing to
// compare, and a name we could not read is not evidence of anything.
func adoptNameSimilarity(a, b string) float64 {
	ka, kb := adoptNameKey(a), adoptNameKey(b)
	if ka == "" || kb == "" {
		return 0
	}
	if ka == kb {
		return 1
	}
	longest := max(len(ka), len(kb))
	return 1 - float64(levenshtein(ka, kb))/float64(longest)
}

// levenshtein is the standard edit distance between two strings, computed
// over two rolling rows. Hand-rolled rather than pulled in: it is fifteen
// lines and the project avoids a dependency it can spell out (GO.md).
// Operates on runes so a multi-byte name is not measured in bytes.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// adoptVersionsAgree reports whether a scanned archive's parsed version and
// a candidate's version are the same release. Both must be non-empty - an
// absent version on either side is not agreement - and they are compared
// with the same leading-"v" tolerance version strings are written with.
func adoptVersionsAgree(scanned, candidate string) bool {
	norm := func(v string) string {
		return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	}
	if scanned == "" || candidate == "" {
		return false
	}
	return norm(scanned) == norm(candidate)
}

// adoptCandidateScore scores one candidate against a scanned archive's
// detected name and (when the filename parser found one) version. The name
// carries the score; an agreeing version closes adoptVersionAgreementBonus
// of the remaining distance to 1.
func adoptCandidateScore(scannedName, scannedVersion string, candidate domain.Mod) float64 {
	score := adoptNameSimilarity(scannedName, candidate.Name)
	if score > 0 && adoptVersionsAgree(scannedVersion, candidate.Version) {
		score += (1 - score) * adoptVersionAgreementBonus
	}
	return score
}

// adoptBestCandidate picks the best-scoring candidate for a scanned
// archive, or nil when none of them clears adoptMatchThreshold. The score
// it returns is the BEST score seen either way, so a caller can say how
// close the nearest miss was.
//
// Ties are broken deterministically, in this order: the higher score, then
// a candidate whose version agrees with the scanned one, then the lower
// source ID, then the lower mod ID. Two runs over the same catalogue - in
// any order - therefore always adopt the same mod.
func adoptBestCandidate(scannedName, scannedVersion string, candidates []domain.Mod) (*domain.Mod, float64) {
	var best *domain.Mod
	bestScore := 0.0
	bestVersionAgrees := false

	for i := range candidates {
		c := &candidates[i]
		score := adoptCandidateScore(scannedName, scannedVersion, *c)
		agrees := adoptVersionsAgree(scannedVersion, c.Version)

		if best == nil || betterAdoptCandidate(score, agrees, *c, bestScore, bestVersionAgrees, *best) {
			best, bestScore, bestVersionAgrees = c, score, agrees
		}
	}

	if best == nil || bestScore < adoptMatchThreshold {
		return nil, bestScore
	}
	return best, bestScore
}

// betterAdoptCandidate is adoptBestCandidate's ordering, split out so the
// tie-break chain reads as one expression per rule.
func betterAdoptCandidate(score float64, agrees bool, mod domain.Mod, bestScore float64, bestAgrees bool, best domain.Mod) bool {
	if score != bestScore {
		return score > bestScore
	}
	if agrees != bestAgrees {
		return agrees
	}
	if mod.SourceID != best.SourceID {
		return mod.SourceID < best.SourceID
	}
	return mod.ID < best.ID
}
