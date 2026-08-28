package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedLocalMod seeds an imported-from-disk mod: present in the profile, but
// with no remote source, so CheckUpdates filters it out.
func seedLocalMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name string) {
	t.Helper()
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, domain.SourceLocal, modID, "1.0", name+".esp", []byte("data")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: domain.SourceLocal, Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(game.ID, "default")
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: domain.SourceLocal, ModID: modID, Version: "1.0"}))
}

func localUpdateGame(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := setupDoDeployTest(t)
	game.SourceIDs = map[string]string{"src": "src"}
	return svc, game
}

// TestDoUpdate_AllLocal_DoesNotClaimUpToDate: local mods are filtered before
// any source is queried, so "All mods are up to date" describes a check that
// never happened.
func TestDoUpdate_AllLocal_DoesNotClaimUpToDate(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedLocalMod(t, svc, game, "localA", "Local A")

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.NotContains(t, out, "up to date", "nothing was checked, so nothing can be up to date")
	assert.Contains(t, out, "local", "should say the mods were skipped as local")
}

// TestDoUpdate_ReportsSkippedLocalCount: mixed profile — the checkable mod is
// reported normally, and the local one is disclosed rather than vanishing.
func TestDoUpdate_ReportsSkippedLocalCount(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedLocalMod(t, svc, game, "localA", "Local A")

	out := captureStdout(t, func() error {
		// The seeded remote source is unregistered, so the check fails and
		// doUpdate reports non-zero; this test is about the output.
		_ = doUpdate(context.Background(), svc, game, nil)
		return nil
	})

	assert.Contains(t, out, "1 local mod", "should report the skipped local mod")
}

// TestDoUpdate_SingleLocalMod_SaysLocalNotNotFound: the lookup matched on
// source as well as ID, so a local mod reported "not found in profile" — with
// exit 1 — for a mod that is plainly in the profile.
func TestDoUpdate_SingleLocalMod_SaysLocalNotNotFound(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedLocalMod(t, svc, game, "localA", "Local A")

	var err error
	out := captureStdout(t, func() error {
		err = doUpdate(context.Background(), svc, game, []string{"localA"})
		return nil
	})

	assert.NoError(t, err, "a present-but-uncheckable mod is not an error")
	assert.NotContains(t, out, "not found", "the mod is in the profile")
	assert.Contains(t, out, "local", "should say why it cannot be checked")
}

// TestDoUpdate_SingleUnknownMod_StillErrors guards the other direction: a mod
// genuinely absent from the profile must still be an error.
func TestDoUpdate_SingleUnknownMod_StillErrors(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedLocalMod(t, svc, game, "localA", "Local A")

	err := doUpdate(context.Background(), svc, game, []string{"nosuchmod"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestDoUpdate_JSONReportsSkipped: --json consumers have the same blind spot.
func TestDoUpdate_JSONReportsSkipped(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedLocalMod(t, svc, game, "localA", "Local A")
	seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")
	setPolicy(t, svc, game, "b", domain.UpdatePinned)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	var out struct {
		Skipped struct {
			Pinned int `json:"pinned"`
			Local  int `json:"local"`
		} `json:"skipped"`
	}
	raw := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	assert.Equal(t, 1, out.Skipped.Local)
	assert.Equal(t, 1, out.Skipped.Pinned)
}

// TestDoUpdate_CheckFailed_DoesNotClaimUpToDate: when CheckUpdates fails, the
// warning goes to stderr and stdout used to say "All mods are up to date" —
// currency was never established, and a caller reading stdout sees a false
// success. Same defect class as the local/pinned cases this file covers.
//
// The seeded mod's source is not registered with the service, so the check
// errors and returns no updates.
func TestDoUpdate_CheckFailed_DoesNotClaimUpToDate(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")

	out := captureStdout(t, func() error {
		_ = doUpdate(context.Background(), svc, game, nil)
		return nil
	})

	assert.NotContains(t, out, "up to date", "the check failed, so currency was never established")
}

// TestDoUpdate_SingleMod_AmbiguousAcrossSources: mod IDs are only unique within
// a source, so a profile can hold the same ID twice. Reporting one arbitrarily
// as "belongs to source X" would name whichever happened to come last.
func TestDoUpdate_SingleMod_AmbiguousAcrossSources(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedLocalMod(t, svc, game, "12345", "Local Twin")
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "12345", SourceID: "curseforge", Name: "Remote Twin", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))

	err := doUpdate(context.Background(), svc, game, []string{"12345"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "curseforge", "must name the candidate sources")
	assert.Contains(t, err.Error(), domain.SourceLocal, "must name the candidate sources")
}

// TestDoUpdate_AmbiguousRemoteOnly_OmitsLocalCaveat: the local aside is only
// relevant when a candidate actually is local; on a purely remote ambiguity it
// is noise.
func TestDoUpdate_AmbiguousRemoteOnly_OmitsLocalCaveat(t *testing.T) {
	svc, game := localUpdateGame(t)
	for _, src := range []string{"nexusmods", "curseforge"} {
		require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
			Mod:          domain.Mod{ID: "12345", SourceID: src, Name: "Twin " + src, Version: "1.0", GameID: game.ID},
			ProfileName:  "default",
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
		}))
	}

	err := doUpdate(context.Background(), svc, game, []string{"12345"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "curseforge, nexusmods", "sources listed, sorted")
	assert.NotContains(t, err.Error(), "local mods cannot", "no local candidate, so no local caveat")
}

// TestDoUpdate_CheckFailed_ReturnsError: a check that failed should exit
// non-zero. It reported the failure itself (warning on stderr plus the
// "did not complete" line), so ErrReported suppresses a duplicate message.
func TestDoUpdate_CheckFailed_ReturnsError(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")

	var err error
	_ = captureStdout(t, func() error {
		err = doUpdate(context.Background(), svc, game, nil)
		return nil
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReported, "already reported, so Execute must not print it again")
}

// TestDoUpdate_CheckFailed_JSONCarriesError: --json consumers cannot see the
// stderr warning, so the failure has to be in the document itself.
func TestDoUpdate_CheckFailed_JSONCarriesError(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	var err error
	raw := captureStdout(t, func() error {
		err = doUpdate(context.Background(), svc, game, nil)
		return nil
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReported)

	// Exactly one document: if Execute also printed {"error":...}, a caller
	// piping stdout to a parser would choke on trailing content.
	dec := json.NewDecoder(strings.NewReader(raw))
	var out struct {
		Error   string `json:"error"`
		Updates []any  `json:"updates"`
	}
	require.NoError(t, dec.Decode(&out))
	assert.NotEmpty(t, out.Error, "the failure must be visible in the JSON")
	assert.Empty(t, out.Updates)
	// Checks the unread remainder rather than Decoder.More(). More() does in
	// fact return true for a second top-level document, but that is incidental
	// to what it documents ("another element in the current array or object"),
	// and it says nothing about trailing bytes that are not valid JSON.
	trailing := strings.TrimSpace(raw[dec.InputOffset():])
	assert.Empty(t, trailing, "stdout must hold exactly one JSON document, found trailing content")
}

// TestDoUpdate_CheckSucceeded_ReturnsNil guards the other direction: a check
// that completed must not start failing just because nothing needed updating.
func TestDoUpdate_CheckSucceeded_ReturnsNil(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedLocalMod(t, svc, game, "localA", "Local A") // filtered, so no source is queried

	_ = captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	err := func() error {
		var e error
		_ = captureStdout(t, func() error {
			e = doUpdate(context.Background(), svc, game, nil)
			return nil
		})
		return e
	}()
	assert.NoError(t, err, "nothing failed; everything was simply skipped")
}

// TestReportError_SuppressesAlreadyReported pins the Execute-side contract.
func TestReportError_SuppressesAlreadyReported(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		reportError(fmt.Errorf("wrapped: %w", ErrReported))
		return nil
	})
	assert.Empty(t, out, "an already-reported error must not be printed again")

	out = captureStdout(t, func() error {
		reportError(errors.New("some other failure"))
		return nil
	})
	assert.Contains(t, out, "some other failure", "unreported errors still print")
}
