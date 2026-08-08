# go-unrealpak Module + CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract lmm's `internal/unrealpak` into a standalone public Go module with a general-purpose CLI, and have lmm consume it as a dependency (#170).

**Architecture:** The package moves verbatim to the root of a new repo via `git subtree split`, preserving its history. A `cmd/unrealpak` CLI wraps the existing public API with five subcommands — `info`, `list`, `cat`, `extract`, `build` — using only the standard library. The package stays game-agnostic: no Icarus knowledge, no game-specific constants. lmm then swaps its import path and deletes the internal copy.

**Tech Stack:** Go (stdlib only — no external dependencies anywhere in the module), GitHub Actions, `git subtree split`.

## Global Constraints

- Module path: `github.com/DonovanMods/go-unrealpak`. Package name: `unrealpak`, at the repo root.
- CLI binary name: `unrealpak`, at `cmd/unrealpak`.
- **Zero external dependencies.** Library and CLI both. Use `flag` from the stdlib, not Cobra. This is a deliberate selling point for a format library — do not add a dependency for convenience.
- **Game-agnostic.** No Icarus mount points, `data/` prefixes, `.EXMOD` knowledge, or any other game-specific value may enter this module. Those live in the consumer.
- Go directive: `go 1.25` (matches the toolchain the code is tested against in lmm).
- License: MIT, `Copyright (c) 2026 Donovan C. Young` — copied from lmm's `LICENSE`.
- **Writes are stored (uncompressed) only. Reads handle stored and Zlib. Oodle is out of scope** — index reads never need it; an Oodle _payload_ read must fail with an error naming the entry.
- **No silent fallbacks.** Zero or ambiguous matches, unsupported shapes, and failed hash gates are loud errors with a non-zero exit.
- Every CLI subcommand writes results to an injected `io.Writer`, never directly to `os.Stdout`, so it is testable.

---

## File Structure

**New repo `go-unrealpak`:**

| File                                                                    | Responsibility                                                      |
| ----------------------------------------------------------------------- | ------------------------------------------------------------------- |
| `go.mod`                                                                | Module declaration. No `require` block — there are no dependencies. |
| `LICENSE`                                                               | MIT, copied verbatim from lmm.                                      |
| `README.md`                                                             | What it is, install, CLI usage, library usage, scope limits.        |
| `docs/format.md`                                                        | The byte-level UE v11 pak format spec.                              |
| `pak.go`, `reader.go`, `writer.go`                                      | The package, moved verbatim. Unchanged.                             |
| `reader_test.go`, `writer_test.go`, `roundtrip_test.go`, `zlib_test.go` | Moved verbatim. Unchanged.                                          |
| `cmd/unrealpak/main.go`                                                 | `main` + `run(args, out)` dispatch.                                 |
| `cmd/unrealpak/commands.go`                                             | The five subcommand implementations.                                |
| `cmd/unrealpak/sidecar.go`                                              | `.unrealpak.json` read/write.                                       |
| `cmd/unrealpak/main_test.go`                                            | CLI tests, driven through `run`.                                    |
| `.github/workflows/test.yml`                                            | gofmt + vet + race tests.                                           |

**Modified in `linux-mod-manager`:** `go.mod`, `internal/core/service.go`, `internal/source/icarus/{compile,merge,pakconvert}.go`, and six test files — import path only. `internal/unrealpak/` is deleted.

---

### Task 1: Extract the package into a new module

**Files:**

- Create: new repo `go-unrealpak` containing `go.mod`, `LICENSE`, `.github/workflows/test.yml`, and the moved `pak.go`/`reader.go`/`writer.go` + their tests

> **Steps 1-3 are already done (2026-08-07).** The repo exists at
> <https://github.com/DonovanMods/go-unrealpak> (public), carries the package's
> 14-commit history with the files at the repo root, and is cloned locally at
> `~/Projects/apps/go-unrealpak` on `main`. **Start at Step 4.** It has no
> `go.mod` yet, so it will not build until Step 4 lands — that is expected, not
> a broken checkout.

**Interfaces:**

- Consumes: nothing
- Produces: the module `github.com/DonovanMods/go-unrealpak` exposing `Open(path string) (*Reader, error)`, `(*Reader).Close() error`, `(*Reader).MountPoint() string`, `(*Reader).IndexHash() string`, `(*Reader).Files() []FileEntry`, `(*Reader).ReadFile(path string) ([]byte, error)`, `FileEntry{Path string; Size int64}`, `Create(path string, opts ...Option) (*Writer, error)`, `Option`, `WithMountPoint(mountPoint string) Option`, `(*Writer).AddFile(mountPath string, data []byte) error`, `(*Writer).Close() error`, and `ErrUnsupportedFormat`

- [x] **Step 1: Split the package history out of lmm** — DONE 2026-08-07

From the lmm checkout, on `develop`:

```bash
git subtree split --prefix=internal/unrealpak -b unrealpak-split
git log --oneline unrealpak-split | head -5   # expect the package's own history, files at root
```

- [x] **Step 2: Create the empty GitHub repo** — DONE 2026-08-07

```bash
gh repo create DonovanMods/go-unrealpak --public \
  --description "Pure-Go reader and writer for Unreal Engine v11 .pak archives, with a CLI."
```

- [x] **Step 3: Push the split history as `main`** — DONE 2026-08-07

```bash
cd ~/Projects/apps
git clone https://github.com/DonovanMods/go-unrealpak.git
cd go-unrealpak
git pull ../linux-mod-manager unrealpak-split
git push -u origin main
ls   # expect: pak.go reader.go reader_test.go roundtrip_test.go writer.go writer_test.go zlib_test.go
```

- [ ] **Step 4: Add go.mod, LICENSE, and CI**

`go.mod`:

```
module github.com/DonovanMods/go-unrealpak

go 1.25
```

Copy `LICENSE` from the lmm checkout verbatim.

`.github/workflows/test.yml`:

```yaml
name: test

on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: gofmt
        run: |
          unformatted=$(gofmt -l .)
          if [ -n "$unformatted" ]; then
            echo "gofmt needed on:" >&2
            echo "$unformatted" >&2
            exit 1
          fi

      - name: go vet
        run: go vet ./...

      - name: go test (race)
        run: go test -race ./...
```

- [ ] **Step 5: Verify the moved package builds and its tests pass unchanged**

Run: `go build ./... && go test -race ./...`
Expected: PASS. These are the tests that already covered this code in lmm; they are the regression gate for the move. If any fails, the move is wrong — do not edit the tests to accommodate it.

- [ ] **Step 6: Verify there are no dependencies**

Run: `go mod tidy && git diff --exit-code go.mod`
Expected: no change, and `go.mod` still has no `require` block.

- [ ] **Step 7: Add a real-file regression test**

Every existing reader test reads a pak this package's own writer just
produced — a closed loop that never sees real cooker output (Zlib block
lists, 1 MiB alignment padding between entries, a populated pruned
directory index). This test breaks that loop. It is skipped unless
`UNREALPAK_TEST_PAK` names a real pak, so CI and contributors without a game
installed are unaffected.

Create `realfile_test.go`:

```go
package unrealpak

import (
	"os"
	"testing"
)

// TestRealPak_DecodesShippedArchive reads a pak produced by a real Unreal
// cooker rather than by this package's writer. Set UNREALPAK_TEST_PAK to a
// shipped .pak to run it, e.g.
//
//	UNREALPAK_TEST_PAK=/path/to/Icarus/Content/Data/data.pak go test -run RealPak -v
func TestRealPak_DecodesShippedArchive(t *testing.T) {
	path := os.Getenv("UNREALPAK_TEST_PAK")
	if path == "" {
		t.Skip("set UNREALPAK_TEST_PAK to a real .pak to run this test")
	}

	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	defer r.Close() //nolint:errcheck

	files := r.Files()
	if len(files) == 0 {
		t.Fatal("real pak decoded to zero entries")
	}
	if r.MountPoint() == "" {
		t.Error("real pak decoded to an empty mount point")
	}
	if r.IndexHash() == "" {
		t.Error("index hash is empty")
	}
	t.Logf("%s: mount %q, %d entries", path, r.MountPoint(), len(files))

	// Read every entry the reader claims to support. Unsupported ones
	// (Oodle) must fail with ErrUnsupportedFormat rather than garbage or a
	// panic; anything else read must match its recorded size, which is the
	// per-entry SHA1 gate doing its job.
	var read, unsupported int
	for _, f := range files {
		data, err := r.ReadFile(f.Path)
		if err != nil {
			if errors.Is(err, ErrUnsupportedFormat) {
				unsupported++
				continue
			}
			t.Errorf("ReadFile(%s): %v", f.Path, err)
			continue
		}
		if int64(len(data)) != f.Size {
			t.Errorf("%s: read %d bytes, index says %d", f.Path, len(data), f.Size)
		}
		read++
	}
	t.Logf("read %d entries, %d unsupported (Oodle)", read, unsupported)
	if read == 0 {
		t.Error("no entry was readable; the reader is not exercising real data")
	}
}
```

Add `"errors"` to the test file's imports.

- [ ] **Step 8: Run the real-file test against a shipped pak**

```bash
UNREALPAK_TEST_PAK=/data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Data/data.pak \
  go test -run RealPak -v
```

Expected: PASS, logging 298 entries with 0 unsupported (that pak is stored +
Zlib only). Then repeat against a pak that _does_ contain Oodle, to prove the
unsupported path is clean rather than merely untaken:

```bash
UNREALPAK_TEST_PAK=/data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Paks/pakchunk0-WindowsNoEditor.pak \
  go test -run RealPak -v
```

Expected: PASS with a non-zero unsupported count and no errors.

- [ ] **Step 9: Confirm the test skips cleanly without the env var**

Run: `go test -run RealPak -v`
Expected: SKIP, not FAIL. CI must stay green on a machine with no game installed.

- [ ] **Step 10: Commit**

```bash
gofmt -w .
git add go.mod LICENSE .github/workflows/test.yml realfile_test.go
git commit -m "chore: add module declaration, license, CI, and a real-file test

Extracted from DonovanMods/linux-mod-manager internal/unrealpak via
git subtree split, preserving the package's history (#170).

The real-file test is env-gated (UNREALPAK_TEST_PAK): every other reader
test reads a pak this package's own writer produced, which never exercises
real cooker output."
git push
```

---

### Task 2: CLI scaffold with `info` and `list`

**Files:**

- Create: `cmd/unrealpak/main.go`, `cmd/unrealpak/commands.go`, `cmd/unrealpak/main_test.go`

**Interfaces:**

- Consumes: `unrealpak.Open`, `(*Reader).Close/MountPoint/IndexHash/Files`, `FileEntry{Path, Size}` from Task 1
- Produces: `run(args []string, out io.Writer) error` — the testable entry point every later CLI task extends with a new `case` in its switch; `openPak(path string) (*unrealpak.Reader, error)`; `sortedFiles(r *unrealpak.Reader) []unrealpak.FileEntry`

- [ ] **Step 1: Write the failing test**

`cmd/unrealpak/main_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/go-unrealpak"
)

// writeFixturePak builds a small real pak with the library itself, so CLI
// tests exercise the same reader path production callers use.
func writeFixturePak(t *testing.T, mount string, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.pak")
	w, err := unrealpak.Create(path, unrealpak.WithMountPoint(mount))
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := w.AddFile(name, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun_List_PrintsSortedMountRelativePaths(t *testing.T) {
	pak := writeFixturePak(t, "../../../Game/Content/", map[string][]byte{
		"b/second.json": []byte("22"),
		"a/first.json":  []byte("1"),
	})

	var out bytes.Buffer
	if err := run([]string{"list", pak}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	first := strings.Index(got, "a/first.json")
	second := strings.Index(got, "b/second.json")
	if first < 0 || second < 0 {
		t.Fatalf("both entries must be listed; got:\n%s", got)
	}
	if first > second {
		t.Errorf("entries must be sorted by path; got:\n%s", got)
	}
}

func TestRun_List_JSONIsMachineReadable(t *testing.T) {
	pak := writeFixturePak(t, "../../../Game/Content/", map[string][]byte{
		"a/first.json": []byte("1"),
	})

	var out bytes.Buffer
	if err := run([]string{"list", "--json", pak}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	var doc struct {
		MountPoint string `json:"mountPoint"`
		Files      []struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
		} `json:"files"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if doc.MountPoint != "../../../Game/Content/" {
		t.Errorf("mountPoint = %q", doc.MountPoint)
	}
	if len(doc.Files) != 1 || doc.Files[0].Path != "a/first.json" || doc.Files[0].Size != 1 {
		t.Errorf("files = %+v", doc.Files)
	}
}

func TestRun_Info_ReportsMountAndEntryCount(t *testing.T) {
	pak := writeFixturePak(t, "../../../Game/Content/", map[string][]byte{
		"a/first.json": []byte("1"),
	})

	var out bytes.Buffer
	if err := run([]string{"info", pak}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	for _, want := range []string{"../../../Game/Content/", "entries", "1"} {
		if !strings.Contains(got, want) {
			t.Errorf("info output missing %q; got:\n%s", want, got)
		}
	}
}

func TestRun_UnknownSubcommand_Errors(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"frobnicate"}, &out)
	if err == nil {
		t.Fatal("unknown subcommand must be an error, not a silent no-op")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("error should name the unknown subcommand; got %v", err)
	}
}

func TestRun_NoArgs_Errors(t *testing.T) {
	var out bytes.Buffer
	if err := run(nil, &out); err == nil {
		t.Fatal("no arguments must be an error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/unrealpak/ -run TestRun -v`
Expected: FAIL — `undefined: run`

- [ ] **Step 3: Write the implementation**

`cmd/unrealpak/main.go`:

```go
// Command unrealpak inspects and builds Unreal Engine v11 .pak archives.
//
// It is deliberately game-agnostic: mount points and entry paths are
// whatever the caller supplies, and no game's conventions are baked in.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "unrealpak:", err)
		os.Exit(1)
	}
}

const usage = `unrealpak - inspect and build Unreal Engine v11 .pak archives

usage:
  unrealpak info    <pak>
  unrealpak list    <pak> [--json]
  unrealpak cat     <pak> <path>
  unrealpak extract <pak> <dir> [--filter <glob>]
  unrealpak build   <dir> <pak> [--mount <mountpoint>]
`

// run is the testable entry point: every subcommand writes its results to
// out rather than os.Stdout so tests can drive the real dispatch path.
func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("no subcommand given\n\n%s", usage)
	}
	switch args[0] {
	case "info":
		return cmdInfo(args[1:], out)
	case "list":
		return cmdList(args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q\n\n%s", args[0], usage)
	}
}
```

`cmd/unrealpak/commands.go`:

```go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/DonovanMods/go-unrealpak"
)

// openPak opens path, wrapping the error with the path so a failure names
// the file the user actually passed.
func openPak(path string) (*unrealpak.Reader, error) {
	r, err := unrealpak.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return r, nil
}

// sortedFiles returns the pak's entries ordered by mount-relative path. The
// index's own order reflects how the pak was written, which is not stable
// across writers; sorting makes CLI output diffable.
func sortedFiles(r *unrealpak.Reader) []unrealpak.FileEntry {
	files := r.Files()
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func cmdInfo(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("info takes exactly one pak path")
	}
	r, err := openPak(fs.Arg(0))
	if err != nil {
		return err
	}
	defer r.Close() //nolint:errcheck

	files := r.Files()
	var total int64
	for _, f := range files {
		total += f.Size
	}
	fmt.Fprintf(out, "mount point: %s\n", r.MountPoint())
	fmt.Fprintf(out, "entries:     %d\n", len(files))
	fmt.Fprintf(out, "total size:  %d bytes\n", total)
	fmt.Fprintf(out, "index hash:  %s\n", r.IndexHash())
	return nil
}

type listJSON struct {
	MountPoint string         `json:"mountPoint"`
	IndexHash  string         `json:"indexHash"`
	Files      []listFileJSON `json:"files"`
}

type listFileJSON struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func cmdList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("list takes exactly one pak path")
	}
	r, err := openPak(fs.Arg(0))
	if err != nil {
		return err
	}
	defer r.Close() //nolint:errcheck

	files := sortedFiles(r)
	if *asJSON {
		doc := listJSON{MountPoint: r.MountPoint(), IndexHash: r.IndexHash(), Files: make([]listFileJSON, 0, len(files))}
		for _, f := range files {
			doc.Files = append(doc.Files, listFileJSON{Path: f.Path, Size: f.Size})
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	for _, f := range files {
		fmt.Fprintf(out, "%10d  %s\n", f.Size, f.Path)
	}
	return nil
}
```

Note `flag.ContinueOnError` with `SetOutput(io.Discard)`: the default `ExitOnError` would call `os.Exit` inside a test.

Note the `--json` flag must precede the pak path, since Go's `flag` stops parsing at the first non-flag argument.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/unrealpak/ -run TestRun -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
gofmt -w ./cmd
git add cmd/unrealpak/
git commit -m "feat(cli): add unrealpak info and list"
git push
```

---

### Task 3: `cat` and `extract`

**Files:**

- Create: `cmd/unrealpak/sidecar.go`
- Modify: `cmd/unrealpak/main.go` (add two `case`s), `cmd/unrealpak/commands.go`, `cmd/unrealpak/main_test.go`

**Interfaces:**

- Consumes: `run`, `openPak`, `sortedFiles` from Task 2; `(*Reader).ReadFile` from Task 1
- Produces: `sidecarName` (const `".unrealpak.json"`), `writeSidecar(dir, mountPoint string) error`, `readSidecar(dir string) (string, error)` — Task 4's `build` reads the mount point back through `readSidecar`

- [ ] **Step 1: Write the failing test**

Append to `cmd/unrealpak/main_test.go`:

```go
func TestRun_Cat_WritesEntryBytesVerbatim(t *testing.T) {
	pak := writeFixturePak(t, "../../../Game/Content/", map[string][]byte{
		"a/first.json": []byte(`{"hello":"world"}`),
	})

	var out bytes.Buffer
	if err := run([]string{"cat", pak, "a/first.json"}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.String() != `{"hello":"world"}` {
		t.Errorf("cat output = %q", out.String())
	}
}

func TestRun_Cat_MissingEntry_Errors(t *testing.T) {
	pak := writeFixturePak(t, "../../../Game/Content/", map[string][]byte{
		"a/first.json": []byte("1"),
	})

	var out bytes.Buffer
	err := run([]string{"cat", pak, "nope.json"}, &out)
	if err == nil {
		t.Fatal("missing entry must be a loud error, never empty output")
	}
	if !strings.Contains(err.Error(), "nope.json") {
		t.Errorf("error should name the missing entry; got %v", err)
	}
}

func TestRun_Extract_WritesFilesAtMountRelativePathsPlusSidecar(t *testing.T) {
	pak := writeFixturePak(t, "../../../Game/Content/", map[string][]byte{
		"a/first.json":  []byte("1"),
		"b/second.json": []byte("22"),
	})
	dir := t.TempDir()

	var out bytes.Buffer
	if err := run([]string{"extract", pak, dir}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	for name, want := range map[string]string{
		"a/first.json":  "1",
		"b/second.json": "22",
	} {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("reading extracted %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	mount, err := readSidecar(dir)
	if err != nil {
		t.Fatalf("readSidecar: %v", err)
	}
	if mount != "../../../Game/Content/" {
		t.Errorf("sidecar mountPoint = %q", mount)
	}
}

func TestRun_Extract_FilterSelectsASubset(t *testing.T) {
	pak := writeFixturePak(t, "../../../Game/Content/", map[string][]byte{
		"a/first.json": []byte("1"),
		"b/second.txt": []byte("22"),
	})
	dir := t.TempDir()

	var out bytes.Buffer
	if err := run([]string{"extract", pak, dir, "--filter", "a/*"}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "a", "first.json")); err != nil {
		t.Errorf("filtered-in file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b", "second.txt")); !os.IsNotExist(err) {
		t.Errorf("filtered-out file should not exist; stat err = %v", err)
	}
}

// A crafted pak could carry an entry path that climbs out of the target
// directory. Extraction must refuse it rather than write outside dir.
func TestRun_Extract_RefusesPathEscape(t *testing.T) {
	dir := t.TempDir()
	if err := checkExtractPath(dir, "../escape.json"); err == nil {
		t.Error("a parent-traversal entry path must be refused")
	}
	if err := checkExtractPath(dir, "/absolute.json"); err == nil {
		t.Error("an absolute entry path must be refused")
	}
	if err := checkExtractPath(dir, "safe/nested.json"); err != nil {
		t.Errorf("an ordinary nested path must be allowed; got %v", err)
	}
}
```

Add `"os"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/unrealpak/ -run 'TestRun_(Cat|Extract)' -v`
Expected: FAIL — `undefined: readSidecar`, `undefined: writeSidecar`, `undefined: checkExtractPath`

- [ ] **Step 3: Write the sidecar helper**

`cmd/unrealpak/sidecar.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// sidecarName is the file extract drops into its output directory to record
// the source pak's mount point, so build can reproduce it without the user
// having to remember and retype it.
const sidecarName = ".unrealpak.json"

type sidecar struct {
	MountPoint string `json:"mountPoint"`
}

func writeSidecar(dir, mountPoint string) error {
	data, err := json.MarshalIndent(sidecar{MountPoint: mountPoint}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sidecarName), append(data, '\n'), 0o644)
}

// readSidecar returns the mount point recorded in dir. A missing sidecar is
// reported as such so build can tell "no recorded mount" apart from "the
// sidecar is corrupt" and ask the user for --mount in the first case only.
func readSidecar(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, sidecarName))
	if err != nil {
		return "", err
	}
	var s sidecar
	if err := json.Unmarshal(data, &s); err != nil {
		return "", fmt.Errorf("parsing %s: %w", sidecarName, err)
	}
	if s.MountPoint == "" {
		return "", fmt.Errorf("%s records no mountPoint", sidecarName)
	}
	return s.MountPoint, nil
}
```

- [ ] **Step 4: Write the two subcommands**

Append to `cmd/unrealpak/commands.go`:

```go
func cmdCat(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("cat", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("cat takes a pak path and an entry path")
	}
	r, err := openPak(fs.Arg(0))
	if err != nil {
		return err
	}
	defer r.Close() //nolint:errcheck

	data, err := r.ReadFile(fs.Arg(1))
	if err != nil {
		return fmt.Errorf("reading %s: %w", fs.Arg(1), err)
	}
	_, err = out.Write(data)
	return err
}

// checkExtractPath refuses an entry path that would write outside dir. Pak
// entry paths are attacker-controlled in any archive the user did not build
// themselves, so this is the extract-side equivalent of a zip-slip guard.
func checkExtractPath(dir, entryPath string) error {
	if filepath.IsAbs(entryPath) || strings.HasPrefix(entryPath, "/") {
		return fmt.Errorf("entry %q: absolute paths are not allowed", entryPath)
	}
	target := filepath.Join(dir, filepath.FromSlash(entryPath))
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return fmt.Errorf("entry %q: %w", entryPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("entry %q: escapes the output directory", entryPath)
	}
	return nil
}

func cmdExtract(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	filter := fs.String("filter", "", "only extract entries whose path matches this glob")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("extract takes a pak path and an output directory")
	}
	pakPath, dir := fs.Arg(0), fs.Arg(1)

	r, err := openPak(pakPath)
	if err != nil {
		return err
	}
	defer r.Close() //nolint:errcheck

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	count := 0
	for _, f := range sortedFiles(r) {
		if *filter != "" {
			ok, err := path.Match(*filter, f.Path)
			if err != nil {
				return fmt.Errorf("bad --filter pattern %q: %w", *filter, err)
			}
			if !ok {
				continue
			}
		}
		if err := checkExtractPath(dir, f.Path); err != nil {
			return err
		}
		data, err := r.ReadFile(f.Path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", f.Path, err)
		}
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		count++
	}

	if err := writeSidecar(dir, r.MountPoint()); err != nil {
		return err
	}
	fmt.Fprintf(out, "extracted %d entries to %s (mount point recorded in %s)\n", count, dir, sidecarName)
	return nil
}
```

Add `"os"`, `"path"`, `"path/filepath"`, and `"strings"` to `commands.go`'s imports.

- [ ] **Step 5: Wire the subcommands into dispatch**

In `cmd/unrealpak/main.go`, add to the switch in `run`, after the `list` case:

```go
	case "cat":
		return cmdCat(args[1:], out)
	case "extract":
		return cmdExtract(args[1:], out)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./cmd/unrealpak/ -v`
Expected: PASS (all tests, including Task 2's)

- [ ] **Step 7: Commit**

```bash
gofmt -w ./cmd
git add cmd/unrealpak/
git commit -m "feat(cli): add unrealpak cat and extract

extract records the source mount point in a .unrealpak.json sidecar so
build can reproduce it, and refuses entry paths that escape the output
directory."
git push
```

---

### Task 4: `build`, and the extract→build round trip

**Files:**

- Modify: `cmd/unrealpak/main.go` (one `case`), `cmd/unrealpak/commands.go`, `cmd/unrealpak/main_test.go`

**Interfaces:**

- Consumes: `run`, `openPak`, `sortedFiles` from Task 2; `readSidecar`, `sidecarName` from Task 3; `unrealpak.Create`, `WithMountPoint`, `(*Writer).AddFile`, `(*Writer).Close` from Task 1
- Produces: `cmdBuild(args []string, out io.Writer) error` — the last subcommand; no later task consumes it

- [ ] **Step 1: Write the failing test**

Append to `cmd/unrealpak/main_test.go`:

```go
func TestRun_Build_ProducesAReadablePakWithTheGivenMount(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a", "first.json"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPak := filepath.Join(t.TempDir(), "built.pak")

	var out bytes.Buffer
	if err := run([]string{"build", src, outPak, "--mount", "../../../Game/Content/"}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	r, err := unrealpak.Open(outPak)
	if err != nil {
		t.Fatalf("built pak is not readable: %v", err)
	}
	defer r.Close()
	if r.MountPoint() != "../../../Game/Content/" {
		t.Errorf("MountPoint = %q", r.MountPoint())
	}
	data, err := r.ReadFile("a/first.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "1" {
		t.Errorf("entry = %q", data)
	}
}

// The sidecar exists so a plain extract->build cycle needs no flags at all.
func TestRun_Build_DefaultsMountFromSidecar(t *testing.T) {
	pak := writeFixturePak(t, "../../../Game/Content/", map[string][]byte{
		"a/first.json":  []byte("1"),
		"b/second.json": []byte("22"),
	})
	dir := t.TempDir()
	rebuilt := filepath.Join(t.TempDir(), "rebuilt.pak")

	var out bytes.Buffer
	if err := run([]string{"extract", pak, dir}, &out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if err := run([]string{"build", dir, rebuilt}, &out); err != nil {
		t.Fatalf("build: %v", err)
	}

	orig, err := unrealpak.Open(pak)
	if err != nil {
		t.Fatal(err)
	}
	defer orig.Close()
	round, err := unrealpak.Open(rebuilt)
	if err != nil {
		t.Fatalf("round-tripped pak is not readable: %v", err)
	}
	defer round.Close()

	if orig.MountPoint() != round.MountPoint() {
		t.Errorf("mount point drifted: %q -> %q", orig.MountPoint(), round.MountPoint())
	}
	if len(orig.Files()) != len(round.Files()) {
		t.Fatalf("entry count drifted: %d -> %d", len(orig.Files()), len(round.Files()))
	}
	for _, f := range orig.Files() {
		want, err := orig.ReadFile(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := round.ReadFile(f.Path)
		if err != nil {
			t.Errorf("round-tripped pak missing %s: %v", f.Path, err)
			continue
		}
		if !bytes.Equal(want, got) {
			t.Errorf("%s: content drifted", f.Path)
		}
	}
}

// The sidecar must never end up inside the pak it describes.
func TestRun_Build_ExcludesTheSidecar(t *testing.T) {
	pak := writeFixturePak(t, "../../../Game/Content/", map[string][]byte{
		"a/first.json": []byte("1"),
	})
	dir := t.TempDir()
	rebuilt := filepath.Join(t.TempDir(), "rebuilt.pak")

	var out bytes.Buffer
	if err := run([]string{"extract", pak, dir}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"build", dir, rebuilt}, &out); err != nil {
		t.Fatal(err)
	}

	r, err := unrealpak.Open(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, f := range r.Files() {
		if f.Path == sidecarName {
			t.Error("the sidecar must not be packed into the pak")
		}
	}
}

func TestRun_Build_NoMountAndNoSidecar_Errors(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "only.json"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := run([]string{"build", src, filepath.Join(t.TempDir(), "x.pak")}, &out)
	if err == nil {
		t.Fatal("build with no --mount and no sidecar must fail loudly, not guess a default")
	}
	if !strings.Contains(err.Error(), "--mount") {
		t.Errorf("error should tell the user how to fix it; got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/unrealpak/ -run TestRun_Build -v`
Expected: FAIL — `unknown subcommand "build"`

- [ ] **Step 3: Write the implementation**

Append to `cmd/unrealpak/commands.go`:

```go
func cmdBuild(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mount := fs.String("mount", "", "mount point to record in the built pak")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("build takes an input directory and an output pak path")
	}
	dir, pakPath := fs.Arg(0), fs.Arg(1)

	// An explicit --mount always wins; the sidecar is only a convenience for
	// the extract->build cycle. Neither present is a hard error: guessing a
	// mount point produces a pak that loads and silently does nothing.
	mountPoint := *mount
	if mountPoint == "" {
		recorded, err := readSidecar(dir)
		if err != nil {
			return fmt.Errorf("no --mount given and no usable %s in %s: %w "+
				"(pass --mount <mountpoint>)", sidecarName, dir, err)
		}
		mountPoint = recorded
	}

	// os.DirEntry, not fs.DirEntry: the flag.FlagSet above is bound to `fs`,
	// so naming the io/fs package here would be shadowed. os.DirEntry is an
	// alias for the same type, so this needs no extra import at all.
	var entries []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == sidecarName {
			return nil // metadata about the pak, never content of it
		}
		entries = append(entries, rel)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("%s contains no files to pack", dir)
	}
	sort.Strings(entries)

	w, err := unrealpak.Create(pakPath, unrealpak.WithMountPoint(mountPoint))
	if err != nil {
		return fmt.Errorf("creating %s: %w", pakPath, err)
	}
	for _, rel := range entries {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			w.Close() //nolint:errcheck
			os.Remove(pakPath)
			return err
		}
		if err := w.AddFile(rel, data); err != nil {
			w.Close() //nolint:errcheck
			os.Remove(pakPath)
			return fmt.Errorf("adding %s: %w", rel, err)
		}
	}
	if err := w.Close(); err != nil {
		os.Remove(pakPath)
		return fmt.Errorf("finalizing %s: %w", pakPath, err)
	}

	fmt.Fprintf(out, "built %s: %d entries, mount point %s\n", pakPath, len(entries), mountPoint)
	return nil
}
```

No new imports are needed for this step — `os`, `sort`, `filepath`, `flag`,
`fmt`, and `io` are already present from Tasks 2 and 3.

The `os.Remove` on every failure path mirrors the fail-clean contract lmm's own compile path uses: a half-written pak left on disk could be picked up and deployed.

- [ ] **Step 4: Wire it into dispatch**

In `cmd/unrealpak/main.go`, add to the switch in `run`:

```go
	case "build":
		return cmdBuild(args[1:], out)
```

- [ ] **Step 5: Run the full CLI suite**

Run: `go test -race ./... -v`
Expected: PASS — all library tests and all CLI tests

- [ ] **Step 6: Commit**

```bash
gofmt -w ./cmd
git add cmd/unrealpak/
git commit -m "feat(cli): add unrealpak build

Mount point comes from --mount or the extract sidecar; with neither, build
fails rather than guessing, since a wrong mount produces a pak that loads
and silently does nothing."
git push
```

---

### Task 5: README and format documentation

**Files:**

- Create: `README.md`, `docs/format.md`

**Interfaces:**

- Consumes: the CLI surface from Tasks 2-4
- Produces: nothing consumed by later tasks

- [ ] **Step 1: Write `docs/format.md`**

Copy the byte-level spec from the lmm checkout at
`docs/plans/archive/icarus-pak-format-findings.md`, keeping Part 1 (footer
layout), Part 2 (primary index, encoded entry bit-packing, full directory
index, path-hash index and its FNV-1a recipe, local headers, data-region
packing), and dropping Part 3 (hosted base-table dumps — an Icarus product
decision with no bearing on the format).

Edit as you copy:

- Remove Icarus-specific framing: this documents UE v11 paks, not Icarus's.
- Keep the empirical numbers and worked examples. They are what makes the
  document trustworthy, and they were verified against 173,078 real entries
  across 34 paks.
- Keep the two corrections already noted in that document (`bEncryptedIndex`
  sits _before_ `Magic`; `data.pak` is Zlib not Oodle). The falsified-twice
  history is a feature of the record, not noise.
- Add a header noting the source: empirical decode against a real UE 4.25+
  title, not Epic documentation.

- [ ] **Step 2: Write `README.md`**

````markdown
# go-unrealpak

Pure-Go reader and writer for Unreal Engine v11 (`PakFile_Version_Fnv64BugFix`)
`.pak` archives, plus a CLI. No cgo, no external dependencies.

Existing tooling is Rust (repak) or Python (u4pak); this exists because a Go
mod manager needed to read and write these archives in-process.

## Scope

|            |                                                                                                         |
| ---------- | ------------------------------------------------------------------------------------------------------- |
| Read       | stored (uncompressed) and Zlib entries                                                                  |
| Write      | stored entries only                                                                                     |
| Oodle      | **not supported** — indexes read fine, but reading an Oodle _payload_ returns an error naming the entry |
| Encryption | not supported; an encrypted index is rejected                                                           |
| Versions   | v10+ (the path-hash index format); v11 is what it writes                                                |

The format documentation is in [docs/format.md](docs/format.md), decoded
empirically and verified against 173,078 entries across 34 real paks.

## CLI

```
go install github.com/DonovanMods/go-unrealpak/cmd/unrealpak@latest
```

```
unrealpak info    <pak>                          # version, mount point, entry count, index hash
unrealpak list    <pak> [--json]                 # entries with sizes, sorted by path
unrealpak cat     <pak> <path>                   # one entry's bytes to stdout
unrealpak extract <pak> <dir> [--filter <glob>]  # entries to dir, + a .unrealpak.json sidecar
unrealpak build   <dir> <pak> [--mount <mount>]  # pack dir; mount defaults to the sidecar's
```

`extract` records the source mount point in `.unrealpak.json` so a plain
`extract` → `build` cycle round-trips without flags. `build` refuses to guess a
mount point: a wrong one produces a pak that loads and silently does nothing.

## Library

```go
r, err := unrealpak.Open("pakchunk0-WindowsNoEditor.pak")
if err != nil {
    return err
}
defer r.Close()

fmt.Println(r.MountPoint(), len(r.Files()))

data, err := r.ReadFile("Engine/Config/Base.ini")
```

```go
w, err := unrealpak.Create("out_P.pak", unrealpak.WithMountPoint("../../../Game/Content/"))
if err != nil {
    return err
}
if err := w.AddFile("data/Thing.json", payload); err != nil {
    return err
}
return w.Close()
```

## License

MIT
````

- [ ] **Step 3: Verify every documented command actually works**

Run each command from the README against a pak you build with the CLI itself:

```bash
go build -o /tmp/unrealpak ./cmd/unrealpak
mkdir -p /tmp/paksrc/data && echo '{}' > /tmp/paksrc/data/Thing.json
/tmp/unrealpak build /tmp/paksrc /tmp/out_P.pak --mount ../../../Game/Content/
/tmp/unrealpak info /tmp/out_P.pak
/tmp/unrealpak list /tmp/out_P.pak --json
/tmp/unrealpak cat /tmp/out_P.pak data/Thing.json
/tmp/unrealpak extract /tmp/out_P.pak /tmp/pakout
```

Expected: every command succeeds; `cat` prints `{}`; `extract` writes
`/tmp/pakout/data/Thing.json` and `/tmp/pakout/.unrealpak.json`. Fix the README
if any documented flag or output differs — the docs must describe what shipped.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/format.md
git commit -m "docs: add README and the empirical v11 pak format spec"
git push
```

---

### Task 6: Tag v0.1.0 and swap lmm's dependency

**Files:**

- Modify (go-unrealpak): none — tag only
- Modify (lmm): `go.mod`, `go.sum`, `internal/core/service.go`, `internal/source/icarus/compile.go`, `internal/source/icarus/merge.go`, `internal/source/icarus/pakconvert.go`, `cmd/lmm/install_compile_test.go`, `internal/core/service_icarus_compile_test.go`, `internal/source/icarus/compile_test.go`, `internal/source/icarus/helpers_test.go`, `internal/source/icarus/merge_test.go`, `internal/source/icarus/pakconvert_test.go`, `internal/tui/health_e2e_test.go`, `internal/tui/service_core_recompile_test.go`
- Delete (lmm): `internal/unrealpak/`

**Interfaces:**

- Consumes: the tagged module from Tasks 1-5
- Produces: lmm building against `github.com/DonovanMods/go-unrealpak`

- [ ] **Step 1: Tag the module**

`v0.1.0` rather than `v1.0.0`: the API is small and settled in practice, but
this is its first release outside lmm and pre-1.0 leaves room to correct the
surface without a major bump.

```bash
cd ~/Projects/apps/go-unrealpak
git tag v0.1.0 && git push origin v0.1.0
```

- [ ] **Step 2: Create the lmm branch**

```bash
cd ~/Projects/apps/linux-mod-manager
git checkout develop && git pull
git checkout -b feat/170-unrealpak-module
```

- [ ] **Step 3: Add the dependency and rewrite every import**

```bash
go get github.com/DonovanMods/go-unrealpak@v0.1.0
grep -rl '"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"' --include='*.go' . \
  | xargs sed -i 's|"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"|"github.com/DonovanMods/go-unrealpak"|'
```

The package name is `unrealpak` in both, so every `unrealpak.Open` /
`unrealpak.Create` / `unrealpak.Reader` reference at the call sites is
unchanged — only the import line moves.

- [ ] **Step 4: Delete the internal copy**

```bash
git rm -r internal/unrealpak
```

- [ ] **Step 5: Verify the swap**

Run: `go mod tidy && gofmt -l ./cmd ./internal && go vet ./... && go test -race ./...`
Expected: PASS, no gofmt output. The icarus and core suites exercise the
library heavily through `Compile`, `MergeCompile`, and `convertPakToBundle`;
they passing against the module is the integration gate for the extraction.

Also confirm nothing still references the old path:

Run: `grep -rn 'internal/unrealpak' --include='*.go' . ; echo "exit=$?"`
Expected: no matches.

- [ ] **Step 6: Add the CHANGELOG entry**

Under `## [Unreleased]`, in the `### Changed` section (create it if absent):

```markdown
- `internal/unrealpak` is now the standalone module
  [`github.com/DonovanMods/go-unrealpak`](https://github.com/DonovanMods/go-unrealpak),
  consumed as a dependency. No behavior change — the package moved verbatim,
  with its history, and gained a `unrealpak` CLI (`info`/`list`/`cat`/
  `extract`/`build`) that lmm does not use. (#170)
```

- [ ] **Step 7: Commit and open the PR**

```bash
git add -A
git commit -m "refactor: consume go-unrealpak as a module instead of internal/unrealpak

The package moved verbatim to github.com/DonovanMods/go-unrealpak via
git subtree split, preserving its history, and lmm now depends on v0.1.0.
Call sites are unchanged: the package name is still unrealpak, so only the
import line moves.

Fixes #170"
git push -u origin feat/170-unrealpak-module
gh pr create --base develop \
  --title "refactor: extract internal/unrealpak into the go-unrealpak module (#170)" \
  --body "Fixes #170.

\`internal/unrealpak\` is now [\`github.com/DonovanMods/go-unrealpak\`](https://github.com/DonovanMods/go-unrealpak) v0.1.0.

## What moved

The package went out verbatim via \`git subtree split\`, keeping its own commit history. No source change: the package name is still \`unrealpak\`, so every call site is untouched and only the import line moves. \`internal/unrealpak/\` is deleted here.

## What the module adds

A \`unrealpak\` CLI (\`info\`/\`list\`/\`cat\`/\`extract\`/\`build\`) that lmm does not use, the empirical v11 format spec as \`docs/format.md\`, and an env-gated real-file test that reads shipped game paks rather than ones the writer produced. The module has zero external dependencies and stays game-agnostic — no Icarus constants crossed over.

## Verification

- \`go mod tidy\`, \`gofmt\`, \`go vet\`, \`go test -race ./...\` all clean
- No remaining references to the old import path
- The icarus and core suites exercise the library through \`Compile\`, \`MergeCompile\`, and \`convertPakToBundle\`; those passing against the module is the integration gate

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

Note the explicit `--base develop`: `main` is the default branch, so a
forgotten flag targets protected `main`.

---

## Follow-ups (not in this plan)

- The Icarus mod-authoring skill in `Icarus-Mods`, which drives this CLI — see
  §3 of the design doc.
- Lowering the `go` directive below 1.25 once tested against an older
  toolchain. The package uses nothing newer than `strings.Cut`, so the real
  floor is likely much lower, but it is unverified.
- Oodle support, if a caller ever needs to read base-game asset payloads.
  `docs/format.md` records where the method table lives.
