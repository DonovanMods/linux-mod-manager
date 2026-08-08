# Batch Pre-Resolution Before install.before_all (#214) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A batch-path install whose primary file pre-resolution fails (bad `--file` ID, mixed pak+exmodz) aborts before `install.before_all` fires.

**Architecture:** Hoist the BATCH path's primary pre-resolution block (pool fetch → `selectInstallTargetFiles` → `ValidateInstallFileSelection`) from inside the `len(plan.Dependencies) > 0` branch (currently after the hook) to a new guarded block placed after the STRICT block and BEFORE `lockedInstallRefusal` — making both paths follow the same order: resolve pins → lock gate → hooks. The block is read-only (source fetch + pure selection), so success-path behavior is unchanged.

**Tech Stack:** Go, testify; existing fixtures in `internal/core/flows_variant_exclusivity_test.go` (stub MergeCompiler source, batch-path seam test) and the flows tests' hook-recording patterns.

**Spec:** `docs/plans/2026-08-04-deploy-convergence-and-batch-hook-order-design.md` Part 1. Issue: #214.

## Global Constraints

- Branch `fix/214-batch-preresolve-order` off `develop`; PR `--base develop`.
- No version bump; CHANGELOG entry under `[Unreleased]` / `### Fixed`.
- TDD; gofmt (tabs); never pipe `go test` into another command in a `&&` chain.
- Success-path behavior byte-identical: `install.before_all` still runs exactly once, after the lock gate, before any mod installs; `primaryOverrideFiles` semantics unchanged (nil when no pins; #96/#140 "pins the primary's selection only" preserved).
- The #96/#140/#140-item-2 doc comments describing the batch path's ordering must be updated wherever they claim the pre-resolution happens after the hook (grep `pre-resolution` and the ApplyInstall doc block).

---

### Task 1: Hoist + tests

**Files:**

- Modify: `internal/core/flows.go` (ApplyInstall: current pre-resolution block at ~3731-3751 moves above `lockedInstallRefusal` at ~3686; ApplyInstall's doc comment ~3591-3625; any batch-ordering doc comments the grep finds)
- Test: `internal/core/flows_variant_exclusivity_test.go` (extend — the batch fixture from #211 already exists there)

**Interfaces:**

- Consumes: existing `resolveInstallCandidatePool`, `selectInstallTargetFiles`, `ValidateInstallFileSelection`, the batch stub fixture, and whatever hook-recording mechanism existing flows tests use (grep `before_all` in `internal/core/*_test.go`; if none records invocations, use `InstallOptions.Hooks`/`HookRunner` with a `ResolvedHooks` whose `install.before_all` script writes a sentinel file in `t.TempDir()` — mirror how hook tests elsewhere in the repo drive real hooks; report NEEDS_CONTEXT if no drivable pattern exists).
- Produces: no signature changes. `primaryOverrideFiles` is declared before the hoisted block and consumed by the batch loop exactly as today.

- [ ] **Step 1: Write the failing tests**

Two additions to `flows_variant_exclusivity_test.go`, built on the existing batch-path fixture (mod with one dependency):

```go
// TestApplyInstall_BatchPath_PreResolutionFailure_SkipsBeforeAllHook is
// #214: a failing primary pre-resolution (here: the #211 mixed-variant
// rejection) must abort BEFORE install.before_all fires. The hook writes a
// sentinel file; the sentinel must not exist after the failed install.
func TestApplyInstall_BatchPath_PreResolutionFailure_SkipsBeforeAllHook(t *testing.T) {
	// fixture: batch plan (non-empty Dependencies), opts.TargetFileIDs =
	// []string{"pak", "exmodz"}, hooks wired so install.before_all runs
	//   touch <tmpdir>/before_all_ran
	// Assert: ApplyInstall errors with "alternate forms of the same mod";
	// the sentinel file does NOT exist; no downloads occurred.
}

// Same shape for the pre-existing failure class: opts.TargetFileIDs =
// []string{"no-such-id"} -> "file ID no-such-id not found", sentinel absent.
func TestApplyInstall_BatchPath_BadFileID_SkipsBeforeAllHook(t *testing.T) { /* as above */ }

// Success-path ordering guard: batch install with valid pins succeeds AND
// the sentinel exists (hook still runs on the happy path).
func TestApplyInstall_BatchPath_Success_StillRunsBeforeAll(t *testing.T) { /* ... */ }
```

Write all three fully against the discovered hook-driving pattern.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/ -run TestApplyInstall_BatchPath -v`
Expected: the two Skips tests FAIL (sentinel exists today — hook fires first); the success test PASSES already (pin, keep as regression guard; note that in the report).

- [ ] **Step 3: Implement the hoist**

Move the `primaryOverrideFiles` pre-resolution block (the `if opts.TargetVersion != "" || len(opts.TargetFileIDs) > 0 { ... }` inside the batch branch) to a new block between the STRICT block's close and `lockedInstallRefusal`:

```go
	// #214: the BATCH path's primary pre-resolution (#96/#140 - pins the
	// primary's file selection only) runs HERE, before the lock gate and
	// install.before_all, for the same reason the STRICT path resolves
	// pins up front: a selection the user asked for that cannot be honored
	// must fail before any hook or side effect. The block is read-only
	// (candidate-pool fetch + pure selection), so a successful install is
	// byte-identical to the previous ordering.
	var primaryOverrideFiles []domain.DownloadableFile
	if len(plan.Dependencies) > 0 && (opts.TargetVersion != "" || len(opts.TargetFileIDs) > 0) {
		primary := plan.Mod
		pool, err := s.resolveInstallCandidatePool(ctx, primary.SourceID, &primary, plan.ShowArchived, opts.TargetVersion)
		if err != nil {
			return result, err
		}
		primaryOverrideFiles, err = selectInstallTargetFiles(pool, opts.TargetFileIDs)
		if err != nil {
			return result, err
		}
		if err := s.ValidateInstallFileSelection(primary.SourceID, primaryOverrideFiles); err != nil {
			return result, err
		}
	}
```

Adapt to the block's exact current contents (copy it verbatim including its comments; do not re-derive) — the batch branch then keeps only the use of `primaryOverrideFiles`. Match existing local-variable declarations (the batch branch previously declared `primary`; keep a local copy in the hoisted block as shown so `&primary` stays addressable). Update ApplyInstall's doc comment and any other stale ordering description.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -run TestApplyInstall_BatchPath -v` then the full package `go test ./internal/core/` and `gofmt -l internal/core/` (expect empty). All pre-existing batch tests (including #211's seam test) must stay green — they now fail before the hook, which none of them asserts against.

- [ ] **Step 5: CHANGELOG + commit**

Under `## [Unreleased]` / `### Fixed`:

```markdown
- Batch installs (dependencies present) now resolve and validate the primary
  mod's `--version`/`--file` pins before the `install.before_all` hook runs,
  so an unhonorable selection can no longer fire user hooks first. (#214)
```

```bash
git add internal/core/flows.go internal/core/flows_variant_exclusivity_test.go CHANGELOG.md
git commit -m "fix: resolve batch primary pins before install.before_all (#214)"
```

---

### Task 2: PR

- [ ] **Step 1: Final verification** — `go build -o lmm ./cmd/lmm`, `go vet ./...`, `go test ./...` (separately, unpiped); `trunk check` if available.
- [ ] **Step 2: Push and open PR**

```bash
git push -u origin fix/214-batch-preresolve-order
gh pr create --base develop \
  --title "fix: resolve batch primary pins before install.before_all (#214)" \
  --body "Fixes #214 (close manually after merge). <summary>. 🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

- [ ] **Step 3: Copilot triage** — `gh-await-review` in background; triage every comment; re-check after pushes.
