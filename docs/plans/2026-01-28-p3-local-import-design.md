# P3 Local Mod Import Design

**Date**: 2026-01-28
**Priority**: P3 (after Checksum Verification and Conflict Detection)
**Goal**: Allow users to install mods from local archive files downloaded manually

---

## Overview

Enable importing mods from local archive files (zip, 7z, rar) without going through NexusMods download flow. Supports auto-linking to NexusMods for update tracking when filename matches their naming pattern.

## Command Interface

```bash
lmm import <archive-path> [--game <game-id>] [--profile <name>] [--force]
```

**Arguments:**

- `archive-path` - Path to local archive file (required)

**Flags:**

- `--game, -g` - Target game (optional if default game is set)
- `--profile, -p` - Target profile (default: "default")
- `--source, -s` - Explicit source for linking (default: auto-detect or "local")
- `--id` - Explicit mod ID for linking to NexusMods
- `--force, -f` - Install without conflict prompts

**Examples:**

```bash
lmm import ~/Downloads/SkyUI-12604-5-2SE.zip --game skyrim-se
lmm import mod.7z                              # uses default game
lmm import mod.zip --id 12345 --source nexusmods  # explicit linking
```

## Design Decisions

| Decision             | Choice                                              | Rationale                               |
| -------------------- | --------------------------------------------------- | --------------------------------------- |
| Mod identity         | Smart filename parsing with fallback                | Best UX - auto-links when possible      |
| Mod name             | Extracted folder name, fallback to archive basename | Folder names are often more descriptive |
| Version              | Parse from filename, fallback to "unknown"          | Automatic, no user friction             |
| Original archive     | Leave untouched                                     | Safest, least surprising                |
| Local source type    | Hardcoded string constant                           | No-op interface is a code smell         |
| Multi-file support   | Single file only                                    | Simple, shell tools handle batch        |
| Archive path storage | Don't store                                         | Avoids stale references                 |

## Filename Parsing

NexusMods downloads follow pattern: `ModName-ModID-Version.ext`

**Examples:**

- `SkyUI_5_2_SE-12604-5-2SE.zip` → mod_id: 12604, version: 5.2SE
- `Unofficial Skyrim Special Edition Patch-266-4-3-0a-1725557498.7z` → mod_id: 266, version: 4.3.0a
- `SKSE64-30379-2-2-6-1703618069.7z` → mod_id: 30379, version: 2.2.6

**Parsing logic:**

1. Strip extension
2. Regex match: `^(.+)-(\d+)-(.+)$` (name, mod ID, version)
3. Strip trailing timestamps (10+ digit suffixes)
4. Normalize version: replace `-` with `.`

**Fallbacks:**

- No pattern match → source_id: "local", mod_id: generated UUID
- No version detected → version: "unknown"

## Local Source Handling

No `LocalSource` interface implementation. Just a string constant:

```go
// In internal/domain/mod.go
const SourceLocal = "local"
```

Handling in code:

- Import command sets `mod.SourceID = domain.SourceLocal`
- Update checker skips mods where `SourceID == domain.SourceLocal`
- List command displays "(local)" for these mods

## Import Flow

1. Validate archive exists and format is supported (.zip, .7z, .rar)
2. Parse filename for NexusMods pattern (or use explicit `--id`/`--source`)
3. If linked to NexusMods, fetch mod metadata via API for accurate name/info
4. Extract archive to temp directory
5. Detect mod name from top-level folder structure
6. Move extracted files to cache location
7. Check for conflicts (use existing `installer.GetConflicts()`)
8. If conflicts and no `--force`, prompt user
9. Deploy via existing `installer.Install()`
10. Save to database and profile

## New Files

### `cmd/lmm/import.go`

Import command implementation with flag handling and user interaction.

### `internal/core/filename_parser.go`

```go
// ParsedFilename contains extracted info from a NexusMods-style filename
type ParsedFilename struct {
    ModID    string
    Version  string
    BaseName string
}

// ParseNexusModsFilename attempts to extract mod ID and version
func ParseNexusModsFilename(filename string) *ParsedFilename

// DetectModName returns a display name from extracted archive contents
func DetectModName(extractedPath, archiveBasename string) string
```

## Modified Files

### `internal/core/service.go`

Add `ImportMod()` method:

```go
type ImportOptions struct {
    SourceID    string
    ModID       string
    ProfileName string
}

type ImportResult struct {
    Mod            *domain.Mod
    FilesExtracted int
    LinkedSource   string
    AutoDetected   bool
}

func (s *Service) ImportMod(ctx context.Context, archivePath string, game *domain.Game, opts ImportOptions) (*ImportResult, error)
```

### `internal/domain/mod.go`

Add constant:

```go
const SourceLocal = "local"
```

## Testing Strategy

**Unit tests:**

- `filename_parser_test.go` - table-driven tests for filename patterns
- `service_test.go` - ImportMod with various scenarios

**Integration tests:**

- End-to-end import with temp directories
- Conflict handling verification
- NexusMods linking verification

**Test fixtures:**

- Small test archives in `testdata/`
- Mock NexusMods responses for linked imports

## Edge Cases

- Archive with no top-level folder → use archive basename for name
- Archive contains only one file (not an archive structure) → treat as single-file mod
- NexusMods API unavailable when linked → warn and continue with parsed info
- Mod ID collision with existing local mod → generate new UUID
- Unsupported archive format → clear error message

## Future Considerations (Not in Scope)

- Directory watching for auto-import
- Batch import from directory
- Archive path storage for re-import
- Local mod "update" via file replacement
