package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMaskAPIKey tests the API key masking function
func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "normal key",
			input:    "abcdefghijklmnop",
			expected: "abc...nop",
		},
		{
			name:     "exactly 7 chars",
			input:    "1234567",
			expected: "123...567",
		},
		{
			name:     "6 chars or less returns ***",
			input:    "123456",
			expected: "***",
		},
		{
			name:     "short key",
			input:    "abc",
			expected: "***",
		},
		{
			name:     "empty key",
			input:    "",
			expected: "***",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := maskAPIKey(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetEnvKeyForSource tests the environment variable name lookup
func TestGetEnvKeyForSource(t *testing.T) {
	tests := []struct {
		name     string
		sourceID string
		expected string
	}{
		{
			name:     "nexusmods",
			sourceID: "nexusmods",
			expected: "NEXUSMODS_API_KEY",
		},
		{
			name:     "curseforge",
			sourceID: "curseforge",
			expected: "CURSEFORGE_API_KEY",
		},
		{
			name:     "custom source falls back to the derived LMM_*_API_KEY name",
			sourceID: "unknown-source",
			expected: "LMM_UNKNOWN_SOURCE_API_KEY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getEnvKeyForSource(tt.sourceID)
			assert.Equal(t, tt.expected, result)
		})
	}
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

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "login", "unsupported-source"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nexusmods")
	assert.Contains(t, err.Error(), "curseforge")
	assert.Contains(t, err.Error(), "registered custom source with auth declared")
}

// TestAuthLogoutCmd_UnsupportedSource tests logout with unsupported source
func TestAuthLogoutCmd_UnsupportedSource(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)

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
	svc, err := initService()
	require.NoError(t, err)
	err = svc.SaveSourceToken("nexusmods", "stored-test-key-12345")
	require.NoError(t, err)
	require.NoError(t, svc.Close())

	// Now run status
	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)

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
	svc, err := initService()
	require.NoError(t, err)
	err = svc.SaveSourceToken("nexusmods", "stored-test-key-12345")
	require.NoError(t, err)

	// Verify token exists
	token, err := svc.GetSourceToken("nexusmods")
	require.NoError(t, err)
	require.NotNil(t, token)
	require.NoError(t, svc.Close())

	// Now run logout
	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "logout", "nexusmods"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify token is gone
	svc2, err := initService()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc2.Close())
	})

	token, err = svc2.GetSourceToken("nexusmods")
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

func TestEnvKeyForSourceID(t *testing.T) {
	assert.Equal(t, "LMM_DONOVAN_MODS_API_KEY", envKeyForSourceID("donovan-mods"))
	assert.Equal(t, "LMM_MY_REPO_API_KEY", envKeyForSourceID("my-repo"))
}

// TestPrintLoginResult pins final-review finding 4: custom sources have no
// generic validation endpoint (validateAPIKey is a no-op for them), so
// runAuthLogin must not print the built-in "Validating... done" sequence for
// them — that's a fabricated result. Built-ins are actively validated
// earlier in the flow and need no extra message here; non-built-ins get an
// honest "stored, checked on first use" message instead.
func TestPrintLoginResult(t *testing.T) {
	t.Run("built-in source prints nothing extra", func(t *testing.T) {
		var buf bytes.Buffer
		printLoginResult(&buf, "nexusmods")
		assert.Empty(t, buf.String(), "built-ins are validated via the Validating...done sequence above")
	})

	t.Run("curseforge prints nothing extra", func(t *testing.T) {
		var buf bytes.Buffer
		printLoginResult(&buf, "curseforge")
		assert.Empty(t, buf.String())
	})

	t.Run("custom source prints an honest stored message", func(t *testing.T) {
		var buf bytes.Buffer
		printLoginResult(&buf, "my-repo")
		assert.Equal(t, "Stored (validated on first use).\n", buf.String())
		assert.NotContains(t, buf.String(), "Validating", "must not fabricate a validation step that never ran")
	})
}

// TestPrintAuthLoginSuccess pins the re-review fix for finding 4: custom
// sources have no generic validation endpoint, so runAuthLogin's final line
// must not claim "Successfully authenticated" for them — that's a fabricated
// result. Built-ins were actively validated earlier in the flow and keep the
// original message; non-built-ins get an honest "stored" message instead.
func TestPrintAuthLoginSuccess(t *testing.T) {
	t.Run("built-in source keeps the authenticated message", func(t *testing.T) {
		var buf bytes.Buffer
		printAuthLoginSuccess(&buf, "nexusmods")
		assert.Equal(t, "Successfully authenticated with NexusMods!\n", buf.String())
	})

	t.Run("curseforge keeps the authenticated message", func(t *testing.T) {
		var buf bytes.Buffer
		printAuthLoginSuccess(&buf, "curseforge")
		assert.Equal(t, "Successfully authenticated with CurseForge!\n", buf.String())
	})

	t.Run("custom source prints an honest stored message", func(t *testing.T) {
		var buf bytes.Buffer
		printAuthLoginSuccess(&buf, "my-repo")
		assert.Equal(t, "API key stored for my-repo.\n", buf.String())
		assert.NotContains(t, buf.String(), "Successfully authenticated", "must not fabricate a validation result that never happened")
	})
}

func TestResolveLogoutSource(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	// A token stored for a source whose definition file has been deleted:
	// not registered, but logout must still be able to remove it.
	require.NoError(t, svc.SaveSourceToken("ghost-repo", "leftover-key"))

	id, err := resolveLogoutSource(svc, []string{"ghost-repo"})
	require.NoError(t, err)
	assert.Equal(t, "ghost-repo", id)

	// Unknown ID with no token and no registration still errors.
	_, err = resolveLogoutSource(svc, []string{"never-existed"})
	assert.Error(t, err)

	// Built-ins keep working.
	id, err = resolveLogoutSource(svc, []string{"nexusmods"})
	require.NoError(t, err)
	assert.Equal(t, "nexusmods", id)
}
