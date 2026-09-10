package httpclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The AuthQueryParam and DoForm additions (#269) exist for Steam's Web API,
// which takes its key as a query parameter and its arguments as a form POST.
// Both must leave every existing client untouched: the tests here pin the
// new behaviour, and TestAuthHeaderClientNeverGainsAQueryParam pins the
// absence of the new one on a header-auth client.

func TestDoForm_PostsURLEncodedBodyAndDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "/ISteamRemoteStorage/GetPublishedFileDetails/v1/", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		form, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		assert.Equal(t, "2", form.Get("itemcount"))
		assert.Equal(t, "111", form.Get("publishedfileids[0]"))
		assert.Equal(t, "222", form.Get("publishedfileids[1]"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"resultcount":2}}`))
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:        srv.URL,
		AuthQueryParam: "key",
		AuthLabel:      "Steam",
	})

	form := url.Values{}
	form.Set("itemcount", "2")
	form.Set("publishedfileids[0]", "111")
	form.Set("publishedfileids[1]", "222")

	var out struct {
		Response struct {
			ResultCount int `json:"resultcount"`
		} `json:"response"`
	}
	require.NoError(t, c.DoForm(context.Background(), "/ISteamRemoteStorage/GetPublishedFileDetails/v1/", form, &out))
	assert.Equal(t, 2, out.Response.ResultCount)
}

func TestDoForm_InjectsAuthQueryParamWhenKeySet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "secret", r.URL.Query().Get("key"))
		assert.Empty(t, r.Header.Get("key"), "a query-param source must not also send a header")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:        srv.URL,
		APIKey:         "secret",
		AuthQueryParam: "key",
		AuthLabel:      "Steam",
	})
	var out struct{}
	require.NoError(t, c.DoForm(context.Background(), "/q", url.Values{}, &out))
}

func TestDoForm_OmitsAuthQueryParamWhenKeyEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, has := r.URL.Query()["key"]
		assert.False(t, has, "a keyless call carries no empty key= parameter")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:        srv.URL,
		AuthQueryParam: "key",
		AuthLabel:      "Steam",
	})
	var out struct{}
	require.NoError(t, c.DoForm(context.Background(), "/q", url.Values{}, &out))
}

func TestDoForm_PreservesAPathThatAlreadyCarriesAQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "480", q.Get("appid"))
		assert.Equal(t, "secret", q.Get("key"))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:        srv.URL,
		APIKey:         "secret",
		AuthQueryParam: "key",
		AuthLabel:      "Steam",
	})
	var out struct{}
	require.NoError(t, c.DoForm(context.Background(), "/q?appid=480", url.Values{}, &out))
}

func TestDoForm_401MapsToErrAuthRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL: srv.URL, AuthQueryParam: "key", AuthLabel: "Steam",
	})
	var out struct{}
	err := c.DoForm(context.Background(), "/q", url.Values{}, &out)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrAuthRequired))
	assert.Contains(t, err.Error(), "Steam API key required")
}

func TestDoForm_ErrorMapperSeesStatusAndBody(t *testing.T) {
	sentinel := errors.New("mapped")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Please verify your key= parameter"))
	}))
	defer srv.Close()

	var sawStatus int
	var sawBody string
	c := httpclient.New(httpclient.Options{
		BaseURL: srv.URL, AuthQueryParam: "key", AuthLabel: "Steam",
		ErrorMapper: func(status int, body []byte, _ string) error {
			sawStatus, sawBody = status, string(body)
			return sentinel
		},
	})
	var out struct{}
	err := c.DoForm(context.Background(), "/q", url.Values{}, &out)
	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, http.StatusForbidden, sawStatus)
	assert.Contains(t, sawBody, "verify your key=")
}

func TestDoForm_ContextCancellationPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := httpclient.New(httpclient.Options{BaseURL: srv.URL, AuthQueryParam: "key", AuthLabel: "Steam"})
	var out struct{}
	require.Error(t, c.DoForm(ctx, "/q", url.Values{}, &out))
}

func TestNew_AcceptsEitherAuthHeaderOrAuthQueryParam(t *testing.T) {
	assert.NotPanics(t, func() {
		httpclient.New(httpclient.Options{BaseURL: "http://x", AuthQueryParam: "key", AuthLabel: "Steam"})
	})
	assert.Panics(t, func() {
		httpclient.New(httpclient.Options{BaseURL: "http://x", AuthLabel: "Steam"})
	}, "neither auth mechanism is still a programming error")
}

// TestAuthHeaderClientNeverGainsAQueryParam is the "leaves every existing
// client byte-for-byte unchanged" proof for AuthQueryParam: a client
// configured the way NexusMods and CurseForge configure theirs sends its key
// in the header and nowhere else, on both DoJSON and DoForm.
func TestAuthHeaderClientNeverGainsAQueryParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "secret", r.Header.Get("apikey"))
		assert.Empty(t, r.URL.RawQuery, "a header-auth client adds no query parameters")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL: srv.URL, APIKey: "secret", AuthHeader: "apikey", AuthLabel: "Test",
	})
	var out struct{}
	require.NoError(t, c.DoJSON(context.Background(), http.MethodGet, "/", &out))
	require.NoError(t, c.DoForm(context.Background(), "/", url.Values{}, &out))
}
