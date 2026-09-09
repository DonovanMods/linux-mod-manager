package serve_test

// The colour-contrast ratchet (issue 334's a11y pass).
//
// TestNoHardcodedColors already guarantees every visual colour in this
// application comes from one of app.css's token sets. This one guarantees
// those tokens are actually READABLE - in BOTH of them. A token set is the
// right unit for that: a contrast failure in a token is a failure at every
// one of the dozens of sites that paints with it, so fixing the token fixes
// them all, and testing the token catches the failure before any site does.
//
// It reads app.css directly rather than measuring a rendered page. A
// browser measurement would only ever cover the elements that happened to
// be on screen in that scenario, which is precisely the coverage gap a
// design-token system exists to close - and it would need a browser, so it
// would skip on a machine without one.
//
// The bar is WCAG 2.1 AA for normal text: 4.5:1. Non-text indicators
// (borders, the focus ring) are held to 3:1, level AA's own bar for
// "non-text contrast" (SC 1.4.11) - a 4.5 ring would force the accent
// darker than the design's palette, for a requirement that does not exist.

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three token blocks app.css defines, in the order its own header
// comment lists them: the light baseline, the system-dark override, and the
// user's explicit dark override.
var (
	lightBlock     = regexp.MustCompile(`(?s):root \{.*?\n\}`)
	systemDarkBloc = regexp.MustCompile(`(?s)@media \(prefers-color-scheme: dark\) \{\s*:root:not\(\[data-theme="light"\]\) \{.*?\n  \}\n\}`)
	userDarkBlock  = regexp.MustCompile(`(?s):root\[data-theme="dark"\] \{.*?\n\}`)

	hexToken = regexp.MustCompile(`(--[a-z-]+):\s*(#[0-9a-fA-F]{6})\s*;`)
)

// textPairs are the foreground/background token combinations this
// application actually paints TEXT in. Each is a real pair, not a
// cross-product: the surfaces are the four app.css defines, and every text
// and semantic colour is rendered on at least one of them somewhere (a card
// title on --surface-raised, a menu item on --surface-overlay, a table's
// zebra row and every input on --surface-sunken, the page itself on
// --surface-base). Testing the full grid rather than tracking which colour
// meets which surface today is deliberate: it means MOVING a component from
// one surface to another can never introduce an unreadable pairing.
var textPairs = struct {
	foregrounds []string
	backgrounds []string
	explicit    [][2]string
}{
	foregrounds: []string{
		"--text-primary", "--text-secondary", "--text-muted",
		"--accent", "--accent-hover", "--good", "--warn", "--danger",
	},
	backgrounds: []string{
		"--surface-base", "--surface-raised", "--surface-overlay", "--surface-sunken",
	},
	// The pairs that are not foreground-on-surface: text painted on a
	// SEMANTIC fill. --accent-contrast exists for exactly one job (the
	// primary button's label, and the activity bell's count badge) and
	// --text-inverted for one more (the failed-jobs badge on --danger),
	// so each is asserted against the fill it was created for - including
	// the hover state, which is a different colour and just as readable a
	// surface must be.
	explicit: [][2]string{
		{"--accent-contrast", "--accent"},
		{"--accent-contrast", "--accent-hover"},
		{"--text-inverted", "--danger"},
	},
}

// nonTextPairs are the indicator colours: SC 1.4.11's 3:1, not 4.5:1.
//
// --border-strong is in here because it is the ONLY thing that identifies
// several controls as controls: a button's fill is --surface-raised on a
// --surface-base page and an input's is --surface-sunken, neither of which
// differs from its background by anything a person could see. Its sibling
// --border-subtle is deliberately absent - it draws dividers between things
// that are already obviously separate (a card's edge, a list's rules),
// which is decoration, and holding it to 3:1 would make it a second
// --border-strong under a name that promised otherwise.
var nonTextPairs = [][2]string{
	{"--focus-ring", "--surface-base"},
	{"--focus-ring", "--surface-raised"},
	{"--focus-ring", "--surface-overlay"},
	{"--focus-ring", "--surface-sunken"},
	{"--border-strong", "--surface-base"},
	{"--border-strong", "--surface-raised"},
	{"--border-strong", "--surface-overlay"},
	{"--border-strong", "--surface-sunken"},
}

// TestThemeTokenContrast holds both Launcher token sets to WCAG AA.
func TestThemeTokenContrast(t *testing.T) {
	css := readAppCSS(t)

	for _, set := range []struct {
		name   string
		tokens map[string]string
	}{
		{"light", tokensIn(t, css, lightBlock)},
		{"dark", tokensIn(t, css, userDarkBlock)},
	} {
		t.Run(set.name, func(t *testing.T) {
			for _, fg := range textPairs.foregrounds {
				for _, bg := range textPairs.backgrounds {
					assertContrast(t, set.tokens, fg, bg, 4.5)
				}
			}
			for _, pair := range textPairs.explicit {
				assertContrast(t, set.tokens, pair[0], pair[1], 4.5)
			}
			for _, pair := range nonTextPairs {
				assertContrast(t, set.tokens, pair[0], pair[1], 3.0)
			}
		})
	}
}

// TestDarkTokenSetsAgree pins the one duplication app.css cannot avoid:
// the dark palette is written out TWICE, once under
// prefers-color-scheme and once under the explicit [data-theme="dark"]
// override, because a media query cannot be re-scoped by an attribute
// selector without repeating its body. Nothing but this test stops the two
// from drifting - and a drift would mean the app looks different depending
// on whether the user picked dark or merely runs a dark system, which is
// the kind of bug nobody reports because everyone only ever sees one half.
func TestDarkTokenSetsAgree(t *testing.T) {
	css := readAppCSS(t)
	system := tokensIn(t, css, systemDarkBloc)
	user := tokensIn(t, css, userDarkBlock)

	assert.Equal(t, user, system,
		"app.css's two dark token blocks must define the same values")
}

func readAppCSS(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("spa/app.css")
	require.NoError(t, err)
	return string(data)
}

// tokensIn returns every `--name: #rrggbb;` declaration inside the first
// block re matches. Non-hex values (the rgb()-with-alpha scrim and shadow,
// the font stacks, the spacing scale) are skipped: they carry no colour a
// contrast ratio is defined over.
func tokensIn(t *testing.T, css string, re *regexp.Regexp) map[string]string {
	t.Helper()
	block := re.FindString(css)
	require.NotEmpty(t, block, "app.css must contain the token block %s", re)

	tokens := map[string]string{}
	for _, m := range hexToken.FindAllStringSubmatch(block, -1) {
		tokens[m[1]] = strings.ToLower(m[2])
	}
	require.NotEmpty(t, tokens)
	return tokens
}

func assertContrast(t *testing.T, tokens map[string]string, fg, bg string, want float64) {
	t.Helper()
	fgHex, ok := tokens[fg]
	require.True(t, ok, "app.css defines no %s in this token set", fg)
	bgHex, ok := tokens[bg]
	require.True(t, ok, "app.css defines no %s in this token set", bg)

	got := contrastRatio(t, fgHex, bgHex)
	assert.GreaterOrEqualf(t, got, want,
		"%s (%s) on %s (%s) is %.2f:1, below the %.1f:1 bar - fix the TOKEN, not the sites that use it",
		fg, fgHex, bg, bgHex, got, want)
}

// contrastRatio is WCAG 2.1's own formula: (L1+0.05)/(L2+0.05) over the
// two relative luminances, lighter first.
func contrastRatio(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := relativeLuminance(t, a), relativeLuminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// relativeLuminance is WCAG 2.1's definition for an sRGB colour.
func relativeLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	channels := [3]float64{}
	for i := range channels {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		require.NoError(t, err, "parsing %s", hex)
		c := float64(v) / 255
		if c <= 0.03928 {
			channels[i] = c / 12.92
		} else {
			channels[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
}
