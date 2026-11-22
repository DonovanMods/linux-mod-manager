package nexusmods

// #673: a collection profile's update check asks NexusMods for the
// collection's latest published revision - one small GraphQL request, sent
// through the same retrying client (#671) every other request uses.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ source.CollectionRevisionChecker = (*NexusMods)(nil)

func TestLatestCollectionRevision_AsksForTheLatestRevisionOnly(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, collectionLatestRevisionQuery, req.Query)
		assert.Equal(t, map[string]any{"slug": "iszwwe", "domain": "cyberpunk2077", "adult": true}, req.Variables,
			"no revision variable: NexusMods answers the latest published one")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"collectionRevision":{"revisionNumber":31,"collection":{"slug":"iszwwe","game":{"domainName":"cyberpunk2077"}}}}}`)
	}))
	t.Cleanup(srv.Close)
	nm := New(nil, "testapikey")
	nm.client.graphqlURL = srv.URL

	rev, err := nm.LatestCollectionRevision(context.Background(), "cyberpunk2077", "iszwwe")
	require.NoError(t, err)
	assert.Equal(t, 31, rev)
	assert.Equal(t, int32(1), calls.Load(), "one request")
}

func TestLatestCollectionRevision_GoesThroughTheRetryingClient(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"collectionRevision":{"revisionNumber":4,"collection":{"slug":"abcdef","game":{"domainName":"skyrimspecialedition"}}}}}`)
	}))
	t.Cleanup(srv.Close)
	nm := New(nil, "testapikey")
	nm.client.graphqlURL = srv.URL

	rev, err := nm.LatestCollectionRevision(context.Background(), "skyrimspecialedition", "abcdef")
	require.NoError(t, err)
	assert.Equal(t, 4, rev)
	assert.Equal(t, int32(2), calls.Load(), "a 429 is retried by #671's transport, not surfaced")
}

func TestLatestCollectionRevision_Refusals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Variables["slug"] {
		case "gone":
			_, _ = io.WriteString(w, `{"errors":[{"message":"Collection not found","extensions":{"code":"NOT_FOUND"}}],"data":null}`)
		case "other":
			_, _ = io.WriteString(w, `{"data":{"collectionRevision":{"revisionNumber":2,"collection":{"slug":"other","game":{"domainName":"fallout4"}}}}}`)
		default:
			_, _ = io.WriteString(w, `{"errors":[{"message":"boom"}],"data":null}`)
		}
	}))
	t.Cleanup(srv.Close)
	nm := New(nil, "testapikey")
	nm.client.graphqlURL = srv.URL
	ctx := context.Background()

	_, err := nm.LatestCollectionRevision(ctx, "skyrimspecialedition", "gone")
	require.ErrorIs(t, err, source.ErrInvalidReference)
	_, err = nm.LatestCollectionRevision(ctx, "skyrimspecialedition", "other")
	require.ErrorIs(t, err, source.ErrInvalidReference, "a collection for another game is not this one")
	_, err = nm.LatestCollectionRevision(ctx, "skyrimspecialedition", "broken")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
	_, err = nm.LatestCollectionRevision(ctx, "skyrimspecialedition", "Not A Slug!")
	require.ErrorIs(t, err, source.ErrInvalidReference, "refused before any request")
	_, err = nm.LatestCollectionRevision(ctx, "bad domain", "abcdef")
	require.ErrorIs(t, err, source.ErrGameIdentifierInvalid)
}
