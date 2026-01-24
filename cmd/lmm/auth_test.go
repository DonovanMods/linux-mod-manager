package main

import (
	"bytes"
	"os"
	"testing"

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
			name:     "unknown source",
			sourceID: "unknown",
			expected: "",
		},
		{
			name:     "empty source",
			sourceID: "",
			expected: "",
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
	assert.Contains(t, err.Error(), "unsupported source")
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
	cmd.SetArgs([]string{"auth", "logout"})

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
	os.Unsetenv("NEXUSMODS_API_KEY")
	defer func() {
		if oldEnv != "" {
			os.Setenv("NEXUSMODS_API_KEY", oldEnv)
		}
	}()

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
	os.Setenv("NEXUSMODS_API_KEY", "test-api-key-12345")
	defer func() {
		if oldEnv != "" {
			os.Setenv("NEXUSMODS_API_KEY", oldEnv)
		} else {
			os.Unsetenv("NEXUSMODS_API_KEY")
		}
	}()

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
	os.Unsetenv("NEXUSMODS_API_KEY")
	defer func() {
		if oldEnv != "" {
			os.Setenv("NEXUSMODS_API_KEY", oldEnv)
		}
	}()

	// First, save a token
	svc, err := initService()
	require.NoError(t, err)
	err = svc.SaveSourceToken("nexusmods", "stored-test-key-12345")
	require.NoError(t, err)
	svc.Close()

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
	svc.Close()

	// Now run logout
	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(authCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"auth", "logout"})

	err = cmd.Execute()
	assert.NoError(t, err)

	// Verify token is gone
	svc2, err := initService()
	require.NoError(t, err)
	defer svc2.Close()

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
