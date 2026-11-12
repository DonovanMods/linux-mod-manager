package core_test

// #621 review: the untrusted-input promises of a collection import, each
// pinned with the payload the review named.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/require"
)

func planDoc(t *testing.T, svc *core.Service, doc, gameID string) (*core.ImportPlan, error) {
	t.Helper()
	return svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(doc), GameID: gameID})
}

// S1: a hook command that would display as another command is refused,
// never rewritten.
func TestCollectionReview_HookCommandsThatHideThemselvesAreRefused(t *testing.T) {
	svc, _ := recipient(t)
	for name, command := range map[string]string{
		"ANSI erase and carriage return": `"/tmp/evil.sh\e[2K\r    - install.after_all: /usr/bin/true"`,
		"bidi override":                  "\"/tmp/\\u202eevil.sh\"",
		"zero-width space":               "\"/usr/bin/tr\\u200bue\"",
		"newline":                        `"/tmp/a\n/usr/bin/true"`,
	} {
		doc := "lmm_collection: 1\nname: p\nmods: []\nhooks:\n  install:\n    after_all: " + command + "\n"
		_, err := planDoc(t, svc, doc, "cyber")
		require.ErrorIs(t, err, core.ErrInvalidCollection, name)
		require.Contains(t, err.Error(), "install.after_all", name)
	}
	plan, err := planDoc(t, svc, "lmm_collection: 1\nname: p\nmods: []\nhooks:\n  install:\n    after_all: /usr/bin/true\n", "cyber")
	require.NoError(t, err)
	require.Equal(t, "/usr/bin/true", plan.Collection.Hooks[0].Command)
}

// S2 and S6: override paths and game.sources keys are refused, not
// rewritten, when they hold control characters; an override that would
// escape the game directory is refused at plan time.
func TestCollectionReview_OverridePathsAndSourceKeys(t *testing.T) {
	svc, _ := recipient(t)
	for name, doc := range map[string]string{
		"override with an escape": "lmm_collection: 1\nname: p\nmods: []\noverrides:\n  \"a\\e[2Jb.ini\": x\n",
		"override escaping":       "lmm_collection: 1\nname: p\nmods: []\noverrides:\n  ../../../.bashrc: evil\n",
		"override absolute":       "lmm_collection: 1\nname: p\nmods: []\noverrides:\n  /etc/passwd: evil\n",
		"source key with escape":  "lmm_collection: 1\nname: p\nmods: []\ngame:\n  sources:\n    \"nexus\\e[31mmods\": x\n",
	} {
		_, err := planDoc(t, svc, doc, "cyber")
		require.ErrorIs(t, err, core.ErrInvalidCollection, name)
	}
	plan, err := planDoc(t, svc, "lmm_collection: 1\nname: p\nmods: []\noverrides:\n  engine/config/user.ini: x\n", "cyber")
	require.NoError(t, err)
	require.Equal(t, []string{"engine/config/user.ini"}, plan.Collection.Overrides)
}

// S3: only a plain web address stays a link; any other is dropped from
// the item and named in a warning.
func TestCollectionReview_UnsafeURLsAreDropped(t *testing.T) {
	svc, _ := recipient(t)
	doc := `lmm_collection: 1
name: p
mods: []
origin:
  url: "javascript:alert(1)"
external:
  - {name: js, url: "javascript:alert(1)"}
  - {name: data, url: "data:text/html,<script>alert(1)</script>"}
  - {name: file, url: "file:///etc/passwd"}
  - {name: relative, url: "/api/v1/profiles"}
  - {name: fine, url: "https://example.invalid/tool"}
`
	plan, err := planDoc(t, svc, doc, "cyber")
	require.NoError(t, err)
	c := plan.Collection
	for _, e := range c.External[:4] {
		require.Empty(t, e.URL, e.Name)
	}
	require.Equal(t, "https://example.invalid/tool", c.External[4].URL)
	require.Nil(t, c.Origin, "an origin left with nothing is no origin")
	joined := strings.Join(c.Warnings, "\n")
	for _, name := range []string{"js", "data", "file", "relative", "origin"} {
		require.Contains(t, joined, name)
	}
}

// S5: a document that has the lmm_collection key with no whole number in
// it is an invalid collection, never a version-0 export.
func TestCollectionReview_AnInvalidVersionKeyIsNeverVersionZero(t *testing.T) {
	svc, _ := recipient(t)
	for _, value := range []string{`"1"`, "", "one", "1.5", "[1]", "0"} {
		doc := "lmm_collection: " + value + "\nname: p\ngame_id: cyber\nmods: []\n"
		require.True(t, core.IsCollectionDocument([]byte(doc)), value)
		_, err := planDoc(t, svc, doc, "cyber")
		require.ErrorIs(t, err, core.ErrInvalidCollection, value)
	}
	require.False(t, core.IsCollectionDocument([]byte("name: p\ngame_id: cyber\nmods: []\n")))
}

// S7: a shared source id does not match a game whose Steam app id says it
// is another game: Fallout 4 / Fallout 4 VR, Skyrim SE / Skyrim VR.
func TestCollectionReview_ASteamAppIDContradictsASharedSourceID(t *testing.T) {
	for _, tc := range []struct {
		name, local, localApp, docApp, source string
	}{
		{"fallout 4 vs VR", "fallout4vr", "611660", "377160", "fallout4"},
		{"skyrim SE vs VR", "skyrimvr", "611670", "489830", "skyrimspecialedition"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFlowsTestService(t)
			require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
				ID: tc.local, Name: tc.local, ModPath: t.TempDir(),
				SourceIDs: map[string]string{"nexusmods": tc.source, "steamworkshop": tc.localApp},
			}))
			doc := "lmm_collection: 1\nname: p\nmods: []\ngame:\n  steam_app_id: \"" + tc.docApp + "\"\n  sources: {nexusmods: " + tc.source + "}\n"

			_, err := planDoc(t, svc, doc, "")
			var gameErr *core.CollectionGameError
			require.True(t, errors.As(err, &gameErr), "%v", err)
			require.Contains(t, err.Error(), tc.docApp)
			require.Contains(t, err.Error(), tc.localApp)
			require.Contains(t, err.Error(), "--game")

			plan, err := planDoc(t, svc, doc, tc.local)
			require.NoError(t, err, "--game overrides")
			require.NotEmpty(t, plan.Collection.Warnings, "and says the ids disagree")
		})
	}

	// With both games present, the Steam app id picks the right one.
	svc := newFlowsTestService(t)
	for _, g := range []*domain.Game{
		{ID: "fallout4", Name: "Fallout 4", ModPath: t.TempDir(), SourceIDs: map[string]string{"nexusmods": "fallout4", "steamworkshop": "377160"}},
		{ID: "fallout4vr", Name: "Fallout 4 VR", ModPath: t.TempDir(), SourceIDs: map[string]string{"nexusmods": "fallout4", "steamworkshop": "611660"}},
	} {
		require.NoError(t, svc.SaveGame(context.Background(), g))
	}
	plan, err := planDoc(t, svc, "lmm_collection: 1\nname: p\nmods: []\ngame:\n  steam_app_id: \"377160\"\n  sources: {nexusmods: fallout4}\n", "")
	require.NoError(t, err)
	require.Equal(t, "fallout4", plan.Collection.GameID)
}

// N3: a game chosen by --game (or the web UI's selection) that shares
// nothing with the document's identity is said so.
func TestCollectionReview_AChosenGameSharingNothingIsWarned(t *testing.T) {
	svc, _ := recipient(t)
	plan, err := planDoc(t, svc, "lmm_collection: 1\nname: p\nmods: []\ngame:\n  name: Other\n  sources: {nexusmods: other}\n", "cyber")
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan.Collection.Warnings, "\n"), "shares no Steam app id or source id")

	plan, err = planDoc(t, svc, "lmm_collection: 1\nname: p\nmods: []\n", "cyber")
	require.NoError(t, err)
	require.Empty(t, plan.Collection.Warnings, "a document naming no game has nothing to disagree with")
}
