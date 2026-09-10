package core

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

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
//
// The rule is a similarity ratio (adoptNameSimilarity) with three
// modifiers, each of which exists because the ratio alone got a whole class
// of real names wrong:
//
//   - Digits must agree, so a sequel number is never elided (finding 2).
//   - A key shorter than adoptShortKeyRunes must match exactly, so one
//     letter in a short name is a different mod, not a typo (finding 2).
//   - The scanned name is also compared against the candidate's HEAD
//     SEGMENT - the text before a subtitle separator - so the dominant
//     "Name - Subtitle" catalogue shape is adoptable, capped at
//     adoptHeadSegmentCap so it always reads as probable (finding 3).
//
// A candidate that merely has more WORDS in it than the archive is still
// refused - that is #27's own SkyUI/SkyUI Flashlite case, and the head
// segment rule deliberately does not reach it.
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
	// suggests, so "Winter Overhaul 1.0" can be adopted as the source's
	// "Winter Overhaul Redux" 1.0 (0.737 -> 0.803). Deliberately small: a
	// quarter of the gap cannot
	// lift a genuinely different name over the bar - "SkyUI" against "SkyUI
	// Flashlite" 5.2 only reaches 0.52 and is still refused.
	adoptVersionAgreementBonus = 0.25

	// adoptShortKeyRunes is the normalised-key length below which a single
	// edit stops being a spelling difference and becomes a different name.
	// "Vortex"/"Vertex", "Nordic UI"/"Nordic UX" and "Campfire"/"Campsite"
	// are all one or two edits apart and all score 0.75 or better as a
	// RATIO, because the ratio is only as strict as the name is long; they
	// are also three different pairs of unrelated mods. A pair whose LONGER
	// key is under twelve runes must therefore match exactly (once
	// normalised, and possibly via a head segment) for the names to count
	// as the same mod at all: "Vortex" is never "Vertex", and "SkyUI" is
	// never "SkyUI SE". The floor is deliberately gated on the longer of
	// the two, so a short scanned name is still scored as a ratio against a
	// longer candidate ("True Storms" / "True Storms SE", 0.833).
	// Twelve is where one edit costs less than the 0.9 strong band -
	// 1-1/12 = 0.917 - so it is exactly the length at which the ratio
	// starts calling a single edit "a spelling difference" on its own.
	adoptShortKeyRunes = 12

	// adoptHeadSegmentCap is the highest score a HEAD-SEGMENT match can
	// reach (see adoptHeadSegment). Matching "Ordinator" against the head
	// of "Ordinator - Perks of Skyrim" is good evidence, but the subtitle
	// was elided to get there and the user should see that before
	// confirming an adopt, so the score is held inside the probable band -
	// under adoptStrongThreshold - however well the head itself matched. A
	// version agreement lifts it to 0.888, which is still probable.
	adoptHeadSegmentCap = 0.85
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
// KEPT so that adoptDigitsAgree can read them back out of the key; keeping
// them is not by itself what separates "Mod 2" from "Mod 3" (one edit over
// a five-rune key is cheap), the digit gate in adoptNameSimilarity is.
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
// (identical), as 1 - levenshtein(a, b)/max(runes(a), runes(b)) over their
// normalised keys, subject to two outright refusals. Edit distance rather
// than token overlap because the differences that should still match are
// spelling-sized (a plural, a hyphen, a capital) while the differences that
// should not are whole extra words - and a ratio over the LONGER name is
// what makes an extra word expensive.
//
// The ratio alone is not enough, because it is only as strict as the name
// is long. Two refusals bound it (Track C review, finding 2):
//
//   - Digits must agree (adoptDigitsAgree). A sequel number is the whole
//     difference between two mods that share every other rune: "Sim
//     Settlements 2"/"Sim Settlements 3" is 0.93 as a ratio and is not the
//     same mod. Adopting an archive as the wrong sequel attaches it to the
//     wrong version history and the wrong update target, which is #27's
//     entire premise.
//   - A pair whose LONGER key is shorter than adoptShortKeyRunes must match
//     exactly. One edit in a six-rune name is "Vortex" against "Vertex",
//     not a typo of it. Gated on the longer key, so a short scanned name is
//     still scored as a ratio against a longer candidate.
//
// An empty key on either side scores 0: there is nothing to compare, and a
// name we could not read is not evidence of anything.
func adoptNameSimilarity(a, b string) float64 {
	ka, kb := adoptNameKey(a), adoptNameKey(b)
	if ka == "" || kb == "" {
		return 0
	}
	if ka == kb {
		return 1
	}
	if !adoptDigitsAgree(ka, kb) {
		return 0
	}
	longest := max(utf8.RuneCountInString(ka), utf8.RuneCountInString(kb))
	if longest < adoptShortKeyRunes {
		return 0
	}
	return 1 - float64(levenshtein(ka, kb))/float64(longest)
}

// adoptDigitsAgree reports whether two normalised keys carry the same digit
// runs, in the same order: "simsettlements2" and "simsettlements3" do not,
// "biggerbackpack" and "biggerbackpacks" (neither has any) do. Runs rather
// than individual digits so "xp32" and "xp33" differ once, not twice, and
// so "fallout4" never matches "fallout44".
func adoptDigitsAgree(ka, kb string) bool {
	runs := func(key string) []string {
		var out []string
		var cur strings.Builder
		for _, r := range key {
			if unicode.IsDigit(r) {
				cur.WriteRune(r)
				continue
			}
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		}
		if cur.Len() > 0 {
			out = append(out, cur.String())
		}
		return out
	}
	return slices.Equal(runs(ka), runs(kb))
}

// adoptHeadSegmentSeparators are the punctuation marks a catalogue name
// uses to hang a subtitle off the name a mod is actually known by:
// "Ordinator - Perks of Skyrim", "HDT-SMP (Skinned Mesh Physics)",
// "Immersive Citizens: AI Overhaul". Each is anchored on a space so an
// intra-word hyphen ("HDT-SMP") is not a separator.
var adoptHeadSegmentSeparators = []string{" - ", " \u2014 ", " \u2013 ", ": ", " ("}

// adoptHeadSegment returns the part of a catalogue name before its first
// subtitle separator, or name unchanged when it carries none. The archive
// on disk is almost always named after the short name - "Ordinator" - while
// the catalogue row it must match carries the subtitle too, and the ratio
// over the LONGER name refuses that pairing outright (0.41). Scoring
// against the head segment as well is what keeps the dominant
// "Name - Subtitle" shape adoptable (Track C review, finding 3).
//
// It deliberately does NOT strip trailing WORDS: "SkyUI Flashlite" and
// "RaceMenu Special Edition" have no separator, so nothing distinguishes
// them from #27's own case, and they stay refused. That is the rule's
// limit, not an oversight - a mod whose catalogue name simply has more
// words in it is imported as a local mod, and `lmm mod edit --source`
// re-links it.
//
// Two catalogue rows can share a head segment ("Alternate Start - Live
// Another Life" and "Alternate Start - Realm of Lorkhan"), in which case
// both score exactly adoptHeadSegmentCap against the same scanned name.
// adoptBestCandidate refuses that tie rather than resolving it on IDs (see
// its doc comment).
func adoptHeadSegment(name string) string {
	head := name
	for _, sep := range adoptHeadSegmentSeparators {
		if i := strings.Index(head, sep); i > 0 {
			head = head[:i]
		}
	}
	return strings.TrimSpace(head)
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
// carries the score: the scanned name is compared both against the
// candidate's full name and against its head segment, and the better of the
// two wins, with the head-segment result capped at adoptHeadSegmentCap so
// an elided subtitle can never read as better than a probable match. An
// agreeing version then closes adoptVersionAgreementBonus of whatever
// distance to 1 is left.
func adoptCandidateScore(scannedName, scannedVersion string, candidate domain.Mod) float64 {
	score, _ := adoptCandidateScoreVia(scannedName, scannedVersion, candidate)
	return score
}

// adoptCandidateScoreVia is adoptCandidateScore plus whether the score came
// from the candidate's HEAD SEGMENT rather than its full name. The caller
// needs that to tell an ordinary tie (the same mod listed twice) from a
// head-segment collision between two different mods, which it refuses.
func adoptCandidateScoreVia(scannedName, scannedVersion string, candidate domain.Mod) (score float64, viaHead bool) {
	score = adoptNameSimilarity(scannedName, candidate.Name)
	if head := adoptHeadSegment(candidate.Name); head != candidate.Name {
		if headScore := min(adoptNameSimilarity(scannedName, head), adoptHeadSegmentCap); headScore > score {
			score, viaHead = headScore, true
		}
	}
	if score > 0 && adoptVersionsAgree(scannedVersion, candidate.Version) {
		score += (1 - score) * adoptVersionAgreementBonus
	}
	return score, viaHead
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
//
// One tie is REFUSED rather than broken (Track C re-review, N5): when the
// winner scored through a head segment and another candidate ties it
// exactly, two different catalogue rows hang subtitles off the same name
// ("Alternate Start - Live Another Life" and "Alternate Start - Realm of
// Lorkhan") and nothing in the archive says which one it is. An ID
// tie-break would attach it to the wrong mod half the time, so the entry
// stays untracked - the same answer this rule gives every other ambiguity.
// The score is still returned, so the caller can say how close it came.
func adoptBestCandidate(scannedName, scannedVersion string, candidates []domain.Mod) (*domain.Mod, float64) {
	var best *domain.Mod
	bestScore := 0.0
	bestVersionAgrees := false
	bestViaHead := false
	tiedWithBest := false

	for i := range candidates {
		c := &candidates[i]
		score, viaHead := adoptCandidateScoreVia(scannedName, scannedVersion, *c)
		agrees := adoptVersionsAgree(scannedVersion, c.Version)

		if best == nil || betterAdoptCandidate(score, agrees, *c, bestScore, bestVersionAgrees, *best) {
			// Assigned, not OR'd: a new best that BEATS the previous one
			// ends whatever ambiguity there was, and leaving the flag set
			// refused a strictly better unique match because two worse
			// candidates had tied earlier in the list.
			tiedWithBest = best != nil && score == bestScore
			best, bestScore, bestVersionAgrees, bestViaHead = c, score, agrees, viaHead
			continue
		}
		if score == bestScore && (c.SourceID != best.SourceID || c.ID != best.ID) {
			tiedWithBest = true
		}
	}

	if best == nil || bestScore < adoptMatchThreshold {
		return nil, bestScore
	}
	if bestViaHead && tiedWithBest {
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
