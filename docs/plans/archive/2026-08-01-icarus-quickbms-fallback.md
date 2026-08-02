# QuickBMS Auto-Extraction Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When the hosted community base-table dump is stale (or absent), compile Icarus mods anyway by extracting the installed `data.pak` with QuickBMS — the one always-week-correct source of base truth — automatically, announced, and behind the same validation gate as every other source.

**Architecture:** A new `internal/source/icarus/quickbms.go` owns detection (`quickbms_path` config → `exec.LookPath`), invocation (`exec.CommandContext` with a timeout, driving an embedded UE4 `.bms` script), normalization of QuickBMS's output into the dump-tree shape `loadLocalDump` already consumes, and a per-build extraction cache under the service data dir. `DumpStore.DumpForBuild` becomes an explicit four-step chain — `data_dump_path` → per-build extraction cache → hosted dump → QuickBMS auto-run — with every step passing the existing `validateDump` byte-compare gate and a single exhaustive error when all fail. `Compile`'s call flow is untouched; the two new per-game settings reach it through a `source.CompileRequest` struct that replaces the `Compiler` interface's positional path arguments.

**Tech Stack:** Go 1.25.6 (this repo's version), stdlib only for lmm code (`os/exec`, `embed`, `context`, `archive/tar` already in use) — no new third-party dependencies. QuickBMS itself is an external, optional, runtime-detected binary; its `.bms` script ships via `go:embed` (pending Task 1's license check).

## Global Constraints

Binding rules, copied from the approved design ([`2026-08-01-icarus-quickbms-fallback-design.md`](2026-08-01-icarus-quickbms-fallback-design.md)):

- **Stdlib only for lmm code.** `go.mod` gains nothing. `os/exec` and `embed` are stdlib; QuickBMS is an external binary, not a Go dependency.
- **Fail loud, no silent fallbacks** (repo precedent #95). Extraction errors, validation failures, and missing tools each produce specific, remedial messages. The chain-exhausted error names every attempt and why it failed. Partial extraction output is removed on failure — the same hygiene as `Compile`'s partial-pak cleanup.
- **The external binary is optional, runtime-detected, and announced — never required.** Absence of QuickBMS is a normal chain-miss, not an error. This is lmm's first sanctioned external-binary invocation; it must not become a hard dependency of building, testing, or running lmm.
- **`auto_extract` defaults to `true`.** Setting it `false` opts out of the auto-run leg entirely. There is no interactive prompt.
- **Every source passes the same `validateDump` gate** (byte-compare of the 40 stored tables against the installed pak). QuickBMS output is week-correct by construction; the gate proves the extraction wasn't mangled. Never silently fall back _across weeks_ — a stale-but-validating source is impossible by construction of the gate.
- **No CLI or TUI surface.** Both new settings are `games.yaml`-only; the feature is pipeline-internal and announced through the existing logging/writer pattern. CLI/TUI parity holds trivially — shared core path, no new capability to surface in either.
- **Epic-branch workflow.** This is a story on `epic/icarus-136`: branch from it, PR back into it with `--base epic/icarus-136`, and reference [#174](https://github.com/DonovanMods/linux-mod-manager/issues/174) in commits and the PR. The epic merges to `develop` as one PR when complete. No version bump in this story.
- **Out of scope** (from the design, restated so it is not rediscovered mid-implementation): extraction for any other game; any non-QuickBMS extractor; dump self-hosting; prompting UIs; making QuickBMS a required dependency.

### `SPIKE-CONFIRM:` markers

Task 1 pins facts that Tasks 2–7 encode as best assumptions. Every such site carries a single-line `SPIKE-CONFIRM:` comment. After Task 1, `grep -rn 'SPIKE-CONFIRM' docs/plans/2026-08-01-icarus-quickbms-fallback.md internal/` finds all of them for the revision pass. Do not invent a second tag spelling.

---

## Task 1: Empirical spike — build QuickBMS, extract the real `data.pak`, verify Oodle

**This is the gate. No product code in this task.** If Linux QuickBMS cannot decompress Icarus's Oodle-compressed tables, **STOP**: the design's premise is falsified and the feature must be rethought before any of Tasks 2–7 begin.

**Files:**

- Create: `docs/plans/icarus-quickbms-spike-findings.md` (scratch findings doc, gitignored alongside the other `docs/plans/*` in-flight docs — do not `git add`)

**Interfaces:**

- Produces (consumed by Tasks 2–4 as the `SPIKE-CONFIRM:` answers): the exact QuickBMS invocation and flag order, the `.bms` script's canonical URL + filename + license verdict, QuickBMS's output directory layout, extraction wall-clock runtime, and the recommended permanent install route.

- [ ] **Step 1: Check for a packaged QuickBMS (do NOT install, do NOT sudo)**

Record availability only — the spike builds user-locally regardless, so nothing here needs root.

```bash
# Arch/CachyOS: is it in the AUR?
curl -s 'https://aur.archlinux.org/rpc/v5/search/quickbms' | python3 -m json.tool | head -40
# Is anything already on PATH?
command -v quickbms || echo "quickbms not on PATH (expected: not yet installed)"
```

Record: AUR package name(s) if any, their version and last-updated date, and whether a binary was already present.

- [ ] **Step 2: Build QuickBMS user-locally (no root)**

QuickBMS is Luigi Auriemma's tool; the Linux build is a plain `make`. Build under the scratch dir, never into `/usr`.

```bash
WORK="$HOME/.local/src/quickbms"
mkdir -p "$WORK" && cd "$WORK"
curl -sL -o quickbms.zip https://aluigi.altervista.org/papers/quickbms.zip
unzip -o -q quickbms.zip
# Build dependencies are vendored in the tarball; the Makefile targets Linux directly.
make 2>&1 | tail -20
ls -la quickbms
./quickbms 2>&1 | head -5   # prints version banner + usage
```

Record: the exact commands that worked, any build errors and their fixes, the resulting binary path, and the version banner. If `make` fails for missing system headers, record exactly which — a build that needs root-installed `-dev` packages is a material finding for the "optional dependency" story.

Then install it where the config will point at it, still without root:

```bash
mkdir -p "$HOME/.local/bin" && cp "$WORK/quickbms" "$HOME/.local/bin/quickbms"
command -v quickbms   # ~/.local/bin is already on this machine's PATH per the dotfiles
```

- [ ] **Step 3: Obtain the ecosystem UE4 `.bms` script and check its license**

The dump repo `GODOFMINECRAFT4/IcarusData` — the source Part 3 of the findings doc identified — commits its extraction toolchain alongside the tables, including the `.bms` script it drives. That is the ecosystem-proven script for this exact pak.

```bash
cd "$WORK"
curl -sL -o unreal_pak.bms \
  "https://raw.githubusercontent.com/GODOFMINECRAFT4/IcarusData/master/unreal_pak.bms"
wc -l unreal_pak.bms && head -40 unreal_pak.bms
# The canonical upstream (aluigi's script index) for provenance + license comparison:
curl -sL -o unreal_pak.upstream.bms "https://aluigi.altervista.org/bms/unreal_tournament_4.bms"
diff -u unreal_pak.upstream.bms unreal_pak.bms | head -40 || true
```

**License check (this decides the embedding strategy):** read the script's header comment block and the upstream page for a license statement. Record the verdict explicitly as one of:

- **EMBED OK** — the script carries a permissive/public-domain grant, or none plus an explicit "free to use" statement. Tasks 2–3 keep `go:embed`.
- **MURKY** — no grant, or terms that restrict redistribution. Then Tasks 2–3 switch to the design's fallback: download-on-demand from the canonical URL into the cache dir, with the URL and checksum pinned in code. Note this flips `SPIKE-CONFIRM:` sites in Task 2 Step 3 and Task 3 Step 3.

Record the exact license text found (or "none present"), the URL it came from, and the verdict.

- [ ] **Step 4: Extract the real `data.pak`**

```bash
PAK=/data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Data/data.pak
OUT="$WORK/extracted"
rm -rf "$OUT" && mkdir -p "$OUT"
time quickbms "$WORK/unreal_pak.bms" "$PAK" "$OUT" 2>&1 | tail -30
```

Record verbatim: the full command, its exit code, the tail of its output, and the wall-clock time.

Then map the output layout precisely — Task 3's normalization depends on it:

```bash
find "$OUT" -name '*.json' | wc -l                 # expect 298
find "$OUT" -name 'DataTableMetadata.json'          # the pak's root-level table: reveals the tree root
find "$OUT" -maxdepth 3 -type d | head -20          # is the tree nested under a mount-path prefix?
find "$OUT" -name 'D_Factions.json'
find "$OUT" -name 'D_ItemsStatic.json' -exec ls -la {} \;
```

Record: the number of `.json` files, the absolute path of `DataTableMetadata.json`, and whether the table tree sits at `$OUT` directly or beneath a prefix (the pak's mount point is the absolute cook path `C:/BA/work/92bbbfa44df12262/Temp/Data/`, so QuickBMS may recreate part of it).

- [ ] **Step 5: Verify against `unrealpak` ground truth**

Two assertions decide the gate. Run from the repo so `internal/unrealpak` is importable.

```bash
cd /home/dyoung/Projects/orca/workspaces/linux-mod-manager/icarus-136
mkdir -p /tmp/qbms-spike && cat > /tmp/qbms-spike/main.go <<'EOF'
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

func main() {
	pakPath, outDir := os.Args[1], os.Args[2]
	pak, err := unrealpak.Open(pakPath)
	if err != nil {
		panic(err)
	}
	defer pak.Close()

	var stored, storedMatch, oodle, oodleFound, oodleValidJSON int
	var mismatches []string
	for _, f := range pak.Files() {
		extracted := filepath.Join(outDir, filepath.FromSlash(f.Path))
		got, readErr := os.ReadFile(extracted)
		shipped, pakErr := pak.ReadFile(f.Path)
		switch {
		case errors.Is(pakErr, unrealpak.ErrUnsupportedFormat): // Oodle: previously unreachable
			oodle++
			if readErr != nil {
				mismatches = append(mismatches, "OODLE NOT EXTRACTED: "+f.Path)
				continue
			}
			oodleFound++
			var v any
			if json.Unmarshal(bytes.TrimPrefix(got, []byte("\xef\xbb\xbf")), &v) == nil {
				oodleValidJSON++
			} else {
				mismatches = append(mismatches, "INVALID JSON: "+f.Path)
			}
			continue
		case pakErr != nil:
			mismatches = append(mismatches, "PAK READ ERROR: "+f.Path+": "+pakErr.Error())
			continue
		}
		stored++
		if readErr != nil {
			mismatches = append(mismatches, "MISSING: "+f.Path)
			continue
		}
		// Extraction may write LF or CRLF; the pak stores CRLF. Compare in the
		// pak's own shape, exactly as toCRLF does at ingest.
		norm := strings.ReplaceAll(strings.ReplaceAll(string(got), "\r\n", "\n"), "\n", "\r\n")
		if !bytes.Equal([]byte(norm), shipped) {
			mismatches = append(mismatches, "DIFFERS: "+f.Path)
			continue
		}
		storedMatch++
	}
	fmt.Printf("stored tables:  %d  byte-identical after CRLF normalization: %d\n", stored, storedMatch)
	fmt.Printf("oodle tables:   %d  extracted: %d  valid JSON: %d\n", oodle, oodleFound, oodleValidJSON)
	for _, m := range mismatches {
		fmt.Println("  " + m)
	}
	if storedMatch == stored && oodleFound == oodle && oodleValidJSON == oodle {
		fmt.Println("GATE: PASS")
		return
	}
	fmt.Println("GATE: FAIL")
}
EOF
go run /tmp/qbms-spike/main.go \
  /data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Data/data.pak \
  "$HOME/.local/src/quickbms/extracted"
```

**Acceptance:**

- All **40** stored tables byte-identical to `unrealpak` reads (after CRLF normalization).
- All **258** Oodle tables extracted and parsing as valid JSON — including `Items/D_ItemsStatic.json` (~7.3 MB), which `unrealpak` cannot read at all.

Spot-check the headline table by hand too:

```bash
ls -la "$HOME/.local/src/quickbms/extracted"/**/D_ItemsStatic.json 2>/dev/null || \
  find "$HOME/.local/src/quickbms/extracted" -name D_ItemsStatic.json -exec ls -la {} \;
find "$HOME/.local/src/quickbms/extracted" -name D_ItemsStatic.json -exec sh -c \
  'python3 -m json.tool < "$1" | head -5' _ {} \;
```

- [ ] **Step 6: STOP-AND-REVISE GATE**

If `GATE: FAIL` — specifically if the Oodle tables did **not** decompress — **stop here**. Do not start Task 2. Write the findings doc with exactly what was observed (QuickBMS version, script, command, error output) and report the premise as falsified: the design's core claim is that Linux QuickBMS provides Oodle decompression, and without it this feature cannot exist in this shape.

If stored tables mismatch but Oodle works, that is a normalization problem, not a falsified premise: record the exact difference (line endings? BOM? trailing newline?) and carry it into Task 3's normalization as a `SPIKE-CONFIRM:`-resolved detail.

- [ ] **Step 7: Write the findings doc**

Create `docs/plans/icarus-quickbms-spike-findings.md` recording, in this order:

1. AUR/package availability (Step 1) and the **recommended permanent install route** for the user (AUR package vs. the user-local build), with the reasoning.
2. The exact build commands that worked, the binary path, and the version banner.
3. The `.bms` script: canonical URL, filename, size, and the **license verdict** (EMBED OK / MURKY) with the license text quoted.
4. The exact extraction invocation, exit code, output tail, and wall-clock runtime.
5. The output layout: where `DataTableMetadata.json` landed, whether a prefix directory wraps the tree, and the `.json` count.
6. The gate results: stored byte-identical count, Oodle extracted/valid-JSON count, and any mismatches.
7. A short "answers to `SPIKE-CONFIRM:`" section listing each marker from Tasks 2–7 and its resolved value, so the revision pass is mechanical.

- [ ] **Step 8: Commit**

The findings doc is gitignored (`docs/plans/*`), so there is nothing to commit for this task. Record completion in the task tracker instead and proceed to Task 2 only if the gate passed.

---

## Task 2: `quickbms.go` — binary detection and the embedded script

**Files:**

- Create: `internal/source/icarus/quickbms.go`
- Create: `internal/source/icarus/quickbms_test.go`
- Create: `internal/source/icarus/embedded/unreal_pak.bms` (the Task 1 script; `SPIKE-CONFIRM:` filename and whether embedding is permitted at all)

**Interfaces:**

- Consumes: nothing from this package yet — detection is self-contained.
- Produces: `func findQuickBMS(configuredPath string) (string, error)`, `var errQuickBMSNotFound = errors.New(...)`, `func writeScriptTo(dir string) (string, error)`, `const quickbmsBinaryName`. Task 3 depends on all four.

- [ ] **Step 1: Write the failing tests**

```go
package icarus

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeStubQuickBMS writes an executable stub named quickbms into dir and
// returns its path. body is the shell script body appended after the shebang;
// the caller controls exactly what the stub does with its arguments.
//
// The stub is /bin/sh, so these tests are Linux/Unix-only — which matches this
// project's target. Windows skips (see skipIfNoShell).
func writeStubQuickBMS(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "quickbms")
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub quickbms: %v", err)
	}
	return p
}

func skipIfNoShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub quickbms is a /bin/sh script; this project targets Linux")
	}
}

// prependToPATH puts dir at the FRONT of PATH for the test's duration, so the
// stub shadows any real quickbms the developer happens to have installed.
//
// It must prepend rather than replace: the stub is a /bin/sh script that calls
// mkdir, so a PATH containing only the stub's own directory would leave the
// stub unable to find its own utilities — it would exit 0 having silently
// written nothing, and the test would fail with a confusing "tables disagree"
// instead of an obvious "stub broke". Tests that need NO quickbms on PATH set
// PATH to a bare empty dir instead, which is safe because nothing executes.
func prependToPATH(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestFindQuickBMS_ConfiguredPathWins(t *testing.T) {
	skipIfNoShell(t)
	dir := t.TempDir()
	stub := writeStubQuickBMS(t, dir, "exit 0\n")

	// An empty PATH proves the configured path is used directly, not looked up.
	t.Setenv("PATH", t.TempDir())

	got, err := findQuickBMS(stub)
	if err != nil {
		t.Fatalf("findQuickBMS(%q): %v", stub, err)
	}
	if got != stub {
		t.Errorf("findQuickBMS = %q, want the configured path %q", got, stub)
	}
}

func TestFindQuickBMS_ConfiguredPathMissing_IsActionable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "quickbms")

	_, err := findQuickBMS(missing)
	if err == nil {
		t.Fatal("expected an error for a configured quickbms_path that does not exist, got nil")
	}
	// A wrong quickbms_path is user misconfiguration, not a chain-miss: it must
	// NOT report as errQuickBMSNotFound, or the chain would quietly skip it.
	if errors.Is(err, errQuickBMSNotFound) {
		t.Error("a configured-but-missing quickbms_path must be a hard error, not a chain-miss")
	}
	if !strings.Contains(err.Error(), "quickbms_path") {
		t.Errorf("error %q should name the quickbms_path setting", err)
	}
}

func TestFindQuickBMS_FallsBackToPATH(t *testing.T) {
	skipIfNoShell(t)
	dir := t.TempDir()
	stub := writeStubQuickBMS(t, dir, "exit 0\n")
	prependToPATH(t, dir)

	got, err := findQuickBMS("")
	if err != nil {
		t.Fatalf("findQuickBMS(\"\"): %v", err)
	}
	if got != stub {
		t.Errorf("findQuickBMS = %q, want the PATH-resolved stub %q", got, stub)
	}
}

func TestFindQuickBMS_AbsentFromPATH_IsChainMiss(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir: nothing to find

	_, err := findQuickBMS("")
	if !errors.Is(err, errQuickBMSNotFound) {
		t.Fatalf("findQuickBMS error = %v, want errQuickBMSNotFound (a normal chain-miss)", err)
	}
}

func TestWriteScriptTo_ProducesAUsableScriptFile(t *testing.T) {
	dir := t.TempDir()

	p, err := writeScriptTo(dir)
	if err != nil {
		t.Fatalf("writeScriptTo: %v", err)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading written script: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("written .bms script is empty; the embedded script did not make it into the binary")
	}
	if filepath.Dir(p) != dir {
		t.Errorf("script written to %q, want it inside %q", p, dir)
	}
}
```

- [ ] **Step 2: Run to verify they fail (RED)**

```bash
cd /home/dyoung/Projects/orca/workspaces/linux-mod-manager/icarus-136
go test ./internal/source/icarus/... -run 'TestFindQuickBMS|TestWriteScriptTo' -v
```

Expected: FAIL — `findQuickBMS`, `errQuickBMSNotFound`, `writeScriptTo` undefined.

- [ ] **Step 3: Implement detection + embedding in `quickbms.go`**

First place the script from Task 1:

```bash
mkdir -p internal/source/icarus/embedded
cp "$HOME/.local/src/quickbms/unreal_pak.bms" internal/source/icarus/embedded/unreal_pak.bms
```

```go
package icarus

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// quickbmsBinaryName is the executable looked up on PATH when quickbms_path is
// not configured.
//
// SPIKE-CONFIRM: Task 1 Step 2 pins the binary's installed name. QuickBMS also
// ships a "quickbms_4gb_files" variant for >4 GB inputs; Icarus's data.pak is
// 2.4 MB, so the base binary is the right default.
const quickbmsBinaryName = "quickbms"

// embeddedScriptName is the .bms script driving the extraction.
//
// SPIKE-CONFIRM: Task 1 Step 3 pins the filename AND whether embedding is
// permitted. If the license verdict is MURKY, this constant and the go:embed
// directive below are replaced by download-on-demand into the cache dir,
// pinning the canonical URL and a content checksum.
const embeddedScriptName = "unreal_pak.bms"

//go:embed embedded/unreal_pak.bms
var embeddedScripts embed.FS

// errQuickBMSNotFound reports that no QuickBMS binary is available. This is a
// normal chain-miss — the tool is optional (see Global Constraints) — so the
// base-table chain records it as one attempt among several rather than
// aborting. A configured-but-wrong quickbms_path is deliberately NOT this
// error: that is user misconfiguration and must be loud.
var errQuickBMSNotFound = errors.New("quickbms not found")

// findQuickBMS resolves the QuickBMS executable. A non-empty configuredPath
// (the game's quickbms_path) is used verbatim and must exist; otherwise PATH
// is searched.
func findQuickBMS(configuredPath string) (string, error) {
	if configuredPath != "" {
		info, err := os.Stat(configuredPath)
		if err != nil {
			return "", fmt.Errorf("icarus: the configured quickbms_path %s is not usable: %w", configuredPath, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("icarus: the configured quickbms_path %s is a directory, not an executable", configuredPath)
		}
		return configuredPath, nil
	}
	found, err := exec.LookPath(quickbmsBinaryName)
	if err != nil {
		return "", fmt.Errorf("%w on PATH: %v", errQuickBMSNotFound, err)
	}
	return found, nil
}

// writeScriptTo materializes the embedded .bms script inside dir and returns
// its path. QuickBMS takes a script file path, so the embedded bytes need a
// real file; callers pass a temp dir they own and clean up.
func writeScriptTo(dir string) (string, error) {
	body, err := embeddedScripts.ReadFile("embedded/" + embeddedScriptName)
	if err != nil {
		return "", fmt.Errorf("icarus: reading embedded %s: %w", embeddedScriptName, err)
	}
	p := filepath.Join(dir, embeddedScriptName)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		return "", fmt.Errorf("icarus: writing %s: %w", p, err)
	}
	return p, nil
}
```

- [ ] **Step 4: Run tests to verify they pass (GREEN)**

```bash
go test ./internal/source/icarus/... -run 'TestFindQuickBMS|TestWriteScriptTo' -v
```

Expected: PASS for all five.

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/quickbms.go internal/source/icarus/quickbms_test.go internal/source/icarus/embedded/
git commit -m "feat: detect QuickBMS and embed the UE4 extraction script (#174)"
```

---

## Task 3: `quickbms.go` — invocation, normalization, and the per-build extraction cache

**Files:**

- Modify: `internal/source/icarus/quickbms.go`
- Modify: `internal/source/icarus/quickbms_test.go`

**Interfaces:**

- Consumes: `findQuickBMS`, `writeScriptTo` (Task 2); `loadLocalDump`, `Build` (existing `datadump.go`).
- Produces: `func extractedCacheDir(dataDir string, b Build) string`, `func runQuickBMS(ctx context.Context, exe, scriptPath, pakPath, rawDir string) error`, `func normalizeExtraction(rawDir, destDir string) error`, `func findTreeRoot(rawDir string) (string, error)`, `func extractWithQuickBMS(ctx context.Context, exe, pakPath, cacheDir string) (*Dump, error)`, `const quickbmsTimeout`, `const rootMarkerTable`. Task 4 depends on `extractedCacheDir` and `extractWithQuickBMS`; Tasks 4 and 6 use `rootMarkerTable` in their stub fixtures.

- [ ] **Step 1: Write the failing tests**

Append to `quickbms_test.go`:

```go
// stubEmitting builds a stub-quickbms body that recreates files under the
// output directory ($3). Each entry is written with printf so the exact bytes
// (including CRLF) are reproducible from a POSIX shell.
func stubEmitting(files map[string]string) string {
	body := "out=\"$3\"\n"
	// Deterministic order keeps the generated script stable across runs.
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		esc := strings.ReplaceAll(files[name], "\\", "\\\\")
		esc = strings.ReplaceAll(esc, "%", "%%")
		esc = strings.ReplaceAll(esc, "\r", "\\r")
		esc = strings.ReplaceAll(esc, "\n", "\\n")
		esc = strings.ReplaceAll(esc, "\"", "\\\"")
		body += fmt.Sprintf("mkdir -p \"$out/%s\"\n", filepath.Dir(name))
		body += fmt.Sprintf("printf \"%s\" > \"$out/%s\"\n", esc, name)
	}
	body += "exit 0\n"
	return body
}

func TestExtractedCacheDir_IsPerBuild(t *testing.T) {
	b := Build{Major: 3, Minor: 0, Patch: 21, Changelist: 155335}
	got := extractedCacheDir("/data", b)
	want := filepath.Join("/data", "icarus", "extracted", "3.0.21.155335")
	if got != want {
		t.Errorf("extractedCacheDir = %q, want %q", got, want)
	}
}

func TestExtractWithQuickBMS_ProducesADumpAndPopulatesTheCache(t *testing.T) {
	skipIfNoShell(t)
	const rel = "Factions/D_Factions.json"
	const body = "{\r\n    \"Rows\": []\r\n}"
	binDir := t.TempDir()
	stub := writeStubQuickBMS(t, binDir, stubEmitting(map[string]string{
		rel:                      body,
		"DataTableMetadata.json": "{}",
	}))
	cacheDir := filepath.Join(t.TempDir(), "extracted", "3.0.21.155335")

	dump, err := extractWithQuickBMS(context.Background(), stub, "/nonexistent/data.pak", cacheDir)
	if err != nil {
		t.Fatalf("extractWithQuickBMS: %v", err)
	}
	got, ok := dump.Table(rel)
	if !ok {
		t.Fatalf("dump has no table %q", rel)
	}
	if string(got) != body {
		t.Errorf("table bytes = %q, want %q", got, body)
	}
	// The cache directory must be left populated for the next run to reuse.
	if _, err := os.Stat(filepath.Join(cacheDir, filepath.FromSlash(rel))); err != nil {
		t.Errorf("cache dir was not populated: %v", err)
	}
}

// QuickBMS may recreate part of the pak's mount path above the table tree.
// Normalization must find the real root, identified by the pak's known
// root-level table.
func TestExtractWithQuickBMS_StripsAMountPathPrefix(t *testing.T) {
	skipIfNoShell(t)
	const rel = "Factions/D_Factions.json"
	binDir := t.TempDir()
	const prefix = "C/BA/work/Temp/Data/"
	stub := writeStubQuickBMS(t, binDir, stubEmitting(map[string]string{
		prefix + rel:             "{\r\n}",
		prefix + rootMarkerTable: "{}",
	}))
	cacheDir := filepath.Join(t.TempDir(), "extracted", "b")

	dump, err := extractWithQuickBMS(context.Background(), stub, "/nonexistent/data.pak", cacheDir)
	if err != nil {
		t.Fatalf("extractWithQuickBMS: %v", err)
	}
	if _, ok := dump.Table(rel); !ok {
		t.Fatalf("table %q not found after prefix stripping; tables = %v", rel, dumpKeys(dump))
	}
}

func TestExtractWithQuickBMS_NonZeroExit_IsActionable(t *testing.T) {
	skipIfNoShell(t)
	binDir := t.TempDir()
	stub := writeStubQuickBMS(t, binDir, "echo 'oodle plugin missing' >&2\nexit 3\n")
	cacheDir := filepath.Join(t.TempDir(), "extracted", "b")

	_, err := extractWithQuickBMS(context.Background(), stub, "/nonexistent/data.pak", cacheDir)
	if err == nil {
		t.Fatal("expected an error when quickbms exits non-zero, got nil")
	}
	if !strings.Contains(err.Error(), "oodle plugin missing") {
		t.Errorf("error %q should carry the tool's own output", err)
	}
	// Partial output must not survive as a poisoned cache.
	if _, statErr := os.Stat(cacheDir); statErr == nil {
		t.Error("cache dir must not exist after a failed extraction")
	}
}

func TestExtractWithQuickBMS_EmptyOutput_IsActionable(t *testing.T) {
	skipIfNoShell(t)
	binDir := t.TempDir()
	stub := writeStubQuickBMS(t, binDir, "exit 0\n") // succeeds, writes nothing
	cacheDir := filepath.Join(t.TempDir(), "extracted", "b")

	_, err := extractWithQuickBMS(context.Background(), stub, "/nonexistent/data.pak", cacheDir)
	if err == nil {
		t.Fatal("expected an error when quickbms produces no tables, got nil")
	}
	if _, statErr := os.Stat(cacheDir); statErr == nil {
		t.Error("cache dir must not exist after an empty extraction")
	}
}

func TestExtractWithQuickBMS_Timeout(t *testing.T) {
	skipIfNoShell(t)
	binDir := t.TempDir()
	stub := writeStubQuickBMS(t, binDir, "sleep 5\n")
	cacheDir := filepath.Join(t.TempDir(), "extracted", "b")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := extractWithQuickBMS(ctx, stub, "/nonexistent/data.pak", cacheDir)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("extraction took %v; the context deadline did not kill the process", elapsed)
	}
	if _, statErr := os.Stat(cacheDir); statErr == nil {
		t.Error("cache dir must not exist after a timed-out extraction")
	}
}

// dumpKeys lists a dump's table paths, for failure messages.
func dumpKeys(d *Dump) []string {
	out := make([]string, 0, len(d.tables))
	for k := range d.tables {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
```

Add to `quickbms_test.go`'s imports: `"context"`, `"fmt"`, `"sort"`, `"time"`.

- [ ] **Step 2: Run to verify they fail (RED)**

```bash
go test ./internal/source/icarus/... -run 'TestExtract|TestExtractedCacheDir' -v
```

Expected: FAIL — `extractedCacheDir`, `extractWithQuickBMS`, `rootMarkerTable` undefined.

- [ ] **Step 3: Implement invocation, normalization, and caching**

Append to `quickbms.go` (and extend its import block with `"context"`, `"io/fs"`, `"strings"`, `"time"`):

```go
// quickbmsTimeout bounds a single extraction. The real data.pak is 2.4 MB and
// extracts in seconds; this ceiling exists to kill a wedged process, not to
// pace a slow one.
//
// SPIKE-CONFIRM: Task 1 Step 4 records the real wall-clock runtime. If it is
// anywhere near this bound, raise it — a timeout that can fire on a healthy
// machine would turn a working feature into a flaky one.
const quickbmsTimeout = 10 * time.Minute

// quickbmsWaitDelay bounds how long Wait blocks for output pipes *after* the
// context has already killed the process. Without it, a killed child that left
// a grandchild holding the inherited stdout/stderr pipe would hang
// CombinedOutput until that grandchild exits — the timeout would fire, the
// process would die, and the call would still block. Two seconds is generous
// for draining a pipe and only ever applies on the already-failing path.
const quickbmsWaitDelay = 2 * time.Second

// rootMarkerTable is a table the pak stores at its own root. QuickBMS may
// recreate part of the pak's mount point (an absolute cook path,
// "C:/BA/work/.../Temp/Data/") above the table tree, so the tree's real root is
// found by locating this file rather than assuming a fixed depth.
//
// SPIKE-CONFIRM: Task 1 Step 4 records where DataTableMetadata.json actually
// landed. If QuickBMS writes the tree directly at the output root, this search
// still resolves correctly (depth 0) — the marker approach is layout-agnostic
// by design and needs no change either way.
const rootMarkerTable = "DataTableMetadata.json"

// maxExtractedTableSize caps a single extracted table, mirroring
// maxTarEntrySize's role for the hosted dump. The largest real table is 7.3 MB.
const maxExtractedTableSize = 64 << 20

// extractedCacheDir is where a build's extracted tables live:
// <dataDir>/icarus/extracted/<build>/. Keying by build means a game update
// naturally misses the cache and re-extracts, and stale builds' directories are
// simply never consulted.
func extractedCacheDir(dataDir string, b Build) string {
	return filepath.Join(dataDir, "icarus", "extracted", b.String())
}

// runQuickBMS invokes the tool, capturing its output for error reporting.
//
// SPIKE-CONFIRM: Task 1 Step 4 pins the exact argument order and any required
// flags. The documented QuickBMS form is
// `quickbms <script.bms> <input archive> <output folder>`; if the spike needed
// extra flags (e.g. -o to overwrite without prompting), add them here.
func runQuickBMS(ctx context.Context, exe, scriptPath, pakPath, rawDir string) error {
	cmd := exec.CommandContext(ctx, exe, scriptPath, pakPath, rawDir)
	cmd.WaitDelay = quickbmsWaitDelay
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("icarus: QuickBMS extraction did not finish within %s: %w", quickbmsTimeout, ctxErr)
		}
		return fmt.Errorf("icarus: QuickBMS failed extracting %s: %w\n%s", pakPath, err, tailOutput(out))
	}
	return nil
}

// tailOutput trims a tool's captured output to its last few lines, which is
// where QuickBMS reports the actual failure.
func tailOutput(out []byte) string {
	const maxLines = 15
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// normalizeExtraction copies QuickBMS's raw output into destDir in the
// dump-tree shape loadLocalDump consumes: table paths relative to destDir,
// with any mount-path prefix stripped.
func normalizeExtraction(rawDir, destDir string) error {
	treeRoot, err := findTreeRoot(rawDir)
	if err != nil {
		return err
	}
	copied := 0
	err = filepath.WalkDir(treeRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxExtractedTableSize {
			return fmt.Errorf("extracted table %s is %d bytes, exceeding the %d-byte cap",
				p, info.Size(), maxExtractedTableSize)
		}
		rel, err := filepath.Rel(treeRoot, p)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(destDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return err
		}
		copied++
		return nil
	})
	if err != nil {
		return fmt.Errorf("icarus: normalizing QuickBMS output: %w", err)
	}
	if copied == 0 {
		return fmt.Errorf("icarus: QuickBMS produced no JSON tables under %s", rawDir)
	}
	return nil
}

// findTreeRoot locates the directory holding rootMarkerTable — the real root of
// the extracted table tree, whatever prefix directories sit above it. The
// shallowest match wins, so a table tree that legitimately nests a same-named
// file deeper cannot displace the true root.
func findTreeRoot(rawDir string) (string, error) {
	best := ""
	bestDepth := -1
	err := filepath.WalkDir(rawDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != rootMarkerTable {
			return nil
		}
		dir := filepath.Dir(p)
		depth := strings.Count(filepath.ToSlash(dir), "/")
		if bestDepth == -1 || depth < bestDepth {
			best, bestDepth = dir, depth
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("icarus: scanning QuickBMS output %s: %w", rawDir, err)
	}
	if best == "" {
		return "", fmt.Errorf("icarus: QuickBMS output under %s has no %s — "+
			"the extraction did not produce a recognizable data.pak table tree", rawDir, rootMarkerTable)
	}
	return best, nil
}

// extractWithQuickBMS runs a full extraction into cacheDir and returns the
// resulting tables.
//
// The extraction lands in a sibling staging directory and is renamed into
// cacheDir only on success, so cacheDir is either absent or complete — a
// half-written cache can never be mistaken for a usable one on the next run.
// Every failure path removes its partial output, the same hygiene Compile
// applies to a partial pak.
func extractWithQuickBMS(ctx context.Context, exe, pakPath, cacheDir string) (dump *Dump, err error) {
	parent := filepath.Dir(cacheDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("icarus: preparing extraction cache %s: %w", parent, err)
	}
	staging, err := os.MkdirTemp(parent, ".extract-*")
	if err != nil {
		return nil, fmt.Errorf("icarus: preparing extraction staging: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(staging)  //nolint:errcheck // best-effort cleanup of a failed run
			_ = os.RemoveAll(cacheDir) //nolint:errcheck // never leave a partial cache behind
		}
	}()

	rawDir := filepath.Join(staging, "raw")
	if err = os.MkdirAll(rawDir, 0o755); err != nil {
		return nil, fmt.Errorf("icarus: preparing extraction output dir: %w", err)
	}
	scriptPath, err := writeScriptTo(staging)
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithTimeout(ctx, quickbmsTimeout)
	defer cancel()
	if err = runQuickBMS(runCtx, exe, scriptPath, pakPath, rawDir); err != nil {
		return nil, err
	}

	normalized := filepath.Join(staging, "tree")
	if err = normalizeExtraction(rawDir, normalized); err != nil {
		return nil, err
	}

	// Publish atomically: the cache is complete the instant it exists.
	if err = os.RemoveAll(cacheDir); err != nil {
		return nil, fmt.Errorf("icarus: clearing stale extraction cache %s: %w", cacheDir, err)
	}
	if err = os.Rename(normalized, cacheDir); err != nil {
		return nil, fmt.Errorf("icarus: publishing extraction cache %s: %w", cacheDir, err)
	}
	_ = os.RemoveAll(staging) //nolint:errcheck // best-effort: the useful output already moved

	dump, err = loadLocalDump(cacheDir)
	if err != nil {
		return nil, err
	}
	return dump, nil
}
```

- [ ] **Step 4: Run tests to verify they pass (GREEN)**

```bash
go test ./internal/source/icarus/... -v
```

Expected: PASS, including every pre-existing test in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/quickbms.go internal/source/icarus/quickbms_test.go
git commit -m "feat: run QuickBMS and cache its normalized output per build (#174)"
```

---

## Task 4: Chain integration — four ordered sources, one exhaustive error

**Files:**

- Modify: `internal/source/icarus/datadump.go`
- Modify: `internal/source/icarus/datadump_test.go`
- Modify: `internal/source/icarus/icarus.go` (`SetDataDir` now stores the directory)

**Interfaces:**

- Consumes: `extractedCacheDir`, `extractWithQuickBMS`, `findQuickBMS`, `errQuickBMSNotFound` (Tasks 2–3); `loadLocalDump`, `validateDump`, `detectBuild` (existing).
- Produces: `type chainInput struct{...}`, `func (s *DumpStore) DumpForBuild(ctx context.Context, in chainInput) (*Dump, error)` (replaces the 3-arg form), `func newDumpStore(httpClient *http.Client, dataDir string) *DumpStore`, `func installRootFromPak(basePakPath string) string`. Task 5 depends on `chainInput` and the new `DumpForBuild`.

- [ ] **Step 1: Write the failing tests**

Append to `datadump_test.go`. Existing `DumpForBuild` tests must also be migrated to the struct form — that migration is part of Step 3.

```go
// newTestStore builds a DumpStore whose hosted leg points at srv and whose
// data dir is a temp dir, the shape every chain test needs.
func newTestStore(t *testing.T, treeURL, dataDir string) *DumpStore {
	t.Helper()
	s := newDumpStore(http.DefaultClient, dataDir)
	s.treeURL = treeURL
	return s
}

// installWithPak lays out a minimal game install (version.json + data.pak) and
// returns the install root and the pak path.
func installWithPak(t *testing.T, tables map[string][]byte) (root, pakPath string) {
	t.Helper()
	root = t.TempDir()
	cfg := filepath.Join(root, "Icarus", "Config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	const vjson = `{"Name":"Icarus","Version":{"Major":3,"Minor":0,"Patch":21,` +
		`"Changelist":155335,"BuildType":"Shipping","FeatureLevel":"DangerousHorizons"},` +
		`"Data":{"Changelist":155151}}`
	if err := os.WriteFile(filepath.Join(cfg, "version.json"), []byte(vjson), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "Icarus", "Content", "Data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pakPath = filepath.Join(dataDir, "data.pak")
	w, err := unrealpak.Create(pakPath)
	if err != nil {
		t.Fatalf("creating test base pak: %v", err)
	}
	for rel, body := range tables {
		if err := w.AddFile(rel, body); err != nil {
			t.Fatalf("AddFile(%q): %v", rel, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing test base pak: %v", err)
	}
	return root, pakPath
}

func TestInstallRootFromPak(t *testing.T) {
	got := installRootFromPak("/games/Icarus/Icarus/Content/Data/data.pak")
	if want := "/games/Icarus"; got != want {
		t.Errorf("installRootFromPak = %q, want %q", got, want)
	}
}

// Leg 2: a populated per-build cache is used without touching the network or
// running the tool.
func TestDumpForBuild_UsesPerBuildExtractionCache(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	body := []byte("{\r\n    \"Rows\": []\r\n}")
	_, pak := installWithPak(t, map[string][]byte{rel: body})

	dataDir := t.TempDir()
	cache := extractedCacheDir(dataDir, Build{Major: 3, Patch: 21, Changelist: 155335})
	if err := os.MkdirAll(filepath.Join(cache, "Factions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, filepath.FromSlash(rel)), body, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("hosted dump was fetched even though the per-build cache was populated")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := newTestStore(t, srv.URL, dataDir)
	dump, err := store.DumpForBuild(context.Background(), chainInput{basePakPath: pak, autoExtract: true})
	if err != nil {
		t.Fatalf("DumpForBuild: %v", err)
	}
	if got, ok := dump.Table(rel); !ok || !bytes.Equal(got, body) {
		t.Errorf("table = %q (found=%v), want %q", got, ok, body)
	}
}

// Leg 4: hosted dump stale -> QuickBMS runs, and its output is cached.
func TestDumpForBuild_AutoRunsQuickBMSWhenHostedDumpIsStale(t *testing.T) {
	skipIfNoShell(t)
	const rel = "Factions/D_Factions.json"
	body := []byte("{\r\n    \"Rows\": []\r\n}")
	_, pak := installWithPak(t, map[string][]byte{rel: body})

	// Hosted dump serves a different week's content: it must fail validation.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGz(t, "IcarusData-old", map[string]string{rel: "{\n    \"Rows\": [1]\n}"}))
	}))
	defer srv.Close()

	binDir := t.TempDir()
	writeStubQuickBMS(t, binDir, stubEmitting(map[string]string{
		rel:             "{\r\n    \"Rows\": []\r\n}",
		rootMarkerTable: "{}",
	}))
	prependToPATH(t, binDir)

	dataDir := t.TempDir()
	store := newTestStore(t, srv.URL, dataDir)

	dump, err := store.DumpForBuild(context.Background(), chainInput{basePakPath: pak, autoExtract: true})
	if err != nil {
		t.Fatalf("DumpForBuild: %v", err)
	}
	if got, ok := dump.Table(rel); !ok || !bytes.Equal(got, body) {
		t.Errorf("table = %q (found=%v), want the extracted %q", got, ok, body)
	}
	cache := extractedCacheDir(dataDir, Build{Major: 3, Patch: 21, Changelist: 155335})
	if _, statErr := os.Stat(filepath.Join(cache, filepath.FromSlash(rel))); statErr != nil {
		t.Errorf("extraction was not cached for reuse: %v", statErr)
	}
}

// An extraction that does not reproduce the installed pak's own stored tables
// is a mangled extraction, not a wrong week — it must fail loudly AND leave no
// cache behind for the next run to trust.
func TestDumpForBuild_ExtractionFailingValidation_IsRejectedAndNotCached(t *testing.T) {
	skipIfNoShell(t)
	const rel = "Factions/D_Factions.json"
	_, pak := installWithPak(t, map[string][]byte{rel: []byte("{\r\n    \"Rows\": []\r\n}")})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGz(t, "IcarusData-old", map[string]string{rel: "{\n    \"Rows\": [1]\n}"}))
	}))
	defer srv.Close()

	// The stub emits a table whose bytes differ from the pak's own copy.
	binDir := t.TempDir()
	writeStubQuickBMS(t, binDir, stubEmitting(map[string]string{
		rel:             "{\r\n    \"Rows\": [99]\r\n}",
		rootMarkerTable: "{}",
	}))
	prependToPATH(t, binDir)

	dataDir := t.TempDir()
	store := newTestStore(t, srv.URL, dataDir)

	_, err := store.DumpForBuild(context.Background(), chainInput{basePakPath: pak, autoExtract: true})
	if err == nil {
		t.Fatal("expected an error when the extraction does not match the installed pak")
	}
	cache := extractedCacheDir(dataDir, Build{Major: 3, Patch: 21, Changelist: 155335})
	if _, statErr := os.Stat(cache); statErr == nil {
		t.Error("a cache that failed validation must be removed, not left for the next run to reuse")
	}
}

func TestDumpForBuild_AutoExtractFalse_SkipsQuickBMS(t *testing.T) {
	skipIfNoShell(t)
	const rel = "Factions/D_Factions.json"
	_, pak := installWithPak(t, map[string][]byte{rel: []byte("{\r\n}")})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGz(t, "IcarusData-old", map[string]string{rel: "{\n    \"x\": 1\n}"}))
	}))
	defer srv.Close()

	binDir := t.TempDir()
	writeStubQuickBMS(t, binDir, "echo 'stub must not run' >&2\nexit 9\n")
	prependToPATH(t, binDir)

	store := newTestStore(t, srv.URL, t.TempDir())
	_, err := store.DumpForBuild(context.Background(), chainInput{basePakPath: pak, autoExtract: false})
	if err == nil {
		t.Fatal("expected the chain to fail with auto_extract disabled and a stale hosted dump")
	}
	if !strings.Contains(err.Error(), "auto_extract") {
		t.Errorf("error %q should explain that auto_extract is disabled", err)
	}
}

func TestDumpForBuild_MissingBinary_ChainErrorNamesEveryAttempt(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	_, pak := installWithPak(t, map[string][]byte{rel: []byte("{\r\n}")})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGz(t, "IcarusData-old", map[string]string{rel: "{\n    \"x\": 1\n}"}))
	}))
	defer srv.Close()
	t.Setenv("PATH", t.TempDir()) // no quickbms anywhere

	store := newTestStore(t, srv.URL, t.TempDir())
	_, err := store.DumpForBuild(context.Background(), chainInput{basePakPath: pak, autoExtract: true})
	if err == nil {
		t.Fatal("expected an exhausted-chain error, got nil")
	}
	for _, want := range []string{"hosted", "QuickBMS", "data_dump_path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("chain-exhausted error %q should mention %q", err, want)
		}
	}
}

func TestDumpForBuild_ExplicitLocalDirFailure_DoesNotFallThrough(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	_, pak := installWithPak(t, map[string][]byte{rel: []byte("{\r\n}")})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("hosted dump was fetched despite an explicit data_dump_path")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	local := t.TempDir() // exists, but holds no tables
	store := newTestStore(t, srv.URL, t.TempDir())
	_, err := store.DumpForBuild(context.Background(),
		chainInput{basePakPath: pak, localDumpDir: local, autoExtract: true})
	if err == nil {
		t.Fatal("expected an error for an explicit but unusable data_dump_path")
	}
}
```

Add to `datadump_test.go`'s imports: `"bytes"` (if absent).

- [ ] **Step 2: Run to verify they fail (RED)**

```bash
go test ./internal/source/icarus/... -run 'TestDumpForBuild|TestInstallRootFromPak' -v
```

Expected: FAIL — `chainInput`, `installRootFromPak`, and the 2-arg `newDumpStore` are undefined; existing `DumpForBuild` call sites no longer compile.

- [ ] **Step 3: Implement the chain**

In `datadump.go`, replace the `DumpStore` type, its constructor, and `DumpForBuild` with:

```go
// DumpStore acquires base data tables for the installed game, trying every
// source in a fixed order (see DumpForBuild). It does not cache the hosted
// dump to disk; it does own the per-build QuickBMS extraction cache under
// dataDir.
type DumpStore struct {
	httpClient *http.Client
	dataDir    string
	treeURL    string // overridable in tests
}

func newDumpStore(httpClient *http.Client, dataDir string) *DumpStore {
	return &DumpStore{httpClient: httpClient, dataDir: dataDir, treeURL: defaultDumpTreeURL}
}

// chainInput carries everything the base-table chain needs for one compile.
// These are per-game settings, so they arrive per call rather than living on
// the store.
type chainInput struct {
	basePakPath  string // the installed game's data.pak
	localDumpDir string // game.BaseDataPath (data_dump_path); "" when unset
	autoExtract  bool   // game.AutoExtract (auto_extract); default true
	quickbmsPath string // game.QuickBMSPath (quickbms_path); "" when unset
}

// sourceAttempt records one leg of the chain for the exhausted-chain error.
type sourceAttempt struct {
	name   string
	reason string
}

// DumpForBuild returns base data tables that provably match the installed
// game, trying four sources in order:
//
//  1. data_dump_path — the explicit user override. If set, it is the ONLY
//     source consulted: falling through from an override the user deliberately
//     configured would hide their misconfiguration.
//  2. The per-build QuickBMS extraction cache — a previous auto-run's output
//     for this exact build. A cheap disk check that makes extraction a
//     once-per-game-update cost.
//  3. The hosted community dump — the zero-dependency path, kept ahead of the
//     auto-run so users who need no external tool never invoke one.
//  4. A QuickBMS auto-run — extracts the installed pak, which is week-correct
//     by construction. Skipped when auto_extract is false or no binary exists.
//
// Every source passes the same validateDump byte-compare gate; a source that
// fails it is recorded and the chain continues (legs 2-4), because those are
// derived or third-party sources rather than stated user intent. When all fail,
// one error enumerates every attempt and its reason.
func (s *DumpStore) DumpForBuild(ctx context.Context, in chainInput) (*Dump, error) {
	// Leg 1: explicit override — no fallthrough.
	if in.localDumpDir != "" {
		dump, err := loadLocalDump(in.localDumpDir)
		if err != nil {
			return nil, err
		}
		if err := validateDump(dump, in.basePakPath); err != nil {
			return nil, fmt.Errorf("%w (tables were read from the configured data_dump_path %s)", err, in.localDumpDir)
		}
		return dump, nil
	}

	var attempts []sourceAttempt

	build, buildErr := detectBuild(installRootFromPak(in.basePakPath))
	cacheDir := ""
	if buildErr == nil && s.dataDir != "" {
		cacheDir = extractedCacheDir(s.dataDir, build)
	}

	// Leg 2: per-build extraction cache.
	switch {
	case cacheDir == "":
		attempts = append(attempts, sourceAttempt{"cached QuickBMS extraction",
			fmt.Sprintf("no cache location available (%v)", buildErr)})
	default:
		dump, err := loadLocalDump(cacheDir)
		switch {
		case err != nil:
			attempts = append(attempts, sourceAttempt{"cached QuickBMS extraction",
				fmt.Sprintf("no usable cache at %s", cacheDir)})
		default:
			if vErr := validateDump(dump, in.basePakPath); vErr != nil {
				attempts = append(attempts, sourceAttempt{"cached QuickBMS extraction",
					fmt.Sprintf("cache at %s failed validation: %v", cacheDir, vErr)})
			} else {
				return dump, nil
			}
		}
	}

	// Leg 3: hosted community dump.
	dump, err := s.fetchTree(ctx, s.treeURL)
	switch {
	case err != nil:
		attempts = append(attempts, sourceAttempt{"hosted community dump", err.Error()})
	default:
		if vErr := validateDump(dump, in.basePakPath); vErr != nil {
			attempts = append(attempts, sourceAttempt{"hosted community dump", vErr.Error()})
		} else {
			return dump, nil
		}
	}

	// Leg 4: QuickBMS auto-run.
	switch {
	case !in.autoExtract:
		attempts = append(attempts, sourceAttempt{"QuickBMS auto-extraction",
			"disabled by auto_extract: false in games.yaml"})
	case cacheDir == "":
		attempts = append(attempts, sourceAttempt{"QuickBMS auto-extraction",
			fmt.Sprintf("cannot determine the installed build to cache under (%v)", buildErr)})
	default:
		exe, findErr := findQuickBMS(in.quickbmsPath)
		switch {
		case errors.Is(findErr, errQuickBMSNotFound):
			attempts = append(attempts, sourceAttempt{"QuickBMS auto-extraction",
				"no quickbms binary on PATH (install QuickBMS, or set quickbms_path in games.yaml)"})
		case findErr != nil:
			// A configured-but-wrong quickbms_path is misconfiguration, not a
			// chain-miss: surface it directly rather than burying it in the
			// exhausted-chain summary.
			return nil, findErr
		default:
			extracted, exErr := extractWithQuickBMS(ctx, exe, in.basePakPath, cacheDir)
			if exErr != nil {
				attempts = append(attempts, sourceAttempt{"QuickBMS auto-extraction", exErr.Error()})
				break
			}
			if vErr := validateDump(extracted, in.basePakPath); vErr != nil {
				// The extraction came from the installed pak itself, so a
				// validation failure means the extraction was mangled, not that
				// it is the wrong week. Do not keep the bad cache.
				_ = os.RemoveAll(cacheDir) //nolint:errcheck // best-effort
				attempts = append(attempts, sourceAttempt{"QuickBMS auto-extraction",
					fmt.Sprintf("extracted tables did not match the installed pak: %v", vErr)})
				break
			}
			return extracted, nil
		}
	}

	return nil, chainExhaustedError(attempts)
}

// chainExhaustedError renders every attempted source, why it failed, and what
// the user can do about it.
func chainExhaustedError(attempts []sourceAttempt) error {
	var b strings.Builder
	b.WriteString("icarus: no usable source of base data tables for the installed game. Tried:\n")
	for _, a := range attempts {
		fmt.Fprintf(&b, "  - %s: %s\n", a.name, a.reason)
	}
	b.WriteString("Remedies: point data_dump_path at your own unpacked data.pak JSON tree, " +
		"install QuickBMS (or set quickbms_path) so lmm can extract it for you, " +
		"or wait for the hosted community dump to catch up with your game version.")
	return errors.New(b.String())
}

// installRootFromPak recovers the game install root from the base pak's path.
// resolveBasePak builds it as <root>/Icarus/Content/Data/data.pak, so the root
// is four directories up.
func installRootFromPak(basePakPath string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(basePakPath))))
}
```

**Migrate every pre-existing call site.** Both signatures changed, so `go build ./...` and
`go vet ./...` are the checklist — but these are the exact sites as of this plan's writing:

- `newDumpStore` gains a second argument. Eight sites in `datadump_test.go` (lines ~151, 177,
  218, 242, 257, 274, 288, 327), one in `compile_test.go` (~line 30), and one in `icarus.go`
  (handled above). In tests, pass `t.TempDir()` as the data dir unless the test asserts on the
  extraction cache, in which case hoist it to a variable so the assertion can find it.
- `DumpForBuild` takes the struct. Eight sites in `datadump_test.go` (lines ~154, 180, 221,
  245, 260, 276, 292, 331) and one in `compile.go` (~line 48, further reworked by Task 5):
  `store.DumpForBuild(ctx, pak, "")` becomes
  `store.DumpForBuild(ctx, chainInput{basePakPath: pak})`, and
  `store.DumpForBuild(ctx, pak, local)` becomes
  `store.DumpForBuild(ctx, chainInput{basePakPath: pak, localDumpDir: local})`.

Note what the migration means semantically: those pre-existing tests leave `autoExtract` at its
zero value, `false`, which is correct for them — they were written to exercise the hosted and
local-dir legs, and a stray system-installed QuickBMS must not silently rescue a case they
expect to fail. Any test that _does_ want the auto-run leg sets `autoExtract: true` explicitly
and controls `PATH` (see the new tests above). Leaving the zero value to mean "no auto-run" is
what keeps this migration behavior-preserving.

Then in `icarus.go`, make `SetDataDir` actually keep the directory — its previous "dataDir's value is unused" comment is now stale and must go:

```go
// SetDataDir wires the service's data directory into the base-table dump
// store, which uses it for the per-build QuickBMS extraction cache
// (<dataDir>/icarus/extracted/<build>/, #174). Compile is gated on this having
// been called at all (see TestIcarus_Compile_WithoutDataDir_FailsLoudly). This
// is a post-construction setter rather than a New parameter because Task 8 of
// the #136 plan froze New(httpClient, projectID) at exactly those two params —
// its call site already depends on that signature — so the data dir arrives the
// same way API keys do: an optional setter the registration pipeline calls when
// present (mirroring its existing SetAPIKey wiring).
func (s *Icarus) SetDataDir(dataDir string) {
	s.dumps = newDumpStore(s.firestore.httpClient, dataDir)
}
```

Update the stale reference in `cmd/lmm/root.go`'s `registerSource` doc comment, which currently claims dataDir's value is unused:

```go
// registerSource runs src through the shared registration steps used for
// both built-in and custom sources: collision check (first registration
// wins, warning on customSourceWarnWriter) → API-key resolution (env var via
// envKeyFor, falling back to the stored DB token) → SetAPIKey when the
// source accepts one → SetDataDir when the source accepts one (Icarus's
// Compile is gated on SetDataDir having been called at all: that call
// constructs the base-table dump store, which uses dataDir for its per-build
// QuickBMS extraction cache, #174. New itself can't take dataDir since Task
// 8/9 froze its 2-arg signature) → RegisterSource.
```

- [ ] **Step 4: Run tests to verify they pass (GREEN)**

```bash
go test ./internal/source/icarus/... -v
go build ./...
```

Expected: PASS. `go build` catches the `newDumpStore`/`DumpForBuild` call-site migrations.

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/datadump.go internal/source/icarus/datadump_test.go internal/source/icarus/icarus.go cmd/lmm/root.go
git commit -m "feat: chain base-table sources through a QuickBMS auto-extraction fallback (#174)"
```

---

## Task 5: Config plumbing — `auto_extract` and `quickbms_path`

**Files:**

- Modify: `internal/domain/game.go` (two new `Game` fields)
- Modify: `internal/storage/config/games.go` (two new YAML keys, load + save)
- Modify: `internal/storage/config/games_test.go`
- Modify: `internal/source/source.go` (`Compiler` takes a request struct)
- Modify: `internal/source/icarus/icarus.go`, `internal/source/icarus/compile.go`
- Modify: `internal/core/service.go` (build the request from the game)
- Modify: `internal/core/service_icarus_compile_test.go`, `internal/source/icarus/compile_test.go`

**Interfaces:**

- Consumes: `chainInput` (Task 4).
- Produces: `domain.Game.AutoExtract bool`, `domain.Game.QuickBMSPath string`, `source.CompileRequest`, `Compiler.Compile(ctx context.Context, req CompileRequest) error`, `icarus.Compile(ctx context.Context, dumps *DumpStore, req source.CompileRequest) error`.

> **Design note (resolved ambiguity).** The design says "`Compile`'s exported signature is unchanged", but two new per-game settings have to reach the chain and only the caller holds the game. Keeping positional parameters would make `Compile` take six strings in a row — a transposition hazard flagged during the #136 r4 review. Replacing them with `source.CompileRequest` keeps the _call flow_ unchanged (which is what the design's constraint protects), removes the hazard, and makes the next addition free. No CLI/TUI surface changes.

- [ ] **Step 1: Write the failing tests**

Add to `internal/storage/config/games_test.go`:

```go
func TestLoadGames_AutoExtractDefaultsTrue(t *testing.T) {
	dir := t.TempDir()
	yaml := "games:\n  icarus:\n    name: Icarus\n    install_path: /games/icarus\n" +
		"    mod_path: /games/icarus/mods\n"
	if err := os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	games, err := LoadGames(dir)
	if err != nil {
		t.Fatalf("LoadGames: %v", err)
	}
	if !games["icarus"].AutoExtract {
		t.Error("AutoExtract = false, want true when auto_extract is absent (default true)")
	}
}

func TestLoadGames_AutoExtractExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	yaml := "games:\n  icarus:\n    name: Icarus\n    install_path: /games/icarus\n" +
		"    mod_path: /games/icarus/mods\n    auto_extract: false\n" +
		"    quickbms_path: ~/bin/quickbms\n"
	if err := os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	games, err := LoadGames(dir)
	if err != nil {
		t.Fatalf("LoadGames: %v", err)
	}
	g := games["icarus"]
	if g.AutoExtract {
		t.Error("AutoExtract = true, want false when auto_extract: false is set")
	}
	if g.QuickBMSPath == "" || strings.HasPrefix(g.QuickBMSPath, "~") {
		t.Errorf("QuickBMSPath = %q, want a ~-expanded absolute path", g.QuickBMSPath)
	}
}

// auto_extract: false must survive a load/save round trip; the default (true)
// must not be written out, matching deploy_mode's only-if-non-default rule.
func TestSaveGame_AutoExtractRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, g := range []*domain.Game{
		{ID: "icarus", Name: "Icarus", InstallPath: "/g", ModPath: "/m", AutoExtract: false},
		{ID: "other", Name: "Other", InstallPath: "/g2", ModPath: "/m2", AutoExtract: true},
	} {
		if err := SaveGame(dir, g); err != nil {
			t.Fatalf("SaveGame(%s): %v", g.ID, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "auto_extract: false") {
		t.Errorf("saved games.yaml should record auto_extract: false, got:\n%s", raw)
	}
	if strings.Contains(string(raw), "auto_extract: true") {
		t.Errorf("saved games.yaml should omit the default auto_extract: true, got:\n%s", raw)
	}

	reloaded, err := LoadGames(dir)
	if err != nil {
		t.Fatalf("LoadGames: %v", err)
	}
	if reloaded["icarus"].AutoExtract {
		t.Error("icarus AutoExtract = true after round trip, want false")
	}
	if !reloaded["other"].AutoExtract {
		t.Error("other AutoExtract = false after round trip, want true")
	}
}
```

- [ ] **Step 2: Run to verify they fail (RED)**

```bash
go test ./internal/storage/config/... -run 'AutoExtract' -v
```

`games_test.go` currently imports only `os`, `path/filepath` and `testing`; these tests add
`strings` and `github.com/DonovanMods/linux-mod-manager/internal/domain`.

Expected: FAIL — `domain.Game` has no `AutoExtract`/`QuickBMSPath`.

- [ ] **Step 3: Implement the config plumbing and request struct**

In `internal/domain/game.go`, beside `BaseDataPath`:

```go
	// AutoExtract enables extracting the installed data.pak with QuickBMS when
	// no other base-table source matches the installed game. Defaults to true;
	// set auto_extract: false in games.yaml to opt out (compile games only)
	AutoExtract bool

	// QuickBMSPath is optional: an explicit QuickBMS executable for installs
	// where it is not on PATH (compile games only)
	QuickBMSPath string
```

In `internal/storage/config/games.go`, on `GameConfig`:

```go
	AutoExtract  *bool  `yaml:"auto_extract,omitempty"`
	QuickBMSPath string `yaml:"quickbms_path,omitempty"`
```

`*bool` rather than `bool` because this setting defaults to **true**: a plain bool cannot distinguish "absent" from "explicitly false", and `omitempty` would drop a legitimate `false`. Pointer-typed optional fields are the existing convention here (`ProfileHookConfigYAML` uses `*string`).

In `loadGamesLocked`'s `domain.Game` literal, beside `BaseDataPath`:

```go
			AutoExtract:  cfg.AutoExtract == nil || *cfg.AutoExtract,
			QuickBMSPath: ExpandPath(cfg.QuickBMSPath),
```

In `saveGamesLocked`'s `GameConfig` literal, beside `BaseDataPath`:

```go
			QuickBMSPath: game.QuickBMSPath,
```

and, next to the existing "only write deploy_mode if not the default" block:

```go
		// Only write auto_extract when it differs from the true default, so a
		// hand-written games.yaml stays free of redundant keys.
		if !game.AutoExtract {
			disabled := false
			cfg.AutoExtract = &disabled
		}
```

In `internal/source/source.go`, replace the `Compiler` interface:

```go
// CompileRequest carries everything a Compiler needs for one compile. It is a
// struct rather than positional parameters because every field is a string or
// flag resolved from the same game config, and a six-argument call of
// same-typed values is a transposition hazard.
type CompileRequest struct {
	// BasePakPath is the installed game's base pak, resolved by the caller
	// from game.InstallPath.
	BasePakPath string
	// BaseDataPath is the game's optional data_dump_path: a local unpacked
	// data.pak JSON tree used instead of any other base-table source. "" when
	// unset.
	BaseDataPath string
	// AutoExtract enables the QuickBMS auto-extraction fallback (game's
	// auto_extract, default true).
	AutoExtract bool
	// QuickBMSPath is the game's optional quickbms_path override. "" when
	// unset, meaning PATH is searched.
	QuickBMSPath string
	// SourceFilePath is the just-downloaded file to compile.
	SourceFilePath string
	// OutputPath is where the compiled result must be written.
	OutputPath string
}

// Compiler is implemented by sources whose downloaded files need
// transforming into a different artifact before deployment (Icarus's
// .exmodz -> .pak). Service consults it, when DeployMode is DeployCompile,
// after downloading but before committing the file to cache — the result
// replaces the downloaded file in cache, so everything downstream (Install,
// the linker) treats it exactly like a DeployCopy file.
type Compiler interface {
	Compile(ctx context.Context, req CompileRequest) error
}
```

In `internal/source/icarus/icarus.go`:

```go
// Compile implements source.Compiler by delegating to the package-level
// Compile function. The base-table dump store is supplied from the source
// itself; every per-game setting the chain needs arrives on req, since only
// the caller has the game's config.
func (s *Icarus) Compile(ctx context.Context, req source.CompileRequest) error {
	if s.dumps == nil {
		return fmt.Errorf("source %q: not initialized with a data directory (SetDataDir was never called)", s.ID())
	}
	return Compile(ctx, s.dumps, req)
}
```

In `internal/source/icarus/compile.go`, change the signature and the chain call; everything between is untouched:

```go
// Compile reads req.SourceFilePath's .EXMOD diff, applies it to the game's base
// data tables, bundles in any pre-built assets the .EXMODZ carries, and writes
// the result as a new pak at req.OutputPath ready to deploy as-is.
//
// The base tables come from the source chain in DumpForBuild — a local
// data_dump_path, a cached QuickBMS extraction, the hosted community dump, or a
// fresh QuickBMS auto-run — not from req.BasePakPath: 258 of the 298 tables in
// a real data.pak are Oodle-compressed and cannot be read with the stdlib.
// req.BasePakPath is still opened, for two things it alone can answer — which
// tables the installed game actually has (so a bare, hyphen-flattened
// CurrentFile resolves to a real mount path), and whether the chosen source
// matches the installed week (DumpForBuild byte-checks it against the tables
// the pak stores uncompressed). A source that does not match fails the whole
// compile.
func Compile(ctx context.Context, dumps *DumpStore, req source.CompileRequest) (err error) {
	exmodzData, err := os.ReadFile(req.SourceFilePath)
	if err != nil {
		return fmt.Errorf("icarus: reading %s: %w", req.SourceFilePath, err)
	}
	bundle, err := ParseExmodz(exmodzData)
	if err != nil {
		return fmt.Errorf("icarus: %s: %w", req.SourceFilePath, err)
	}

	base, err := unrealpak.Open(req.BasePakPath)
	if err != nil {
		return fmt.Errorf("icarus: opening base pak %s: %w", req.BasePakPath, err)
	}
	defer base.Close() //nolint:errcheck

	// Loaded and validated before anything is written, so a week mismatch or an
	// exhausted source chain fails before a half-built pak exists on disk.
	dump, err := dumps.DumpForBuild(ctx, chainInput{
		basePakPath:  req.BasePakPath,
		localDumpDir: req.BaseDataPath,
		autoExtract:  req.AutoExtract,
		quickbmsPath: req.QuickBMSPath,
	})
	if err != nil {
		return err
	}

	out, err := unrealpak.Create(req.OutputPath)
	if err != nil {
		return fmt.Errorf("icarus: creating %s: %w", req.OutputPath, err)
	}
```

The rest of `Compile`'s body is unchanged except that the three remaining `outputPakPath` references inside the cleanup `defer` and the final `out.Close()` error become `req.OutputPath`:

```go
	defer func() {
		if err == nil {
			return
		}
		_ = out.Close() //nolint:errcheck
		if rmErr := os.Remove(req.OutputPath); rmErr != nil && !os.IsNotExist(rmErr) {
			err = fmt.Errorf("%w (additionally, removing partial output %s failed: %v)", err, req.OutputPath, rmErr)
		}
	}()
```

```go
	if err := out.Close(); err != nil {
		return fmt.Errorf("icarus: finalizing %s: %w", req.OutputPath, err)
	}
	return nil
}
```

`compile.go` gains `"github.com/DonovanMods/linux-mod-manager/internal/source"` in its imports.

In `internal/core/service.go`, at the compile call site:

```go
		if err := compiler.Compile(ctx, source.CompileRequest{
			BasePakPath:    basePakPath,
			BaseDataPath:   game.BaseDataPath,
			AutoExtract:    game.AutoExtract,
			QuickBMSPath:   game.QuickBMSPath,
			SourceFilePath: archivePath,
			OutputPath:     destPath,
		}); err != nil {
			return nil, fmt.Errorf("compiling mod: %w", err)
		}
```

Update the `fakeCompilerSource` in `internal/core/service_icarus_compile_test.go` to the new signature:

```go
func (s *fakeCompilerSource) Compile(ctx context.Context, req source.CompileRequest) error {
	s.compileCalls++
	data, err := os.ReadFile(req.SourceFilePath)
	if err != nil {
		return err
	}
	return os.WriteFile(req.OutputPath, data, 0o644)
}
```

and migrate `internal/source/icarus/compile_test.go`'s `Compile(...)` calls to the struct form, e.g.:

```go
	err := Compile(context.Background(), dumps, source.CompileRequest{
		BasePakPath:    basePak,
		SourceFilePath: exmodzPath,
		OutputPath:     outputPath,
	})
```

- [ ] **Step 4: Run tests to verify they pass (GREEN)**

```bash
go build ./...
go test ./internal/storage/config/... ./internal/source/... ./internal/core/... -v 2>&1 | tail -30
```

Expected: PASS across all three packages.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/game.go internal/storage/config/games.go internal/storage/config/games_test.go \
        internal/source/source.go internal/source/icarus/icarus.go internal/source/icarus/compile.go \
        internal/source/icarus/compile_test.go internal/core/service.go internal/core/service_icarus_compile_test.go
git commit -m "feat: add auto_extract and quickbms_path game settings (#174)"
```

---

## Task 6: Announce the auto-run through the existing diagnostics convention

**Files:**

- Modify: `internal/source/icarus/datadump.go`
- Modify: `internal/source/icarus/datadump_test.go`

**Interfaces:**

- Consumes: `DumpStore` (Task 4).
- Produces: `func (s *DumpStore) SetAnnounceWriter(w io.Writer)`, `func (s *DumpStore) announcef(format string, args ...any)`.

> **Design note (resolved ambiguity).** The design says the auto-run is "announced through existing progress/logging". This codebase has two such mechanisms and neither fits directly: `ProgressFunc` is download-specific and never reaches `Compile`, and the `Notes`/`Warnings` result-slice convention (`internal/core/flows.go`) requires a result struct that `Compiler.Compile` (error-only) does not have. Threading notes up through `DownloadModResult` would ripple into both the CLI and the TUI — which the "no CLI/TUI surface" constraint forbids. The remaining precedent is `cmd/lmm/root.go`'s `customSourceWarnWriter`: an injectable `io.Writer` defaulting to stderr, used exactly for "deep code must tell the user something". This task follows that, scoped to the store so tests stay parallel-safe (no package-level mutable state).

- [ ] **Step 1: Write the failing test**

```go
func TestDumpForBuild_AnnouncesTheAutoRun(t *testing.T) {
	skipIfNoShell(t)
	const rel = "Factions/D_Factions.json"
	body := []byte("{\r\n    \"Rows\": []\r\n}")
	_, pak := installWithPak(t, map[string][]byte{rel: body})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGz(t, "IcarusData-old", map[string]string{rel: "{\n    \"Rows\": [1]\n}"}))
	}))
	defer srv.Close()

	binDir := t.TempDir()
	stub := writeStubQuickBMS(t, binDir, stubEmitting(map[string]string{
		rel:             "{\r\n    \"Rows\": []\r\n}",
		rootMarkerTable: "{}",
	}))
	prependToPATH(t, binDir)

	var announced bytes.Buffer
	store := newTestStore(t, srv.URL, t.TempDir())
	store.SetAnnounceWriter(&announced)

	if _, err := store.DumpForBuild(context.Background(),
		chainInput{basePakPath: pak, autoExtract: true}); err != nil {
		t.Fatalf("DumpForBuild: %v", err)
	}

	got := announced.String()
	if !strings.Contains(got, stub) {
		t.Errorf("announcement %q should name the command being run (%s)", got, stub)
	}
	if !strings.Contains(got, "data.pak") {
		t.Errorf("announcement %q should name what is being extracted", got)
	}
	// The reason matters as much as the action: the user needs to know why an
	// external tool suddenly ran.
	if !strings.Contains(strings.ToLower(got), "hosted") {
		t.Errorf("announcement %q should explain why the fallback was needed", got)
	}
}

func TestDumpStore_AnnounceWriter_DefaultsToStderrAndNeverPanics(t *testing.T) {
	s := newDumpStore(http.DefaultClient, t.TempDir())
	// No SetAnnounceWriter call: announcef must be safe on a zero-value writer.
	s.announcef("hello %s", "world")
}
```

- [ ] **Step 2: Run to verify it fails (RED)**

```bash
go test ./internal/source/icarus/... -run 'Announce' -v
```

Expected: FAIL — `SetAnnounceWriter`, `announcef` undefined.

- [ ] **Step 3: Implement the announcement**

Add `"io"` to `datadump.go`'s imports, add the field to `DumpStore`:

```go
type DumpStore struct {
	httpClient *http.Client
	dataDir    string
	treeURL    string    // overridable in tests
	announce   io.Writer // nil means os.Stderr; see announcef
}
```

and add:

```go
// SetAnnounceWriter redirects the store's user-facing announcements. Tests
// inject a buffer; production leaves it nil, which means stderr.
func (s *DumpStore) SetAnnounceWriter(w io.Writer) { s.announce = w }

// announcef tells the user about something they did not ask for directly —
// specifically, that lmm is about to invoke an external tool on their behalf.
// It follows cmd/lmm/root.go's customSourceWarnWriter precedent (an injectable
// writer defaulting to stderr) because Compiler.Compile returns only an error,
// with no result struct to carry the Notes/Warnings slices internal/core uses.
func (s *DumpStore) announcef(format string, args ...any) {
	w := s.announce
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, format, args...) //nolint:errcheck // diagnostics: a failed write must not fail a compile
}
```

In `DumpForBuild`'s leg 4, immediately before calling `extractWithQuickBMS`:

```go
			s.announcef("lmm: the hosted base-table dump does not match your installed Icarus "+
				"build (%s), so lmm is extracting the game's own data.pak with QuickBMS.\n"+
				"     Running: %s <embedded %s> %s\n"+
				"     This runs once per game update; results are cached in %s.\n"+
				"     Set auto_extract: false in games.yaml to disable this.\n",
				build, exe, embeddedScriptName, in.basePakPath, cacheDir)
			extracted, exErr := extractWithQuickBMS(ctx, exe, in.basePakPath, cacheDir)
```

- [ ] **Step 4: Run tests to verify they pass (GREEN)**

```bash
go test ./internal/source/icarus/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/datadump.go internal/source/icarus/datadump_test.go
git commit -m "feat: announce QuickBMS auto-extraction before running it (#174)"
```

---

## Task 7: Documentation — README, configuration.md, CHANGELOG

**Files:**

- Modify: `README.md`
- Modify: `docs/configuration.md`
- Modify: `CHANGELOG.md`

**Interfaces:**

- Consumes: the settings from Task 5.
- Produces: no code.

- [ ] **Step 1: Update `docs/configuration.md`**

Add two rows to the per-game settings table, beside the existing `data_dump_path` row:

```markdown
| `auto_extract` | bool | no | Compile-mode only: extract the installed `data.pak` with QuickBMS when no other base-table source matches the installed game (default `true`) |
| `quickbms_path` | string | no | Compile-mode only: explicit QuickBMS executable, for installs where it is not on `PATH` |
```

and extend the `compile` deploy-mode paragraph:

```markdown
- **`compile`**: The downloaded file is compiled into a new artifact before caching (currently Icarus only: an `.exmodz` diff is applied to the game's base data tables to produce a deployable `_P.pak`). Only sources that implement compiling support this mode. Base data tables are resolved in order: your own `data_dump_path` tree, a previously cached QuickBMS extraction for the installed build, the hosted community dump, and finally a fresh QuickBMS extraction of the installed `data.pak`. Every source is byte-validated against the installed game, and a mismatch is a hard error rather than a silent fallback. QuickBMS is optional — if it is not installed, that step is simply skipped — and `auto_extract: false` disables it entirely. Extraction runs once per game update; its output is cached under the lmm data directory.
```

- [ ] **Step 2: Update `README.md`**

Extend the `icarus` example block's commented settings:

```yaml
icarus:
  name: "Icarus"
  install_path: "/path/to/Steam/steamapps/common/Icarus"
  mod_path: "/path/to/Steam/steamapps/common/Icarus/Icarus/Content/Paks/mods"
  deploy_mode: compile
  # data_dump_path: ~/icarus-data-dump # Optional: compile from your own
  # unpacked data.pak JSON tree instead of the hosted community dump
  # auto_extract: false      # Optional: disable extracting data.pak with
  # QuickBMS when the hosted dump lags your game version (default: true)
  # quickbms_path: ~/.local/bin/quickbms # Optional: QuickBMS not on PATH
  sources:
    icarus: "icarus"
```

and add a short paragraph after the block:

```markdown
Compiling needs the game's base data tables. lmm prefers sources that need no extra tooling — your own `data_dump_path`, then the hosted community dump — but that dump can lag a fresh game update. When it does, and [QuickBMS](https://aluigi.altervista.org/quickbms.htm) is available on `PATH` (or at `quickbms_path`), lmm extracts the installed `data.pak` itself, announces that it is doing so, and caches the result until the next game update. QuickBMS is entirely optional: without it, lmm simply reports that no base-table source matched and tells you the remedies.
```

- [ ] **Step 3: Update `CHANGELOG.md`**

Under `[Unreleased]` → `### Added` (this story adds no version bump; the epic carries one at release):

```markdown
- **QuickBMS auto-extraction fallback for Icarus compiles**: when the hosted community base-table dump does not match your installed game version, lmm now extracts the game's own `data.pak` using [QuickBMS](https://aluigi.altervista.org/quickbms.htm) — the always-week-correct source — instead of failing. Base tables are resolved in order (`data_dump_path` → cached extraction → hosted dump → fresh extraction), every source is byte-validated against the installed pak, and extraction results are cached per game build so it runs once per update. QuickBMS is optional and runtime-detected: lmm announces the exact command before running it, and two new `games.yaml` settings (`auto_extract`, default `true`, and `quickbms_path`) control it. No new CLI flag or TUI screen (#174)
```

- [ ] **Step 4: Verify the docs build/lint clean**

```bash
trunk check --filter=markdownlint README.md docs/configuration.md CHANGELOG.md 2>&1 | tail -20
```

Expected: no new findings.

- [ ] **Step 5: Commit**

```bash
git add README.md docs/configuration.md CHANGELOG.md
git commit -m "docs: document the QuickBMS auto-extraction fallback (#174)"
```

---

## Task 8: Update the post-plan manual-validation checklist

**Files:**

- Modify: `docs/plans/2026-07-29-icarus-exmod-pak-compilation.md` (the #136 plan's "Post-plan manual validation" section — gitignored, so this is a working-doc edit with no commit)

**Interfaces:**

- Consumes: everything above.
- Produces: no code.

- [ ] **Step 1: Add the QuickBMS validation items**

Append to that plan's "Post-plan manual validation" list:

```markdown
6. **QuickBMS auto-extraction (#174).** On the reference machine, with the hosted dump still
   behind the installed build:
   - `quickbms` resolves (Task 1 installed it to `~/.local/bin`); `lmm install` of an Icarus
     `.exmodz` prints the announcement naming the command, the pak, and the reason, then
     completes.
   - The extraction cache appears at `<dataDir>/icarus/extracted/<build>/` and holds ~298
     `.json` tables.
   - A second install of the same or another `.exmodz` does **not** re-run QuickBMS (the
     announcement is absent) — the per-build cache is reused.
   - `auto_extract: false` in `games.yaml` restores the previous behavior: the compile fails
     with the exhausted-chain error naming all four sources and their remedies.
   - Renaming the binary out of `PATH` (with `auto_extract` back to default) produces the
     same exhausted-chain error, with the QuickBMS leg reported as "no quickbms binary".
7. **The first real end-to-end compile.** This feature is what finally unblocks it: with a
   validated base-table source available, compile the real `Bear_Mount.EXMODZ` from the
   catalog and confirm `Bear_Mount_P.pak` is produced, that `unrealpak.Open` on it enumerates
   the patched tables plus the bundled assets, and that the patched table differs from the
   base table only in the rows the `.EXMOD` targets. Then deploy it and confirm Icarus loads
   it in-game — the last unverified link (see item 3's mount-point question, which this is
   the natural moment to settle).
```

- [ ] **Step 2: No commit**

`docs/plans/*` is gitignored (`.gitignore:65`). Leave the edit in the working tree.

---

## Post-plan manual validation (this plan)

1. Run the full suite with no QuickBMS on `PATH` at all — every test in
   `internal/source/icarus` must still pass, proving the tool is genuinely optional and that
   no test silently depends on a real binary:

   ```bash
   env PATH=/usr/bin:/bin go test ./internal/source/icarus/... -count=1
   ```

2. Confirm the embedded script actually ships in the binary (a `go:embed` typo fails at build
   time, but an empty file does not):

   ```bash
   go build -o /tmp/lmm ./cmd/lmm && strings /tmp/lmm | grep -c 'quickbms\|comtype'
   ```

3. Re-grep for leftover markers once Task 1's answers are folded in:

   ```bash
   grep -rn 'SPIKE-CONFIRM' internal/ docs/plans/2026-08-01-icarus-quickbms-fallback.md
   ```

   Every remaining hit must be either resolved-and-deleted or still genuinely unknown.
