package serve

// api_js_constants_internal_test.go pins #333 Minor #12: the SPA's own
// maxUploadBytes mirrors the server's real cap only in comment - nothing
// ties the two together, so a change to one silently drifts from the
// other, turning a friendly client-side pre-check into a wrong one.
// Options.MaxUploadBytes stays test-only (server.go's own doc comment):
// the wire carries no such field, so this reads the SPA's literal
// straight out of api.js and compares it against the real production
// constant instead.

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// maxUploadBytesLiteral matches api.js's own maxUploadBytes declaration -
// a product of integer factors (2 * 1024 * 1024 * 1024), never a single
// literal, so the source stays as readable as the Go constant's own "2 <<
// 30 // 2 GiB" comment.
var maxUploadBytesLiteral = regexp.MustCompile(`(?m)^export const maxUploadBytes = ([0-9* ]+);`)

// TestSPAMaxUploadBytesMatchesTheServerConstant reads
// spa/app/api.js's maxUploadBytes literal and multiplies out its factors,
// then compares the result against uploads.go's own maxUploadBytes -
// byte for byte, not just "both look like 2 GiB".
func TestSPAMaxUploadBytesMatchesTheServerConstant(t *testing.T) {
	data, err := os.ReadFile("spa/app/api.js")
	require.NoError(t, err)

	m := maxUploadBytesLiteral.FindSubmatch(data)
	require.NotNil(t, m, "spa/app/api.js must declare \"export const maxUploadBytes = ...;\"")

	spaValue := int64(1)
	for _, factor := range strings.Split(string(m[1]), "*") {
		n, err := strconv.ParseInt(strings.TrimSpace(factor), 10, 64)
		require.NoError(t, err, "factor %q in api.js's maxUploadBytes", factor)
		spaValue *= n
	}

	require.Equal(t, int64(maxUploadBytes), spaValue,
		"spa/app/api.js's maxUploadBytes (%d) has drifted from uploads.go's maxUploadBytes (%d)", spaValue, maxUploadBytes)
}

// motionBaseCSS matches app.css's --motion-base token declaration - the
// duration every "arriving" animation in this UI runs at. Anchored at the
// two-space indent of the top-level :root block on purpose: the
// prefers-reduced-motion override declares the same token, one level
// deeper, at 0ms, and matching THAT would make this test assert that the
// application animates nothing.
var motionBaseCSS = regexp.MustCompile(`(?m)^  --motion-base: (\d+)ms;`)

// motionExitJS matches spa/app/motion.js's own copy of it.
var motionExitJS = regexp.MustCompile(`(?m)^const EXIT_MILLIS = (\d+);`)

// TestSPAExitAnimationMatchesTheMotionToken pins the one duplication issue
// 334's motion pass could not avoid.
//
// The slide-over's exit needs a TIMER, because the thing that closes it is
// a route change Preact answers by unmounting the subtree - so the panel has
// to be held on screen for as long as the CSS animation runs. That means
// motion.js carries a millisecond count that app.css also declares, and
// nothing but this test stops the two from drifting into a panel that
// vanishes mid-animation (or lingers after it).
func TestSPAExitAnimationMatchesTheMotionToken(t *testing.T) {
	css, err := os.ReadFile("spa/app.css")
	require.NoError(t, err)
	js, err := os.ReadFile("spa/app/motion.js")
	require.NoError(t, err)

	cssMatch := motionBaseCSS.FindSubmatch(css)
	require.NotNil(t, cssMatch, "spa/app.css must declare --motion-base in milliseconds")
	jsMatch := motionExitJS.FindSubmatch(js)
	require.NotNil(t, jsMatch, "spa/app/motion.js must declare EXIT_MILLIS")

	require.Equal(t, string(cssMatch[1]), string(jsMatch[1]),
		"motion.js's EXIT_MILLIS (%sms) has drifted from app.css's --motion-base (%sms)",
		jsMatch[1], cssMatch[1])
}
