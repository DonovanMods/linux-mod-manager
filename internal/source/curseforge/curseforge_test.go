package curseforge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCurseForge_ImplementsModSource(t *testing.T) {
	// Compile-time check that CurseForge implements ModSource
	var _ source.ModSource = (*CurseForge)(nil)
}

// Compile-time conformance pins: CurseForge implements the optional metadata
// interfaces the source-registry design (#76) expects of a built-in.
var (
	_ source.EnvKeyProvider           = (*CurseForge)(nil)
	_ source.KeyValidator             = (*CurseForge)(nil)
	_ source.AuthInstructionsProvider = (*CurseForge)(nil)
	_ source.GameCatalog              = (*CurseForge)(nil)
	_ source.TypeLabeler              = (*CurseForge)(nil)
	_ source.CapabilityReporter       = (*CurseForge)(nil)
)

func TestCurseForge_ID(t *testing.T) {
	cf := New(nil, "")
	assert.Equal(t, "curseforge", cf.ID())
}

func TestCurseForge_Name(t *testing.T) {
	cf := New(nil, "")
	assert.Equal(t, "CurseForge", cf.Name())
}

func TestCurseForge_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": 238222,
					"gameId": 432,
					"name": "Just Enough Items (JEI)",
					"slug": "jei",
					"summary": "View Items and Recipes",
					"downloadCount": 150000000,
					"thumbsUpCount": 5000,
					"primaryCategoryId": 420,
					"authors": [{"id": 1, "name": "mezz"}],
					"logo": {"thumbnailUrl": "https://example.com/jei.png"},
					"latestFiles": [
						{"id": 12345, "displayName": "jei-1.20.1-15.3.0.4", "fileName": "jei.jar"}
					],
					"dateModified": "2024-01-15T10:30:00Z"
				}
			],
			"pagination": {"index": 0, "pageSize": 20, "resultCount": 1, "totalCount": 1}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	result, err := cf.Search(context.Background(), source.SearchQuery{
		GameID:   "432",
		Query:    "jei",
		PageSize: 20,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalCount)
	assert.Equal(t, 0, result.Page)
	assert.Equal(t, 20, result.PageSize)
	mods := result.Mods
	require.Len(t, mods, 1)

	assert.Equal(t, "238222", mods[0].ID)
	assert.Equal(t, "curseforge", mods[0].SourceID)
	assert.Equal(t, "Just Enough Items (JEI)", mods[0].Name)
	assert.Equal(t, "15.3.0.4", mods[0].Version)
	assert.Equal(t, "mezz", mods[0].Author)
	assert.Equal(t, "View Items and Recipes", mods[0].Summary)
	assert.Equal(t, "432", mods[0].GameID)
	assert.Equal(t, int64(150000000), mods[0].Downloads)
	assert.Equal(t, int64Ptr(5000), mods[0].Endorsements)
	assert.Equal(t, "https://example.com/jei.png", mods[0].PictureURL)
}

func TestCurseForge_Search_InvalidGameID(t *testing.T) {
	cf := New(nil, "test-api-key")

	_, err := cf.Search(context.Background(), source.SearchQuery{
		GameID: "not-a-number",
		Query:  "test",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-number")
}

func TestCurseForge_GetMod(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"id": 238222,
				"gameId": 432,
				"name": "Just Enough Items (JEI)",
				"summary": "View Items and Recipes",
				"downloadCount": 150000000,
				"authors": [{"name": "mezz"}],
				"latestFiles": [
					{"displayName": "jei-1.20.1-15.3.0.4"}
				],
				"dateModified": "2024-01-15T10:30:00Z"
			}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	mod, err := cf.GetMod(context.Background(), "432", "238222")
	require.NoError(t, err)

	assert.Equal(t, "238222", mod.ID)
	assert.Equal(t, "curseforge", mod.SourceID)
	assert.Equal(t, "Just Enough Items (JEI)", mod.Name)
	assert.Equal(t, "15.3.0.4", mod.Version)
}

func TestCurseForge_GetDependencies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": 12345,
					"dependencies": [
						{"modId": 306612, "relationType": 3},
						{"modId": 123456, "relationType": 2}
					]
				}
			],
			"pagination": {"index": 0, "pageSize": 50, "resultCount": 1, "totalCount": 1}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	mod := &domain.Mod{ID: "238222", GameID: "432"}
	deps, err := cf.GetDependencies(context.Background(), mod)
	require.NoError(t, err)

	// Should only return required dependencies (relationType 3)
	require.Len(t, deps, 1)
	assert.Equal(t, "curseforge", deps[0].SourceID)
	assert.Equal(t, "306612", deps[0].ModID)
}

func TestCurseForge_GetModFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": 12345,
					"displayName": "jei-1.20.1-15.3.0.4",
					"fileName": "jei-1.20.1-15.3.0.4.jar",
					"fileLength": 1234567,
					"releaseType": 1
				},
				{
					"id": 12344,
					"displayName": "jei-1.20.1-15.3.0.3-beta",
					"fileName": "jei-1.20.1-15.3.0.3.jar",
					"fileLength": 1234000,
					"releaseType": 2
				}
			],
			"pagination": {"index": 0, "pageSize": 50, "resultCount": 2, "totalCount": 2}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	mod := &domain.Mod{ID: "238222", GameID: "432"}
	files, err := cf.GetModFiles(context.Background(), mod)
	require.NoError(t, err)
	require.Len(t, files, 2)

	assert.Equal(t, "12345", files[0].ID)
	assert.Equal(t, "jei-1.20.1-15.3.0.4", files[0].Name)
	assert.Equal(t, "jei-1.20.1-15.3.0.4.jar", files[0].FileName)
	assert.Equal(t, int64(1234567), files[0].Size)
	assert.True(t, files[0].IsPrimary)
	assert.Equal(t, "Release", files[0].Category)

	assert.Equal(t, "12344", files[1].ID)
	assert.False(t, files[1].IsPrimary)
	assert.Equal(t, "Beta", files[1].Category)
}

func TestCurseForge_GetDownloadURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": "https://edge.forgecdn.net/files/1234/567/jei.jar"
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	mod := &domain.Mod{ID: "238222", GameID: "432"}
	url, err := cf.GetDownloadURL(context.Background(), mod, "12345")
	require.NoError(t, err)
	assert.Equal(t, "https://edge.forgecdn.net/files/1234/567/jei.jar", url)
}

func TestCurseForge_CheckUpdates(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Return different versions based on call
		if callCount == 1 {
			// First mod has an update
			_, _ = w.Write([]byte(`{
				"data": {
					"id": 238222,
					"name": "JEI",
					"latestFiles": [{"displayName": "jei-1.20.1-15.4.0.0"}],
					"dateModified": "2024-01-20T10:30:00Z"
				}
			}`))
		} else {
			// Second mod is up to date
			_, _ = w.Write([]byte(`{
				"data": {
					"id": 306612,
					"name": "Fabric API",
					"latestFiles": [{"displayName": "fabric-api-0.92.0"}],
					"dateModified": "2024-01-15T10:30:00Z"
				}
			}`))
		}
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	installed := []domain.InstalledMod{
		{
			Mod: domain.Mod{
				ID:       "238222",
				SourceID: "curseforge",
				Name:     "JEI",
				Version:  "15.3.0.4",
				GameID:   "432",
			},
		},
		{
			Mod: domain.Mod{
				ID:       "306612",
				SourceID: "curseforge",
				Name:     "Fabric API",
				Version:  "0.92.0",
				GameID:   "432",
			},
		},
	}

	updates, err := cf.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)

	// Only JEI should have an update
	require.Len(t, updates, 1)
	assert.Equal(t, "238222", updates[0].InstalledMod.ID)
	assert.Equal(t, "15.4.0.0", updates[0].NewVersion)
}

func TestCurseForge_ExchangeToken(t *testing.T) {
	cf := New(nil, "")

	_, err := cf.ExchangeToken(context.Background(), "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key authentication")
}

// TestCurseForge_AuthGetters folds the trivial auth-state getters into one
// small test, per the task brief.
func TestCurseForge_AuthGetters(t *testing.T) {
	cf := New(nil, "")
	assert.False(t, cf.IsAuthenticated())
	assert.Equal(t, "https://console.curseforge.com/", cf.AuthURL())

	cf.SetAPIKey("new-key")
	assert.True(t, cf.IsAuthenticated())
}

// TestCurseForge_ResolveGameID covers resolveGameID's branches beyond the
// numeric-ID fast path already exercised by TestCurseForge_Search: slug
// resolution (with caching), display-name matching, "not found", and the
// wrapped GetGames failure.
func TestCurseForge_ResolveGameID(t *testing.T) {
	t.Run("numeric ID short-circuits without an API call", func(t *testing.T) {
		cf := New(nil, "")
		id, err := cf.resolveGameID(context.Background(), "432")
		require.NoError(t, err)
		assert.Equal(t, 432, id)
	})

	t.Run("resolves slug via GetGames and caches it", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"data": [{"id": 432, "name": "Minecraft", "slug": "minecraft"}],
				"pagination": {"index": 0, "pageSize": 50, "resultCount": 1, "totalCount": 1}
			}`))
		}))
		defer server.Close()

		cf := New(server.Client(), "test-api-key")
		cf.client.SetBaseURL(server.URL)

		id, err := cf.resolveGameID(context.Background(), "minecraft")
		require.NoError(t, err)
		assert.Equal(t, 432, id)
		assert.Equal(t, 1, callCount)

		id, err = cf.resolveGameID(context.Background(), "minecraft")
		require.NoError(t, err)
		assert.Equal(t, 432, id)
		assert.Equal(t, 1, callCount, "second lookup for the same slug must hit the cache, not the API")
	})

	t.Run("matches by display name case-insensitively", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"data": [{"id": 5, "name": "World of Warcraft", "slug": "wow"}],
				"pagination": {"index": 0, "pageSize": 50, "resultCount": 1, "totalCount": 1}
			}`))
		}))
		defer server.Close()

		cf := New(server.Client(), "test-api-key")
		cf.client.SetBaseURL(server.URL)

		id, err := cf.resolveGameID(context.Background(), "World Of Warcraft")
		require.NoError(t, err)
		assert.Equal(t, 5, id)
	})

	t.Run("slug not found among games", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"data": [],
				"pagination": {"index": 0, "pageSize": 50, "resultCount": 0, "totalCount": 0}
			}`))
		}))
		defer server.Close()

		cf := New(server.Client(), "test-api-key")
		cf.client.SetBaseURL(server.URL)

		_, err := cf.resolveGameID(context.Background(), "nonexistent-slug")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "game not found")
	})

	t.Run("GetGames failure is wrapped", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		}))
		defer server.Close()

		cf := New(server.Client(), "test-api-key")
		cf.client.SetBaseURL(server.URL)

		_, err := cf.resolveGameID(context.Background(), "minecraft")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetching games to resolve slug")
	})
}

func TestCurseForge_EnvKey(t *testing.T) {
	cf := New(nil, "")
	assert.Equal(t, "CURSEFORGE_API_KEY", cf.EnvKey())
}

func TestCurseForge_TypeLabel(t *testing.T) {
	cf := New(nil, "")
	assert.Equal(t, "built-in", cf.TypeLabel())
}

func TestCurseForge_Capabilities(t *testing.T) {
	cf := New(nil, "")
	assert.Equal(t, source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true, Versions: true}, cf.Capabilities())
}

func TestCurseForge_AuthInstructions(t *testing.T) {
	cf := New(nil, "")
	want := "To authenticate with CurseForge:\n" +
		"1. Visit https://console.curseforge.com/\n" +
		"2. Create a project and generate an API key\n" +
		"3. Copy your API key\n"
	assert.Equal(t, want, cf.AuthInstructions())
}

func TestCurseForge_ValidateKey_OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "good-key", r.Header.Get("x-api-key"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [{"id": 432, "name": "Minecraft", "slug": "minecraft"}],
			"pagination": {"index": 0, "pageSize": 50, "resultCount": 1, "totalCount": 1}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "")
	cf.client.SetBaseURL(server.URL)

	err := cf.ValidateKey(context.Background(), "good-key")
	assert.NoError(t, err)
}

func TestCurseForge_ValidateKey_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	cf := New(server.Client(), "")
	cf.client.SetBaseURL(server.URL)

	err := cf.ValidateKey(context.Background(), "bad-key")
	assert.Error(t, err)
}

// TestCurseForge_ValidateKey_WrongKeyAgainstAuthenticatedReceiver pins that
// ValidateKey checks the candidate key, never falling back to a key already
// stored on the receiver. A receiver constructed with a valid stored key
// ("good-key") must still fail when asked to validate a different candidate.
func TestCurseForge_ValidateKey_WrongKeyAgainstAuthenticatedReceiver(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [{"id": 432, "name": "Minecraft", "slug": "minecraft"}],
			"pagination": {"index": 0, "pageSize": 50, "resultCount": 1, "totalCount": 1}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "good-key")
	cf.client.SetBaseURL(server.URL)

	err := cf.ValidateKey(context.Background(), "bad-key")
	assert.Error(t, err, "ValidateKey must use the candidate key, not the receiver's stored key")

	// Bonus: prove the server logic actually discriminates on the header.
	err = cf.ValidateKey(context.Background(), "good-key")
	assert.NoError(t, err)
}

func TestCurseForge_ListGames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": 432, "name": "Minecraft", "slug": "minecraft"},
				{"id": 4471, "name": "World of Warcraft", "slug": "wow"}
			],
			"pagination": {"index": 0, "pageSize": 50, "resultCount": 2, "totalCount": 2}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	games, err := cf.ListGames(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []source.GameEntry{
		{ID: "432", Name: "Minecraft", Slug: "minecraft"},
		{ID: "4471", Name: "World of Warcraft", Slug: "wow"},
	}, games)
}

// TestCurseForge_ListGames_PropagatesAuthRequired pins that ListGames's
// "listing games: %w" wrap (curseforge.go) preserves the domain.ErrAuthRequired
// sentinel all the way from the HTTP layer's 403 mapping
// (client.go's mapError) through GetGames's own "getting games: %w" wrap.
// cmd/lmm callers (game add's catalog-search flow among them) rely on
// errors.Is(err, domain.ErrAuthRequired) surviving this whole chain so they
// can rewrap it into a friendly "run lmm auth login curseforge" message
// instead of a raw API error.
func TestCurseForge_ListGames_PropagatesAuthRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error": "API key required"}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "")
	cf.client.SetBaseURL(server.URL)

	_, err := cf.ListGames(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrAuthRequired)
}

// TestModToDomain_DescriptionNotAliasedToSummary (#235): the CurseForge mod
// response carries no full-description field, and the adapter used to paper
// over that by copying Summary into Description — so every surface showing
// both rendered the same paragraph twice. When the source has no description,
// the field must stay empty.
func TestModToDomain_DescriptionNotAliasedToSummary(t *testing.T) {
	mod := modToDomain(Mod{ID: 1, Name: "JEI", Summary: "View Items and Recipes"}, "432")

	assert.Equal(t, "View Items and Recipes", mod.Summary)
	assert.Empty(t, mod.Description, "Description must not be a copy of Summary (#235)")
}
