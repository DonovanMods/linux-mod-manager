# P1: Checksum Verification Design

**Date**: 2026-01-28
**Priority**: 1 of 7
**Phase**: Storage Foundation
**Related Issue**: #8

---

## Overview

Calculate MD5 checksum after download, store in database, and provide verification command for cache integrity checking.

**Why this approach**: NexusMods API does not provide MD5 hashes when querying files by ID. The API only supports reverse lookup (find file info BY hash). Pre-download verification is not possible, so we calculate and store checksums for post-download cache integrity.

---

## Data Model

### Database Migration V6

```sql
-- Batch with P2 (conflict detection) in single migration
ALTER TABLE installed_mod_files ADD COLUMN checksum TEXT;
```

### Domain Type

Add to `DownloadableFile` in `internal/domain/mod.go`:

```go
type DownloadableFile struct {
    // ... existing fields ...
    Checksum string  // MD5 hash (populated after download)
}
```

---

## Download Flow

### Modified Downloader Signature

```go
// DownloadResult contains the outcome of a download
type DownloadResult struct {
    Path     string // Final file path
    Size     int64  // Bytes downloaded
    Checksum string // MD5 hash of downloaded file
}

// Download fetches a file and returns its checksum
func (d *Downloader) Download(ctx context.Context, url, destPath string, progressFn ProgressFunc) (*DownloadResult, error)
```

### Implementation

Hash during copy using `TeeReader` (avoids reading file twice):

```go
// Create a TeeReader that writes to both file and hash
hasher := md5.New()
teeReader := io.TeeReader(progressReader, hasher)

_, err = io.Copy(file, teeReader)
// ...
checksum := hex.EncodeToString(hasher.Sum(nil))
```

---

## Installer Integration

After download succeeds:

1. Store checksum in `installed_mod_files` table
2. Display verification success to user

```go
result, err := s.downloader.Download(ctx, downloadURL, archivePath, progressFn)
if err != nil {
    return err
}

// Store checksum with file record
err = s.db.SaveFileChecksum(gameID, profileName, sourceID, modID, fileID, result.Checksum)
```

### User Output

```
Downloading SkyUI-12345-5.2.zip (2.3 MB)...
[████████████████████████████████] 100%
✓ Checksum: a1b2c3d4e5f6...
Extracting...
```

### Skip Flag

```bash
lmm install "skyui" --skip-verify
```

When set, download normally but skip checksum calculation and storage.

---

## Verify Command

### Usage

```bash
# Verify all cached mods for a game
lmm verify --game skyrim-se

# Verify specific mod
lmm verify 12345 --game skyrim-se

# Auto-fix corrupted/missing files
lmm verify --fix --game skyrim-se
```

### Output

```
Verifying 24 cached mods...
✓ SkyUI (12345) - OK
✓ USSEP (266) - OK
✗ RaceMenu (789) - CORRUPTED (expected a1b2c3..., got 9z8y7x...)
✗ ENB Helper (456) - MISSING (cache deleted)
⚠ Old Mod (111) - NO CHECKSUM (installed before v1.0)

2 issues, 1 warning found.
```

### Logic

1. Get installed mods from DB with their checksums
2. For each mod's files:
   - If cache missing → report MISSING
   - If no stored checksum → report NO CHECKSUM (warning)
   - If checksum mismatch → report CORRUPTED
3. With `--fix`: re-download CORRUPTED and MISSING files

---

## Error Handling

### Download Failures

- Network error during download → existing retry logic, no checksum stored
- Partial download → temp file cleaned up, no checksum stored

### Verification Failures (with `--fix`)

```
✗ RaceMenu (789) - CORRUPTED
  Re-downloading...
  [████████████████████████████████] 100%
  ✓ Fixed (new checksum: d4e5f6...)
```

### Edge Cases

| Scenario                         | Behavior                                                              |
| -------------------------------- | --------------------------------------------------------------------- |
| Mod installed before v1.0        | No checksum in DB, verify shows warning, `--fix` downloads and stores |
| Cache manually deleted           | verify shows MISSING, `--fix` re-downloads                            |
| DB has checksum but file missing | Same as above                                                         |
| `--skip-verify` used on install  | No checksum stored, verify shows warning                              |
| Local import (P4)                | Calculate checksum on import, store normally                          |

---

## Files to Modify

| File                                | Changes                                       |
| ----------------------------------- | --------------------------------------------- |
| `internal/storage/db/migrations.go` | V6 migration adding checksum column           |
| `internal/storage/db/files.go`      | `SaveFileChecksum`, `GetFileChecksum` methods |
| `internal/core/downloader.go`       | Return `DownloadResult` with checksum         |
| `internal/core/installer.go`        | Store checksum, support `--skip-verify`       |
| `cmd/lmm/verify.go`                 | New verify command                            |
| `cmd/lmm/install.go`                | Add `--skip-verify` flag                      |

---

## Testing Strategy

### Unit Tests (`internal/core/downloader_test.go`)

- `TestDownload_ReturnsChecksum` - verify MD5 calculated correctly
- `TestDownload_ChecksumMatchesContent` - download known content, verify hash
- `TestDownload_SkipVerify` - verify no checksum when flag set

### Integration Tests (`internal/core/installer_test.go`)

- `TestInstall_StoresChecksum` - verify checksum saved to DB after install
- `TestInstall_SkipVerifyFlag` - verify no checksum with flag

### Verify Command Tests (`cmd/lmm/verify_test.go`)

- `TestVerify_DetectsCorruption` - modify cached file, verify detection
- `TestVerify_DetectsMissing` - delete cached file, verify detection
- `TestVerify_NoChecksum` - old install without checksum, verify warning
- `TestVerify_Fix` - verify re-download on `--fix`

### DB Tests (`internal/storage/db/files_test.go`)

- `TestSaveFileChecksum` - store and retrieve checksum
- `TestMigrationV6` - verify column added correctly

---

## Not Included (YAGNI)

- **Pre-download verification** - API doesn't support it
- **API lookup after download** - Extra calls, limited value
- **SHA256** - MD5 sufficient for corruption detection
- **Checksum in profile export** - Not needed for portability
