package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReportError_JSON_DownloadError pins the wire shape of #513's typed
// download failure - the document `lmm serve` hands the SPA in a failed
// install or update job, and the detailsCoverage entry for
// core.DownloadError.
func TestReportError_JSON_DownloadError(t *testing.T) {
	withJSONOutput(t)

	err := fmt.Errorf("download failed: %w", &core.DownloadError{
		SourceID: "curseforge", ModID: "238222", ModName: "Just Enough Items",
		ModURL: "https://www.curseforge.com/minecraft/mc-mods/jei", ManualDownload: true,
		Err: errors.New("mod author has disabled third-party downloads"),
	})
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"download failed: mod author has disabled third-party downloads\",\n"+
		"  \"details\": {\n"+
		"    \"source_id\": \"curseforge\",\n"+
		"    \"mod_id\": \"238222\",\n"+
		"    \"mod_name\": \"Just Enough Items\",\n"+
		"    \"mod_url\": \"https://www.curseforge.com/minecraft/mc-mods/jei\",\n"+
		"    \"manual_download\": true\n"+
		"  }\n"+
		"}\n", out)
}

// #535: the failure names the file it was for, beside the mod - what the
// web UI's "Install from file…" expects the downloaded archive to be.
func TestReportError_JSON_DownloadError_NamesTheFile(t *testing.T) {
	withJSONOutput(t)

	err := &core.DownloadError{
		SourceID: "curseforge", ModID: "238222", ManualDownload: true,
		FileID: "5001", FileName: "jei-1.20.1.jar", Err: errors.New("refused"),
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"refused\",\n"+
		"  \"details\": {\n"+
		"    \"source_id\": \"curseforge\",\n"+
		"    \"mod_id\": \"238222\",\n"+
		"    \"manual_download\": true,\n"+
		"    \"file_id\": \"5001\",\n"+
		"    \"file_name\": \"jei-1.20.1.jar\"\n"+
		"  }\n"+
		"}\n", out)
}

// TestReportError_Human_DownloadError_NamesThePage: every download failure
// with a usable page says where to get the file by hand.
func TestReportError_Human_DownloadError_NamesThePage(t *testing.T) {
	err := fmt.Errorf("download failed: %w", &core.DownloadError{
		SourceID: "nexusmods", ModID: "42", ModURL: "https://www.nexusmods.com/skyrim/mods/42",
		Err: errors.New("downloading mod: connection reset"),
	})
	out, _ := captureStderrErr(t, func() error { reportError(err); return nil })

	assert.Contains(t, out, "download failed: downloading mod: connection reset")
	assert.Contains(t, out, "Download it manually from: https://www.nexusmods.com/skyrim/mods/42")
	assert.NotContains(t, out, "lmm import", "an ordinary failure is not a manual-install walkthrough")
}

// TestReportError_Human_DownloadError_ManualDownloadSaysHowToImportIt: a
// source that refuses automated downloads also gets the import command, the
// notice `lmm install` used to print on a string match.
func TestReportError_Human_DownloadError_ManualDownloadSaysHowToImportIt(t *testing.T) {
	err := &core.DownloadError{
		SourceID: "curseforge", ModID: "238222", ModURL: "https://www.curseforge.com/minecraft/mc-mods/jei",
		ManualDownload: true, Err: errors.New("the author disabled API downloads"),
	}
	out, _ := captureStderrErr(t, func() error { reportError(err); return nil })

	assert.Contains(t, out, "Download it manually from: https://www.curseforge.com/minecraft/mc-mods/jei")
	assert.Contains(t, out, "lmm import <downloaded-file> --id 238222")
}

// TestReportError_Human_DownloadError_WithoutAPageSaysNothingExtra: no URL
// (the source gave none, or core refused an unsafe one) means no "visit" line.
func TestReportError_Human_DownloadError_WithoutAPageSaysNothingExtra(t *testing.T) {
	err := &core.DownloadError{SourceID: "custom", ModID: "m1", Err: errors.New("boom")}
	out, _ := captureStderrErr(t, func() error { reportError(err); return nil })

	assert.Contains(t, out, "boom")
	assert.NotContains(t, out, "manually")
}

// TestReportError_Human_DownloadError_JSONModeStaysAnEnvelope: the hint is
// terminal text; --json carries the page in the envelope instead.
func TestReportError_Human_DownloadError_JSONModeStaysAnEnvelope(t *testing.T) {
	withJSONOutput(t)
	err := &core.DownloadError{SourceID: "s", ModID: "m", ModURL: "https://example.test/m", Err: errors.New("boom")}
	out, _ := captureStderrErr(t, func() error { reportError(err); return nil })
	assert.Empty(t, out)
}

// TestDoInstall_DownloadFailure_PrintsThePage is the end-to-end form: a real
// install of a Steam Workshop item the publisher refuses, reported the way
// main reports it, names the item's page.
func TestDoInstall_DownloadFailure_PrintsThePage(t *testing.T) {
	svc, game := setupWorkshopInstallTest(t, "3000000002")

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.Error(t, err)

	out, _ := captureStderrErr(t, func() error { reportError(err); return nil })
	assert.Contains(t, out, "Download it manually from: https://steamcommunity.com/sharedfiles/filedetails/?id=3000000002")
}

// TestPrintBatchDownloadPages: a bulk update lists the page of each mod whose
// download failed, and only of those.
func TestPrintBatchDownloadPages(t *testing.T) {
	failed := []core.UpdateBatchFailure{
		{Mod: "nexusmods:1", Name: "Has Page", Error: "downloading update: boom", ModURL: "https://www.nexusmods.com/g/mods/1"},
		{Mod: "nexusmods:2", Name: "No Page", Error: "downloading update: boom"},
		{Mod: "nexusmods:3", Name: "Not A Download", Error: "stale plan"},
	}
	out := captureStdout(t, func() error { printBatchDownloadPages(failed); return nil })

	assert.Contains(t, out, "Has Page: https://www.nexusmods.com/g/mods/1")
	assert.Contains(t, out, "Download it manually from")
	assert.NotContains(t, out, "No Page")
	assert.NotContains(t, out, "Not A Download")

	empty := captureStdout(t, func() error { printBatchDownloadPages(nil); return nil })
	assert.Empty(t, empty)
}

// TestDoModShow_WontPrintAnUnsafeURL: `URL:` is the same http(s)-only rule
// as every other place a mod's page appears.
func TestDoModShow_WontPrintAnUnsafeURL(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "/relative"} {
		svc, game, src := setupDoModLockTest(t)
		mod := richMod(game.ID)
		mod.SourceURL = bad
		src.AddMod(mod, nil)

		out := captureStdout(t, func() error { return doModShow(context.Background(), svc, game, "a") })
		assert.NotContains(t, out, "URL:", "%q must not be printed as a page", bad)
	}
}

// TestApplySingleUpdate_DownloadFailure_PrintsThePage: `lmm update <mod>`
// never printed a page; a failed download now ends with where to get the
// file by hand.
func TestApplySingleUpdate_DownloadFailure_PrintsThePage(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old")})
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1", SourceURL: "https://example.test/mods/mod1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	// No AddDownload("new-1", ...): the file's URL answers 404.

	var err error
	captureStdout(t, func() error {
		err = applySingleUpdate(context.Background(), svc, game, mod, "default")
		return nil
	})
	require.Error(t, err)

	out, _ := captureStderrErr(t, func() error { reportError(err); return nil })
	assert.Contains(t, out, "downloading update:")
	assert.Contains(t, out, "Download it manually from: https://example.test/mods/mod1")
}
