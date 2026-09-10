package httpclient_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A query-parameter auth client puts the user's API key in the request URL,
// and net/http reports a transport failure as a *url.Error whose Error()
// embeds the RESOLVED url. Every error this package returns is rendered to
// a terminal, written into an HTTP error envelope and pasted into bug
// reports, so the key must not be in any of them (W2 review, Critical 1).
//
// The failure is a transport one on purpose: the 4xx paths were already
// covered, and they are not where the leak was.

const secretKey = "SUPERSECRETKEY"

// closedPortURL returns a base URL nothing is listening on, so the very
// next request against it fails in the transport rather than reaching a
// server.
func closedPortURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return "http://" + addr
}

func TestDoJSON_ATransportFailureNeverEchoesTheAPIKey(t *testing.T) {
	c := httpclient.New(httpclient.Options{
		BaseURL:        closedPortURL(t),
		APIKey:         secretKey,
		AuthQueryParam: "key",
		AuthLabel:      "Steam",
	})

	var out struct{}
	err := c.DoJSON(context.Background(), "GET", "/IPublishedFileService/QueryFiles/v1/?appid=480", &out)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretKey, "the API key must never travel out inside an error")
	assert.NotContains(t, err.Error(), "key=", "nor the parameter that carries it")
	assert.Contains(t, err.Error(), "/IPublishedFileService/QueryFiles/v1/",
		"the caller-supplied path names the request instead")
}

func TestDoForm_ATransportFailureNeverEchoesTheAPIKey(t *testing.T) {
	c := httpclient.New(httpclient.Options{
		BaseURL:        closedPortURL(t),
		APIKey:         secretKey,
		AuthQueryParam: "key",
		AuthLabel:      "Steam",
	})

	var out struct{}
	err := c.DoForm(context.Background(), "/ISteamRemoteStorage/GetCollectionDetails/v1/", url.Values{}, &out)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretKey)
	assert.Contains(t, err.Error(), "/ISteamRemoteStorage/GetCollectionDetails/v1/")
}

func TestDoJSON_AnUnbuildableRequestNeverEchoesTheAPIKey(t *testing.T) {
	// A base URL http.NewRequest cannot parse fails BEFORE the transport,
	// and url.Parse's own error is a *url.Error carrying the same resolved
	// url — the second place the key could escape.
	c := httpclient.New(httpclient.Options{
		BaseURL:        "http://exa mple.invalid",
		APIKey:         secretKey,
		AuthQueryParam: "key",
		AuthLabel:      "Steam",
	})

	var out struct{}
	err := c.DoJSON(context.Background(), "GET", "/q", &out)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretKey)
}

func TestDoJSON_AHeaderAuthClientStillNamesThePathOnATransportFailure(t *testing.T) {
	// The redaction rewrites the message for every source, not only the
	// query-param ones, so pin what a header-auth caller now reads.
	c := httpclient.New(httpclient.Options{
		BaseURL:    closedPortURL(t),
		APIKey:     secretKey,
		AuthHeader: "apikey",
		AuthLabel:  "NexusMods",
	})

	var out struct{}
	err := c.DoJSON(context.Background(), "GET", "/v1/games.json", &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "executing request to /v1/games.json")
	assert.NotContains(t, err.Error(), secretKey)
	assert.True(t, strings.Contains(err.Error(), "connect") || strings.Contains(err.Error(), "refused"),
		"the underlying transport error is still reported: %v", err)
}

func TestDoJSON_ASuccessfulCallIsUnaffectedByTheRedaction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, secretKey, r.URL.Query().Get("key"))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:        srv.URL,
		APIKey:         secretKey,
		AuthQueryParam: "key",
		AuthLabel:      "Steam",
	})
	var out struct{ OK bool }
	require.NoError(t, c.DoJSON(context.Background(), "GET", "/q", &out))
	assert.True(t, out.OK)
}
