# Exmodz Default + Variant Exclusivity (#211) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When an Icarus mod publishes both a `pak` and an `exmodz` variant, every selection path defaults to the mergeable `exmodz`, and a selection mixing the two variants is rejected everywhere with a clear message.

**Architecture:** Source-level `IsPrimary` on the exmodz variant flips every existing default (CLI default mark + `--yes`, TUI plan, batch, profile apply, `selectDeployFiles`) with no core plumbing. A new `Service.ValidateInstallFileSelection` enforces variant exclusivity for `MergeCompiler` sources at the core install seams (backstop, per the #197 CLI-seam lesson), and the CLI chooser applies the same rule interactively with a re-prompt.

**Tech Stack:** Go, testify; existing fixtures: icarus source httptest Firestore stubs, core flows-test service builders, `promptMultiSelectionFrom`-style reader injection in cmd/lmm.

**Spec:** `docs/plans/2026-08-03-manifest-aware-deploy-and-exmodz-default-design.md` Part 2. Issue: #211. Predecessor #210 is merged (`c6dac33`).

## Global Constraints

- Branch `feat/211-exmodz-default` off `develop`; PR `--base develop`.
- No version bump; CHANGELOG entry under `[Unreleased]` (feature = MINOR-class for the release batch).
- TDD: every behavior change lands with its failing test written and run first.
- gofmt (tabs); error wrapping with `%w`; never pipe `go test` into another command in a `&&` chain.
- Rejection message, exact string everywhere: `pak and exmodz are alternate forms of the same mod - select one`.
- Escape hatch preserved: `--file pak` (alone) and explicit chooser selection of the pak keep working.
- Out of scope (documented, not implemented): already-installed mods keep their stored FileIDs on update/redeploy (`selectVersionedDeployFiles` untouched); mods installed pre-#211 with both variants are not migrated.

---

### Task 1: Icarus source — exmodz primary + descriptions

**Files:**

- Modify: `internal/source/icarus/icarus.go` (`GetModFiles`, ~lines 150-176)
- Test: `internal/source/icarus/icarus_test.go` (extend the existing `TestIcarus_GetModFiles_ReturnsExmodzAndPak` suite at ~line 135; mirror its httptest Firestore stub setup)

**Interfaces:**

- Consumes: nothing new.
- Produces: `GetModFiles` behavior later tasks rely on — with both variants present, the `exmodz` entry has `IsPrimary: true`; `Description` is `"mergeable EXMOD - recommended"` for exmodz and `"prebuilt PAK"` for pak (set on every returned file, single-variant included).

- [ ] **Step 1: Write the failing tests**

Extend the icarus test file, mirroring the existing stub-server pattern (the suite already builds a Firestore doc with a `files` map — reuse its helper/shape verbatim):

```go
func TestIcarus_GetModFiles_BothVariants_ExmodzIsPrimary(t *testing.T) {
	// Stub doc with files: {"pak": "<url>/Mod_P.pak", "exmodz": "<url>/Mod.exmodz"}
	// (construct exactly like TestIcarus_GetModFiles_ReturnsExmodzAndPak).
	files := getModFilesFromStub(t /* ... same fixture plumbing ... */)
	require.Len(t, files, 2)
	byID := map[string]domain.DownloadableFile{}
	for _, f := range files {
		byID[f.ID] = f
	}
	assert.True(t, byID["exmodz"].IsPrimary, "exmodz must be the default when both variants exist")
	assert.False(t, byID["pak"].IsPrimary)
	assert.Equal(t, "mergeable EXMOD - recommended", byID["exmodz"].Description)
	assert.Equal(t, "prebuilt PAK", byID["pak"].Description)
}

func TestIcarus_GetModFiles_SingleVariant_StaysPrimary(t *testing.T) {
	// pak-only doc: pak entry IsPrimary (existing single-file rule unchanged)
	// and Description "prebuilt PAK". Then exmodz-only doc: exmodz IsPrimary,
	// Description "mergeable EXMOD - recommended".
}
```

Write the second test fully (two sub-cases or two stub docs) following the file's conventions — the assertions above are the contract. If the existing suite has no reusable stub helper, inline the httptest server exactly as the neighboring test does.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/source/icarus/ -run TestIcarus_GetModFiles -v`
Expected: the new both-variants test FAILS (no primary today with two files, empty descriptions); existing tests still pass.

- [ ] **Step 3: Implement**

In `GetModFiles`, set `Description` inside the loop and mark exmodz primary when both exist:

```go
	for _, kind := range []string{"pak", "exmodz"} {
		rawURL, ok := filesField[kind].(string)
		if !ok || rawURL == "" {
			continue
		}
		description := "prebuilt PAK"
		if kind == "exmodz" {
			description = "mergeable EXMOD - recommended"
		}
		out = append(out, domain.DownloadableFile{
			ID:          kind,
			Name:        kind,
			FileName:    fileNameFromURL(rawURL, kind),
			Category:    strings.ToUpper(kind),
			Description: description,
		})
	}
	if len(out) == 1 {
		out[0].IsPrimary = true
	} else {
		// #211: with both variants published, the mergeable exmodz is the
		// default everywhere IsPrimary is honored (CLI default mark, --yes,
		// TUI plan, batch, profile apply, selectDeployFiles) - the prebuilt
		// pak stays explicitly selectable.
		for i := range out {
			if out[i].ID == "exmodz" {
				out[i].IsPrimary = true
			}
		}
	}
```

Update the function's doc comment ("A single file is marked primary...") to describe the new rule.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/source/icarus/ -v`
Expected: PASS (whole package).

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/icarus.go internal/source/icarus/icarus_test.go
git commit -m "feat: icarus prefers exmodz variant as primary, labels chooser rows (#211)"
```

---

### Task 2: Core variant-exclusivity validation

**Files:**

- Modify: `internal/core/service.go` (new method near `GetSource`, ~line 107)
- Modify: `internal/core/flows.go` (`resolveStrictInstallFiles` ~line 3576; ApplyInstall strict path's final plan.Files; the batch path's TargetFileIDs pre-resolution site)
- Test: `internal/core/flows_variant_exclusivity_test.go` (new)

**Interfaces:**

- Consumes: `source.MergeCompiler` (internal/source/source.go:146), `isExmodzFile(fileName string) bool` (internal/core/service.go, unexported), `s.registry.Get`.
- Produces: `func (s *Service) ValidateInstallFileSelection(sourceID string, files []domain.DownloadableFile) error` — nil for <2 files, unknown source, or non-MergeCompiler source; the exact rejection error otherwise when the selection mixes an exmodz file with any other file. Task 3's CLI closure wraps this method.

- [ ] **Step 1: Write the failing tests**

New file `internal/core/flows_variant_exclusivity_test.go`. For the unit tests, package `core_test` with an in-registry stub source implementing `MergeCompiler`: FIRST look for an existing test stub in `internal/core` that already implements `source.MergeCompiler` (the #197 merged-pak suites have one — grep `MergeCompiler` in `internal/core/*_test.go`) and reuse it via the same registration pattern those tests use. If none is reusable from `core_test`, define a minimal local stub satisfying `source.ModSource` + `source.MergeCompiler` the way the flows tests define theirs. Tests:

```go
// Unit: the rule itself.
func TestValidateInstallFileSelection(t *testing.T) {
	// service with two registered sources: one MergeCompiler ("mc"),
	// one plain ("plain") - mirror the flows-test service builder.
	pakFile := domain.DownloadableFile{ID: "pak", FileName: "Mod_P.pak"}
	exmodzFile := domain.DownloadableFile{ID: "exmodz", FileName: "Mod.exmodz"}

	cases := []struct {
		name     string
		sourceID string
		files    []domain.DownloadableFile
		wantErr  bool
	}{
		{"mixed on merge-compiler source rejected", "mc", []domain.DownloadableFile{pakFile, exmodzFile}, true},
		{"exmodz alone allowed", "mc", []domain.DownloadableFile{exmodzFile}, false},
		{"pak alone allowed (escape hatch)", "mc", []domain.DownloadableFile{pakFile}, false},
		{"two non-exmodz files allowed", "mc", []domain.DownloadableFile{pakFile, {ID: "extra", FileName: "readme.pak"}}, false},
		{"mixed on plain source allowed", "plain", []domain.DownloadableFile{pakFile, exmodzFile}, false},
		{"unknown source is not this check's problem", "ghost", []domain.DownloadableFile{pakFile, exmodzFile}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.ValidateInstallFileSelection(tc.sourceID, tc.files)
			if tc.wantErr {
				require.ErrorContains(t, err, "alternate forms of the same mod")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// Seam: explicit --file pins through ApplyInstall's strict resolution.
// Mirror an existing ApplyInstall strict-path test's fixture (plan built by
// PlanInstall against the stub MergeCompiler source whose GetModFiles
// returns both variants), then:
//   opts.TargetFileIDs = []string{"pak", "exmodz"}
// ApplyInstall must fail with the rejection message BEFORE any download
// side effect (assert no cache entry was created).

// Seam: caller-supplied mixed plan.Files (the CLI interactive override
// shape): build the plan, overwrite plan.Files with both variants, no
// TargetFileIDs. ApplyInstall must fail with the same message.
```

Write the two seam tests as real code against the discovered fixtures (the file's other ApplyInstall tests show the service/plan construction — mirror the closest one; report NEEDS_CONTEXT if no ApplyInstall test drives a stubbed source end-to-end).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/ -run 'TestValidateInstallFileSelection|TestApplyInstall.*Variant' -v`
Expected: unit test FAILS (method undefined — compile error first); after stubbing the method to `return nil`, seam tests FAIL (mixed selections currently install).

- [ ] **Step 3: Implement**

Method in service.go (near GetSource):

```go
// ValidateInstallFileSelection rejects an install selection that mixes a
// merge-compile source's exmodz variant with any other file (#211): the two
// are alternate forms of the same mod, and installing both double-applies
// its table edits (the pak deploys standalone while the exmodz joins the
// merged pak). Sources that don't implement source.MergeCompiler are never
// restricted, single-file selections are always fine, and an unknown
// sourceID is not this check's problem - pool resolution errors on it
// first.
func (s *Service) ValidateInstallFileSelection(sourceID string, files []domain.DownloadableFile) error {
	if len(files) < 2 {
		return nil
	}
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil
	}
	if _, ok := src.(source.MergeCompiler); !ok {
		return nil
	}
	var exmodz, other bool
	for _, f := range files {
		if isExmodzFile(f.FileName) {
			exmodz = true
		} else {
			other = true
		}
	}
	if exmodz && other {
		return fmt.Errorf("pak and exmodz are alternate forms of the same mod - select one")
	}
	return nil
}
```

Wire three seams (grep `selectInstallTargetFiles(` for the exact call sites):

1. `resolveStrictInstallFiles`: after `selectInstallTargetFiles` returns a selection, `if err := s.ValidateInstallFileSelection(plan.SourceID, selection); err != nil { return nil, err }`.
2. ApplyInstall strict path: after the strict resolution folds pins into plan.Files (i.e. the final selection, covering caller-supplied interactive overrides where resolveStrictInstallFiles returned (nil, nil)), validate `plan.Files` the same way before the lock gate / any side effect.
3. The batch path's up-front TargetFileIDs pre-resolution for the primary (ApplyInstall doc comment: "#96/#140, which pins the primary's file selection only") — validate its resolved selection identically.

If sites 1 and 2 turn out to be the same code point (strict path always passes through one place), one call there is correct — say so in the report rather than adding a redundant second call.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -v`
Expected: new tests PASS; full package green (default selections are single-file, so nothing else trips the rule).

- [ ] **Step 5: Commit**

```bash
git add internal/core/service.go internal/core/flows.go internal/core/flows_variant_exclusivity_test.go
git commit -m "feat: reject mixed pak+exmodz install selections at core seams (#211)"
```

---

### Task 3: CLI chooser — re-prompt on mixed selection, hard error on --file

**Files:**

- Modify: `cmd/lmm/install.go` (`selectInstallFiles` ~line 104, its caller where `plan.Files` is overridden ~line 520-533)
- Test: `cmd/lmm/install_test.go` (or the file where cmd/lmm's selection tests live — find `parseRangeSelection`/`promptMultiSelectionFrom` tests and colocate)

**Interfaces:**

- Consumes: `Service.ValidateInstallFileSelection` (Task 2).
- Produces: `selectInstallFiles(files []domain.DownloadableFile, validate func([]domain.DownloadableFile) error) ([]*domain.DownloadableFile, error)` — signature gains the validate parameter (nil = no validation, preserving other callers if any; grep for callers and update them all). Interactive path re-prompts on a validation error; `--file` path returns it.

- [ ] **Step 1: Write the failing tests**

`selectInstallFiles` reads the prompt via `promptMultiSelection` (os.Stdin). Refactor for testability the way the file already does it: extract `selectInstallFilesFrom(r io.Reader, files []domain.DownloadableFile, validate func([]domain.DownloadableFile) error)` with `selectInstallFiles` delegating with `os.Stdin` (mirror `promptMultiSelection`/`promptMultiSelectionFrom`). Tests:

```go
func TestSelectInstallFiles_MixedSelectionReprompts(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "pak", FileName: "Mod_P.pak", Category: "PAK"},
		{ID: "exmodz", FileName: "Mod.exmodz", Category: "EXMODZ", IsPrimary: true},
	}
	validate := func(sel []domain.DownloadableFile) error {
		// same shape the real closure has - reject mixed
		var ex, other bool
		for _, f := range sel {
			if strings.HasSuffix(strings.ToLower(f.FileName), ".exmodz") { ex = true } else { other = true }
		}
		if ex && other { return fmt.Errorf("pak and exmodz are alternate forms of the same mod - select one") }
		return nil
	}
	// First input line picks both (rejected, re-prompted); second picks 2.
	in := strings.NewReader("1,2\n2\n")
	selected, err := selectInstallFilesFrom(in, files, validate)
	require.NoError(t, err)
	require.Len(t, selected, 1)
	assert.Equal(t, "exmodz", selected[0].ID)
}

func TestSelectInstallFiles_FileFlagMixedRejected(t *testing.T) {
	// installFileID global set to "pak,exmodz" (save/restore the global the
	// way neighboring tests handle flag globals); validate as above.
	// Expect the error returned (no re-prompt on explicit flags).
}
```

Write the second test fully, handling the `installFileID` package global with save/defer-restore.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/lmm/ -run TestSelectInstallFiles -v`
Expected: FAIL — `selectInstallFilesFrom` undefined.

- [ ] **Step 3: Implement**

- Extract `selectInstallFilesFrom(r io.Reader, files, validate)`; keep all existing logic. Apply `validate` (when non-nil):
  - `--file` path: validate the resolved selection; return the error as-is.
  - single-file fast path and `--yes` default: single file — validation is a no-op but call it anyway (uniform).
  - interactive loop: after `promptMultiSelectionFrom` returns and the selection is materialized, validate; on error `fmt.Printf("Invalid selection: %v\n", err)` and loop back to the prompt (restructure so the prompt+materialize+validate sequence loops — mirror `promptMultiSelectionFrom`'s own retry style).
- At the caller (where `plan.Files` is overridden, ~install.go:520-533): build the closure

  ```go
  validate := func(sel []domain.DownloadableFile) error {
      return service.ValidateInstallFileSelection(plan.SourceID, sel)
  }
  ```

  (adjust to the caller's actual variable names for the service and plan) and pass it. Grep for any other `selectInstallFiles(` callers and pass `nil` or the equivalent closure as appropriate — every caller must compile and be listed in your report.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/lmm/ -v`
Expected: PASS (whole package).

- [ ] **Step 5: Commit**

```bash
git add cmd/lmm/install.go cmd/lmm/install_test.go
git commit -m "feat: CLI chooser enforces variant exclusivity with re-prompt (#211)"
```

---

### Task 4: Default-selection acceptance test + CHANGELOG + docs

**Files:**

- Test: `internal/core/flows_variant_exclusivity_test.go` (extend from Task 2)
- Modify: `CHANGELOG.md` (`[Unreleased]`), `docs/configuration.md` (~line 62, the `compile` bullet)

**Interfaces:**

- Consumes: Task 1's IsPrimary behavior through `PlanInstall`'s `selectDeployFiles` default (`internal/core/flows.go:2940-2962` region).
- Produces: acceptance evidence for the CLI/TUI parity claim (TUI installs `plan.Files` verbatim — `internal/tui/service_core.go:1291`'s documented contract — so a core `PlanInstall` assertion covers both interfaces).

- [ ] **Step 1: Write the acceptance test**

Using the same stub MergeCompiler source as Task 2 (GetModFiles returns both variants with exmodz `IsPrimary` — i.e. the stub mirrors Task 1's real behavior):

```go
// TestPlanInstall_BothVariants_DefaultsToExmodz is #211's parity acceptance:
// PlanInstall's non-interactive default (what the TUI, --yes, and every
// batch path install) must pick the exmodz variant when both exist. The TUI
// has no file chooser and installs plan.Files exactly as planned
// (internal/tui/service_core.go), so this single core assertion covers both
// interfaces.
func TestPlanInstall_BothVariants_DefaultsToExmodz(t *testing.T) {
	// fixture per Task 2's seam tests
	plan, err := svc.PlanInstall(ctx /* ... */)
	require.NoError(t, err)
	require.Len(t, plan.Files, 1)
	assert.Equal(t, "exmodz", plan.Files[0].ID)
}
```

Fill in the fixture from the file's existing tests. Expected: PASSES immediately if the stub marks exmodz primary (that is the point — IsPrimary flows through with zero core changes); to prove the assertion bites, temporarily flip the stub's primary to "pak" and confirm the test fails, then restore (note this RED check in your report).

- [ ] **Step 2: CHANGELOG**

Under `## [Unreleased]`, `### Added` (create the heading if absent, Added→Changed→Fixed order):

```markdown
### Added

- Icarus mods that publish both a prebuilt pak and a mergeable `.exmodz`
  now install the `.exmodz` by default everywhere (TUI, `--yes`, batch and
  profile installs, and the CLI chooser's default), with descriptive
  chooser labels. Selecting both variants together is rejected — they are
  alternate forms of the same mod; `--file pak` remains the escape hatch
  for installing the prebuilt pak alone. (#211)
```

- [ ] **Step 3: docs/configuration.md**

In the `compile` deploy-mode bullet (~line 62), append one sentence:

```markdown
When a mod publishes both a prebuilt pak and an `.exmodz`, lmm installs the `.exmodz` by default; the pak remains selectable explicitly (CLI chooser or `--file pak`), and installing both together is rejected as they are alternate forms of the same mod.
```

- [ ] **Step 4: Full verification + commit**

Run: `go test ./...` (once, unpiped), `go vet ./...`, `gofmt -l cmd/ internal/` (expect empty).

```bash
git add internal/core/flows_variant_exclusivity_test.go CHANGELOG.md docs/configuration.md
git commit -m "test: exmodz-default parity acceptance + changelog/docs (#211)"
```

---

### Task 5: PR

- [ ] **Step 1: Final verification** — `go build -o lmm ./cmd/lmm`, `go vet ./...`, then `go test ./...` (separately); `trunk check` if available.
- [ ] **Step 2: Push and open PR**

```bash
git push -u origin feat/211-exmodz-default
gh pr create --base develop \
  --title "feat: default to exmodz variant, reject mixed pak+exmodz installs (#211)" \
  --body "Fixes #211 (close manually after merge). <summary>. 🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

- [ ] **Step 3: Copilot triage** — `gh-await-review` in background; triage every comment; re-check after any fix push.

## Post-merge verification (user's machine)

Reinstall or newly install a both-variants Icarus mod (e.g. Increase Stacks publishes exmodz-only, laanp pak-only — a both-variants mod like the user's own Larger Resource Stacks is the target): `lmm -g icarus install <mod>` with no flags must pick the exmodz (cache gains `.lmm-source-exmodz`, no standalone pak deploy), and `lmm -g icarus install <mod> --file pak,exmodz` must be rejected with the exact message.
