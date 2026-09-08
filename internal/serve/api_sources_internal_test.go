package serve

// httptest coverage for the custom-source editor routes (api_sources.go).
// The fixture server registers exactly one fake source and no built-in, so
// nothing here can reach a real mod host; every definition written is a
// directory source over a t.TempDir().

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// directoryYAML is a valid directory definition over path.
func directoryYAML(id, name, path string) string {
	return fmt.Sprintf("id: %s\nname: %s\ntype: directory\ndirectory:\n  path: %s\n", id, name, path)
}

// jsonBody wraps v as a JSON request body.
func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// decodeSourceList decodes a source-list response.
func decodeSourceList(t *testing.T, body []byte) []app.SourceInfo {
	t.Helper()
	var infos []app.SourceInfo
	require.NoError(t, json.Unmarshal(body, &infos))
	return infos
}

// sourceIDs returns the ids in a decoded source list.
func sourceIDs(infos []app.SourceInfo) []string {
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	return ids
}

// decodeEnvelopeDetails decodes a failure body's "details" member as a
// generic object - the wire view a client sees, rather than the Go type
// that produced it.
func decodeEnvelopeDetails(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var env struct {
		Error   string         `json:"error"`
		Details map[string]any `json:"details"`
	}
	require.NoError(t, json.Unmarshal(body, &env))
	return env.Details
}

func TestAPISources_ListsTheRegistryAndItsBrokenDefinitions(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/sources", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{fixtureSourceID}, sourceIDs(decodeSourceList(t, rec.Body.Bytes())))

	// A definition that cannot even parse must still be visible - it is
	// exactly the diagnostic a user debugging their YAML needs.
	require.NoError(t, os.MkdirAll(app.SourcesDir(s.svc.ConfigDir()), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(app.SourcesDir(s.svc.ConfigDir()), "broken.yaml"), []byte("id: [unclosed\n"), 0644))

	rec = doAPI(s, http.MethodGet, "/api/v1/sources", "")
	require.Equal(t, http.StatusOK, rec.Code)
	infos := decodeSourceList(t, rec.Body.Bytes())
	require.Len(t, infos, 2)
	assert.Equal(t, "error", infos[1].Type)
	assert.Contains(t, infos[1].ErrorMessage, "parsing YAML")
}

func TestAPISourceDefinition(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	body := "# notes\n" + directoryYAML("my-mods", "My Mods", t.TempDir())
	require.NoError(t, os.MkdirAll(app.SourcesDir(s.svc.ConfigDir()), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(app.SourcesDir(s.svc.ConfigDir()), "zz.yml"), []byte(body), 0644))

	t.Run("serves the raw text", func(t *testing.T) {
		rec := doAPI(s, http.MethodGet, "/api/v1/sources/my-mods/definition", "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, yamlContentType, rec.Header().Get("Content-Type"))
		assert.Equal(t, body, rec.Body.String(), "comments and formatting survive")
	})

	t.Run("a built-in has no definition file", func(t *testing.T) {
		rec := doAPI(s, http.MethodGet, "/api/v1/sources/nexusmods/definition", "")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("an unknown id", func(t *testing.T) {
		rec := doAPI(s, http.MethodGet, "/api/v1/sources/ghost/definition", "")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestAPISourceValidate(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	t.Run("a valid draft", func(t *testing.T) {
		rec := doAPI(s, http.MethodPost, "/api/v1/sources/validate",
			jsonBody(t, sourceValidateRequest{YAML: directoryYAML("my-mods", "My Mods", t.TempDir())}))
		require.Equal(t, http.StatusOK, rec.Code)

		var report app.SourceValidationReport
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
		assert.True(t, report.Valid)
		assert.Equal(t, "my-mods", report.ID)
		assert.Empty(t, report.Path, "a draft has no file")
	})

	t.Run("an invalid draft is the envelope with the report as details", func(t *testing.T) {
		rec := doAPI(s, http.MethodPost, "/api/v1/sources/validate",
			jsonBody(t, sourceValidateRequest{YAML: "id: BAD_ID\nname: x\ntype: directory\n"}))
		require.Equal(t, http.StatusBadRequest, rec.Code)

		assert.Contains(t, decodeEnvelope(t, rec.Body.Bytes()).Error, "must match")
		details := decodeEnvelopeDetails(t, rec.Body.Bytes())
		require.NotNil(t, details, "details carries the validation report")
		assert.Equal(t, false, details["valid"])
	})

	t.Run("probing a directory source reports what it saw", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "SomeMod"), 0755))
		rec := doAPI(s, http.MethodPost, "/api/v1/sources/validate",
			jsonBody(t, sourceValidateRequest{YAML: directoryYAML("my-mods", "My Mods", root), Probe: true}))
		require.Equal(t, http.StatusOK, rec.Code)

		var report app.SourceValidationReport
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
		require.NotNil(t, report.Probe)
		assert.True(t, report.Probe.OK)
		assert.Contains(t, report.Probe.Summary, "1 mod(s) visible")
	})

	t.Run("a probe failure is 502 and carries the report", func(t *testing.T) {
		// The definition itself is valid - only the LIVE step fails, which
		// is the distinction Valid=true plus probe.error draws.
		missing := filepath.Join(t.TempDir(), "not-here")
		rec := doAPI(s, http.MethodPost, "/api/v1/sources/validate",
			jsonBody(t, sourceValidateRequest{YAML: directoryYAML("my-mods", "My Mods", missing), Probe: true}))
		require.Equal(t, http.StatusBadGateway, rec.Code)

		details := decodeEnvelopeDetails(t, rec.Body.Bytes())
		require.NotNil(t, details)
		assert.Equal(t, true, details["valid"], "the DEFINITION validated; the probe is what failed")
		probe, ok := details["probe"].(map[string]any)
		require.True(t, ok)
		assert.NotEmpty(t, probe["error"])
	})

	t.Run("an empty body is refused", func(t *testing.T) {
		rec := doAPI(s, http.MethodPost, "/api/v1/sources/validate", `{}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("an unknown member is refused", func(t *testing.T) {
		rec := doAPI(s, http.MethodPost, "/api/v1/sources/validate", `{"yamll":"x"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestAPISourceSave(t *testing.T) {
	t.Run("writes the file and registers the source live", func(t *testing.T) {
		s, _ := newDeployFixtureServer(t)
		yaml := directoryYAML("my-mods", "My Mods", t.TempDir())

		rec := doAPI(s, http.MethodPut, "/api/v1/sources/my-mods", jsonBody(t, sourceSaveRequest{YAML: yaml}))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, sourceIDs(decodeSourceList(t, rec.Body.Bytes())), "my-mods",
			"the response is the re-read list, so no follow-up GET is needed")

		onDisk, err := os.ReadFile(filepath.Join(app.SourcesDir(s.svc.ConfigDir()), "my-mods.yaml"))
		require.NoError(t, err)
		assert.Equal(t, yaml, string(onDisk))

		src, err := s.svc.GetSource("my-mods")
		require.NoError(t, err)
		assert.Equal(t, "My Mods", src.Name(), "the RUNNING registry serves it - no restart")
	})

	t.Run("an edit swaps the live source", func(t *testing.T) {
		s, _ := newDeployFixtureServer(t)
		require.Equal(t, http.StatusOK, doAPI(s, http.MethodPut, "/api/v1/sources/my-mods",
			jsonBody(t, sourceSaveRequest{YAML: directoryYAML("my-mods", "Before", t.TempDir())})).Code)
		require.Equal(t, http.StatusOK, doAPI(s, http.MethodPut, "/api/v1/sources/my-mods",
			jsonBody(t, sourceSaveRequest{YAML: directoryYAML("my-mods", "After", t.TempDir())})).Code)

		src, err := s.svc.GetSource("my-mods")
		require.NoError(t, err)
		assert.Equal(t, "After", src.Name())
	})

	t.Run("the path id must equal the document's id", func(t *testing.T) {
		s, _ := newDeployFixtureServer(t)
		rec := doAPI(s, http.MethodPut, "/api/v1/sources/my-mods",
			jsonBody(t, sourceSaveRequest{YAML: directoryYAML("other", "Other", t.TempDir())}))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.NoDirExists(t, app.SourcesDir(s.svc.ConfigDir()))
	})

	t.Run("a built-in id is claimed already", func(t *testing.T) {
		s, _ := newDeployFixtureServer(t)
		rec := doAPI(s, http.MethodPut, "/api/v1/sources/nexusmods",
			jsonBody(t, sourceSaveRequest{YAML: directoryYAML("nexusmods", "Impostor", t.TempDir())}))
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.NoDirExists(t, app.SourcesDir(s.svc.ConfigDir()))
	})

	t.Run("an unconstructable definition is refused with its report", func(t *testing.T) {
		s, _ := newDeployFixtureServer(t)
		rec := doAPI(s, http.MethodPut, "/api/v1/sources/my-mods",
			jsonBody(t, sourceSaveRequest{YAML: directoryYAML("my-mods", "My Mods", filepath.Join(t.TempDir(), "nope"))}))
		require.Equal(t, http.StatusBadRequest, rec.Code)

		details := decodeEnvelopeDetails(t, rec.Body.Bytes())
		require.NotNil(t, details)
		assert.Equal(t, false, details["valid"])
		assert.NoDirExists(t, app.SourcesDir(s.svc.ConfigDir()))
	})

	t.Run("a missing CSRF token is refused before anything is written", func(t *testing.T) {
		s, _ := newDeployFixtureServer(t)
		req := apiRequest(s, http.MethodPut, "/api/v1/sources/my-mods",
			jsonBody(t, sourceSaveRequest{YAML: directoryYAML("my-mods", "My Mods", t.TempDir())}))
		req.Header.Del(csrfHeaderName)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.NoDirExists(t, app.SourcesDir(s.svc.ConfigDir()))
	})
}

func TestAPISourceDelete(t *testing.T) {
	save := func(t *testing.T, s *Server) {
		t.Helper()
		require.Equal(t, http.StatusOK, doAPI(s, http.MethodPut, "/api/v1/sources/my-mods",
			jsonBody(t, sourceSaveRequest{YAML: directoryYAML("my-mods", "My Mods", t.TempDir())})).Code)
	}

	t.Run("removes the file and unregisters the source", func(t *testing.T) {
		s, _ := newDeployFixtureServer(t)
		save(t, s)

		rec := doAPI(s, http.MethodDelete, "/api/v1/sources/my-mods", "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.NotContains(t, sourceIDs(decodeSourceList(t, rec.Body.Bytes())), "my-mods")
		assert.NoFileExists(t, filepath.Join(app.SourcesDir(s.svc.ConfigDir()), "my-mods.yaml"))
		_, err := s.svc.GetSource("my-mods")
		assert.Error(t, err)
	})

	t.Run("a built-in and an unknown id are both 404", func(t *testing.T) {
		s, _ := newDeployFixtureServer(t)
		assert.Equal(t, http.StatusNotFound, doAPI(s, http.MethodDelete, "/api/v1/sources/nexusmods", "").Code)
		assert.Equal(t, http.StatusNotFound, doAPI(s, http.MethodDelete, "/api/v1/sources/ghost", "").Code)
	})

	t.Run("a source a game still maps is 409 naming the games", func(t *testing.T) {
		s, game := newDeployFixtureServer(t)
		save(t, s)
		game.SourceIDs["my-mods"] = ""
		require.NoError(t, s.svc.SaveGame(t.Context(), game))

		rec := doAPI(s, http.MethodDelete, "/api/v1/sources/my-mods", "")
		require.Equal(t, http.StatusConflict, rec.Code)

		details := decodeEnvelopeDetails(t, rec.Body.Bytes())
		require.NotNil(t, details)
		assert.Equal(t, "my-mods", details["source_id"])
		assert.Equal(t, []any{game.ID}, details["games"])
		assert.FileExists(t, filepath.Join(app.SourcesDir(s.svc.ConfigDir()), "my-mods.yaml"))
	})
}

// TestAPISources_MethodNotAllowedNamesTheWorkingMethods pins the widened
// apiMethodsToProbe: a GET of the save-only path names PUT and DELETE
// instead of answering a bare 404.
func TestAPISources_MethodNotAllowedNamesTheWorkingMethods(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/sources/my-mods", "")
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "PUT, DELETE", rec.Header().Get("Allow"))
}
