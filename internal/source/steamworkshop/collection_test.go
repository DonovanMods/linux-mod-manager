package steamworkshop_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveCollection_KeylessAndReturnsChildrenInOrder(t *testing.T) {
	fx := serveRoutes(t, reply{file: "getcollectiondetails_ok.json"})
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	var resolver source.CollectionResolver = src
	got, err := resolver.ResolveCollection(context.Background(), "2500900001")
	require.NoError(t, err)

	assert.Equal(t, "2500900001", got.ID)
	assert.Equal(t, []string{"3617086610", "3512001122", "2900001111"}, got.ItemIDs)
	assert.Empty(t, fx.requests[0].Get("key"), "GetCollectionDetails is keyless")
}

// TestResolveCollection_SendsNoKeyEvenWhenOneIsRegistered pins the reason
// keyless() exists: the endpoint ignores a key, and a Steam Web API key
// names the account behind the request, so lmm sends none.
func TestResolveCollection_SendsNoKeyEvenWhenOneIsRegistered(t *testing.T) {
	fx := serveRoutes(t, reply{file: "getcollectiondetails_ok.json"})
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)
	src.SetAPIKey("registered")

	_, err := src.ResolveCollection(context.Background(), "2500900001")
	require.NoError(t, err)
	assert.Empty(t, fx.requests[0].Get("key"))
}

func TestResolveCollection_AcceptsEveryShapeAUserCanPaste(t *testing.T) {
	for name, ref := range map[string]string{
		"bare id":         "2500900001",
		"filedetails url": "https://steamcommunity.com/sharedfiles/filedetails/?id=2500900001",
		"workshop url":    "https://steamcommunity.com/workshop/filedetails/?id=2500900001&searchtext=",
		"http url":        "http://steamcommunity.com/sharedfiles/filedetails/?id=2500900001",
		"padded":          "  2500900001\n",
	} {
		t.Run(name, func(t *testing.T) {
			fx := serveRoutes(t, reply{file: "getcollectiondetails_ok.json"})
			src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)
			got, err := src.ResolveCollection(context.Background(), ref)
			require.NoError(t, err)
			assert.Equal(t, "2500900001", got.ID)
		})
	}
}

func TestResolveCollection_RefusesSomethingThatIsNotACollectionReference(t *testing.T) {
	fx := serveRoutes(t, reply{file: "getcollectiondetails_ok.json"})
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	for _, ref := range []string{"", "not-an-id", "https://example.com/?id=1", "https://steamcommunity.com/sharedfiles/filedetails/"} {
		_, err := src.ResolveCollection(context.Background(), ref)
		require.Error(t, err, "ref %q", ref)
	}
	assert.Equal(t, 0, fx.calls, "a reference lmm cannot parse never costs a request")
}

func TestResolveCollection_AnUnavailableCollectionIsAnError(t *testing.T) {
	fx := serveRoutes(t, reply{file: "getcollectiondetails_missing.json"})
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	_, err := src.ResolveCollection(context.Background(), "9999999999")
	require.ErrorIs(t, err, steamworkshop.ErrItemUnavailable)
	assert.Equal(t, 1, fx.calls)
}

// TestParseCollectionURL_OnlyRecognisesARealURL pins the narrower of the two
// parsers: the SPA offers a collection import when what the user typed into
// the SEARCH box is unmistakably a Workshop link. A bare id is not - it is a
// perfectly ordinary search term - so only ResolveCollection (whose caller
// has already said "this is a collection") accepts one.
func TestParseCollectionURL_OnlyRecognisesARealURL(t *testing.T) {
	id, ok := steamworkshop.ParseCollectionURL("https://steamcommunity.com/sharedfiles/filedetails/?id=2500900001")
	assert.True(t, ok)
	assert.Equal(t, "2500900001", id)

	for _, ref := range []string{"skyui", "2500900001", "", "https://example.com/?id=2500900001"} {
		_, ok := steamworkshop.ParseCollectionURL(ref)
		assert.False(t, ok, "%q is not a Workshop collection URL", ref)
	}
}
