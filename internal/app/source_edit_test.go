package app

// Tests for source_edit.go - the write half of the custom-source surface
// (#333). Every fixture is local: a directory source over t.TempDir(), or
// an api source pointed at an httptest server, so nothing here can reach a
// real mod host.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// directoryDefinitionYAML is a minimal, VALID directory definition over
// path - the cheapest definition that both validates and constructs.
func directoryDefinitionYAML(id, name, path string) string {
	return fmt.Sprintf("id: %s\nname: %s\ntype: directory\ndirectory:\n  path: %s\n", id, name, path)
}

func TestValidateSourceContent(t *testing.T) {
	t.Run("a valid definition reports no path", func(t *testing.T) {
		report, def, err := ValidateSourceContent([]byte(directoryDefinitionYAML("my-mods", "My Mods", t.TempDir())))
		require.NoError(t, err)
		assert.True(t, report.Valid)
		assert.Equal(t, "my-mods", report.ID)
		assert.Equal(t, "directory", report.Type)
		assert.Empty(t, report.Path, "content has no file to name")
		assert.Equal(t, "my-mods", def.ID)
	})

	t.Run("an invalid definition reports the same wording a file would", func(t *testing.T) {
		report, _, err := ValidateSourceContent([]byte("id: My-Mods\nname: x\ntype: directory\n"))
		require.Error(t, err)
		assert.False(t, report.Valid)
		require.Len(t, report.Errors, 1)
		assert.Contains(t, report.Errors[0], "invalid definition")
	})

	t.Run("unparseable YAML", func(t *testing.T) {
		report, _, err := ValidateSourceContent([]byte("id: [unclosed\n"))
		require.Error(t, err)
		assert.False(t, report.Valid)
		require.Len(t, report.Errors, 1)
		assert.Contains(t, report.Errors[0], "parsing YAML")
	})
}

func TestIsBuiltinSourceID(t *testing.T) {
	assert.True(t, IsBuiltinSourceID("nexusmods"))
	assert.True(t, IsBuiltinSourceID("curseforge"))
	assert.False(t, IsBuiltinSourceID("my-mods"))
}

func TestSourceDefinitionFile_MatchesTheDefinitionsOwnID(t *testing.T) {
	cfg := t.TempDir()
	// The filename deliberately disagrees with the id: nothing has ever
	// required them to match.
	writeSourceYAML(t, SourcesDir(cfg), "zz-whatever.yml", directoryDefinitionYAML("my-mods", "My Mods", t.TempDir()))
	// A file that does not parse must be skipped, not fatal.
	writeSourceYAML(t, SourcesDir(cfg), "broken.yaml", "id: [unclosed\n")

	path, err := SourceDefinitionFile(cfg, "my-mods")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(SourcesDir(cfg), "zz-whatever.yml"), path)

	_, err = SourceDefinitionFile(cfg, "nope")
	require.ErrorIs(t, err, ErrSourceDefinitionNotFound)

	_, err = SourceDefinitionFile(t.TempDir(), "my-mods")
	require.ErrorIs(t, err, ErrSourceDefinitionNotFound, "a missing sources directory is not-found, not an I/O failure")
}

func TestReadSourceDefinition_ReturnsTheRawText(t *testing.T) {
	cfg := t.TempDir()
	body := "# my notes\n" + directoryDefinitionYAML("my-mods", "My Mods", t.TempDir())
	writeSourceYAML(t, SourcesDir(cfg), "my-mods.yaml", body)

	got, err := ReadSourceDefinition(cfg, "my-mods")
	require.NoError(t, err)
	assert.Equal(t, body, string(got), "comments and formatting survive - this is the editor's text")

	_, err = ReadSourceDefinition(cfg, "nexusmods")
	require.ErrorIs(t, err, ErrSourceDefinitionNotFound)
}

func TestSaveSourceDefinition(t *testing.T) {
	t.Run("a new definition is written and registered live", func(t *testing.T) {
		svc := newTestService(t)
		yaml := directoryDefinitionYAML("my-mods", "My Mods", t.TempDir())

		report, err := SaveSourceDefinition(t.Context(), svc, "my-mods", []byte(yaml))
		require.NoError(t, err)
		assert.True(t, report.Valid)

		path := filepath.Join(SourcesDir(svc.ConfigDir()), "my-mods.yaml")
		onDisk, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, yaml, string(onDisk), "the bytes submitted are the bytes stored")

		src, err := svc.GetSource("my-mods")
		require.NoError(t, err)
		assert.Equal(t, "My Mods", src.Name(), "the running registry serves the saved source, no restart")
	})

	t.Run("an edit rewrites the definition's own file and swaps the source", func(t *testing.T) {
		svc := newTestService(t)
		writeSourceYAML(t, SourcesDir(svc.ConfigDir()), "zz-whatever.yml", directoryDefinitionYAML("my-mods", "Old Name", t.TempDir()))
		registerCustomSources(t.Context(), svc, svc.ConfigDir(), os.Stderr)

		_, err := SaveSourceDefinition(t.Context(), svc, "my-mods", []byte(directoryDefinitionYAML("my-mods", "New Name", t.TempDir())))
		require.NoError(t, err)

		assert.NoFileExists(t, filepath.Join(SourcesDir(svc.ConfigDir()), "my-mods.yaml"), "the existing file is edited, not duplicated")
		onDisk, err := os.ReadFile(filepath.Join(SourcesDir(svc.ConfigDir()), "zz-whatever.yml"))
		require.NoError(t, err)
		assert.Contains(t, string(onDisk), "New Name")

		src, err := svc.GetSource("my-mods")
		require.NoError(t, err)
		assert.Equal(t, "New Name", src.Name())
	})

	t.Run("an id mismatch is refused and writes nothing", func(t *testing.T) {
		svc := newTestService(t)

		_, err := SaveSourceDefinition(t.Context(), svc, "my-mods", []byte(directoryDefinitionYAML("other", "Other", t.TempDir())))
		require.ErrorIs(t, err, ErrSourceIDMismatch)
		assert.NoDirExists(t, SourcesDir(svc.ConfigDir()))
	})

	t.Run("a built-in id is refused", func(t *testing.T) {
		svc := newTestService(t)

		_, err := SaveSourceDefinition(t.Context(), svc, "", []byte(directoryDefinitionYAML("nexusmods", "Impostor", t.TempDir())))
		require.ErrorIs(t, err, ErrBuiltinSourceID)
		assert.NoDirExists(t, SourcesDir(svc.ConfigDir()))
	})

	t.Run("an invalid definition is refused with its report and writes nothing", func(t *testing.T) {
		svc := newTestService(t)

		report, err := SaveSourceDefinition(t.Context(), svc, "", []byte("id: my-mods\nname: My Mods\ntype: directory\n"))
		require.Error(t, err)
		assert.False(t, report.Valid)
		assert.NoDirExists(t, SourcesDir(svc.ConfigDir()))
	})

	t.Run("a definition that validates but cannot construct is refused", func(t *testing.T) {
		svc := newTestService(t)
		missing := filepath.Join(t.TempDir(), "nope")

		report, err := SaveSourceDefinition(t.Context(), svc, "", []byte(directoryDefinitionYAML("my-mods", "My Mods", missing)))
		require.Error(t, err)
		assert.False(t, report.Valid, "a construction failure lands in the same report the editor renders")
		require.NotEmpty(t, report.Errors)
		assert.NoDirExists(t, SourcesDir(svc.ConfigDir()))
	})
}

func TestDeleteSourceDefinition(t *testing.T) {
	t.Run("removes the file and unregisters the source", func(t *testing.T) {
		svc := newTestService(t)
		require.NoError(t, func() error {
			_, err := SaveSourceDefinition(t.Context(), svc, "", []byte(directoryDefinitionYAML("my-mods", "My Mods", t.TempDir())))
			return err
		}())

		require.NoError(t, DeleteSourceDefinition(t.Context(), svc, "my-mods"))
		assert.NoFileExists(t, filepath.Join(SourcesDir(svc.ConfigDir()), "my-mods.yaml"))
		_, err := svc.GetSource("my-mods")
		require.Error(t, err)
	})

	t.Run("refuses a built-in", func(t *testing.T) {
		svc := newTestService(t)
		require.ErrorIs(t, DeleteSourceDefinition(t.Context(), svc, "nexusmods"), ErrBuiltinSourceID)
	})

	t.Run("refuses an id no definition defines", func(t *testing.T) {
		svc := newTestService(t)
		require.ErrorIs(t, DeleteSourceDefinition(t.Context(), svc, "ghost"), ErrSourceDefinitionNotFound)
	})

	t.Run("refuses a source configured games still map, keeping the file", func(t *testing.T) {
		svc := newTestService(t)
		_, err := SaveSourceDefinition(t.Context(), svc, "", []byte(directoryDefinitionYAML("my-mods", "My Mods", t.TempDir())))
		require.NoError(t, err)
		require.NoError(t, svc.SaveGame(t.Context(), &domain.Game{
			ID: "g1", Name: "G1", InstallPath: t.TempDir(), ModPath: t.TempDir(),
			SourceIDs: map[string]string{"my-mods": ""},
		}))

		err = DeleteSourceDefinition(t.Context(), svc, "my-mods")
		var inUse *core.SourceInUseError
		require.ErrorAs(t, err, &inUse)
		assert.Equal(t, []string{"g1"}, inUse.Games)
		assert.FileExists(t, filepath.Join(SourcesDir(svc.ConfigDir()), "my-mods.yaml"))
		_, getErr := svc.GetSource("my-mods")
		assert.NoError(t, getErr, "a refused delete leaves the source registered")
	})
}

// apiKeyEchoServer answers every request with one mod whose NAME is the API
// key the request carried, so a Search result proves which key the live
// source object is actually using.
func apiKeyEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Test-Key")
		if key == "" {
			key = "(none)"
		}
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // best-effort test-server write
		_, _ = fmt.Fprintf(w, `{"results":[{"id":"1","name":%q}]}`, key)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// apiDefinitionYAML is an api definition against base, authenticating with
// the X-Test-Key header - the smallest definition that both constructs and
// proves which key it holds.
func apiDefinitionYAML(id, base string) string {
	return fmt.Sprintf(`id: %s
name: Echo Source
type: api
allow_http: true
api:
  base_url: %s
  auth:
    api_key:
      in: header
      name: X-Test-Key
  endpoints:
    search:
      path: /search
      list: results
  mappings:
    mod:
      id: id
      name: name
`, id, base)
}

// keyInUse asks the REGISTERED source what key it is sending, by searching
// against the echo server.
func keyInUse(t *testing.T, svc *core.Service, sourceID string) string {
	t.Helper()
	src, err := svc.GetSource(sourceID)
	require.NoError(t, err)
	res, err := src.Search(t.Context(), source.SearchQuery{PageSize: 1})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	return res.Mods[0].Name
}

// TestRekeySource closes A1's recorded gap: before #333 a credential saved
// through the web UI only took effect at the next `lmm serve` start,
// because nothing re-keyed the already-registered source object.
func TestRekeySource(t *testing.T) {
	echo := apiKeyEchoServer(t)
	svc := newTestService(t)
	// No environment key, so the stored token is what resolves; the derived
	// name is the one ResolveAPIKey would consult for this id.
	t.Setenv(EnvKeyForSourceID("echo"), "")

	require.NoError(t, svc.SaveSourceToken(t.Context(), "echo", "key-A"))
	writeSourceYAML(t, SourcesDir(svc.ConfigDir()), "echo.yaml", apiDefinitionYAML("echo", echo.URL))
	registerCustomSources(t.Context(), svc, svc.ConfigDir(), os.Stderr)
	require.Equal(t, "key-A", keyInUse(t, svc, "echo"), "the fixture starts on key A")

	// A new key stored the way POST /api/v1/auth/{source} stores it.
	require.NoError(t, svc.SaveSourceToken(t.Context(), "echo", "key-B"))
	assert.Equal(t, "key-A", keyInUse(t, svc, "echo"), "storing alone does not re-key the live source")

	swapped, err := RekeySource(t.Context(), svc, "echo")
	require.NoError(t, err)
	assert.True(t, swapped)
	assert.Equal(t, "key-B", keyInUse(t, svc, "echo"), "the next call through the registry uses the new key")

	// ...and a logout falls back to the env/none state.
	require.NoError(t, svc.DeleteSourceToken(t.Context(), "echo"))
	swapped, err = RekeySource(t.Context(), svc, "echo")
	require.NoError(t, err)
	assert.True(t, swapped)
	assert.Equal(t, "(none)", keyInUse(t, svc, "echo"), "no token and no env var means no key")

	// The environment still wins, exactly as it does at startup.
	t.Setenv(EnvKeyForSourceID("echo"), "key-ENV")
	require.NoError(t, svc.SaveSourceToken(t.Context(), "echo", "key-C"))
	_, err = RekeySource(t.Context(), svc, "echo")
	require.NoError(t, err)
	assert.Equal(t, "key-ENV", keyInUse(t, svc, "echo"))
}

func TestRekeySource_LeavesUnaffectedSourcesAlone(t *testing.T) {
	svc := newTestService(t)

	swapped, err := RekeySource(t.Context(), svc, "not-registered")
	require.NoError(t, err)
	assert.False(t, swapped, "an orphaned token's source may not exist at all")

	// A registered source with no Auth capability is left alone: a key
	// stored for it was never attached in the first place.
	writeSourceYAML(t, SourcesDir(svc.ConfigDir()), "plain.yaml", directoryDefinitionYAML("plain", "Plain", t.TempDir()))
	registerCustomSources(t.Context(), svc, svc.ConfigDir(), os.Stderr)
	before, err := svc.GetSource("plain")
	require.NoError(t, err)

	swapped, err = RekeySource(t.Context(), svc, "plain")
	require.NoError(t, err)
	assert.False(t, swapped)
	after, err := svc.GetSource("plain")
	require.NoError(t, err)
	assert.Same(t, before, after, "the very same object is still registered")
}

// TestRebuildSource_Builtin proves the built-in half of the rebuild - the
// factory path a `POST /api/v1/auth/nexusmods` re-key takes.
func TestRebuildSource_Builtin(t *testing.T) {
	t.Setenv("NEXUSMODS_API_KEY", "")
	svc := newTestService(t)
	require.NoError(t, svc.SaveSourceToken(t.Context(), "nexusmods", "stored-key"))

	src, err := RebuildSource(t.Context(), svc, "nexusmods")
	require.NoError(t, err)
	auth, ok := src.(authChecker)
	require.True(t, ok)
	assert.True(t, auth.IsAuthenticated(), "the rebuilt built-in carries the stored key")

	_, err = RebuildSource(t.Context(), svc, "ghost")
	require.ErrorIs(t, err, ErrSourceDefinitionNotFound)
}
