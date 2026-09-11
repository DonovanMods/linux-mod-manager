package httpclient_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStreamClient builds a client against srv with no credential - the
// Thunderstore shape (#360), which is the only caller DoStream has.
func newStreamClient(t *testing.T, baseURL string) *httpclient.Client {
	t.Helper()
	return httpclient.New(httpclient.Options{
		BaseURL:    baseURL,
		AuthHeader: "unused",
		AuthLabel:  "Test",
	})
}

// TestDoStream_HandsBackAnOpenBody pins the reason this method exists: the
// caller reads the body itself, incrementally, instead of the client
// reading it all into memory to decode.
func TestDoStream_HandsBackAnOpenBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/c/lethal-company/api/v1/package/", r.URL.Path)
		assert.Equal(t, "Wed, 10 Sep 2026 00:00:00 GMT", r.Header.Get("If-Modified-Since"))
		w.Header().Set("Last-Modified", "Wed, 10 Sep 2026 12:00:00 GMT")
		_, _ = w.Write([]byte(`[{"one":1}]`))
	}))
	defer srv.Close()

	c := newStreamClient(t, srv.URL)
	resp, err := c.DoStream(t.Context(), http.MethodGet, "/c/lethal-company/api/v1/package/", map[string]string{
		"If-Modified-Since": "Wed, 10 Sep 2026 00:00:00 GMT",
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "Wed, 10 Sep 2026 12:00:00 GMT", resp.Header.Get("Last-Modified"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"one":1}]`, string(body))
}

// TestDoStream_304IsAnAnswerNotAnError pins the one non-2xx status DoStream
// hands back rather than mapping: a conditional GET asks for it, and a
// caller that could not see it would re-download an unchanged document.
func TestDoStream_304IsAnAnswerNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	resp, err := newStreamClient(t, srv.URL).DoStream(t.Context(), http.MethodGet, "/x", nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusNotModified, resp.StatusCode)
}

// TestDoStream_MapsFailuresLikeDoJSON pins that a stream request is not a
// second error contract: a non-2xx that is not 304 comes back as the same
// "API error (status N)" DoJSON produces, body capped.
func TestDoStream_MapsFailuresLikeDoJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no such community"))
	}))
	defer srv.Close()

	resp, err := newStreamClient(t, srv.URL).DoStream(t.Context(), http.MethodGet, "/c/nope/api/v1/package/", nil)
	require.Error(t, err)
	assert.Nil(t, resp, "a mapped failure closes the body itself; there is nothing for the caller to close")
	assert.Contains(t, err.Error(), "404")
	assert.Contains(t, err.Error(), "no such community")
}

// TestDoStream_ContextCancellationPropagates pins that a cancelled caller
// stops the request rather than the transfer.
func TestDoStream_ContextCancellationPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := newStreamClient(t, srv.URL).DoStream(ctx, http.MethodGet, "/x", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestDoStream_RefusesABodyPastTheCap is T1 review #6. DoStream is the one
// call in lmm that writes an unvalidated response body straight to disk,
// and nothing bounded it: no byte cap, no free-space check, only a
// ten-minute timeout - which on a domestic link is tens of gigabytes. A
// stream cannot be capped by reading it all first, but it can be capped by
// an io.LimitReader, and the overrun has to be a TYPED error rather than a
// truncation the decoder reports as a malformed document.
func TestDoStream_RefusesABodyPastTheCap(t *testing.T) {
	body := make([]byte, 4096)
	for i := range body {
		body[i] = 'x'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	t.Run("a body over the cap", func(t *testing.T) {
		c := httpclient.New(httpclient.Options{
			BaseURL: srv.URL, AuthHeader: "unused", AuthLabel: "Test",
			MaxResponseBytes: 1024,
		})
		resp, err := c.DoStream(t.Context(), http.MethodGet, "/", nil)
		require.NoError(t, err, "the cap is enforced while READING, not before the response exists")
		defer func() { _ = resp.Body.Close() }()

		read, err := io.ReadAll(resp.Body)
		require.Error(t, err)
		assert.ErrorIs(t, err, httpclient.ErrResponseTooLarge)
		assert.LessOrEqual(t, len(read), 1025, "no more than the cap (plus the byte that proved the overrun) is read")
	})

	t.Run("a body exactly at the cap", func(t *testing.T) {
		c := httpclient.New(httpclient.Options{
			BaseURL: srv.URL, AuthHeader: "unused", AuthLabel: "Test",
			MaxResponseBytes: int64(len(body)),
		})
		resp, err := c.DoStream(t.Context(), http.MethodGet, "/", nil)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		read, err := io.ReadAll(resp.Body)
		require.NoError(t, err, "exactly at the cap is not over it")
		assert.Len(t, read, len(body))
	})

	// A body of EXACTLY cap+1 used to be indistinguishable from one at the
	// cap for a consumer that stops as soon as its document is complete -
	// a json.Decoder reading a JSON array stops at the closing "]", so the
	// read that reported the overrun was the read it never made (#409,
	// T1 re-review nit 3). The rule that makes the cap exact whoever the
	// consumer is: the byte past the cap is never HANDED OVER, so a
	// document that needs it is incomplete and the decode fails.
	t.Run("the byte past the cap is never delivered", func(t *testing.T) {
		c := httpclient.New(httpclient.Options{
			BaseURL: srv.URL, AuthHeader: "unused", AuthLabel: "Test",
			MaxResponseBytes: int64(len(body)) - 1,
		})
		resp, err := c.DoStream(t.Context(), http.MethodGet, "/", nil)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		delivered, readErr := 0, error(nil)
		buf := make([]byte, 97) // a size that does not divide the body
		for readErr == nil {
			var n int
			n, readErr = resp.Body.Read(buf)
			delivered += n
		}
		assert.ErrorIs(t, readErr, httpclient.ErrResponseTooLarge)
		assert.LessOrEqual(t, delivered, len(body)-1,
			"a body one byte over the cap must not hand the caller a whole document")
	})

	t.Run("no cap configured", func(t *testing.T) {
		c := newStreamClient(t, srv.URL)
		resp, err := c.DoStream(t.Context(), http.MethodGet, "/", nil)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		read, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Len(t, read, len(body))
	})
}
