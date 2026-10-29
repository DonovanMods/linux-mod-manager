package httpclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoJSON_AnonymousClientSendsOnlyItsUserAgent pins the two options the
// release check (#568) needs: an API that takes no key at all, and a
// User-Agent naming lmm instead of Go's default.
func TestDoJSON_AnonymousClientSendsOnlyItsUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "lmm/2.0.1", r.Header.Get("User-Agent"))
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Empty(t, r.URL.RawQuery)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:   srv.URL,
		Anonymous: true,
		AuthLabel: "GitHub",
		UserAgent: "lmm/2.0.1",
	})
	var out struct{}
	require.NoError(t, c.DoJSON(context.Background(), http.MethodGet, "/", &out))
}

// TestNew_AnonymousStillNeedsALabel: Anonymous lifts only the auth-method
// requirement; the label still names the API in every error.
func TestNew_AnonymousStillNeedsALabel(t *testing.T) {
	assert.Panics(t, func() {
		httpclient.New(httpclient.Options{BaseURL: "http://x", Anonymous: true})
	})
}
