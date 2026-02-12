package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// HookContext provides environment information for hook scripts
type HookContext struct {
	GameID     string
	GamePath   string
	ModPath    string
	ModID      string // Empty for *_all hooks
	ModName    string // Empty for *_all hooks
	ModVersion string // Empty for *_all hooks
	HookName   string // e.g., "install.before_all"
}

// HookResult contains the output from running a hook
type HookResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// HookRunner executes hook scripts with timeout and environment
type HookRunner struct {
	timeout time.Duration
}

// NewHookRunner creates a new hook runner with the given timeout
func NewHookRunner(timeout time.Duration) *HookRunner {
	return &HookRunner{timeout: timeout}
}

// Run executes a hook script and returns its output
func (r *HookRunner) Run(ctx context.Context, scriptPath string, hc HookContext) (*HookResult, error) {
	result := &HookResult{}

	// Check script exists
	info, err := os.Stat(scriptPath)
	if os.IsNotExist(err) {
		return result, fmt.Errorf("hook script not found: %s", scriptPath)
	}
	if err != nil {
		return result, fmt.Errorf("checking hook script: %w", err)
	}

	// Check script is executable
	if info.Mode()&0111 == 0 {
		return result, fmt.Errorf("hook script not executable: %s", scriptPath)
	}

	// Create timeout context
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// Build command
	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.WaitDelay = 100 * time.Millisecond // Allow graceful shutdown after context cancel
	cmd.Env = append(os.Environ(),
		"LMM_GAME_ID="+hc.GameID,
		"LMM_GAME_PATH="+hc.GamePath,
		"LMM_MOD_PATH="+hc.ModPath,
		"LMM_MOD_ID="+hc.ModID,
		"LMM_MOD_NAME="+hc.ModName,
		"LMM_MOD_VERSION="+hc.ModVersion,
		"LMM_HOOK="+hc.HookName,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run command
	err = cmd.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()

	// Handle exit code
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return result, fmt.Errorf("hook timed out after %v: %s", r.timeout, scriptPath)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, fmt.Errorf("hook failed with exit code %d: %s", result.ExitCode, scriptPath)
		}
		return result, fmt.Errorf("running hook: %w", err)
	}

	return result, nil
}

// ResolvedHooks contains the final merged hooks for an operation
type ResolvedHooks struct {
	Install   domain.HookConfig
	Uninstall domain.HookConfig
}

// GetInstallBeforeAll returns the install.before_all command, or "" if not set.
// Nil-safe: returns "" when the receiver is nil.
func (h *ResolvedHooks) GetInstallBeforeAll() string {
	if h == nil {
		return ""
	}
	return h.Install.BeforeAll
}

// GetInstallBeforeEach returns the install.before_each command, or "" if not set.
func (h *ResolvedHooks) GetInstallBeforeEach() string {
	if h == nil {
		return ""
	}
	return h.Install.BeforeEach
}

// GetInstallAfterEach returns the install.after_each command, or "" if not set.
func (h *ResolvedHooks) GetInstallAfterEach() string {
	if h == nil {
		return ""
	}
	return h.Install.AfterEach
}

// GetInstallAfterAll returns the install.after_all command, or "" if not set.
func (h *ResolvedHooks) GetInstallAfterAll() string {
	if h == nil {
		return ""
	}
	return h.Install.AfterAll
}

// GetUninstallBeforeAll returns the uninstall.before_all command, or "" if not set.
func (h *ResolvedHooks) GetUninstallBeforeAll() string {
	if h == nil {
		return ""
	}
	return h.Uninstall.BeforeAll
}

// GetUninstallBeforeEach returns the uninstall.before_each command, or "" if not set.
func (h *ResolvedHooks) GetUninstallBeforeEach() string {
	if h == nil {
		return ""
	}
	return h.Uninstall.BeforeEach
}

// GetUninstallAfterEach returns the uninstall.after_each command, or "" if not set.
func (h *ResolvedHooks) GetUninstallAfterEach() string {
	if h == nil {
		return ""
	}
	return h.Uninstall.AfterEach
}

// GetUninstallAfterAll returns the uninstall.after_all command, or "" if not set.
func (h *ResolvedHooks) GetUninstallAfterAll() string {
	if h == nil {
		return ""
	}
	return h.Uninstall.AfterAll
}

// ResolveHooks merges game-level hooks with profile-level overrides
func ResolveHooks(game *domain.Game, profile *domain.Profile) *ResolvedHooks {
	if game == nil {
		return nil
	}

	resolved := &ResolvedHooks{
		Install:   game.Hooks.Install,
		Uninstall: game.Hooks.Uninstall,
	}

	if profile == nil {
		return resolved
	}

	// Apply profile overrides (only if explicitly set)
	if profile.HooksExplicit.Install.BeforeAll {
		resolved.Install.BeforeAll = profile.Hooks.Install.BeforeAll
	}
	if profile.HooksExplicit.Install.BeforeEach {
		resolved.Install.BeforeEach = profile.Hooks.Install.BeforeEach
	}
	if profile.HooksExplicit.Install.AfterEach {
		resolved.Install.AfterEach = profile.Hooks.Install.AfterEach
	}
	if profile.HooksExplicit.Install.AfterAll {
		resolved.Install.AfterAll = profile.Hooks.Install.AfterAll
	}

	if profile.HooksExplicit.Uninstall.BeforeAll {
		resolved.Uninstall.BeforeAll = profile.Hooks.Uninstall.BeforeAll
	}
	if profile.HooksExplicit.Uninstall.BeforeEach {
		resolved.Uninstall.BeforeEach = profile.Hooks.Uninstall.BeforeEach
	}
	if profile.HooksExplicit.Uninstall.AfterEach {
		resolved.Uninstall.AfterEach = profile.Hooks.Uninstall.AfterEach
	}
	if profile.HooksExplicit.Uninstall.AfterAll {
		resolved.Uninstall.AfterAll = profile.Hooks.Uninstall.AfterAll
	}

	return resolved
}
