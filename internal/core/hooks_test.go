package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHookRunner_Success(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "success.sh")
	script := `#!/bin/bash
echo "stdout message"
echo "stderr message" >&2
exit 0
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))

	runner := NewHookRunner(60 * time.Second)
	ctx := context.Background()
	hc := HookContext{
		GameID:   "skyrim-se",
		GamePath: "/path/to/game",
		ModPath:  "/path/to/mods",
		HookName: "install.before_all",
	}

	result, err := runner.Run(ctx, scriptPath, hc)
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "stdout message")
	assert.Contains(t, result.Stderr, "stderr message")
	assert.Equal(t, 0, result.ExitCode)
}

func TestHookRunner_NonZeroExit(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "fail.sh")
	script := `#!/bin/bash
echo "error occurred" >&2
exit 42
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))

	runner := NewHookRunner(60 * time.Second)
	ctx := context.Background()
	hc := HookContext{GameID: "test", HookName: "test.hook"}

	result, err := runner.Run(ctx, scriptPath, hc)
	require.Error(t, err)
	assert.Equal(t, 42, result.ExitCode)
	assert.Contains(t, result.Stderr, "error occurred")
}

func TestHookRunner_Timeout(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "slow.sh")
	script := `#!/bin/bash
sleep 10
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))

	runner := NewHookRunner(100 * time.Millisecond)
	ctx := context.Background()
	hc := HookContext{GameID: "test", HookName: "test.hook"}

	_, err := runner.Run(ctx, scriptPath, hc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestHookRunner_NotFound(t *testing.T) {
	runner := NewHookRunner(60 * time.Second)
	ctx := context.Background()
	hc := HookContext{GameID: "test", HookName: "test.hook"}

	_, err := runner.Run(ctx, "/nonexistent/script.sh", hc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestHookRunner_NotExecutable(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "noexec.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/bash\necho hi"), 0644)) // no exec bit

	runner := NewHookRunner(60 * time.Second)
	ctx := context.Background()
	hc := HookContext{GameID: "test", HookName: "test.hook"}

	_, err := runner.Run(ctx, scriptPath, hc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not executable")
}

func TestHookRunner_EnvVars(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "env.sh")
	script := `#!/bin/bash
echo "GAME_ID=$LMM_GAME_ID"
echo "GAME_PATH=$LMM_GAME_PATH"
echo "MOD_PATH=$LMM_MOD_PATH"
echo "MOD_ID=$LMM_MOD_ID"
echo "MOD_NAME=$LMM_MOD_NAME"
echo "MOD_VERSION=$LMM_MOD_VERSION"
echo "HOOK=$LMM_HOOK"
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))

	runner := NewHookRunner(60 * time.Second)
	ctx := context.Background()
	hc := HookContext{
		GameID:     "skyrim-se",
		GamePath:   "/path/to/game",
		ModPath:    "/path/to/mods",
		ModID:      "12345",
		ModName:    "SkyUI",
		ModVersion: "5.2",
		HookName:   "install.after_each",
	}

	result, err := runner.Run(ctx, scriptPath, hc)
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "GAME_ID=skyrim-se")
	assert.Contains(t, result.Stdout, "MOD_ID=12345")
	assert.Contains(t, result.Stdout, "MOD_NAME=SkyUI")
	assert.Contains(t, result.Stdout, "HOOK=install.after_each")
}
