package main

// #666 at the CLI: `lmm profile import --switch` switches to the imported
// profile once the import finishes; `--no-switch` never does. With neither,
// an interactive run shows the switch's plan and asks "Switch to <profile>
// now? [Y/n]" after the import; a run that is not interactive - stdin not a
// terminal, -y, --json - keeps the import recorded-only, as before.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// switchImportFixture is setupDoProfileImportTest's game with seedAltProfile's
// profiles: "default" active and deploying shared.esp, "alt" recording
// "altonly" (cached). The import brings a new profile, "coll".
func switchImportFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, _ := setupDoProfileImportTest(t)
	seedAltProfile(t, svc, game, "test-src")
	setFlag(t, &profileImportSwitch, false)
	setFlag(t, &profileImportNoSwitch, false)
	setFlag(t, &profileImportYes, false)
	setFlag(t, &jsonOutput, false)
	setFlag(t, &profileImportStdinIsTerminal, func() bool { return false })
	return svc, game
}

// feedStdin makes stdin read text, then EOF.
func feedStdin(t *testing.T, text string) {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(text)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; _ = r.Close() })
}

func collDoc(t *testing.T, mods ...string) []byte {
	t.Helper()
	refs := make([]domain.ModReference, 0, len(mods))
	for _, m := range mods {
		refs = append(refs, domain.ModReference{SourceID: "test-src", ModID: m, Version: "1.0"})
	}
	return buildImportProfileData(t, "g1", "coll", refs)
}

func activeProfileName(t *testing.T, svc *core.Service, game *domain.Game) string {
	t.Helper()
	p, err := getProfileManager(svc).GetDefault(context.Background(), game.ID)
	require.NoError(t, err)
	return p.Name
}

func TestDoProfileImport_SwitchFlagSwitchesOnceImported(t *testing.T) {
	svc, game := switchImportFixture(t)
	setFlag(t, &profileImportSwitch, true)
	setFlag(t, &profileImportYes, true)

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doProfileImport(context.Background(), svc, game, collDoc(t, "altonly"))
	})

	require.NoError(t, err)
	assert.Contains(t, stdout, "default's deployed mods are taken down", "the plan says what the switch takes down before anything runs")
	assert.Contains(t, stdout, "Switching to profile: coll")
	assert.Contains(t, stdout, "✓ Switched to profile: coll")
	assert.NotContains(t, stdout, "Switch to coll now?", "--switch asks nothing")
	assert.Equal(t, "coll", activeProfileName(t, svc, game))
	assert.FileExists(t, filepath.Join(game.ModPath, "altonly.esp"))
	assert.NoFileExists(t, filepath.Join(game.ModPath, "shared.esp"), "the old profile's files came down")
}

func TestDoProfileImport_InteractiveOffersTheSwitchDefaultingToYes(t *testing.T) {
	svc, game := switchImportFixture(t)
	setFlag(t, &profileImportStdinIsTerminal, func() bool { return true })
	// "y" records the mods; the switch prompt then reads EOF: its default.
	feedStdin(t, "y\n")

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doProfileImport(context.Background(), svc, game, collDoc(t, "altonly"))
	})

	require.NoError(t, err)
	assert.Contains(t, stdout, "✓ Imported profile: coll")
	assert.Contains(t, stdout, "Will disable 1 mod(s):", "the switch's plan is shown before its prompt")
	assert.Contains(t, stdout, "Switch to coll now? [Y/n]")
	assert.Contains(t, stdout, "✓ Switched to profile: coll")
	assert.Equal(t, "coll", activeProfileName(t, svc, game))
	assert.NoFileExists(t, filepath.Join(game.ModPath, "shared.esp"))
}

func TestDoProfileImport_InteractiveSwitchDeclinedKeepsTheImport(t *testing.T) {
	svc, game := switchImportFixture(t)
	setFlag(t, &profileImportStdinIsTerminal, func() bool { return true })
	// Nothing to record, so the switch's is the only prompt.
	feedStdin(t, "n\n")

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doProfileImport(context.Background(), svc, game, collDoc(t))
	})

	require.NoError(t, err, "declining the switch is no failure: the import is done")
	assert.Contains(t, stdout, "Switch to coll now? [Y/n]")
	assert.Contains(t, stdout, "lmm profile switch coll")
	assert.Equal(t, "default", activeProfileName(t, svc, game))
	assert.FileExists(t, filepath.Join(game.ModPath, "shared.esp"))
	_, err = getProfileManager(svc).Get(context.Background(), game.ID, "coll")
	require.NoError(t, err, "the imported profile stays")
}

func TestDoProfileImport_NotInteractiveKeepsTodaysBehaviour(t *testing.T) {
	for name, set := range map[string]func(t *testing.T){
		"stdin not a terminal": func(t *testing.T) {},
		"-y": func(t *testing.T) {
			setFlag(t, &profileImportStdinIsTerminal, func() bool { return true })
			setFlag(t, &profileImportYes, true)
		},
		"--no-switch": func(t *testing.T) {
			setFlag(t, &profileImportStdinIsTerminal, func() bool { return true })
			setFlag(t, &profileImportNoSwitch, true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, game := switchImportFixture(t)
			set(t)
			feedStdin(t, "y\n")

			stdout, _, err := captureStdoutAndStderr(t, func() error {
				return doProfileImport(context.Background(), svc, game, collDoc(t, "altonly"))
			})

			require.NoError(t, err)
			assert.NotContains(t, stdout, "Switch to coll now?")
			assert.Contains(t, stdout, "Recorded: 1 (not deployed")
			assert.Equal(t, "default", activeProfileName(t, svc, game))
			assert.FileExists(t, filepath.Join(game.ModPath, "shared.esp"))
		})
	}
}

func TestDoProfileImport_SwitchJSONReportsTheSwitch(t *testing.T) {
	svc, game := switchImportFixture(t)
	setFlag(t, &profileImportSwitch, true)
	setFlag(t, &profileImportYes, true)
	setFlag(t, &jsonOutput, true)

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doProfileImport(context.Background(), svc, game, collDoc(t, "altonly"))
	})

	require.NoError(t, err)
	var doc core.ProfileImportResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &doc), stdout)
	require.NotNil(t, doc.Switch)
	assert.True(t, doc.Switch.Switched)
	assert.Equal(t, "default", doc.Switch.From)
	assert.Equal(t, "coll", doc.Switch.To)
	assert.Equal(t, "coll", activeProfileName(t, svc, game))
}

func TestDoProfileImport_AFailedSwitchExitsNonZeroKeepingTheImport(t *testing.T) {
	svc, game := switchImportFixture(t)
	setFlag(t, &profileImportSwitch, true)
	setFlag(t, &profileImportYes, true)

	// "gone" is a mod the source does not have: the import fails it, and
	// the switch, which retries it, fails it again.
	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doProfileImport(context.Background(), svc, game, collDoc(t, "altonly", "gone"))
	})

	require.Error(t, err)
	assert.Equal(t, exitError, exitCodeFor(err))
	assert.Contains(t, stdout, "Profile coll is now active, but 1 mod(s) failed")
	assert.Equal(t, "coll", activeProfileName(t, svc, game))
}

func TestRunProfileImport_RefusesContradictorySwitchFlags(t *testing.T) {
	for name, set := range map[string]func(t *testing.T){
		"--switch --no-switch": func(t *testing.T) {
			setFlag(t, &profileImportSwitch, true)
			setFlag(t, &profileImportNoSwitch, true)
		},
		"--switch --no-install": func(t *testing.T) {
			setFlag(t, &profileImportSwitch, true)
			setFlag(t, &profileImportNoInstall, true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			setFlag(t, &profileImportSwitch, false)
			setFlag(t, &profileImportNoSwitch, false)
			setFlag(t, &profileImportNoInstall, false)
			set(t)
			err := runProfileImport(&cobra.Command{}, []string{filepath.Join(t.TempDir(), "never-read.yaml")})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--switch")
		})
	}
}

// emptyActiveFixture is setupDoProfileImportTest's game with one profile,
// "default", active and empty: importing an empty "coll" leaves a switch
// with no mod changes at all.
func emptyActiveFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, _ := setupDoProfileImportTest(t)
	ctx := context.Background()
	pm := getProfileManager(svc)
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	setFlag(t, &profileImportSwitch, false)
	setFlag(t, &profileImportNoSwitch, false)
	setFlag(t, &profileImportYes, false)
	setFlag(t, &jsonOutput, false)
	setFlag(t, &profileImportStdinIsTerminal, func() bool { return true })
	return svc, game
}

// #666 review: a switch with nothing to deploy or take down is still a
// switch - the offer asks before it, and a decline keeps the active
// profile.
func TestDoProfileImport_TheOfferAsksEvenWithNoModChanges(t *testing.T) {
	t.Run("declined", func(t *testing.T) {
		svc, game := emptyActiveFixture(t)
		feedStdin(t, "n\n")

		stdout, _, err := captureStdoutAndStderr(t, func() error {
			return doProfileImport(context.Background(), svc, game, collDoc(t))
		})

		require.NoError(t, err)
		assert.Contains(t, stdout, "switching deploys and takes down nothing")
		assert.Contains(t, stdout, "Switch to coll now? [Y/n]")
		assert.Contains(t, stdout, "Not switched.")
		assert.Equal(t, "default", activeProfileName(t, svc, game), "a decline keeps the active profile")
	})
	t.Run("accepted", func(t *testing.T) {
		svc, game := emptyActiveFixture(t)
		feedStdin(t, "y\n")

		stdout, _, err := captureStdoutAndStderr(t, func() error {
			return doProfileImport(context.Background(), svc, game, collDoc(t))
		})

		require.NoError(t, err)
		assert.Contains(t, stdout, "Switch to coll now? [Y/n]")
		assert.Contains(t, stdout, "✓ Switched to profile: coll")
		assert.Equal(t, "coll", activeProfileName(t, svc, game))
	})
}

// #666 review: when no single profile is marked active, the switch only
// marks the imported one - the offer says so, and asks its own question.
func TestOfferImportSwitch_AFlagOnlySwitchSaysWhatItDoes(t *testing.T) {
	for name, answer := range map[string]string{"declined": "n\n", "accepted": "\n"} {
		t.Run(name, func(t *testing.T) {
			svc, game := emptyActiveFixture(t)
			ctx := context.Background()
			_, err := getProfileManager(svc).Create(ctx, game.ID, "coll")
			require.NoError(t, err)
			unmarkProfile(t, game.ID, "default")
			feedStdin(t, answer)

			stdout, _, err := captureStdoutAndStderr(t, func() error {
				return offerImportSwitch(ctx, svc, game, &core.ProfileImportResult{ProfileName: "coll", RecordedOnly: true, ActiveProfile: "default"})
			})

			require.NoError(t, err)
			assert.Contains(t, stdout, "only marks it as the active profile")
			assert.Contains(t, stdout, "Switch to coll now? [Y/n]")
			assert.NotContains(t, stdout, "Mark coll as the active profile?")
			p, err := getProfileManager(svc).Get(ctx, game.ID, "coll")
			require.NoError(t, err)
			assert.Equal(t, name == "accepted", p.IsDefault)
		})
	}
}

// #666 review: --switch with the install prompt declined says it is not
// switching, rather than dropping the switch without a word.
func TestDoProfileImport_SwitchWithTheInstallDeclinedSaysSo(t *testing.T) {
	svc, game := switchImportFixture(t)
	setFlag(t, &profileImportSwitch, true)
	feedStdin(t, "n\n")

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doProfileImport(context.Background(), svc, game, collDoc(t, "altonly"))
	})

	require.NoError(t, err)
	assert.Contains(t, stdout, "Not switching to coll: switching would install the mods just skipped.")
	assert.NotContains(t, stdout, "Switched to profile")
	assert.Equal(t, "default", activeProfileName(t, svc, game))
}

// #666 review: a collection import that switched to its profile deployed
// its mods, so its summary says "Imported", as the web UI does; one that
// only recorded them says "Recorded".
func TestPrintCollectionImportSummary_SaysImportedAfterASwitch(t *testing.T) {
	setFlag(t, &jsonOutput, false)
	for want, sw := range map[string]*core.ImportSwitch{
		"Imported 2 mods.": {To: "coll", Switched: true},
		"Recorded 2 mods.": {To: "coll", Error: "not switched"},
	} {
		stdout, _, err := captureStdoutAndStderr(t, func() error {
			printCollectionImportSummary(&core.ProfileImportResult{ProfileName: "coll", RecordedOnly: true, Total: 2, Switch: sw})
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, want+"\n", stdout)
	}
}
