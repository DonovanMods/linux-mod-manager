package config_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

func collectionDocFixture() *domain.CollectionDocument {
	return &domain.CollectionDocument{
		FormatVersion: 1,
		Name:          "coop",
		GameID:        "cp2077",
		Info: domain.CollectionInfo{
			ID: "2f8c7a3e-0000-4000-8000-000000000001", Version: 3, Title: "Welcome to Night City",
			Author: "curator", Description: "Line one.\nLine two.", InstallInstructions: "Run once.",
			Origin: &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "iszwwe", Revision: 484},
		},
		Game: domain.CollectionGame{
			Name: "Cyberpunk 2077", SteamAppID: "1091500",
			Sources: map[string]string{"nexusmods": "cyberpunk2077"},
		},
		Requires: []string{"external"},
		Items: []domain.CollectionItem{
			{Ref: domain.ModReference{SourceID: "nexusmods", ModID: "1", Version: "1.8.0", FileIDs: []string{"10"}, Locked: true}, Name: "Much Better Ammo",
				Files: []domain.CollectionFileRef{{ID: "10", MD5: "25aa", Size: 1234}}},
			{Ref: domain.ModReference{SourceID: "nexusmods", ModID: "2", Version: "2", Disabled: true, Category: "UI"}},
		},
		External: []domain.CollectionExternalItem{{Name: "CET", URL: "https://example.invalid/cet", Instructions: "Install by hand."}},
		Hooks:    domain.GameHooks{Install: domain.HookConfig{AfterAll: "/bin/true", BeforeAll: ""}},
		HooksExplicit: domain.GameHooksExplicit{
			Install: domain.HookExplicitFlags{AfterAll: true, BeforeAll: true},
		},
		Overrides: map[string]string{"engine/config/user.ini": "[A]\nb=1\n"},
	}
}

func TestMarshalCollection_Golden(t *testing.T) {
	data, err := config.MarshalCollection(collectionDocFixture())
	require.NoError(t, err)
	want := `lmm_collection: 1
id: 2f8c7a3e-0000-4000-8000-000000000001
version: 3
name: coop
title: Welcome to Night City
author: curator
description: |-
    Line one.
    Line two.
install_instructions: Run once.
game_id: cp2077
game:
    name: Cyberpunk 2077
    steam_app_id: "1091500"
    sources:
        nexusmods: cyberpunk2077
requires:
    - external
origin:
    source_id: nexusmods
    slug: iszwwe
    revision: 484
mods:
    - source_id: nexusmods
      mod_id: "1"
      version: 1.8.0
      file_ids:
        - "10"
      locked: true
      name: Much Better Ammo
      files:
        - id: "10"
          md5: 25aa
          size: 1234
    - source_id: nexusmods
      mod_id: "2"
      version: "2"
      disabled: true
      category: UI
external:
    - name: CET
      url: https://example.invalid/cet
      instructions: Install by hand.
hooks:
    install:
        before_all: ""
        after_all: /bin/true
overrides:
    engine/config/user.ini: |
        [A]
        b=1
`
	require.Equal(t, want, string(data))
}

func TestParseCollection_RoundTrip(t *testing.T) {
	doc := collectionDocFixture()
	data, err := config.MarshalCollection(doc)
	require.NoError(t, err)
	back, err := config.ParseCollection(data)
	require.NoError(t, err)
	require.Equal(t, doc, back)
}

func TestParseCollection_OlderReaderStillImportsIt(t *testing.T) {
	// The ruling (§12): a version-1 document keeps the author's game_id
	// and name, and today's version-0 reader - which ignores unknown keys -
	// imports it by them.
	data, err := config.MarshalCollection(collectionDocFixture())
	require.NoError(t, err)
	profile, err := config.ImportProfile(data)
	require.NoError(t, err)
	require.Equal(t, "coop", profile.Name)
	require.Equal(t, "cp2077", profile.GameID)
	require.Len(t, profile.Mods, 2, "only mods: entries; external items are a separate key")
	require.Equal(t, "1", profile.Mods[0].ModID)
}

func TestCollectionFormatOf(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		want    int
		wantErr bool
	}{
		{"version 0 (no key)", "name: p\ngame_id: g\nmods: []\n", 0, false},
		{"version 1", "lmm_collection: 1\nname: p\n", 1, false},
		{"newer", "lmm_collection: 2\nname: p\n", 2, false},
		{"not a number", "lmm_collection: one\n", 0, true},
		{"not yaml", "\t: [", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := config.CollectionFormatOf([]byte(tc.doc))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
