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
