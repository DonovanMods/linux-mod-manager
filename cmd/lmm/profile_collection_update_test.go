package main

// #613: `lmm profile sync --collection` - a collection profile imported
// from an https address, updated from it. The entries are switched off in
// both versions, so nothing is downloaded: the update is the profile's.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const collectionV1 = `lmm_collection: 1
id: coll-1
version: 1
name: shared
game:
  steam_app_id: "1091500"
mods:
  - {source_id: nexusmods, mod_id: "1", version: "1.0", disabled: true}
  - {source_id: nexusmods, mod_id: "2", version: "1.0", disabled: true}
`

const collectionV2 = `lmm_collection: 1
id: coll-1
version: 2
name: shared
game:
  steam_app_id: "1091500"
mods:
  - {source_id: nexusmods, mod_id: "1", version: "1.1", disabled: true}
  - {source_id: nexusmods, mod_id: "3", version: "1.0", disabled: true}
`

// collectionUpdateService is a Service with a profile imported from an
// address that serves collectionV1, then whatever serve stores.
func collectionUpdateService(t *testing.T) (*core.Service, *domain.Game, func(string)) {
	t.Helper()
	var body atomic.Pointer[string]
	v1 := collectionV1
	body.Store(&v1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(*body.Load()))
	}))
	t.Cleanup(srv.Close)
	svc, game := urlImportService(t, srv)
	setImportNoInstall(t)
	_, _, err := captureStdoutStderrErr(t, func() error {
		return doCollectionURLImport(context.Background(), svc, srv.URL+"/shared.yaml", "")
	})
	require.NoError(t, err)
	return svc, game, func(doc string) { body.Store(&doc) }
}

// setCollectionSyncFlags sets the --collection flags for one test.
func setCollectionSyncFlags(t *testing.T, set func()) {
	t.Helper()
	old := []any{profileSyncCollection, profileSyncRemove, profileSyncRemoveObsolete, profileSyncOverride,
		profileSyncOverrideConflicts, profileSyncWithOptional, profileSyncAnyOverrides, profileSyncYes, profileSyncDryRun}
	t.Cleanup(func() {
		profileSyncCollection = old[0].(bool)
		profileSyncRemove = old[1].([]string)
		profileSyncRemoveObsolete = old[2].(bool)
		profileSyncOverride = old[3].([]string)
		profileSyncOverrideConflicts = old[4].(bool)
		profileSyncWithOptional = old[5].(bool)
		profileSyncAnyOverrides = old[6].(bool)
		profileSyncYes = old[7].(bool)
		profileSyncDryRun = old[8].(bool)
	})
	profileSyncCollection = true
	set()
}

func TestDoCollectionUpdate_PlansKeepsAndRemoves(t *testing.T) {
	svc, game, serve := collectionUpdateService(t)
	serve(collectionV2)
	setCollectionSyncFlags(t, func() { profileSyncYes = true })

	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doCollectionUpdate(context.Background(), svc, game, []string{"shared"})
	})
	require.NoError(t, err, stdout)
	assert.Contains(t, stdout, "version 1 → 2")
	assert.Contains(t, stdout, "+ nexusmods:3 1.0")
	assert.Contains(t, stdout, "~ nexusmods:1 1.0 → 1.1 (version)")
	assert.Contains(t, stdout, "· nexusmods:2: kept, as your own mod")
	assert.Contains(t, stdout, "Remove one with --remove <mod>, or all with --remove-obsolete.")
	assert.Contains(t, stdout, "✓ Updated profile shared to version 2: 1 added, 1 updated.")
	assert.Contains(t, stdout, "Kept 1 mod(s) the collection no longer lists, as your own.")

	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "shared")
	require.NoError(t, err)
	assert.Equal(t, 2, saved.Collection.Version)
	require.NotNil(t, saved.FindRef("nexusmods", "2"), "kept")
	assert.False(t, saved.FindRef("nexusmods", "2").FromCollection)
	assert.Equal(t, "1.1", saved.FindRef("nexusmods", "1").Version)

	// Up to date now.
	stdout, _, err = captureStdoutStderrErr(t, func() error {
		return doCollectionUpdate(context.Background(), svc, game, []string{"shared"})
	})
	require.NoError(t, err)
	assert.Contains(t, stdout, "Profile shared is up to date with its collection (version 2).")
}

func TestDoCollectionUpdate_RemoveByModID(t *testing.T) {
	svc, game, serve := collectionUpdateService(t)
	serve(collectionV2)
	setCollectionSyncFlags(t, func() { profileSyncYes, profileSyncRemove = true, []string{"2"} })

	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doCollectionUpdate(context.Background(), svc, game, []string{"shared"})
	})
	require.NoError(t, err, stdout)
	assert.Contains(t, stdout, "- nexusmods:2: will be removed and uninstalled")
	assert.Contains(t, stdout, "Removed: nexusmods:2")
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "shared")
	require.NoError(t, err)
	assert.Nil(t, saved.FindRef("nexusmods", "2"))
}

func TestDoCollectionUpdate_RefusesAnUnknownChoice(t *testing.T) {
	svc, game, serve := collectionUpdateService(t)
	serve(collectionV2)
	setCollectionSyncFlags(t, func() { profileSyncYes, profileSyncOverride = true, []string{"9"} })
	err := doCollectionUpdate(context.Background(), svc, game, []string{"shared"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `--override 9: no conflicting mod of this update is "9"`)
}

func TestDoCollectionUpdate_DryRunJSON(t *testing.T) {
	svc, game, serve := collectionUpdateService(t)
	serve(collectionV2)
	setCollectionSyncFlags(t, func() { profileSyncDryRun = true })
	out := runJSONCommand(t, func() error {
		return doCollectionUpdate(context.Background(), svc, game, []string{"shared"})
	})
	var plan core.CollectionUpdatePlan
	decodeStrict(t, out, &plan)
	assert.Equal(t, 2, plan.NewVersion)
	assertJSONCLIGolden(t, "profile_sync_collection_dry_run", out, srvURLOf(t, plan.URL), "https://<server>/shared.yaml")

	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "shared")
	require.NoError(t, err)
	assert.Equal(t, 1, saved.Collection.Version, "a dry run changes nothing")
}

func TestDoCollectionUpdate_JSONResult(t *testing.T) {
	svc, game, serve := collectionUpdateService(t)
	serve(collectionV2)
	setCollectionSyncFlags(t, func() { profileSyncYes, profileSyncRemoveObsolete = true, true })
	out := runJSONCommand(t, func() error {
		return doCollectionUpdate(context.Background(), svc, game, []string{"shared"})
	})
	var result core.CollectionUpdateResult
	decodeStrict(t, out, &result)
	assert.Equal(t, []string{"nexusmods:2"}, result.Removed)
	assertJSONCLIGolden(t, "profile_sync_collection_result", out)
}

func TestRunProfileSync_CollectionFlagsNeedCollection(t *testing.T) {
	setCollectionSyncFlags(t, func() { profileSyncCollection, profileSyncRemoveObsolete = false, true })
	profileSyncCmd.SetContext(context.Background())
	err := runProfileSync(profileSyncCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--remove-obsolete update a collection profile; use them with --collection")
}

func TestDoCollectionUpdate_ProfileWithoutOrigin(t *testing.T) {
	svc, game, _ := collectionUpdateService(t)
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{Name: "plain", GameID: game.ID}))
	setCollectionSyncFlags(t, func() {})
	err := doCollectionUpdate(context.Background(), svc, game, []string{"plain"})
	require.Error(t, err)
	assert.True(t, core.IsCollectionNotUpdatable(err))
}

// srvURLOf is the scheme and host of a redacted address, for a golden's
// substitution.
func srvURLOf(t *testing.T, redacted string) string {
	t.Helper()
	const suffix = "/shared.yaml"
	require.Contains(t, redacted, suffix)
	return redacted[:len(redacted)-len(suffix)] + suffix
}

func TestReportError_JSON_CollectionUpdateMismatchError(t *testing.T) {
	withJSONOutput(t)
	err := &core.CollectionUpdateMismatchError{Code: core.CollectionUpdateDifferentCollection, ID: "coll-1", Version: 1, FetchedID: "coll-2", FetchedVersion: 2}
	out := captureStdout(t, func() error { reportError(err); return nil })
	assert.Equal(t, "{\n"+
		"  \"error\": \"the collection's origin now serves collection \\\"coll-2\\\", not this profile's collection \\\"coll-1\\\"; refusing to update from it (import it as a new profile instead)\",\n"+
		"  \"details\": {\n"+
		"    \"code\": \"different_collection\",\n"+
		"    \"id\": \"coll-1\",\n"+
		"    \"version\": 1,\n"+
		"    \"fetched_id\": \"coll-2\",\n"+
		"    \"fetched_version\": 2\n"+
		"  }\n"+
		"}\n", out)
}

func TestDoCollectionUpdate_RefusesAnotherCollection(t *testing.T) {
	svc, game, serve := collectionUpdateService(t)
	serve("lmm_collection: 1\nid: other\nversion: 2\nname: shared\ngame:\n  steam_app_id: \"1091500\"\nmods: []\n")
	setCollectionSyncFlags(t, func() { profileSyncYes = true })
	err := doCollectionUpdate(context.Background(), svc, game, []string{"shared"})
	require.True(t, core.IsCollectionUpdateMismatch(err), "%v", err)
}
