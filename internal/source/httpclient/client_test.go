package httpclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/httpclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoJSON_InjectsAuthHeaderAndDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "secret", r.Header.Get("apikey"), "auth header forwarded")
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "/v1/ping", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"value":42}`))
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:    srv.URL,
		APIKey:     "secret",
		AuthHeader: "apikey",
		AuthLabel:  "Test",
	})

	var out struct {
		OK    bool `json:"ok"`
		Value int  `json:"value"`
	}
	require.NoError(t, c.DoJSON(context.Background(), http.MethodGet, "/v1/ping", &out))
	assert.True(t, out.OK)
	assert.Equal(t, 42, out.Value)
}

func TestDoJSON_OmitsAuthHeaderWhenKeyEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("apikey"))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:    srv.URL,
		AuthHeader: "apikey",
		AuthLabel:  "Test",
	})
	var out struct{}
	require.NoError(t, c.DoJSON(context.Background(), http.MethodGet, "/", &out))
}

func TestDoJSON_401MapsToErrAuthRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:    srv.URL,
		AuthHeader: "apikey",
		AuthLabel:  "TestSource",
	})

	var out struct{}
	err := c.DoJSON(context.Background(), http.MethodGet, "/", &out)
	require.Error(t, err)
	require.ErrorIs(t, err, domain.ErrAuthRequired)
	assert.Contains(t, err.Error(), "TestSource API key required")
}

func TestDoJSON_NonOKReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream exploded"))
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:    srv.URL,
		AuthHeader: "apikey",
		AuthLabel:  "Test",
	})

	var out struct{}
	err := c.DoJSON(context.Background(), http.MethodGet, "/", &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 500")
	assert.Contains(t, err.Error(), "upstream exploded")
}

func TestDoJSON_ErrorMapperShortCircuitsBeforeDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("missing"))
	}))
	defer srv.Close()

	mapped := errors.New("source-specific not found")

	c := httpclient.New(httpclient.Options{
		BaseURL:    srv.URL,
		AuthHeader: "apikey",
		AuthLabel:  "Test",
		ErrorMapper: func(status int, body []byte, path string) error {
			if status == http.StatusNotFound {
				return mapped
			}
			return nil
		},
	})

	var out struct{}
	err := c.DoJSON(context.Background(), http.MethodGet, "/missing", &out)
	require.ErrorIs(t, err, mapped)
}

func TestDoJSON_ErrorMapperReturningNilFallsThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:    srv.URL,
		AuthHeader: "apikey",
		AuthLabel:  "Test",
		// Mapper says "not my concern" by returning nil; default 401 mapping kicks in.
		ErrorMapper: func(status int, body []byte, path string) error { return nil },
	})

	var out struct{}
	err := c.DoJSON(context.Background(), http.MethodGet, "/", &out)
	require.ErrorIs(t, err, domain.ErrAuthRequired)
}

func TestDoJSON_ContextCancellationPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{
		BaseURL:    srv.URL,
		AuthHeader: "apikey",
		AuthLabel:  "Test",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out struct{}
	err := c.DoJSON(ctx, http.MethodGet, "/", &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}
