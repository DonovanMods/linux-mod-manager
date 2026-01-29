# P3 Auto-Dependencies Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Automatically resolve and install mod dependencies during `lmm install`.

**Architecture:** Add dependency resolution to the install command after mod selection. Fetch dependencies via existing `ModSource.GetDependencies()`, filter out installed mods, use existing `DependencyResolver` for topological ordering, then batch-install all mods.

**Tech Stack:** Go, Cobra CLI, existing DependencyResolver

---

## Task 1: Add `--no-deps` Flag

**Files:**

- Modify: `cmd/lmm/install.go:19-29` (flag variables)
- Modify: `cmd/lmm/install.go:52-63` (init function)

**Step 1: Add the flag variable**

In `cmd/lmm/install.go`, add to the var block (after line 28):

```go
var (
	installSource       string
	installProfile      string
	installVersion      string
	installModID        string
	installFileID       string
	installYes          bool
	installShowArchived bool
	skipVerify          bool
	installForce        bool
	installNoDeps       bool  // NEW
)
```

**Step 2: Register the flag in init()**

Add after line 61 (after `installForce` flag):

```go
installCmd.Flags().BoolVar(&installNoDeps, "no-deps", false, "skip automatic dependency installation")
```

**Step 3: Run tests to verify no regression**

Run: `go test ./cmd/lmm/... -v -run TestInstallCmd`
Expected: All existing tests pass

**Step 4: Commit**

```bash
git add cmd/lmm/install.go
git commit -m "feat(install): add --no-deps flag for skipping dependency installation"
```

---

## Task 2: Add `installPlan` Type

**Files:**

- Modify: `cmd/lmm/install.go` (add after imports, before var block)

**Step 1: Add the type definition**

Add after the imports block (around line 17):

```go
// installPlan contains the ordered list of mods to install
type installPlan struct {
	mods    []*domain.Mod // In install order (dependencies first, target last)
	missing []string      // Dependencies that couldn't be fetched (warning only)
}
```

**Step 2: Verify compilation**

Run: `go build ./cmd/lmm`
Expected: Compiles without errors

**Step 3: Commit**

```bash
git add cmd/lmm/install.go
git commit -m "feat(install): add installPlan type for dependency resolution"
```

---

## Task 3: Write `resolveDependencies` Function - Tests First

**Files:**

- Create: `cmd/lmm/install_deps_test.go`

**Step 1: Write the test file**

Create `cmd/lmm/install_deps_test.go`:

```go
package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDepSource implements a minimal source for testing dependency resolution
type mockDepSource struct {
	mods map[string]*domain.Mod           // modID -> Mod
	deps map[string][]domain.ModReference // modID -> dependencies
}

func (m *mockDepSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	if mod, ok := m.mods[modID]; ok {
		return mod, nil
	}
	return nil, domain.ErrModNotFound
}

func (m *mockDepSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	if deps, ok := m.deps[mod.ID]; ok {
		return deps, nil
	}
	return nil, nil
}

func TestResolveDependencies_NoDeps(t *testing.T) {
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Target Mod"},
		},
		deps: map[string][]domain.ModReference{},
	}

	target := src.mods["100"]
	installed := make(map[string]bool)

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 1)
	assert.Equal(t, "100", plan.mods[0].ID)
	assert.Empty(t, plan.missing)
}

func TestResolveDependencies_WithDeps(t *testing.T) {
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Target Mod"},
			"200": {ID: "200", SourceID: "nexusmods", Name: "Dependency A"},
			"300": {ID: "300", SourceID: "nexusmods", Name: "Dependency B"},
		},
		deps: map[string][]domain.ModReference{
			"100": {
				{SourceID: "nexusmods", ModID: "200"},
				{SourceID: "nexusmods", ModID: "300"},
			},
		},
	}

	target := src.mods["100"]
	installed := make(map[string]bool)

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 3)
	// Target should be last
	assert.Equal(t, "100", plan.mods[len(plan.mods)-1].ID)
	assert.Empty(t, plan.missing)
}

func TestResolveDependencies_SkipsInstalled(t *testing.T) {
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Target Mod"},
			"200": {ID: "200", SourceID: "nexusmods", Name: "Already Installed"},
		},
		deps: map[string][]domain.ModReference{
			"100": {{SourceID: "nexusmods", ModID: "200"}},
		},
	}

	target := src.mods["100"]
	installed := map[string]bool{"nexusmods:200": true}

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 1) // Only target, dep is skipped
	assert.Equal(t, "100", plan.mods[0].ID)
}

func TestResolveDependencies_MissingDep(t *testing.T) {
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Target Mod"},
			// "200" is missing (external dependency like SKSE)
		},
		deps: map[string][]domain.ModReference{
			"100": {{SourceID: "nexusmods", ModID: "200"}},
		},
	}

	target := src.mods["100"]
	installed := make(map[string]bool)

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 1) // Only target
	assert.Contains(t, plan.missing, "nexusmods:200")
}

func TestResolveDependencies_TransitiveDeps(t *testing.T) {
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Target"},
			"200": {ID: "200", SourceID: "nexusmods", Name: "Direct Dep"},
			"300": {ID: "300", SourceID: "nexusmods", Name: "Transitive Dep"},
		},
		deps: map[string][]domain.ModReference{
			"100": {{SourceID: "nexusmods", ModID: "200"}},
			"200": {{SourceID: "nexusmods", ModID: "300"}},
		},
	}

	target := src.mods["100"]
	installed := make(map[string]bool)

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 3)
	// Order should be: 300 (transitive), 200 (direct), 100 (target)
	assert.Equal(t, "300", plan.mods[0].ID)
	assert.Equal(t, "200", plan.mods[1].ID)
	assert.Equal(t, "100", plan.mods[2].ID)
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./cmd/lmm/... -v -run TestResolveDependencies`
Expected: FAIL - `resolveDependencies` undefined

**Step 3: Commit test file**

```bash
git add cmd/lmm/install_deps_test.go
git commit -m "test(install): add tests for dependency resolution"
```

---

## Task 4: Implement `resolveDependencies` Function

**Files:**

- Modify: `cmd/lmm/install.go` (add function at end of file)

**Step 1: Add the depFetcher interface**

Add after the `installPlan` type:

```go
// depFetcher is the interface needed for dependency resolution
type depFetcher interface {
	GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error)
	GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error)
}
```

**Step 2: Implement resolveDependencies**

Add at the end of `cmd/lmm/install.go`:

```go
// resolveDependencies fetches all dependencies for a mod and returns them in install order.
// Dependencies are fetched recursively. Already-installed mods are skipped.
// Missing dependencies (not found on source) are recorded but don't cause failure.
func resolveDependencies(ctx context.Context, fetcher depFetcher, target *domain.Mod, installedIDs map[string]bool) (*installPlan, error) {
	plan := &installPlan{}
	visited := make(map[string]bool)

	var collect func(mod *domain.Mod) error
	collect = func(mod *domain.Mod) error {
		key := mod.SourceID + ":" + mod.ID
		if visited[key] {
			return nil
		}
		visited[key] = true

		// Fetch dependencies for this mod
		deps, err := fetcher.GetDependencies(ctx, mod)
		if err != nil {
			// Log but continue - dependency info might not be available
			return nil
		}

		// Process each dependency
		for _, ref := range deps {
			depKey := ref.SourceID + ":" + ref.ModID

			// Skip already installed
			if installedIDs[depKey] {
				continue
			}

			// Skip already visited (handles cycles)
			if visited[depKey] {
				continue
			}

			// Fetch the dependency mod
			depMod, err := fetcher.GetMod(ctx, mod.GameID, ref.ModID)
			if err != nil {
				// Dependency not available (external like SKSE)
				plan.missing = append(plan.missing, depKey)
				continue
			}
			depMod.SourceID = ref.SourceID

			// Recursively collect transitive dependencies
			if err := collect(depMod); err != nil {
				return err
			}

			// Add dependency after its dependencies (topological order)
			plan.mods = append(plan.mods, depMod)
		}

		return nil
	}

	// Collect all dependencies
	if err := collect(target); err != nil {
		return nil, err
	}

	// Add target mod last
	plan.mods = append(plan.mods, target)

	return plan, nil
}
```

**Step 3: Run tests to verify they pass**

Run: `go test ./cmd/lmm/... -v -run TestResolveDependencies`
Expected: All tests pass

**Step 4: Commit**

```bash
git add cmd/lmm/install.go
git commit -m "feat(install): implement resolveDependencies for automatic dependency resolution"
```

---

## Task 5: Add `showInstallPlan` Helper Function

**Files:**

- Modify: `cmd/lmm/install.go` (add after resolveDependencies)

**Step 1: Implement showInstallPlan**

Add after `resolveDependencies`:

```go
// showInstallPlan displays the install plan to the user
func showInstallPlan(plan *installPlan, targetModID string) {
	fmt.Printf("\nInstall plan (%d mod(s)):\n", len(plan.mods))
	for i, mod := range plan.mods {
		label := "[dependency]"
		if mod.ID == targetModID {
			label = "[target]"
		}
		fmt.Printf("  %d. %s v%s (ID: %s) %s\n", i+1, mod.Name, mod.Version, mod.ID, label)
	}

	if len(plan.missing) > 0 {
		fmt.Printf("\n⚠ Warning: %d dependency(ies) not available on source:\n", len(plan.missing))
		for _, m := range plan.missing {
			fmt.Printf("  - %s (may require manual install)\n", m)
		}
	}
}
```

**Step 2: Verify compilation**

Run: `go build ./cmd/lmm`
Expected: Compiles without errors

**Step 3: Commit**

```bash
git add cmd/lmm/install.go
git commit -m "feat(install): add showInstallPlan helper for displaying dependency plan"
```

---

## Task 6: Create Service Wrapper for depFetcher

**Files:**

- Modify: `cmd/lmm/install.go` (add wrapper type)

**Step 1: Add serviceDepFetcher wrapper**

Add after `depFetcher` interface:

```go
// serviceDepFetcher wraps core.Service to implement depFetcher
type serviceDepFetcher struct {
	svc      *core.Service
	sourceID string
}

func (s *serviceDepFetcher) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	return s.svc.GetMod(ctx, s.sourceID, gameID, modID)
}

func (s *serviceDepFetcher) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return s.svc.GetDependencies(ctx, s.sourceID, mod)
}
```

**Step 2: Add GetDependencies to Service (if not present)**

Check if `service.GetDependencies` exists. If not, add to `internal/core/service.go`:

```go
// GetDependencies returns dependencies for a mod from the specified source
func (s *Service) GetDependencies(ctx context.Context, sourceID string, mod *domain.Mod) ([]domain.ModReference, error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil, err
	}
	return src.GetDependencies(ctx, mod)
}
```

**Step 3: Verify compilation**

Run: `go build ./cmd/lmm && go build ./...`
Expected: Compiles without errors

**Step 4: Commit**

```bash
git add cmd/lmm/install.go internal/core/service.go
git commit -m "feat(install): add serviceDepFetcher wrapper for dependency resolution"
```

---

## Task 7: Integrate Dependency Resolution into runInstall

**Files:**

- Modify: `cmd/lmm/install.go:168-170` (after mod selection, before file selection)

**Step 1: Add dependency resolution after mod selection**

Find the line `fmt.Printf("\nSelected: %s v%s by %s\n", mod.Name, mod.Version, mod.Author)` (around line 170).

Replace the section from there through to file selection with:

```go
	fmt.Printf("\nSelected: %s v%s by %s\n", mod.Name, mod.Version, mod.Author)

	// Determine profile name early
	profileName := profileOrDefault(installProfile)

	// Resolve dependencies (unless --no-deps or local mod)
	var modsToInstall []*domain.Mod
	if !installNoDeps && mod.SourceID != domain.SourceLocal {
		fmt.Println("\nResolving dependencies...")

		// Get already-installed mods
		installedMods, _ := service.GetInstalledMods(gameID, profileName)
		installedIDs := make(map[string]bool)
		for _, im := range installedMods {
			installedIDs[im.SourceID+":"+im.ID] = true
		}

		// Resolve dependencies
		fetcher := &serviceDepFetcher{svc: service, sourceID: installSource}
		plan, err := resolveDependencies(ctx, fetcher, mod, installedIDs)
		if err != nil {
			return fmt.Errorf("resolving dependencies: %w", err)
		}

		// If there are dependencies to install, show plan and confirm
		if len(plan.mods) > 1 || len(plan.missing) > 0 {
			showInstallPlan(plan, mod.ID)

			if !installYes {
				fmt.Printf("\nInstall %d mod(s)? [Y/n]: ", len(plan.mods))
				reader := bufio.NewReader(os.Stdin)
				input, _ := reader.ReadString('\n')
				input = strings.TrimSpace(strings.ToLower(input))
				if input == "n" || input == "no" {
					return fmt.Errorf("installation cancelled")
				}
			}
		}

		modsToInstall = plan.mods
	} else {
		modsToInstall = []*domain.Mod{mod}
	}

	// If multiple mods to install (target + deps), use batch install
	if len(modsToInstall) > 1 {
		return installModsWithDeps(ctx, service, game, modsToInstall, profileName)
	}

	// Single mod install - continue with existing flow
	// (rest of runInstall continues here for single mod case)
```

**Step 2: Verify compilation**

Run: `go build ./cmd/lmm`
Expected: Compiles (will have errors about installModsWithDeps - that's Task 8)

**Step 3: Commit partial progress**

```bash
git add cmd/lmm/install.go
git commit -m "feat(install): integrate dependency resolution into install flow (WIP)"
```

---

## Task 8: Implement `installModsWithDeps` Function

**Files:**

- Modify: `cmd/lmm/install.go` (add new function)

**Step 1: Implement installModsWithDeps**

Add after `showInstallPlan`:

```go
// installModsWithDeps installs multiple mods in order (dependencies first)
func installModsWithDeps(ctx context.Context, service *core.Service, game *domain.Game, mods []*domain.Mod, profileName string) error {
	fmt.Printf("\nInstalling %d mod(s)...\n", len(mods))

	// Get profile manager and ensure profile exists
	pm := getProfileManager(service)
	if _, err := pm.Get(game.ID, profileName); err != nil {
		if err == domain.ErrProfileNotFound {
			if _, err := pm.Create(game.ID, profileName); err != nil {
				return fmt.Errorf("could not create profile: %w", err)
			}
		}
	}

	// Get link method for this game
	linkMethod := service.GetGameLinkMethod(game)

	var installed []string
	var failed []string

	for i, mod := range mods {
		fmt.Printf("\n[%d/%d] Installing: %s v%s\n", i+1, len(mods), mod.Name, mod.Version)

		// Set up installer
		linker := service.GetLinker(linkMethod)
		installer := core.NewInstaller(service.GetGameCache(game), linker, service.DB())

		// Check if mod is already installed - if so, uninstall old files first
		existingMod, err := service.GetInstalledMod(mod.SourceID, mod.ID, game.ID, profileName)
		if err == nil && existingMod != nil {
			fmt.Printf("  Removing previous installation...\n")
			if err := installer.Uninstall(ctx, game, &existingMod.Mod, profileName); err != nil {
				if verbose {
					fmt.Printf("  Warning: could not remove old files: %v\n", err)
				}
			}
			if err := service.GetGameCache(game).Delete(game.ID, existingMod.SourceID, existingMod.ID, existingMod.Version); err != nil {
				if verbose {
					fmt.Printf("  Warning: could not clear old cache: %v\n", err)
				}
			}
		}

		// Get available files
		files, err := service.GetModFiles(ctx, mod.SourceID, mod)
		if err != nil {
			fmt.Printf("  Error: failed to get mod files: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}

		// Filter and sort files
		files = filterAndSortFiles(files, installShowArchived)

		if len(files) == 0 {
			fmt.Printf("  Error: no downloadable files available\n")
			failed = append(failed, mod.Name)
			continue
		}

		// Auto-select primary or first file
		var selectedFile *domain.DownloadableFile
		for j := range files {
			if files[j].IsPrimary {
				selectedFile = &files[j]
				break
			}
		}
		if selectedFile == nil {
			selectedFile = &files[0]
		}

		fmt.Printf("  File: %s\n", selectedFile.FileName)

		// Download the mod
		progressFn := func(p core.DownloadProgress) {
			if p.TotalBytes > 0 {
				bar := progressBar(p.Percentage, 20)
				fmt.Printf("\r  [%s] %.1f%%", bar, p.Percentage)
			}
		}

		downloadResult, err := service.DownloadMod(ctx, mod.SourceID, game, mod, selectedFile, progressFn)
		if err != nil {
			fmt.Println()
			fmt.Printf("  Error: download failed: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}
		fmt.Println()

		// Display checksum unless --skip-verify
		if !skipVerify && downloadResult.Checksum != "" {
			displayChecksum := downloadResult.Checksum
			if len(displayChecksum) > 12 {
				displayChecksum = displayChecksum[:12] + "..."
			}
			fmt.Printf("  Checksum: %s\n", displayChecksum)
		}

		// Check for conflicts (unless --force)
		if !installForce {
			conflicts, err := installer.GetConflicts(ctx, game, mod, profileName)
			if err == nil && len(conflicts) > 0 {
				fmt.Printf("  ⚠ %d file conflict(s) - will overwrite\n", len(conflicts))
			}
		}

		if err := installer.Install(ctx, game, mod, profileName); err != nil {
			fmt.Printf("  Error: deployment failed: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}

		// Save to database
		installedMod := &domain.InstalledMod{
			Mod:          *mod,
			ProfileName:  profileName,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			Deployed:     true,
			LinkMethod:   linkMethod,
			FileIDs:      []string{selectedFile.ID},
		}

		if err := service.DB().SaveInstalledMod(installedMod); err != nil {
			fmt.Printf("  Error: failed to save mod: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}

		// Store checksum in database
		if !skipVerify && downloadResult.Checksum != "" {
			if err := service.DB().SaveFileChecksum(
				mod.SourceID, mod.ID, game.ID, profileName, selectedFile.ID, downloadResult.Checksum,
			); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: failed to save checksum: %v\n", err)
			}
		}

		// Add to profile
		modRef := domain.ModReference{
			SourceID: mod.SourceID,
			ModID:    mod.ID,
			Version:  mod.Version,
			FileIDs:  []string{selectedFile.ID},
		}
		if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
			if verbose {
				fmt.Printf("  Warning: could not update profile: %v\n", err)
			}
		}

		fmt.Printf("  ✓ Installed (%d files)\n", downloadResult.FilesExtracted)
		installed = append(installed, mod.Name)
	}

	// Summary
	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Installed: %d\n", len(installed))
	if len(failed) > 0 {
		fmt.Printf("Failed: %d (%s)\n", len(failed), strings.Join(failed, ", "))
	}

	return nil
}
```

**Step 2: Run full test suite**

Run: `go test ./... -v`
Expected: All tests pass

**Step 3: Commit**

```bash
git add cmd/lmm/install.go
git commit -m "feat(install): implement installModsWithDeps for batch dependency installation"
```

---

## Task 9: Add Integration Test for Dependency Resolution

**Files:**

- Modify: `cmd/lmm/install_deps_test.go` (add integration test)

**Step 1: Add integration test**

Add to `cmd/lmm/install_deps_test.go`:

```go
func TestResolveDependencies_CyclicDeps(t *testing.T) {
	// Mod A depends on B, B depends on A
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Mod A", GameID: "skyrim"},
			"200": {ID: "200", SourceID: "nexusmods", Name: "Mod B", GameID: "skyrim"},
		},
		deps: map[string][]domain.ModReference{
			"100": {{SourceID: "nexusmods", ModID: "200"}},
			"200": {{SourceID: "nexusmods", ModID: "100"}},
		},
	}

	target := src.mods["100"]
	installed := make(map[string]bool)

	// Should not infinite loop - visited map prevents it
	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	// Both mods should be in plan (cycle is handled by visited check)
	assert.Len(t, plan.mods, 2)
}

func TestResolveDependencies_DeepTransitive(t *testing.T) {
	// A -> B -> C -> D (4 levels deep)
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"A": {ID: "A", SourceID: "nexusmods", Name: "Mod A", GameID: "skyrim"},
			"B": {ID: "B", SourceID: "nexusmods", Name: "Mod B", GameID: "skyrim"},
			"C": {ID: "C", SourceID: "nexusmods", Name: "Mod C", GameID: "skyrim"},
			"D": {ID: "D", SourceID: "nexusmods", Name: "Mod D", GameID: "skyrim"},
		},
		deps: map[string][]domain.ModReference{
			"A": {{SourceID: "nexusmods", ModID: "B"}},
			"B": {{SourceID: "nexusmods", ModID: "C"}},
			"C": {{SourceID: "nexusmods", ModID: "D"}},
		},
	}

	target := src.mods["A"]
	installed := make(map[string]bool)

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 4)
	// Order should be D, C, B, A (deepest first)
	assert.Equal(t, "D", plan.mods[0].ID)
	assert.Equal(t, "C", plan.mods[1].ID)
	assert.Equal(t, "B", plan.mods[2].ID)
	assert.Equal(t, "A", plan.mods[3].ID)
}
```

**Step 2: Run tests**

Run: `go test ./cmd/lmm/... -v -run TestResolveDependencies`
Expected: All tests pass

**Step 3: Commit**

```bash
git add cmd/lmm/install_deps_test.go
git commit -m "test(install): add tests for cyclic and deep transitive dependencies"
```

---

## Task 10: Update Command Help Text

**Files:**

- Modify: `cmd/lmm/install.go:34-47` (Long description)

**Step 1: Update help text**

Update the `Long` field of `installCmd`:

```go
	Long: `Install a mod from the configured source.

The mod will be searched for by name and added to the specified profile
(or default profile if not specified).

Dependencies are automatically resolved and installed. Use --no-deps to skip.

When selecting files, you can choose multiple files (e.g., main + optional patches)
using comma-separated values or ranges: 1,3,5 or 1-3 or 1,3-5

Examples:
  lmm install "ore stack" --game starrupture
  lmm install "skyui" --game skyrim-se --profile survival
  lmm install --id 12345 --game skyrim-se
  lmm install "mod name" -g skyrim-se -y       # Auto-select and auto-confirm
  lmm install "mod name" -g skyrim-se --no-deps  # Skip dependencies`,
```

**Step 2: Verify help output**

Run: `go build ./cmd/lmm && ./lmm install --help`
Expected: Shows updated help with dependency info

**Step 3: Commit**

```bash
git add cmd/lmm/install.go
git commit -m "docs(install): update help text to mention automatic dependency resolution"
```

---

## Task 11: Run Full Test Suite and Lint

**Files:** None (verification only)

**Step 1: Run all tests**

Run: `go test ./... -v`
Expected: All tests pass

**Step 2: Run linter**

Run: `trunk check`
Expected: No errors (warnings OK)

**Step 3: Fix any issues**

If lint errors, fix them and re-run.

**Step 4: Commit any fixes**

```bash
git add -A
git commit -m "fix: address lint issues"
```

---

## Task 12: Version Bump and Changelog

**Files:**

- Modify: `cmd/lmm/root.go:16` (version)
- Modify: `CHANGELOG.md`

**Step 1: Bump version to 0.11.0**

In `cmd/lmm/root.go`, change:

```go
version = "0.11.0"
```

**Step 2: Update CHANGELOG.md**

Add new section at top (after `# Changelog`):

```markdown
## [0.11.0] - 2026-01-28

### Added

- **Automatic dependency installation**: `lmm install` now resolves and installs mod dependencies automatically
  - Shows install plan with dependencies in topological order
  - Warns about dependencies not available on source (external deps like SKSE)
  - `--no-deps` flag to skip dependency installation
  - `-y` flag auto-confirms dependency installation
```

Update comparison links at bottom.

**Step 3: Commit version bump**

```bash
git add cmd/lmm/root.go CHANGELOG.md
git commit -m "chore: bump version to 0.11.0"
```

---

## Task 13: Final Verification

**Step 1: Build and test**

Run: `go build ./cmd/lmm && go test ./... -v`
Expected: All pass

**Step 2: Manual smoke test (if game configured)**

```bash
./lmm install "skyui" --game skyrim-se --no-deps  # Should work without deps
./lmm install "skyui" --game skyrim-se -y         # Should resolve deps
```

**Step 3: Verify git status**

Run: `git status`
Expected: Clean working tree

**Step 4: Done!**

All tasks complete. Ready for code review.
