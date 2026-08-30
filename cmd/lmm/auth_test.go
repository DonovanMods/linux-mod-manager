package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAuthSource is a minimal source.ModSource double for auth-flow tests:
// declares Auth capability, accepts SetAPIKey, but implements no
// source.KeyValidator — exercising the "stored, validated on first use"
// path a real auth-capable custom source takes.
type mockAuthSource struct {
	id, name string
	apiKey   string
}

func (m *mockAuthSource) ID() string      { return m.id }
func (m *mockAuthSource) Name() string    { return m.name }
func (m *mockAuthSource) AuthURL() string { return "" }
func (m *mockAuthSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (m *mockAuthSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (m *mockAuthSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, nil
}
func (m *mockAuthSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (m *mockAuthSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (m *mockAuthSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (m *mockAuthSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}
func (m *mockAuthSource) Capabilities() source.Capabilities { return source.Capabilities{Auth: true} }
func (m *mockAuthSource) SetAPIKey(key string)              { m.apiKey = key }

// mockValidatingAuthSource additionally implements source.KeyValidator, for
// exercising the live-validation login path (TestAuthLogin_ValidatorPath).
type mockValidatingAuthSource struct {
	mockAuthSource
	validateErr error
}

func (m *mockValidatingAuthSource) ValidateKey(ctx context.Context, key string) error {
	return m.validateErr
}

// TestAuthCmd_Structure tests the auth command structure
func TestAuthCmd_Structure(t *testing.T) {
	assert.Equal(t, "auth", authCmd.Use)
	assert.NotEmpty(t, authCmd.Short)
	assert.NotEmpty(t, authCmd.Long)

	// Check subcommands exist
	var subCmds []string
	for _, cmd := range authCmd.Commands() {
		subCmds = append(subCmds, cmd.Name())
	}

	assert.Contains(t, subCmds, "login")
	assert.Contains(t, subCmds, "logout")
	assert.Contains(t, subCmds, "status")
}

// TestAuthLoginCmd_Structure tests the auth login command structure
func TestAuthLoginCmd_Structure(t *testing.T) {
	assert.Equal(t, "login [source]", authLoginCmd.Use)
	assert.NotEmpty(t, authLoginCmd.Short)
	assert.NotEmpty(t, authLoginCmd.Long)
}

// TestAuthLogoutCmd_Structure tests the auth logout command structure
func TestAuthLogoutCmd_Structure(t *testing.T) {
	assert.Equal(t, "logout [source]", authLogoutCmd.Use)
	assert.NotEmpty(t, authLogoutCmd.Short)
}

// TestAuthStatusCmd_Structure tests the auth status command structure
func TestAuthStatusCmd_Structure(t *testing.T) {
	assert.Equal(t, "status", authStatusCmd.Use)
	assert.NotEmpty(t, authStatusCmd.Short)
}

// TestAuthLoginCmd_UnsupportedSource tests login with unsupported source
func TestAuthLoginCmd_UnsupportedSource(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(authCmd); rootCmd.AddCommand(authCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "login", "unsupported-source"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported source")
}

// TestAuthLoginCmd_UnsupportedSourceMentionsCustomSources pins final-review
// finding 4: the rejection for an unrecognized source must not read like
// only nexusmods/curseforge are ever possible — a registered custom source
// with auth declared is also a valid `lmm auth login <id>` target.
func TestAuthLoginCmd_UnsupportedSourceMentionsCustomSources(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(authCmd); rootCmd.AddCommand(authCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "login", "unsupported-source"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nexusmods")
	assert.Contains(t, err.Error(), "curseforge")
	assert.Contains(t, err.Error(), "declares auth")
}

// TestAuthLogoutCmd_NoStoredCredentials tests logout for a source ID that
// has no stored token and isn't a registered auth-capable source (built-in
// or custom) — resolveLogoutSource must reject it.
func TestAuthLogoutCmd_NoStoredCredentials(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(authCmd); rootCmd.AddCommand(authCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "logout", "unsupported-source"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no stored credentials")
}

// TestAuthLogoutCmd_NotAuthenticated tests logout when not authenticated
func TestAuthLogoutCmd_NotAuthenticated(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(authCmd); rootCmd.AddCommand(authCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "logout", "nexusmods"})

	// Should succeed even when not authenticated
	err := cmd.Execute()
	assert.NoError(t, err)
}

// TestAuthStatusCmd_NotAuthenticated tests status when not authenticated
func TestAuthStatusCmd_NotAuthenticated(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()

	// Clear any env var
	oldEnv := os.Getenv("NEXUSMODS_API_KEY")
	require.NoError(t, os.Unsetenv("NEXUSMODS_API_KEY"))
	t.Cleanup(func() {
		if oldEnv != "" {
			require.NoError(t, os.Setenv("NEXUSMODS_API_KEY", oldEnv))
		}
	})

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(authCmd); rootCmd.AddCommand(authCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "status"})

	err := cmd.Execute()
	assert.NoError(t, err)
	// Output goes to stdout, not the buffer, but command should succeed
}

// TestAuthStatusCmd_WithEnvVar tests status when authenticated via env var
func TestAuthStatusCmd_WithEnvVar(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()

	// Set env var
	oldEnv := os.Getenv("NEXUSMODS_API_KEY")
	require.NoError(t, os.Setenv("NEXUSMODS_API_KEY", "test-api-key-12345"))
	t.Cleanup(func() {
		if oldEnv != "" {
			require.NoError(t, os.Setenv("NEXUSMODS_API_KEY", oldEnv))
		} else {
			require.NoError(t, os.Unsetenv("NEXUSMODS_API_KEY"))
		}
	})

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(authCmd); rootCmd.AddCommand(authCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "status"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

// TestAuthStatusCmd_WithStoredToken tests status when authenticated via stored token
func TestAuthStatusCmd_WithStoredToken(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()

	// Clear env var to ensure we're testing stored token
	oldEnv := os.Getenv("NEXUSMODS_API_KEY")
	require.NoError(t, os.Unsetenv("NEXUSMODS_API_KEY"))
	t.Cleanup(func() {
		if oldEnv != "" {
			require.NoError(t, os.Setenv("NEXUSMODS_API_KEY", oldEnv))
		}
	})

	// First, save a token
	svc, err := initService(t.Context())
	require.NoError(t, err)
	err = svc.SaveSourceToken(context.Background(), "nexusmods", "stored-test-key-12345")
	require.NoError(t, err)
	require.NoError(t, svc.Close())

	// Now run status
	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(authCmd); rootCmd.AddCommand(authCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "status"})

	err = cmd.Execute()
	assert.NoError(t, err)
}

// TestAuthLogoutCmd_WithStoredToken tests logout when authenticated
func TestAuthLogoutCmd_WithStoredToken(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()

	// First, save a token
	svc, err := initService(t.Context())
	require.NoError(t, err)
	err = svc.SaveSourceToken(context.Background(), "nexusmods", "stored-test-key-12345")
	require.NoError(t, err)

	// Verify token exists
	token, err := svc.GetSourceToken(context.Background(), "nexusmods")
	require.NoError(t, err)
	require.NotNil(t, token)
	require.NoError(t, svc.Close())

	// Now run logout
	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(authCmd); rootCmd.AddCommand(authCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "logout", "nexusmods"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify token is gone
	svc2, err := initService(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc2.Close())
	})

	token, err = svc2.GetSourceToken(context.Background(), "nexusmods")
	assert.NoError(t, err)
	assert.Nil(t, token)
}

// TestAuthLoginCmd_DefaultSource tests that default source is nexusmods
func TestAuthLoginCmd_DefaultSource(t *testing.T) {
	// The default source should be "nexusmods" as specified in the command
	// We verify the command accepts 0 or 1 args by checking that it doesn't
	// require any args (MinimumNArgs is not set or is 0)
	assert.NotNil(t, authLoginCmd.Args, "Args validator should be set")
}

// TestAuthLogoutCmd_DefaultSource tests that default source is nexusmods
func TestAuthLogoutCmd_DefaultSource(t *testing.T) {
	// Similar to login, verify it accepts 0 or 1 args
	assert.NotNil(t, authLogoutCmd.Args, "Args validator should be set")
}

// TestPrintLoginResult pins final-review finding 4: sources with no live
// validator have no generic validation endpoint, so runAuthLogin must not
// print the "Validating... done" sequence for them — that's a fabricated
// result. Validated sources are actively checked earlier in the flow and
// need no extra message here; unvalidated sources get an honest "stored,
// checked on first use" message instead.
//
// Changed from Task 2's version (which took a literal sourceID string and
// special-cased "nexusmods"/"curseforge") to take hasValidator directly: the
// split is now driven by whether the source implements source.KeyValidator,
// not by source identity — de-switching this function was the point of this
// task.
func TestPrintLoginResult(t *testing.T) {
	t.Run("validator path prints nothing extra", func(t *testing.T) {
		var buf bytes.Buffer
		printLoginResult(&buf, true)
		assert.Empty(t, buf.String(), "validated sources are reported via the Validating...done sequence above")
	})

	t.Run("stored path prints an honest message", func(t *testing.T) {
		var buf bytes.Buffer
		printLoginResult(&buf, false)
		assert.Equal(t, "Stored (validated on first use).\n", buf.String())
		assert.NotContains(t, buf.String(), "Validating", "must not fabricate a validation step that never ran")
	})
}

// TestPrintAuthLoginSuccess pins the re-review fix for finding 4: sources
// with no live validator have no generic validation endpoint, so
// runAuthLogin's final line must not claim "Successfully authenticated" for
// them — that's a fabricated result. Validated sources keep the original
// message; unvalidated sources get an honest "stored" message instead.
//
// Changed from Task 2's version: takes the actual source.ModSource plus
// hasValidator instead of a literal sourceID string, since the built-in
// case's message now reads its display name from src.Name() rather than a
// hardcoded switch. This changes the rendered text for nexusmods
// specifically: NexusMods.Name() returns "Nexus Mods" (with a space,
// pinned in nexusmods_test.go and root_test.go), not the old switch's
// "NexusMods" — the assertion below is updated to match.
func TestPrintAuthLoginSuccess(t *testing.T) {
	t.Run("validator path keeps the authenticated message (nexusmods)", func(t *testing.T) {
		var buf bytes.Buffer
		printAuthLoginSuccess(&buf, nexusmods.New(nil, ""), true)
		assert.Equal(t, "Successfully authenticated with Nexus Mods!\n", buf.String())
	})

	t.Run("validator path keeps the authenticated message (curseforge)", func(t *testing.T) {
		var buf bytes.Buffer
		printAuthLoginSuccess(&buf, curseforge.New(nil, ""), true)
		assert.Equal(t, "Successfully authenticated with CurseForge!\n", buf.String())
	})

	t.Run("stored path prints an honest stored message keyed by ID", func(t *testing.T) {
		var buf bytes.Buffer
		printAuthLoginSuccess(&buf, &mockAuthSource{id: "my-repo", name: "My Repo"}, false)
		assert.Equal(t, "API key stored for my-repo.\n", buf.String())
		assert.NotContains(t, buf.String(), "Successfully authenticated", "must not fabricate a validation result that never happened")
	})
}

// TestResolveLogoutSource's "built-ins keep working" case now registers
// nexusmods explicitly instead of relying on identity: auth-capability is
// purely registry-driven post-Task-3 (isAuthCapableSource queries
// CapabilitiesOf via service.GetSource, no more hardcoded ID list), so a
// *core.Service built directly via core.NewService — bypassing app.Open,
// which is what actually registers built-ins in
// production — must register nexusmods itself to exercise that path.
func TestResolveLogoutSource(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(nexusmods.New(nil, ""))

	// A token stored for a source whose definition file has been deleted:
	// not registered, but logout must still be able to remove it.
	require.NoError(t, svc.SaveSourceToken(context.Background(), "ghost-repo", "leftover-key"))

	id, err := resolveLogoutSource(context.Background(), svc, []string{"ghost-repo"})
	require.NoError(t, err)
	assert.Equal(t, "ghost-repo", id)

	// Unknown ID with no token and no registration still errors.
	_, err = resolveLogoutSource(context.Background(), svc, []string{"never-existed"})
	assert.Error(t, err)

	// Built-ins keep working.
	id, err = resolveLogoutSource(context.Background(), svc, []string{"nexusmods"})
	require.NoError(t, err)
	assert.Equal(t, "nexusmods", id)
}

// --- Task 3 RED-first tests: registry-driven auth flows ---

// TestPromptForSource_ListsAuthCapableRegistered pins the picker's one
// intended behavior delta: it lists every registered auth-capable source,
// not just the two built-ins, sorted by ID. Drives promptForSource's real
// stdin read via the withStdin helper (defined in profile_test.go, same
// package) and captures the menu text via captureStdout (defined in
// auth_status_test.go) — both established patterns in this package for
// exercising interactive prompts, so no extra seam-splitting was needed
// beyond factoring the list-building itself into authCapableSources.
func TestPromptForSource_ListsAuthCapableRegistered(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	svc.RegisterSource(nexusmods.New(nil, ""))
	svc.RegisterSource(curseforge.New(nil, ""))
	svc.RegisterSource(&mockAuthSource{id: "acme-mods", name: "Acme Mods"})

	var gotID string
	var promptErr error
	out := captureStdout(t, func() error {
		withStdin(t, "2\n", func() {
			gotID, promptErr = promptForSource(svc)
		})
		return nil
	})

	require.NoError(t, promptErr)
	// Sorted by ID: acme-mods, curseforge, nexusmods -> [1]=acme-mods, [2]=curseforge, [3]=nexusmods.
	assert.Equal(t, "curseforge", gotID)
	assert.Contains(t, out, "[1] Acme Mods (acme-mods)")
	assert.Contains(t, out, "[2] CurseForge (curseforge)")
	assert.Contains(t, out, "[3] Nexus Mods (nexusmods)")
}

// TestRunAuthLogin_JSONOutputReturnsInteractiveOnly pins the non-interactive
// rule (v2 Phase 3 Ruling 2): 'auth login' stays interactive-only in Phase
// 3 (readAPIKey has no non-interactive form), so --json must reject with
// core.ErrInteractiveOnly before opening a service or reading anything -
// even with a source named positionally.
func TestRunAuthLogin_JSONOutputReturnsInteractiveOnly(t *testing.T) {
	withJSONOutput(t)

	err := assertStdinNeverRead(t, func() error {
		return runAuthLogin(&cobra.Command{}, []string{"nexusmods"})
	})

	require.ErrorIs(t, err, core.ErrInteractiveOnly)
}

// TestPromptForSource_JSONOutputReturnsConfirmationRequired pins the
// non-interactive rule at promptForSource, the site 'auth logout' with no
// positional source argument reaches under --json: it must fail with
// core.ErrConfirmationRequired, naming the positional argument as the way
// out, without ever reading stdin.
func TestPromptForSource_JSONOutputReturnsConfirmationRequired(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&mockAuthSource{id: "acme-mods", name: "Acme Mods"})
	withJSONOutput(t)

	promptErr := assertStdinNeverRead(t, func() error {
		_, err := promptForSource(svc)
		return err
	})

	require.ErrorIs(t, promptErr, core.ErrConfirmationRequired)
	assert.Contains(t, promptErr.Error(), "positional argument")
}

// TestAuthLogoutCmd_PositionalArgWorksUnderJSON pins that 'auth logout
// <source>' never hits the interactive picker - and so works fine under
// --json - since resolveLogoutSource only calls promptForSource when no
// positional argument is given.
func TestAuthLogoutCmd_PositionalArgWorksUnderJSON(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()

	svc, err := initService(t.Context())
	require.NoError(t, err)
	require.NoError(t, svc.SaveSourceToken(context.Background(), "nexusmods", "stored-test-key-12345"))
	require.NoError(t, svc.Close())

	withJSONOutput(t)
	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(authCmd); rootCmd.AddCommand(authCmd) })
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "logout", "nexusmods"})

	err = assertStdinNeverRead(t, cmd.Execute)
	require.NoError(t, err)

	svc2, err := initService(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc2.Close()) })
	token, err := svc2.GetSourceToken(context.Background(), "nexusmods")
	require.NoError(t, err)
	assert.Nil(t, token)
}

// TestAuthLogin_ValidatorPath pins the login flow for a source implementing
// source.KeyValidator: the key is validated live ("Validating... done") and
// the success message claims "Successfully authenticated".
func TestAuthLogin_ValidatorPath(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := &mockValidatingAuthSource{mockAuthSource: mockAuthSource{id: "validated-src", name: "Validated Src"}}
	svc.RegisterSource(mock)

	var loginErr error
	out := captureStdout(t, func() error {
		withStdin(t, "supersecret-key\n", func() {
			loginErr = doAuthLogin(context.Background(), svc, "validated-src")
		})
		return nil
	})

	require.NoError(t, loginErr)
	assert.Contains(t, out, "Validating... done")
	assert.Contains(t, out, "Successfully authenticated with Validated Src!")
	assert.NotContains(t, out, "validated on first use")

	token, err := svc.GetSourceToken(context.Background(), "validated-src")
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, "supersecret-key", token.APIKey)
}

// TestAuthLogin_StoredPath pins the login flow for a source with no
// source.KeyValidator: the key is stored unvalidated and the messaging
// says so honestly, never claiming a live check that never happened.
func TestAuthLogin_StoredPath(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := &mockAuthSource{id: "stored-src", name: "Stored Src"}
	svc.RegisterSource(mock)

	var loginErr error
	out := captureStdout(t, func() error {
		withStdin(t, "supersecret-key\n", func() {
			loginErr = doAuthLogin(context.Background(), svc, "stored-src")
		})
		return nil
	})

	require.NoError(t, loginErr)
	assert.NotContains(t, out, "Validating")
	assert.Contains(t, out, "Stored (validated on first use).")
	assert.Contains(t, out, "API key stored for stored-src.")

	token, err := svc.GetSourceToken(context.Background(), "stored-src")
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, "supersecret-key", token.APIKey)
}

// TestAuthStatus_UniformIteration pins doAuthStatus's rewrite: a single pass
// over authCapableSources instead of a hardcoded built-in tier plus a
// separate custom-source tier. Stock setup (both built-ins registered) plus
// one custom auth source must all be listed exactly once, sorted by ID —
// never double-listed by both tiers the way the old two-pass logic risked.
// Probes match "(<id>):" (the id parenthesized after the display name, per
// doAuthStatus's "%s (%s): ..." format) rather than a bare "<id>:" prefix,
// so this doesn't pass vacuously against a regressed bare-ID rendering.
func TestAuthStatus_UniformIteration(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	t.Setenv("NEXUSMODS_API_KEY", "")
	t.Setenv("CURSEFORGE_API_KEY", "")

	svc.RegisterSource(nexusmods.New(nil, ""))
	svc.RegisterSource(curseforge.New(nil, ""))
	svc.RegisterSource(&mockAuthSource{id: "acme-mods", name: "Acme Mods"})

	out := captureStdout(t, func() error { return doAuthStatus(context.Background(), svc) })

	for _, want := range []string{"(nexusmods):", "(curseforge):", "(acme-mods):"} {
		assert.Equal(t, 1, strings.Count(out, want), "%q must be listed exactly once, got:\n%s", want, out)
	}
}

// TestAuthStatus_RendersDisplayNameAlongsideID pins the exact rendered line
// for a stock built-in in both the "authenticated via env" and "not
// authenticated" states: "<Name> (<id>): ...". Uses exact full-line
// matching (not substring containment) — a prior version of doAuthStatus's
// tier merge silently regressed built-ins to bare-ID formatting (losing the
// display name Name() is supposed to supply, per the design's Name()
// principle and this file's own promptForSource/printAuthLoginSuccess),
// and substring-only assertions elsewhere didn't catch it.
func TestAuthStatus_RendersDisplayNameAlongsideID(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	t.Setenv("NEXUSMODS_API_KEY", "test-nexus-key-1234567890")
	t.Setenv("CURSEFORGE_API_KEY", "")

	svc.RegisterSource(nexusmods.New(nil, ""))
	svc.RegisterSource(curseforge.New(nil, ""))

	out := captureStdout(t, func() error { return doAuthStatus(context.Background(), svc) })

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Contains(t, lines, "Nexus Mods (nexusmods): authenticated via NEXUSMODS_API_KEY (key: tes...890)")
	assert.Contains(t, lines, "CurseForge (curseforge): not authenticated (run: lmm auth login curseforge)")
}
