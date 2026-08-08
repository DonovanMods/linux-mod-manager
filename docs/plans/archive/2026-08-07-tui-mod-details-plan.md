# TUI Mod Details View Implementation Plan (#86)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the TUI a full-screen mod details view with parity to `lmm mod show`, opened with `enter` from Installed Mods or Search.

**Architecture:** Extract `mod show`'s three-source detail composition into `core.Service.ModDetail` so both interfaces render one set of facts; generalize #224's context-view host off `ScreenHealth` so the view renders on the screen it was opened from; open local-first and enrich from the network in the background.

**Tech Stack:** Go 1.x, Bubble Tea (Elm architecture), lipgloss, testify. No new dependencies.

**Design spec:** `docs/plans/2026-08-07-tui-mod-details-design.md` — read it first.

## Global Constraints

- **Branch:** `feat/86-mod-details`, forked from `develop`. PR with an explicit `--base develop`.
- **No version bump.** CHANGELOG entries go under `[Unreleased]`. `version` in `cmd/lmm/root.go` stays put.
- **Test-first, always.** Every task starts with a failing test and shows it failing before implementing.
- **Go formatting:** tabs, `gofmt`. Run `go fmt ./...` before each commit.
- **Never regenerate the 24 frozen verify goldens** in `cmd/lmm/testdata/verify_golden`. Nothing in this plan touches verify.
- **Commit after every task.** Conventional-commit subjects ending in `(#86)`.
- **Full suite green before any commit:** `go build ./... && go vet ./... && go test ./...`.
- The CLI and TUI are equally first-class; a behavior added to one is added to both.

---

## File Structure

| File                                | Responsibility                                                                                                                         |
| ----------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/core/moddetail.go`        | **new** — `ModDetail`/`InstalledDetail` types and `Service.ModDetail`, the single composition of source metadata + local install state |
| `cmd/lmm/mod.go`                    | `doModShow` becomes a pure renderer over `Service.ModDetail`                                                                           |
| `cmd/lmm/testdata/mod_show_golden/` | **new** — recorded pre-refactor `mod show` output, the extraction's drift guard                                                        |
| `cmd/lmm/mod_show_golden_test.go`   | **new** — golden capture tests                                                                                                         |
| `internal/tui/contextview.go`       | host: drop the `ScreenHealth` jump and `contextReturn`; own the chrome renderer                                                        |
| `internal/tui/moddetails.go`        | **new** — the `contextContent` implementation: render states, scrolling, keys                                                          |
| `internal/tui/app.go`               | `screenView` early check; ungate key routing + nav-away; swallow rule; `enter` on two screens                                          |
| `internal/tui/service.go`           | `ModDetails`/`InstalledDetails` view models; `GetModDetails` on `DataProvider`; prototype impl                                         |
| `internal/tui/service_core.go`      | `coreProvider.GetModDetails`                                                                                                           |
| `internal/tui/mutations.go`         | `openSelectedModDetails` handler, two gen-tagged messages, two resolvers                                                               |
| `internal/tui/prototype/data.go`    | `Description`/`SourceURL`/`PictureURL` on `prototype.Mod`                                                                              |

---

## Accepted deviation (decided, do not re-litigate)

`doModShow` currently calls `svc.GetMod` **before** `resolveProfile`. Because
`Service.ModDetail` receives an already-resolved profile name, the CLI must
resolve the profile first. When **both** the profile and the mod ID are invalid,
the error surfaced changes from "mod not found" to the profile error. Both are
correct errors; only the ordering differs. Task 2 pins the single-failure cases,
which are the ones users actually hit. This is the one intended behavioral
difference in the extraction.

---

### Task 1: Pin `mod show` output with recorded goldens

Records what `mod show` prints **today**, before anything moves. Tasks 2 and 3
are judged against these files.

**Files:**

- Create: `cmd/lmm/mod_show_golden_test.go`
- Create: `cmd/lmm/testdata/mod_show_golden/` (populated by the `-update` run)

**Interfaces:**

- Consumes: existing fixtures `setupDoModLockTest(t) (*core.Service, *domain.Game, *fakeSource)`, `seedLockableMod(t, svc, game, id, name, version)`, `captureStdout(t, func() error) string` (`cmd/lmm/auth_status_test.go:16`).
- Produces: `testdata/mod_show_golden/*.txt` — frozen. Later tasks must not regenerate them except where this plan says so explicitly.

- [ ] **Step 1: Write the golden test**

```go
package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateModShowGoldens re-records the mod show goldens. Run ONCE, before the
// #86 core extraction:
//
//	go test ./cmd/lmm -run TestModShowGolden -update
//
// After that the files are frozen: they are the proof that extracting
// Service.ModDetail did not change a byte of CLI output. Task 3 is the ONLY
// later task allowed to re-record them, and only the description cases.
var updateModShowGoldens = flag.Bool("update", false, "re-record mod show goldens")

func assertModShowGolden(t *testing.T, name, actual string) {
	t.Helper()
	path := filepath.Join("testdata", "mod_show_golden", name+".txt")
	if *updateModShowGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(actual), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden %s missing - record it with -update BEFORE refactoring", path)
	assert.Equal(t, string(want), actual, "mod show output drifted from the recorded golden")
}

// richMod is the fixture every golden case shows: every optional field
// populated, so the goldens pin the omit-when-empty branches by contrast with
// the sparse cases below.
func richMod(gameID string) *domain.Mod {
	endorsements := int64(84213)
	return &domain.Mod{
		ID: "a", SourceID: "src", GameID: gameID,
		Name: "Mod A", Version: "1.5", Author: "Author A",
		Category:     "Utilities",
		Summary:      "A short summary.",
		Description:  "<p>Line one.</p><br/><p>Line &amp; two.</p>",
		SourceURL:    "https://example.invalid/mods/1",
		PictureURL:   "https://example.invalid/img/1.png",
		Endorsements: &endorsements,
	}
}

func TestModShowGolden_NotInstalled(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	src.AddMod(richMod(game.ID), nil)

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})
	assertModShowGolden(t, "not_installed", out)
}

func TestModShowGolden_InstalledUnlocked(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(richMod(game.ID), nil)

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})
	assertModShowGolden(t, "installed_unlocked", out)
}

func TestModShowGolden_InstalledLockedWithConvergeHint(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(richMod(game.ID), nil)
	require.NoError(t, svc.NewProfileManager().SetModLock(game.ID, "default", "src", "a", "1.2.3"))

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})
	assertModShowGolden(t, "installed_locked", out)
}

// Sparse mod: every optional field empty, pinning the omit branches.
func TestModShowGolden_SparseMod(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"}, nil)

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})
	assertModShowGolden(t, "sparse", out)
}

func TestModShowGolden_JSON(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(richMod(game.ID), nil)

	old := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = old })

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})
	assertModShowGolden(t, "json_installed", out)
}
```

- [ ] **Step 2: Run to verify it fails (goldens absent)**

Run: `go test ./cmd/lmm -run TestModShowGolden -v`
Expected: FAIL, five times, with "golden testdata/mod_show_golden/....txt missing - record it with -update BEFORE refactoring".

- [ ] **Step 3: Record the goldens**

Run: `go test ./cmd/lmm -run TestModShowGolden -update`
Expected: PASS. Then inspect the five files — they must contain real output (a `====` banner, `ID: a  Version: 1.5  Author: Author A`, and for the rich cases raw `<p>` tags in the Description, which is the wart Task 3 fixes).

Guard against recording nothing:

```bash
test -s cmd/lmm/testdata/mod_show_golden/not_installed.txt || { echo "EMPTY GOLDEN - abort"; exit 1; }
```

- [ ] **Step 4: Run without `-update` to verify they now pass**

Run: `go test ./cmd/lmm -run TestModShowGolden -v`
Expected: PASS ×5.

- [ ] **Step 5: Commit**

```bash
go fmt ./...
git add cmd/lmm/mod_show_golden_test.go cmd/lmm/testdata/mod_show_golden
git commit -m "test: pin mod show output with recorded goldens before extraction (#86)"
```

---

### Task 2: Extract `Service.ModDetail`; `doModShow` becomes its renderer

Behavior-preserving. Task 1's goldens must stay green **without being touched**.

**Files:**

- Create: `internal/core/moddetail.go`
- Create: `internal/core/moddetail_test.go`
- Modify: `cmd/lmm/mod.go:606-753` (`doModShow`)

**Interfaces:**

- Consumes: `Service.GetMod` (`internal/core/service.go:422`), `Service.GetInstalledMod`, `Service.ModHasPakMergeSource` (`internal/core/merged_pak.go:92`), `config.LoadProfile` (already imported by core — `internal/core/profile.go:30`).
- Produces:

  ```go
  func (s *Service) ModDetail(ctx context.Context, game *domain.Game,
      profile, sourceID, modID string) (*ModDetail, error)

  type ModDetail struct {
      Mod       *domain.Mod
      Installed *InstalledDetail // nil when not installed in profile
  }

  type InstalledDetail struct {
      Version       string
      Profile       string
      UpdatePolicy  domain.UpdatePolicy
      Locked        bool
      LockedVersion string
      ConvertPaks   *bool // nil = not applicable (not "off")
  }
  ```

- [ ] **Step 1: Write the failing test**

```go
package core

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModDetail_NotInstalled: a mod that exists upstream but is absent from
// the profile yields source metadata with a nil Installed block - "not
// installed" is an ordinary state, never an error (doModShow's own
// convention, cmd/lmm/mod.go:618-621).
func TestModDetail_NotInstalled(t *testing.T) {
	svc, game, src := newModDetailTestService(t)
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})

	detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
	require.NoError(t, err)
	require.NotNil(t, detail.Mod)
	assert.Equal(t, "Mod A", detail.Mod.Name)
	assert.Nil(t, detail.Installed, "an uninstalled mod must not carry an Installed block")
}

// TestModDetail_InstalledCarriesPolicyAndProfile joins the DB row.
func TestModDetail_InstalledCarriesPolicyAndProfile(t *testing.T) {
	svc, game, src := newModDetailTestService(t)
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
	require.NoError(t, err)
	require.NotNil(t, detail.Installed)
	assert.Equal(t, "1.5", detail.Installed.Version)
	assert.Equal(t, "default", detail.Installed.Profile)
	assert.Equal(t, domain.UpdatePolicyNotify, detail.Installed.UpdatePolicy)
	assert.False(t, detail.Installed.Locked)
	assert.Nil(t, detail.Installed.ConvertPaks, "a non-compile game must leave ConvertPaks unset, not false")
}

// TestModDetail_LockJoinedFromProfileYAML: the lock lives in the profile YAML,
// not the DB, and LockedVersion is the lock's TARGET (which may differ from
// the installed version - that difference is what drives mod show's converge
// hint).
func TestModDetail_LockJoinedFromProfileYAML(t *testing.T) {
	svc, game, src := newModDetailTestService(t)
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	require.NoError(t, svc.NewProfileManager().SetModLock(game.ID, "default", "src", "a", "1.2.3"))

	detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
	require.NoError(t, err)
	require.NotNil(t, detail.Installed)
	assert.True(t, detail.Installed.Locked)
	assert.Equal(t, "1.2.3", detail.Installed.LockedVersion)
}

// TestModDetail_UnknownModErrors: a source lookup failure is a real error.
func TestModDetail_UnknownModErrors(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)

	_, err := svc.ModDetail(context.Background(), game, "default", "src", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mod not found")
}
```

Write `newModDetailTestService(t) (*Service, *domain.Game, *mockSource)` and
`seedModDetailInstalled(t, svc, game, modID, version)` in the same file,
following the existing `internal/core` test-service helpers (in-memory SQLite,
`t.TempDir()` for config/data — see `internal/core/service_test.go`). Reuse the
package's existing `mockSource` rather than adding another double.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/core -run TestModDetail -v`
Expected: FAIL — `svc.ModDetail undefined (type *Service has no field or method ModDetail)`.

- [ ] **Step 3: Create `internal/core/moddetail.go`**

```go
package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// ModDetail is everything both interfaces need to render a mod's full detail:
// the source-side metadata plus, when the mod is installed in the named
// profile, its local install state. Composed here rather than in each caller
// because the install state spans three stores - the DB row (version,
// policy), the profile YAML (lock), and the game config (pak conversion
// eligibility) - and the CLI/TUI parity directive forbids each surface
// re-deriving that join for itself (#86).
type ModDetail struct {
	Mod       *domain.Mod
	Installed *InstalledDetail
}

// InstalledDetail is the local install state. Nil on ModDetail when the mod
// is not installed in the profile - an ordinary state, not an error.
type InstalledDetail struct {
	Version       string
	Profile       string
	UpdatePolicy  domain.UpdatePolicy
	Locked        bool
	LockedVersion string
	// ConvertPaks is nil when pak conversion does not apply to this mod at
	// all (not a merge-compile game, or no pak merge source) - distinct from
	// a non-nil pointer to false, which means "applies, and is off".
	ConvertPaks *bool
}

// ModDetail fetches modID from sourceID and joins whatever local install
// state exists for it in profile. The source fetch is a live network call for
// remote sources (Service.GetMod does not cache), so callers on a UI thread
// must run this off the render path.
func (s *Service) ModDetail(ctx context.Context, game *domain.Game, profile, sourceID, modID string) (*ModDetail, error) {
	mod, err := s.GetMod(ctx, sourceID, game.ID, modID)
	if err != nil {
		return nil, fmt.Errorf("mod not found: %w", err)
	}
	detail := &ModDetail{Mod: mod}

	// A GetInstalledMod failure means "not installed" - the ordinary case for
	// a mod browsed from search - so it omits the block rather than failing
	// the whole call (verbatim from doModShow's own convention).
	installed, err := s.GetInstalledMod(sourceID, modID, game.ID, profile)
	if err != nil {
		return detail, nil
	}

	info := &InstalledDetail{
		Version:      installed.Version,
		Profile:      profile,
		UpdatePolicy: installed.UpdatePolicy,
	}
	if game.DeployMode == domain.DeployCompile && s.ModHasPakMergeSource(installed) {
		v := installed.ConvertPaks
		info.ConvertPaks = &v
	}
	// Lock lives in the profile YAML, not the DB. A load failure degrades to
	// "unlocked" rather than failing the call - same as doModShow, which
	// ignores this error.
	if prof, perr := config.LoadProfile(s.ConfigDir(), game.ID, profile); perr == nil {
		if ref := prof.FindRef(sourceID, modID); ref != nil && ref.Locked {
			info.Locked = true
			info.LockedVersion = ref.Version
		}
	}
	detail.Installed = info
	return detail, nil
}
```

- [ ] **Step 4: Run to verify the core tests pass**

Run: `go test ./internal/core -run TestModDetail -v`
Expected: PASS ×4.

- [ ] **Step 5: Rewrite `doModShow`'s data-gathering half**

In `cmd/lmm/mod.go`, replace everything from the `mod, err := svc.GetMod(...)`
call through the end of the `installedInfo` block (currently `:613-646`) with:

```go
	// Profile resolves BEFORE the detail call now that the composition lives
	// in core (#86) - see the plan's "Accepted deviation": when BOTH the
	// profile and the mod ID are invalid, the profile error surfaces first.
	profileName, err := resolveProfile(svc, game.ID, modProfile)
	if err != nil {
		return err
	}

	detail, err := svc.ModDetail(ctx, game, profileName, modSource, modID)
	if err != nil {
		return err
	}
	mod := detail.Mod

	var installedInfo *modShowInstalled
	if detail.Installed != nil {
		installedInfo = &modShowInstalled{
			Version:       detail.Installed.Version,
			Profile:       detail.Installed.Profile,
			UpdatePolicy:  policyToString(detail.Installed.UpdatePolicy),
			Locked:        detail.Installed.Locked,
			LockedVersion: detail.Installed.LockedVersion,
			ConvertPaks:   detail.Installed.ConvertPaks,
		}
	}
```

Everything below (the `jsonOutput` block and the human-readable printing) is
**unchanged** — it already reads only `mod` and `installedInfo`.

- [ ] **Step 6: Verify the goldens are still byte-identical**

Run: `go test ./cmd/lmm -run TestModShowGolden -v`
Expected: PASS ×5, **with no `-update`**. If any fail, the extraction changed
output — fix the extraction, never the golden.

- [ ] **Step 7: Full suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all green.

- [ ] **Step 8: Commit**

```bash
go fmt ./...
git add internal/core/moddetail.go internal/core/moddetail_test.go cmd/lmm/mod.go
git commit -m "refactor(core): extract Service.ModDetail; mod show renders over it (#86)"
```

---

### Task 3: Clean HTML in `mod show`'s human output

The one intended CLI behavior change. JSON is deliberately untouched.

**Files:**

- Modify: `cmd/lmm/mod.go` (Description block, currently `:705-714`)
- Modify: `internal/core/changelog.go:8-22` (doc comment only)
- Modify: `cmd/lmm/testdata/mod_show_golden/*.txt` (re-recorded — the only task allowed to)
- Modify: `CHANGELOG.md`

**Interfaces:**

- Consumes: `core.CleanChangelog(html string) string` (`internal/core/changelog.go:28`).
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

Append to `cmd/lmm/mod_show_golden_test.go`:

```go
// TestDoModShow_DescriptionHTMLCleaned (#86): mod show printed a source's raw
// description HTML - literal <p> tags and &amp; entities in the user's
// terminal. It now runs through the same core.CleanChangelog the update flow
// and the TUI already share, so both interfaces render one cleaned text.
// JSON is deliberately NOT cleaned: it is a machine contract, and a consumer
// may want the original markup.
func TestDoModShow_DescriptionHTMLCleaned(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	src.AddMod(richMod(game.ID), nil)

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.Contains(t, out, "Line one.")
	assert.Contains(t, out, "Line & two.")
	assert.NotContains(t, out, "<p>", "raw HTML tags must not reach the terminal")
	assert.NotContains(t, out, "&amp;", "HTML entities must be decoded")
}

// TestDoModShow_JSONDescriptionStaysRaw pins the deliberate asymmetry above.
func TestDoModShow_JSONDescriptionStaysRaw(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	src.AddMod(richMod(game.ID), nil)

	old := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = old })

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "<p>Line one.</p><br/><p>Line &amp; two.</p>", got["description"],
		"--json is a machine contract; the raw description must survive")
}
```

Add `"encoding/json"` to the file's imports.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/lmm -run 'TestDoModShow_(DescriptionHTMLCleaned|JSONDescriptionStaysRaw)' -v`
Expected: `TestDoModShow_DescriptionHTMLCleaned` FAILS on `<p>` still being
present. `TestDoModShow_JSONDescriptionStaysRaw` PASSES already (regression
guard for the next step).

- [ ] **Step 3: Clean the description in the human path only**

In `cmd/lmm/mod.go`, replace the Description block:

```go
	if mod.Description != "" {
		fmt.Println("Description:")
		// #86: descriptions are source HTML. Clean them with the same shared
		// cleaner the update flow and the TUI already use, so all three render
		// identically. CleanChangelog trims for us, so no TrimSpace here.
		// The cap stays: this is a one-shot terminal dump, unlike the TUI's
		// details view, which scrolls the full text instead.
		desc := core.CleanChangelog(mod.Description)
		const maxDesc = 2000
		if len(desc) > maxDesc {
			desc = desc[:maxDesc] + "\n... (truncated; view on site for full description)"
		}
		fmt.Println(desc)
	}
```

- [ ] **Step 4: Run to verify both pass**

Run: `go test ./cmd/lmm -run 'TestDoModShow_(DescriptionHTMLCleaned|JSONDescriptionStaysRaw)' -v`
Expected: PASS ×2.

- [ ] **Step 5: Broaden the cleaner's doc comment**

In `internal/core/changelog.go`, the doc comment opens "CleanChangelog strips
HTML markup from html for readable terminal/TUI display". Append one sentence:

```go
// Despite the name (it predates any other caller), this is a general
// HTML-to-terminal cleaner, not changelog-specific: #86 also routes mod
// descriptions through it, so `lmm mod show` and the TUI's details view
// render a source's markup identically. Left named CleanChangelog to avoid
// churning three unrelated call sites for a rename.
```

- [ ] **Step 6: Re-record the goldens (the only sanctioned regeneration)**

Run: `go test ./cmd/lmm -run TestModShowGolden -update`

Then **inspect the diff** — `git diff cmd/lmm/testdata/mod_show_golden/`. It
must show only Description-block changes in the rich cases: `<p>`/`&amp;`
gone, text intact. `sparse.txt` (no description) and `json_installed.txt` must
be **unchanged**. If anything else moved, the change is wrong.

- [ ] **Step 7: CHANGELOG**

Under `[Unreleased]` → `### Changed`, add (create the section if absent,
keeping Keep-a-Changelog section order — Added, Changed, Fixed):

```markdown
- `lmm mod show` now strips HTML from a mod's description before printing
  it, using the same cleaner the update flow and TUI already share — raw
  `<p>` tags and `&amp;` entities no longer reach the terminal (#86).
  `--json` output is unchanged: it stays the raw source markup.
```

- [ ] **Step 8: Full suite and commit**

```bash
go build ./... && go vet ./... && go test ./...
go fmt ./...
git add cmd/lmm/mod.go cmd/lmm/mod_show_golden_test.go cmd/lmm/testdata/mod_show_golden internal/core/changelog.go CHANGELOG.md
git commit -m "feat(cli): mod show cleans description HTML (#86)"
```

---

### Task 4: Generalize the context-view host and swallow declined keys

Both halves ship together: generalizing without the swallow rule would let
`e`/`x`/`u` reach the mod row behind the pushed view.

**Files:**

- Modify: `internal/tui/contextview.go` (whole file)
- Modify: `internal/tui/app.go:150-153`, `:429-432`, `:966-975`, `:1393+`, `:2131-2147`
- Modify: `internal/tui/navigation.go:15-23` (doc comment)
- Modify: `internal/tui/health_screen_test.go:548-658`

**Interfaces:**

- Consumes: the `contextContent` interface (unchanged), `fakeContextContent` (`health_screen_test.go:517`).
- Produces:

  ```go
  func (m *Model) pushContext(c contextContent)  // no `from` param
  func (m *Model) popContext()                   // no return value
  func (m Model) contextView() string            // full-screen chrome + content
  ```

  `Model.contextReturn` is **deleted**.

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/health_screen_test.go`:

```go
// TestContextHostRendersOnPushingScreen (#86): pushing content no longer
// hijacks the session to ScreenHealth. The details view opened from Installed
// Mods must render there, with the nav bar still highlighting Installed Mods -
// a nav bar reading "Health" over a mod details view was the whole reason the
// host got generalized.
func TestContextHostRendersOnPushingScreen(t *testing.T) {
	m := sizedPrototypeModel(t, "wizardry", 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	fake := &fakeContextContent{}

	m.pushContext(fake)

	assert.Equal(t, ScreenInstalledMods, m.screen, "push must not move the session")
	view := m.View()
	assert.Contains(t, view, "FAKE DETAIL", "pushed content must render on the pushing screen")
}

// TestContextHostEscPopsToSameScreen: esc clears the content and leaves the
// session exactly where it was.
func TestContextHostEscPopsToSameScreen(t *testing.T) {
	m := sizedPrototypeModel(t, "wizardry", 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m.pushContext(&fakeContextContent{})

	m, _ = updateWithMsg(m, tea.KeyMsg{Type: tea.KeyEsc})

	assert.Nil(t, m.contextContent)
	assert.Equal(t, ScreenInstalledMods, m.screen)
	assert.NotContains(t, m.View(), "FAKE DETAIL")
}

// TestContextHostNavAwayClearsFromAnyScreen generalizes #224's stranded-
// content regression test off ScreenHealth.
func TestContextHostNavAwayClearsFromAnyScreen(t *testing.T) {
	m := sizedPrototypeModel(t, "wizardry", 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m.pushContext(&fakeContextContent{})

	m, _ = updateWithMsg(m, keyRunes("3")) // Search

	assert.Equal(t, ScreenSearch, m.screen)
	assert.Nil(t, m.contextContent, "navigating away must never strand pushed content")
	assert.NotContains(t, m.View(), "FAKE DETAIL")
}

// TestContextHostSwallowsDeclinedKeys is the safety half. A key the content
// declines must NOT reach the screen underneath: on Installed Mods that would
// mean arrow keys silently moving the selection behind the view, and e/x/u
// enabling or uninstalling the row the user can no longer see.
func TestContextHostSwallowsDeclinedKeys(t *testing.T) {
	rec := &recordingActions{}
	m := sizedModelWithActions(t, rec, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	before := m.selected[ScreenInstalledMods]
	m.pushContext(&fakeContextContent{}) // declines everything except "x"

	for _, k := range []string{"j", "e", "u"} {
		m, _ = updateWithMsg(m, keyRunes(k))
	}
	m, _ = updateWithMsg(m, tea.KeyMsg{Type: tea.KeyDown})

	assert.Equal(t, before, m.selected[ScreenInstalledMods], "selection must not move behind pushed content")
	assert.Zero(t, rec.EnableModCalls, "a declined key must not trigger a mutation underneath")
	assert.NotNil(t, m.contextContent, "declined keys must not close the view either")
}

// TestContextHostStillAllowsQuitAndHelp: the swallow rule has exits.
func TestContextHostStillAllowsQuitAndHelp(t *testing.T) {
	m := sizedPrototypeModel(t, "wizardry", 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m.pushContext(&fakeContextContent{})

	m2, _ := updateWithMsg(m, keyRunes("?"))
	assert.NotEqual(t, m.showHelp, m2.showHelp, "help must still toggle over pushed content")

	_, cmd := updateWithMsg(m, keyRunes("q"))
	assert.NotNil(t, cmd, "quit must still work over pushed content")
}
```

Update the five existing host tests at `:548-658` for the new signatures:
`pushContext(fake)` (one arg) and, in
`TestHealthContextHostPushRenderKeyAndEscPop`, assert the screen is
**unchanged** rather than `ScreenHealth`. `TestPopContextNoopWhenNothingPushed`
drops its return-value assertion.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/tui -run 'TestContextHost' -v`
Expected: FAIL to compile — `too many arguments in call to m.pushContext`.

- [ ] **Step 3: Rewrite `internal/tui/contextview.go`**

```go
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// contextContent is a pluggable full-screen content view that any screen can
// push over itself (#224, generalized in #86). One-deep by design (YAGNI):
// push replaces any pushed content, esc pops, and navigating to another
// screen pops implicitly. The pushing screen stays the current screen
// throughout - the nav bar keeps highlighting it - so a mod details view
// opened from Installed Mods reads as "Installed Mods, showing a mod",
// not as a jump to some other screen.
type contextContent interface {
	Title() string
	// Lines renders the body for the given content box; the host owns
	// chrome (panel, title, nav) and clamping.
	Lines(width, height int) []string
	// HandleKey lets pushed content consume a key before the outer
	// switch; handled=false means the host swallows it (see updateKey) -
	// it does NOT fall through to the screen underneath.
	HandleKey(msg tea.KeyMsg) (next contextContent, cmd tea.Cmd, handled bool)
	HelpGroup() helpGroup
}

// pushContext replaces the current pushed content (one-deep by design) with
// c. The session's screen is deliberately NOT changed: screenView renders
// pushed content ahead of the per-screen switch, so the content appears over
// whatever screen the user is on and esc simply returns them to it. Pointer
// receiver so updateKey handlers can call it directly.
func (m *Model) pushContext(c contextContent) {
	m.contextContent = c
}

// popContext clears the pushed content, revealing the screen underneath. A
// no-op when nothing is pushed, so callers need no guard.
func (m *Model) popContext() {
	m.contextContent = nil
}

// contextView renders pushed content full-screen: the host owns the panel,
// the title line, truncation, and clamping, so content views stay small.
// Moved here from healthScreenView in #86, since the host is no longer
// Health's private machinery.
func (m Model) contextView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	contentWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)
	contentBudget := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)

	lines := []string{m.theme.PanelTitle.Render(m.contextContent.Title())}
	lines = append(lines, m.contextContent.Lines(contentWidth, max(contentBudget-1, 1))...)
	lines = m.truncateLines(lines, contentWidth)
	lines = m.clampLines(lines, contentBudget)

	return m.panelWithHeight(width, height).Render(strings.Join(lines, "\n"))
}
```

- [ ] **Step 4: Update `app.go`**

**(a)** Delete the `contextReturn Screen` field (`:153`) and rewrite the
`contextContent` field's doc comment (`:146-152`) to drop Health-specificity.

**(b)** `screenView()` (`:1393`) — add after the `inputModal` early-return and
before the screen switch:

```go
	// Pushed content renders over whatever screen pushed it (#86). Placed
	// after the picker/inputModal returns above so modals still outrank it,
	// matching updateKey's own precedence.
	if m.contextContent != nil {
		return m.contextView()
	}
```

**(c)** `case ScreenHealth:` (`:1441`) now reads `return m.healthHomeView()`.
Delete `healthScreenView` (`:2131-2147`) — `contextView` absorbed its body.

**(d)** Key routing (`:966`) — drop the screen gate and add the swallow:

```go
	// Pushed content gets first refusal on every key. Anything it declines is
	// SWALLOWED rather than falling through to the screen underneath (#86) -
	// otherwise arrows would move a selection the user can't see and e/x/u
	// would mutate the row behind the view. Same rule updateOverlayKey
	// already applies for the info overlay. The exits below are the keys that
	// must keep working over any full-screen content: leave, navigate, quit,
	// help.
	if m.contextContent != nil {
		if next, cmd, handled := m.contextContent.HandleKey(msg); handled {
			m.contextContent = next
			return m, cmd
		}
		switch {
		case key.Matches(msg, m.keys.Blur):
			m.popContext()
			return m, nil
		case m.isQuitKey(msg):
			// fall through to the outer switch's quit handling
		case key.Matches(msg, m.keys.Help),
			key.Matches(msg, m.keys.NextScreen), key.Matches(msg, m.keys.PrevScreen),
			isScreenDigit(msg):
			// fall through: navigation and help stay available
		default:
			return m, nil // swallowed
		}
	}
```

`isQuitKey` is a **method** on `Model` (`internal/tui/actions.go:619`), hence
`m.isQuitKey(msg)` — it is deliberately narrower than
`key.Matches(msg, m.keys.Quit)` because a plain `q` must stay typeable in some
contexts.

No `isScreenDigit` helper exists yet. Add one beside the digit handling in
`updateKey` (`screens` is the nav slice at `internal/tui/navigation.go:26`):

```go
// isScreenDigit reports whether msg is one of the nav digits ("1".."6"), so
// the pushed-content swallow rule can let navigation through.
func isScreenDigit(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return false
	}
	return msg.Runes[0] >= '1' && msg.Runes[0] <= rune('0'+len(screens))
}
```

**(e)** Nav-away pop (`:429`) — clear whenever content is pushed:

```go
	// Navigating anywhere pops pushed content (#86: any screen can host it,
	// so this is no longer Health-specific). gotoScreen is the single choke
	// point every nav route funnels through, so one clear here covers them
	// all - including pressing the digit for the screen you are already on.
	if m.contextContent != nil {
		m.contextContent = nil
	}
```

**(f)** `helpGroups()` (`:2857-2866`) — drop `m.screen == ScreenHealth &&`,
leaving `if m.contextContent != nil {`.

- [ ] **Step 5: Update `navigation.go`'s doc comment**

`internal/tui/navigation.go:15-23` describes `ScreenHealth` as the context-view
host. Reword: Health is one screen among six; any screen can host pushed
content since #86.

- [ ] **Step 6: Run the TUI suite**

Run: `go test ./internal/tui -v 2>&1 | tail -40`
Expected: PASS, including all five updated #224 host tests and the five new
ones. The Health action guards (`:856`, `:883`, `:1397`,
`health_conflicts_test.go:304`) must pass **unmodified** — they now assert
defense-in-depth behind the swallow rule.

- [ ] **Step 7: Full suite and commit**

```bash
go build ./... && go vet ./... && go test ./...
go fmt ./...
git add internal/tui/contextview.go internal/tui/app.go internal/tui/navigation.go internal/tui/health_screen_test.go
git commit -m "refactor(tui): any screen can host pushed content; declined keys swallowed (#86)"
```

---

### Task 5: TUI view model and provider methods

**Files:**

- Modify: `internal/tui/service.go` (types, `DataProvider`, prototype impl)
- Modify: `internal/tui/service_core.go` (`coreProvider.GetModDetails`)
- Modify: `internal/tui/prototype/data.go:79-101`
- Modify: `internal/tui/service_test.go` (or create `internal/tui/moddetails_provider_test.go`)

**Interfaces:**

- Consumes: `core.Service.ModDetail` (Task 2), `mapNetworkError` (`service_core.go:1207`), `p.currentGame()`/`p.currentProfile()`.
- Produces:

  ```go
  // DataProvider gains:
  GetModDetails(ctx context.Context, item ModItem) (ModDetails, error)

  type ModDetails struct {
      ID, Name, Version, Author string
      Summary, Description      string
      Category                  string
      SourceURL, PictureURL     string
      Endorsements              int64
      HasEndorsements           bool
      Installed                 *InstalledDetails
      Fetching                  bool
      FetchErr                  string
  }

  type InstalledDetails struct {
      Version, Profile, UpdatePolicy string
      Locked                         bool
      LockedVersion                  string
      ConvertPaks                    *bool
  }

  func modDetailsFromItem(item ModItem) ModDetails // the local-first seed
  ```

- [ ] **Step 1: Write the failing test**

Create `internal/tui/moddetails_provider_test.go`:

```go
package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModDetailsFromItem: the local-first seed. Opening details must render
// immediately from the row already in hand, so everything the row knows has
// to survive the conversion - the network fetch only ADDS.
func TestModDetailsFromItem(t *testing.T) {
	item := ModItem{
		ID: "a", Name: "Mod A", Version: "1.5", Author: "Author A",
		Source: "src", Summary: "A short summary.",
		Endorsements: 42, HasEndorsements: true,
		UpdatePolicy: "notify", Locked: true, LockedVersion: "1.2.3",
		Status: "installed",
	}

	d := modDetailsFromItem(item)

	assert.Equal(t, "Mod A", d.Name)
	assert.Equal(t, "1.5", d.Version)
	assert.Equal(t, "Author A", d.Author)
	assert.Equal(t, "A short summary.", d.Summary)
	assert.Equal(t, int64(42), d.Endorsements)
	assert.True(t, d.HasEndorsements)
	require.NotNil(t, d.Installed, "an installed row must seed the Installed block locally")
	assert.Equal(t, "1.5", d.Installed.Version)
	assert.True(t, d.Installed.Locked)
	assert.Equal(t, "1.2.3", d.Installed.LockedVersion)
	assert.Empty(t, d.Description, "description is network-only; the seed must not invent one")
}

// TestModDetailsFromItem_NotInstalled: a search hit for an uninstalled mod
// gets no Installed block, matching mod show's omit rule.
func TestModDetailsFromItem_NotInstalled(t *testing.T) {
	d := modDetailsFromItem(ModItem{ID: "a", Name: "Mod A", Status: "available"})
	assert.Nil(t, d.Installed)
}

// TestPrototypeGetModDetails: the prototype provider must serve details too,
// or `lmm tui --prototype` and most TUI tests can't exercise the view.
func TestPrototypeGetModDetails(t *testing.T) {
	p := newPrototypeProviderConcrete()
	_, items, err := p.Overview(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, items)

	d, err := p.GetModDetails(t.Context(), items[0])
	require.NoError(t, err)
	assert.Equal(t, items[0].Name, d.Name)
	assert.NotEmpty(t, d.Description, "the prototype must serve a description, or the view has nothing to show")
	assert.NotEmpty(t, d.SourceURL)
}
```

If `t.Context()` is unavailable on this Go version, use `context.Background()`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/tui -run 'TestModDetailsFromItem|TestPrototypeGetModDetails' -v`
Expected: FAIL — `undefined: modDetailsFromItem`.

- [ ] **Step 3: Add the view models and the seed to `service.go`**

```go
// ModDetails is the mod-details view's render model (#86). Seeded locally
// from the ModItem the user selected, then enriched in place by
// GetModDetails - so the view opens instantly and fills in, rather than
// blocking on a network round trip the user may not be able to complete.
type ModDetails struct {
	ID, Name, Version, Author string
	Summary, Description      string
	Category                  string
	SourceURL, PictureURL     string
	Endorsements              int64
	HasEndorsements           bool

	// Installed is nil when the mod is not installed in the active profile,
	// matching `lmm mod show`'s omit rule.
	Installed *InstalledDetails

	// Fetching/FetchErr are set by the model's handler and resolvers, never
	// by a provider; the view reads them to pick its render state.
	Fetching bool
	FetchErr string
}

// InstalledDetails mirrors core.InstalledDetail with the policy already
// rendered to a display string - the TUI has no reason to carry a
// domain.UpdatePolicy. A separate type because a view model is a rendering
// contract, the convention every other TUI row type follows.
type InstalledDetails struct {
	Version       string
	Profile       string
	UpdatePolicy  string
	Locked        bool
	LockedVersion string
	ConvertPaks   *bool // nil = not applicable, not "off"
}

// modDetailsFromItem seeds a details view from the row already on screen.
// Everything here is local: no I/O, so the view can render on the very first
// frame. Description/Category/SourceURL/PictureURL stay empty until the fetch
// lands - inventing placeholders for them would be worse than a blank.
func modDetailsFromItem(item ModItem) ModDetails {
	d := ModDetails{
		ID: item.ID, Name: item.Name, Version: item.Version, Author: item.Author,
		Summary:         item.Summary,
		Endorsements:    item.Endorsements,
		HasEndorsements: item.HasEndorsements,
	}
	if item.Status != "available" {
		d.Installed = &InstalledDetails{
			Version:       item.Version,
			UpdatePolicy:  item.UpdatePolicy,
			Locked:        item.Locked,
			LockedVersion: item.LockedVersion,
		}
		if item.CompileGame && item.HasPakSource {
			v := item.ConvertPaks
			d.Installed.ConvertPaks = &v
		}
	}
	return d
}
```

Add to the `DataProvider` interface (`service.go:288`):

```go
	// GetModDetails fetches full metadata for item's mod and joins local
	// install state. A network call for remote sources - callers must run it
	// off the render path (see mutations.go's openSelectedModDetails).
	GetModDetails(ctx context.Context, item ModItem) (ModDetails, error)
```

- [ ] **Step 4: Implement on `coreProvider` (`service_core.go`)**

Modelled on `AvailableVersions` (`:1674`):

```go
func (p *coreProvider) GetModDetails(ctx context.Context, item ModItem) (ModDetails, error) {
	action := fmt.Sprintf("fetching details for %s", item.Name)
	game := p.currentGame()
	detail, err := p.svc.ModDetail(ctx, game, p.currentProfile(), item.Source, item.ID)
	if err != nil {
		return ModDetails{}, mapNetworkError(action, item.Source, "mod details",
			"run 'lmm mod show "+item.ID+"' in a shell", err)
	}

	out := modDetailsFromItem(item)
	mod := detail.Mod
	out.Name, out.Version, out.Author = mod.Name, mod.Version, mod.Author
	out.Summary, out.Category = mod.Summary, mod.Category
	out.SourceURL, out.PictureURL = mod.SourceURL, mod.PictureURL
	// Same shared cleaner the CLI's mod show and the update flow use, so all
	// three surfaces render a source's markup identically (#86).
	out.Description = core.CleanChangelog(mod.Description)
	if mod.Endorsements != nil {
		out.Endorsements, out.HasEndorsements = *mod.Endorsements, true
	}

	if detail.Installed != nil {
		out.Installed = &InstalledDetails{
			Version:       detail.Installed.Version,
			Profile:       detail.Installed.Profile,
			UpdatePolicy:  policyToString(detail.Installed.UpdatePolicy),
			Locked:        detail.Installed.Locked,
			LockedVersion: detail.Installed.LockedVersion,
			ConvertPaks:   detail.Installed.ConvertPaks,
		}
	} else {
		out.Installed = nil
	}
	return out, nil
}
```

**`policyToString`, not a string conversion.** `domain.UpdatePolicy` is an
`int` (`internal/domain/mod.go:29`), so `string(policy)` would compile and
silently produce a garbage rune — `go vet` flags it. The package already has
`policyToString(domain.UpdatePolicy) string` at
`internal/tui/service_core.go:630`, which `Overview` uses for
`ModItem.UpdatePolicy` (`:164`). Use it.

- [ ] **Step 5: Implement on `prototypeProvider` and extend `prototype.Mod`**

In `internal/tui/prototype/data.go`, add to `Mod` (after `Summary`):

```go
	// Description/SourceURL/PictureURL feed the mod details view (#86). Only
	// a few entries set them; the view's omit-when-empty rules are exactly
	// what the sparse entries exercise.
	Description string
	SourceURL   string
	PictureURL  string
```

Populate them on at least two demo mods in the same file — one rich (multi-
paragraph description, both URLs), one sparse (none) — so the prototype shows
both render paths.

In `internal/tui/service.go`, on `prototypeProvider`:

```go
// allMods is every prototype mod a details view can be opened on: the active
// game's installed mods plus the search catalog - the two lists modItems is
// fed from (Overview at service.go:427, Search at :550).
func (p *prototypeProvider) allMods() []prototype.Mod {
	return append(append([]prototype.Mod(nil), p.activeMods()...), p.data.SearchResults...)
}

func (p *prototypeProvider) GetModDetails(_ context.Context, item ModItem) (ModDetails, error) {
	out := modDetailsFromItem(item)
	for _, m := range p.allMods() {
		if m.ID != item.ID {
			continue
		}
		out.Description, out.SourceURL, out.PictureURL = m.Description, m.SourceURL, m.PictureURL
		break
	}
	return out, nil
}
```

`p.activeMods()` is at `internal/tui/service.go:394`; `p.data.SearchResults` is
the catalog `Search` filters at `:558`.

- [ ] **Step 6: Run to verify the tests pass**

Run: `go test ./internal/tui -run 'TestModDetailsFromItem|TestPrototypeGetModDetails' -v`
Expected: PASS ×3.

- [ ] **Step 7: Fix every other `DataProvider` implementation**

Run: `go build ./... 2>&1 | head -20`

The new interface method breaks `stubProvider` (`app_test.go:1064`) and any
bespoke test double implementing `DataProvider` in full. Add to `stubProvider`:

```go
func (stubProvider) GetModDetails(context.Context, ModItem) (ModDetails, error) {
	return ModDetails{}, nil
}
```

Doubles that embed `stubProvider` inherit it. Fix any that don't, individually.

- [ ] **Step 8: Full suite and commit**

```bash
go build ./... && go vet ./... && go test ./...
go fmt ./...
git add internal/tui/service.go internal/tui/service_core.go internal/tui/prototype/data.go internal/tui/moddetails_provider_test.go internal/tui/app_test.go
git commit -m "feat(tui): ModDetails view model and GetModDetails provider method (#86)"
```

---

### Task 6: The details content view

**Files:**

- Create: `internal/tui/moddetails.go`
- Create: `internal/tui/moddetails_test.go`

**Interfaces:**

- Consumes: `ModDetails`/`InstalledDetails` (Task 5), the `contextContent`
  interface (Task 4), `helpGroup`/`helpRow` (`app.go:2686`, `:2692`).
- Produces:

  ```go
  type modDetailsContent struct {
      details ModDetails
      offset  int
  }
  func newModDetailsContent(d ModDetails) *modDetailsContent
  // satisfies contextContent: Title/Lines/HandleKey/HelpGroup
  ```

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testDetails() ModDetails {
	return ModDetails{
		ID: "12345", Name: "Mod A", Version: "2.2.6", Author: "Author A",
		Category: "Utilities", Summary: "A short summary.",
		Description:     "Line one.\n\nLine two.",
		SourceURL:       "https://example.invalid/mods/1",
		PictureURL:      "https://example.invalid/img/1.png",
		Endorsements:    84213,
		HasEndorsements: true,
		Installed: &InstalledDetails{
			Version: "2.2.3", Profile: "default", UpdatePolicy: "notify",
			Locked: true, LockedVersion: "2.2.3",
		},
	}
}

// TestModDetailsContent_FieldOrderMatchesModShow pins CLI parity: the TUI
// renders the same fields, in the same order, as `lmm mod show`.
func TestModDetailsContent_FieldOrderMatchesModShow(t *testing.T) {
	body := strings.Join(newModDetailsContent(testDetails(), DefaultKeyMap()).Lines(80, 40), "\n")

	order := []string{"ID: 12345", "Category: Utilities", "Endorsements: 84213",
		"URL: https://", "Image: https://", "Summary:", "Description:", "Installed: v2.2.3"}
	last := -1
	for _, want := range order {
		at := strings.Index(body, want)
		require.GreaterOrEqual(t, at, 0, "missing field %q in:\n%s", want, body)
		assert.Greater(t, at, last, "field %q is out of mod show order", want)
		last = at
	}
}

// TestModDetailsContent_OmitsEmptyFields mirrors mod show's omit rules -
// blank labels with nothing after them are noise.
func TestModDetailsContent_OmitsEmptyFields(t *testing.T) {
	d := ModDetails{ID: "a", Name: "Mod A", Version: "1.0", Author: "X"}
	body := strings.Join(newModDetailsContent(d, DefaultKeyMap()).Lines(80, 40), "\n")

	for _, absent := range []string{"Category:", "Endorsements:", "URL:", "Image:", "Summary:", "Description:", "Installed:"} {
		assert.NotContains(t, body, absent)
	}
}

// TestModDetailsContent_InstalledBlockRendersLockAndConverge: parity with
// mod show's Installed section, including the converge hint's condition.
func TestModDetailsContent_InstalledBlockRendersLockAndConverge(t *testing.T) {
	d := testDetails()
	d.Installed.LockedVersion = "1.0.0" // differs from installed 2.2.3
	body := strings.Join(newModDetailsContent(d, DefaultKeyMap()).Lines(80, 40), "\n")

	assert.Contains(t, body, "Installed: v2.2.3 (profile: default)")
	assert.Contains(t, body, "Update policy: notify")
	assert.Contains(t, body, "Lock: locked at v1.0.0 — run 'lmm profile apply' to converge")
}

func TestModDetailsContent_NoConvergeHintWhenLockMatchesInstalled(t *testing.T) {
	body := strings.Join(newModDetailsContent(testDetails(), DefaultKeyMap()).Lines(80, 40), "\n")
	assert.Contains(t, body, "Lock: locked at v2.2.3")
	assert.NotContains(t, body, "converge")
}

// TestModDetailsContent_RenderStates covers the three fetch states.
func TestModDetailsContent_RenderStates(t *testing.T) {
	fetching := testDetails()
	fetching.Fetching = true
	fetching.Description = ""
	assert.Contains(t, strings.Join(newModDetailsContent(fetching, DefaultKeyMap()).Lines(80, 40), "\n"), "(loading…)")

	failed := testDetails()
	failed.Description = ""
	failed.FetchErr = "source unreachable"
	assert.Contains(t, strings.Join(newModDetailsContent(failed, DefaultKeyMap()).Lines(80, 40), "\n"),
		"(unavailable — source unreachable)")
}

// TestModDetailsContent_ScrollsAndConsumesArrows is the safety-critical half:
// the view MUST consume up/down, or the list selection behind it drifts.
func TestModDetailsContent_ScrollsAndConsumesArrows(t *testing.T) {
	d := testDetails()
	d.Description = strings.Repeat("a line of description text\n", 200)
	c := newModDetailsContent(d, DefaultKeyMap())

	first := c.Lines(80, 10)
	next, _, handled := c.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	require.True(t, handled, "the details view must consume Down, not let it move the list behind")
	scrolled := next.(*modDetailsContent).Lines(80, 10)
	assert.NotEqual(t, first, scrolled, "Down must scroll the body")

	_, _, handledUp := next.HandleKey(tea.KeyMsg{Type: tea.KeyUp})
	assert.True(t, handledUp)
	_, _, handledJ := next.HandleKey(keyRunes("j"))
	assert.True(t, handledJ, "j/k aliases must scroll too")
}

// TestModDetailsContent_ShortBodyDoesNotScroll: no phantom scrolling.
func TestModDetailsContent_ShortBodyDoesNotScroll(t *testing.T) {
	c := newModDetailsContent(testDetails(), DefaultKeyMap())
	before := c.Lines(80, 40)
	next, _, handled := c.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	assert.True(t, handled, "arrows stay consumed even when there is nothing to scroll")
	assert.Equal(t, before, next.(*modDetailsContent).Lines(80, 40))
}

func TestModDetailsContent_TitleIsModName(t *testing.T) {
	assert.Equal(t, "Mod A", newModDetailsContent(testDetails(), DefaultKeyMap()).Title())
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/tui -run TestModDetailsContent -v`
Expected: FAIL — `undefined: newModDetailsContent`.

- [ ] **Step 3: Create `internal/tui/moddetails.go`**

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// modDetailsContent is the mod details view (#86): a contextContent pushed
// over Installed Mods or Search, rendering the same fields as `lmm mod show`
// in the same order. Scrolls its whole body rather than truncating, since a
// description can run long - the CLI's 2000-char cap exists only because a
// one-shot terminal dump cannot scroll.
type modDetailsContent struct {
	details ModDetails
	// offset is the first visible body line, mirroring infoOverlay.offset.
	// Clamped on every key and again at render time.
	offset int
	// keys is the session's live KeyMap, not DefaultKeyMap(), so a custom
	// remapping of Up/Down can never desync this view's scrolling from the
	// rest of the TUI - the same reasoning behind overlay.go matching
	// m.keys.Files instead of a literal "f" (Copilot PR #69 finding).
	keys KeyMap
}

func newModDetailsContent(d ModDetails, keys KeyMap) *modDetailsContent {
	return &modDetailsContent{details: d, keys: keys}
}

func (c *modDetailsContent) Title() string { return c.details.Name }

// body builds every line before windowing, so scrolling and clamping operate
// on one flat list. Field order and omit-when-empty rules mirror doModShow
// (cmd/lmm/mod.go) exactly - that parity is the point of the feature.
func (c *modDetailsContent) body() []string {
	d := c.details
	lines := []string{
		fmt.Sprintf("ID: %s   Version: %s   Author: %s", d.ID, d.Version, d.Author),
	}
	if d.Category != "" {
		lines = append(lines, "Category: "+d.Category)
	}
	if d.HasEndorsements {
		lines = append(lines, fmt.Sprintf("Endorsements: %d", d.Endorsements))
	}
	if d.SourceURL != "" {
		lines = append(lines, "URL: "+d.SourceURL)
	}
	if d.PictureURL != "" {
		lines = append(lines, "Image: "+d.PictureURL)
	}

	if d.Summary != "" {
		lines = append(lines, "", "Summary:")
		lines = append(lines, strings.Split(strings.TrimSpace(d.Summary), "\n")...)
	}

	// Description has three states. An empty description with no fetch in
	// flight and no error is simply absent upstream - omit the block, same as
	// mod show, rather than showing an empty heading.
	switch {
	case d.Description != "":
		lines = append(lines, "", "Description:")
		lines = append(lines, strings.Split(strings.TrimSpace(d.Description), "\n")...)
	case d.FetchErr != "":
		lines = append(lines, "", "Description:", "(unavailable — "+d.FetchErr+")")
	case d.Fetching:
		lines = append(lines, "", "Description:", "(loading…)")
	}

	if d.Installed != nil {
		lines = append(lines, "", fmt.Sprintf("Installed: v%s (profile: %s)",
			d.Installed.Version, d.Installed.Profile))
		lines = append(lines, "  Update policy: "+d.Installed.UpdatePolicy)
		if d.Installed.Locked {
			lock := "locked at v" + d.Installed.LockedVersion
			// Only name the converge command when the lock's target actually
			// differs from what's installed - identical condition to
			// doModShow's (cmd/lmm/mod.go:734-736).
			if d.Installed.LockedVersion != d.Installed.Version {
				lock += " — run 'lmm profile apply' to converge"
			}
			lines = append(lines, "  Lock: "+lock)
		} else {
			lines = append(lines, "  Lock: none")
		}
		if d.Installed.ConvertPaks != nil {
			state := "on"
			if !*d.Installed.ConvertPaks {
				state = "off"
			}
			lines = append(lines, "  Pak conversion: "+state)
		}
	}
	return lines
}

// maxOffset is the largest first-visible-line index that still fills height.
func (c *modDetailsContent) maxOffset(height int) int {
	over := len(c.body()) - height
	if over < 0 {
		return 0
	}
	return over
}

func (c *modDetailsContent) Lines(width, height int) []string {
	body := c.body()
	if height < 1 {
		height = 1
	}
	offset := min(max(c.offset, 0), c.maxOffset(height))
	end := min(offset+height, len(body))
	visible := append([]string(nil), body[offset:end]...)

	// Scroll affordance, matching windowedRows' own indicator convention.
	if more := len(body) - end; more > 0 {
		visible = append(visible, fmt.Sprintf("↓ %d more", more))
	}
	return visible
}

// HandleKey consumes scrolling. Consuming up/down is MANDATORY, not a
// nicety: a declined arrow would move the list selection on the screen
// underneath, which the user cannot see (#86). Every other key is declined
// so the host's swallow rule and esc-pop can act on it.
func (c *modDetailsContent) HandleKey(msg tea.KeyMsg) (contextContent, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, c.keys.Down):
		c.offset++
		return c, nil, true
	case key.Matches(msg, c.keys.Up):
		if c.offset > 0 {
			c.offset--
		}
		return c, nil, true
	}
	return c, nil, false
}

func (c *modDetailsContent) HelpGroup() helpGroup {
	return helpGroup{
		name: "mod details",
		entries: []string{
			helpRow("↑/↓", "scroll"),
			helpRow("esc", "back"),
		},
	}
}
```

`c.keys.Up` binds `up`/`k` and `c.keys.Down` binds `down`/`j`
(`internal/tui/keys.go:190-197`), so the `j`/`k` aliases the test asserts come
for free. `KeyMap` is the struct at `keys.go:6`; `DefaultKeyMap()` at `:172`.

- [ ] **Step 4: Run to verify the tests pass**

Run: `go test ./internal/tui -run TestModDetailsContent -v`
Expected: PASS ×8.

- [ ] **Step 5: Full suite and commit**

```bash
go build ./... && go vet ./... && go test ./...
go fmt ./...
git add internal/tui/moddetails.go internal/tui/moddetails_test.go
git commit -m "feat(tui): mod details content view with mod show field parity (#86)"
```

---

### Task 7: Open on `enter`, fetch in the background

**Files:**

- Modify: `internal/tui/mutations.go` (handler, messages, resolvers)
- Modify: `internal/tui/app.go` (`Select` case, message dispatch, help entry)
- Modify: `internal/tui/mutations_test.go` or create `internal/tui/moddetails_open_test.go`

**Interfaces:**

- Consumes: `newModDetailsContent` (Task 6), `modDetailsFromItem` +
  `GetModDetails` (Task 5), `pushContext` (Task 4), `m.selectedMod()`
  (`mutations.go:33`).
- Produces:

  ```go
  func (m Model) openSelectedModDetails() (Model, tea.Cmd)
  type modDetailsFetchedMsg struct { gen int; details ModDetails }
  type modDetailsFailedMsg  struct { gen int; err error }
  func (m Model) resolveModDetailsFetched(msg modDetailsFetchedMsg) (Model, tea.Cmd)
  func (m Model) resolveModDetailsFailed(msg modDetailsFailedMsg) (Model, tea.Cmd)
  ```

- [ ] **Step 1: Write the failing test**

Create `internal/tui/moddetails_open_test.go`:

```go
package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenModDetails_PushesImmediatelyWithLocalData is the local-first
// contract: the view must be on screen before the fetch resolves, seeded from
// the row, so an offline user still sees what the list already knew.
func TestOpenModDetails_PushesImmediatelyWithLocalData(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)

	m, cmd := m.openSelectedModDetails()

	require.NotNil(t, m.contextContent, "the view must be pushed before the fetch resolves")
	require.NotNil(t, cmd, "a fetch must be dispatched")
	assert.Equal(t, ScreenInstalledMods, m.screen)
	assert.True(t, m.action.running)
	body := m.View()
	item, _ := m.selectedMod()
	assert.Contains(t, body, item.Name)
}

// TestOpenModDetails_EnterBindingOnBothScreens: enter is the binding, and it
// works from Installed Mods and Search.
func TestOpenModDetails_EnterBindingOnBothScreens(t *testing.T) {
	for _, screen := range []Screen{ScreenInstalledMods, ScreenSearch} {
		m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
		m, _ = m.gotoScreen(screen)
		if screen == ScreenSearch {
			// Same idiom search_test.go uses to reach a populated results
			// list (e.g. :685) - populatedSearchPage() at search_test.go:654.
			m.search.page = populatedSearchPage()
		}
		m, _ = updateWithMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
		assert.NotNil(t, m.contextContent, "enter must open details on %v", screen)
	}
}

// TestOpenModDetails_EnterStillOpensDashboardMenu: the existing meaning of
// enter on the dashboard must not regress.
func TestOpenModDetails_EnterStillOpensDashboardMenu(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenDashboard)
	m, _ = updateWithMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, m.contextContent, "enter on the dashboard opens a menu entry, not details")
}

// TestOpenModDetails_SingleFlight: a second open while one is in flight is
// refused, matching every other async fetch.
func TestOpenModDetails_SingleFlight(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, _ = m.openSelectedModDetails()
	gen := m.action.gen

	m, cmd := m.openSelectedModDetails()
	assert.Nil(t, cmd, "a second open must be refused while one is running")
	assert.Equal(t, gen, m.action.gen)
}

// TestOpenModDetails_StaleResultDropped
func TestOpenModDetails_StaleResultDropped(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, _ = m.openSelectedModDetails()

	stale := modDetailsFetchedMsg{gen: m.action.gen - 1, details: ModDetails{Description: "STALE"}}
	m, _ = updateWithMsg(m, stale)

	assert.NotContains(t, m.View(), "STALE")
	assert.True(t, m.action.running, "a stale result must not clear the running flag")
}

// TestOpenModDetails_SuccessEnrichesInPlace
func TestOpenModDetails_SuccessEnrichesInPlace(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, _ = m.openSelectedModDetails()

	enriched := ModDetails{Name: "Mod A", ID: "a", Version: "1.0", Author: "X",
		Description: "ENRICHED DESCRIPTION"}
	m, _ = updateWithMsg(m, modDetailsFetchedMsg{gen: m.action.gen, details: enriched})

	assert.False(t, m.action.running)
	assert.Contains(t, m.View(), "ENRICHED DESCRIPTION")
}

// TestOpenModDetails_FailureKeepsLocalView is the degradation contract: a
// failed fetch must never close the view or discard what was already shown.
func TestOpenModDetails_FailureKeepsLocalView(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	item, _ := m.selectedMod()
	m, _ = m.openSelectedModDetails()

	m, _ = updateWithMsg(m, modDetailsFailedMsg{gen: m.action.gen, err: errors.New("source unreachable")})

	require.NotNil(t, m.contextContent, "a failed fetch must not close the view")
	view := m.View()
	assert.Contains(t, view, item.Name, "local data must survive the failure")
	assert.Contains(t, view, "unavailable")
	assert.False(t, m.action.running)
	assert.True(t, m.action.statusIsError)
}

// TestOpenModDetails_NoProviderNoOp
func TestOpenModDetails_NoProviderNoOp(t *testing.T) {
	m := sizedModelWithActions(t, nil, 100, 30)
	m.provider = nil
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, cmd := m.openSelectedModDetails()
	assert.Nil(t, cmd)
	assert.Nil(t, m.contextContent)
}

// TestOpenModDetails_QuitDrainCancels: quitting mid-fetch drains cleanly.
func TestOpenModDetails_QuitDrainCancels(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, _ = m.openSelectedModDetails()
	m.action.draining = true

	_, cmd := updateWithMsg(m, modDetailsFetchedMsg{gen: m.action.gen, details: ModDetails{}})
	assert.NotNil(t, cmd, "a drained quit must produce the quit command")
}

var _ = context.Background
```

`m.provider` is the `DataProvider` field (`internal/tui/app.go:81`);
`sizedModelWithActions` leaves it as the prototype provider, which is what
serves `GetModDetails` in these tests.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/tui -run TestOpenModDetails -v`
Expected: FAIL — `undefined: openSelectedModDetails`.

- [ ] **Step 3: Add the messages and handler to `mutations.go`**

```go
// modDetailsFetchedMsg/modDetailsFailedMsg carry the background enrichment of
// an ALREADY-PUSHED details view (#86). Gen-tagged like every other async
// fetch (see this file's checkUpdatesResultMsg template) so a result that
// lands after the user moved on is discarded.
type modDetailsFetchedMsg struct {
	gen     int
	details ModDetails
}

type modDetailsFailedMsg struct {
	gen int
	err error
}

// openSelectedModDetails pushes the details view for the selected mod and
// kicks off the enrichment fetch. The view is pushed FIRST, seeded from the
// row already on screen, so it renders on the very next frame - blocking on
// the round trip would leave an offline user with nothing, when the list
// already knew the name, version, and install state.
func (m Model) openSelectedModDetails() (Model, tea.Cmd) {
	if m.screen != ScreenInstalledMods && m.screen != ScreenSearch {
		return m, nil
	}
	if m.provider == nil {
		return m, nil
	}
	if m.action.running || m.action.pending != nil {
		return m, nil
	}
	item, ok := m.selectedMod()
	if !ok {
		return m, nil
	}

	seed := modDetailsFromItem(item)
	seed.Fetching = true
	m.pushContext(newModDetailsContent(seed, m.keys))

	if m.action.cancel != nil {
		m.action.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.action.cancel = cancel
	m.action.gen++
	gen := m.action.gen
	m.action.running = true
	m.action.status = fmt.Sprintf("Fetching details for %s…", item.Name)
	m.action.statusIsError = false

	provider := m.provider
	return m, func() tea.Msg {
		details, err := provider.GetModDetails(ctx, item)
		if err != nil {
			return modDetailsFailedMsg{gen: gen, err: err}
		}
		return modDetailsFetchedMsg{gen: gen, details: details}
	}
}

// resolveModDetailsFetched swaps the enriched details into the pushed view,
// preserving the user's scroll position - the fetch adding a description
// should not yank them back to the top.
func (m Model) resolveModDetailsFetched(msg modDetailsFetchedMsg) (Model, tea.Cmd) {
	m.action.running = false
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	if m.action.draining {
		return m.resolveDrainedQuit()
	}
	m.action.status = ""
	if c, ok := m.contextContent.(*modDetailsContent); ok {
		details := msg.details
		details.Fetching = false
		details.FetchErr = ""
		c.details = details
	}
	return m, nil
}

// resolveModDetailsFailed degrades in place: the view stays open on its local
// data and names the reason where the description would have been.
func (m Model) resolveModDetailsFailed(msg modDetailsFailedMsg) (Model, tea.Cmd) {
	m.action.running = false
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	if m.action.draining {
		return m.resolveDrainedQuit()
	}
	m.action.status = msg.err.Error()
	m.action.statusIsError = true
	if c, ok := m.contextContent.(*modDetailsContent); ok {
		c.details.Fetching = false
		c.details.FetchErr = msg.err.Error()
	}
	return m, nil
}
```

- [ ] **Step 4: Wire dispatch and the `enter` binding in `app.go`**

In `Model.Update`'s message switch, beside the other gen-guarded cases:

```go
	case modDetailsFetchedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveModDetailsFetched(msg)
	case modDetailsFailedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveModDetailsFailed(msg)
```

Extend the `Select` case (`:1079-1087`):

```go
	case key.Matches(msg, m.keys.Select):
		// Select ("enter") is context-dependent: Profiles switches to the
		// selected profile, Installed Mods and Search open the selected mod's
		// details (#86), and everywhere else it opens a dashboard menu entry.
		switch m.screen {
		case ScreenProfiles:
			return m.switchSelectedProfile()
		case ScreenInstalledMods, ScreenSearch:
			return m.openSelectedModDetails()
		}
		return m.openSelectedMenuEntry()
```

Add `helpEntry(m.keys.Select)` to the Installed Mods and Search help groups in
`helpGroups()` if they do not already list it.

- [ ] **Step 5: Run to verify the tests pass**

Run: `go test ./internal/tui -run TestOpenModDetails -v`
Expected: PASS ×9.

- [ ] **Step 6: Full suite and commit**

```bash
go build ./... && go vet ./... && go test ./...
go fmt ./...
git add internal/tui/mutations.go internal/tui/app.go internal/tui/moddetails_open_test.go
git commit -m "feat(tui): enter opens mod details, enriched in the background (#86)"
```

---

### Task 8: Docs, stale-text sweep, and final verification

**Files:**

- Modify: `CHANGELOG.md`
- Modify: `README.md`, `cmd/lmm/tui.go` (`Long` help text) — as the sweep finds
- Modify: `docs/plans/2026-08-07-tui-mod-details-design.md` (accepted deviation)

- [ ] **Step 1: CHANGELOG**

Under `[Unreleased]` → `### Added`:

```markdown
- TUI: a mod details view with full `lmm mod show` parity — `enter` on a mod
  in Installed Mods or Search opens it, `esc` returns. Opens instantly from
  data already on screen and fills in description, category, and links from
  the source in the background, so it stays useful offline (#86).
```

- [ ] **Step 2: Stale-text sweep**

This repo's TUI help text has gone stale in two consecutive phases. Run:

```bash
rg -n "not yet|read-only|aren't available|use 'lmm|no details|mod show" README.md docs/*.md cmd/lmm/tui.go internal/tui/*.go
```

Fix anything claiming the TUI cannot show mod details. Check `cmd/lmm/tui.go`'s
`Long` string specifically — it is the file that went stale both times.

- [ ] **Step 3: Record the accepted deviation in the design doc**

Append to the design's "Ratified decisions" the profile-resolution ordering
note from this plan's "Accepted deviation" section, so the design and the
shipped behavior agree.

- [ ] **Step 4: Full verification**

```bash
go build ./... && go vet ./... && go test ./... && trunk check
```

Expected: all green. Confirm the verify goldens are untouched:

```bash
git status --short cmd/lmm/testdata/verify_golden   # must print nothing
```

- [ ] **Step 5: Build the smoke binary**

```bash
go build -o lmm ./cmd/lmm
```

The user smoke-tests TUI work before merge — this is a hard gate, not a
formality. Hand them `./lmm` with a checklist: open details from Installed
Mods and from Search; confirm the nav bar does **not** jump; `esc` returns to
the right screen; arrows scroll the description and do **not** move the list
behind; a long description scrolls; an uninstalled search hit shows no
Installed block; and (if reachable) an offline/failed fetch still shows local
data.

- [ ] **Step 6: Commit**

```bash
git add CHANGELOG.md README.md cmd/lmm/tui.go
git commit -m "docs: mod details view changelog and help-text sweep (#86)"
```

---

## Self-review notes

- **Spec coverage:** decision 1 (local-first) → Tasks 5–7; decision 2 (HTML
  cleaning both interfaces) → Task 3 (CLI) + Task 5 Step 4 (TUI); decision 3
  (generalized host) → Task 4. Core extraction → Task 2, guarded by Task 1.
  All six success criteria have a task; criterion 5 (declined keys) is covered
  twice, at the host (Task 4) and in the view (Task 6).
- **Type consistency:** `ModDetails`/`InstalledDetails` (TUI) vs
  `ModDetail`/`InstalledDetail` (core) are deliberately distinct types; every
  task uses the singular form for core and the plural for the TUI.
- **Deliberate omission:** #87's changelog and CurseForge screenshots are out
  of scope per the design; no task references them.
- **Hedges resolved during review.** The first draft left seven "if X differs,
  do Y" branches — the exact failure mode this plan is meant to prevent. All
  were checked against the source and replaced with the real answer. Two were
  outright bugs:
  - `string(detail.Installed.UpdatePolicy)` — `domain.UpdatePolicy` is an
    `int`, so that compiles into a garbage rune conversion. Now
    `policyToString` (`service_core.go:630`).
  - `isQuitKey(msg, m)` — it is a **method**, `m.isQuitKey(msg)`
    (`actions.go:619`).

  The rest: `m.provider` is the field name (`app.go:81`); `screens`
  (`navigation.go:26`) sizes `isScreenDigit`; no such helper existed, so Task 4
  adds it; `KeyMap`/`DefaultKeyMap()` at `keys.go:6`/`:172` with `Up`/`Down`
  already carrying the `k`/`j` aliases; `populatedSearchPage()` at
  `search_test.go:654`; `activeMods()`/`data.SearchResults` are the prototype's
  two mod lists.
