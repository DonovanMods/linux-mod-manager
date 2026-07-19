# CurseForge Source Implementation Design

## Overview

Add CurseForge as a mod source, implementing the `ModSource` interface to enable searching, downloading, and managing mods from CurseForge alongside NexusMods.

## CurseForge API

**Base URL:** `https://api.curseforge.com`

**Authentication:** API key via `x-api-key` header. Keys obtained from [CurseForge Console](https://console.curseforge.com/).

**Key differences from NexusMods:**
- Uses numeric game IDs (e.g., 432 for Minecraft) vs slugs
- REST API vs GraphQL
- Different data structures (downloadCount vs endorsements)
- File dependencies embedded in file data vs separate endpoint

## API Endpoints

| Endpoint | Purpose | Notes |
|----------|---------|-------|
| `GET /v1/games` | List available games | For discovery |
| `GET /v1/mods/search?gameId={id}&searchFilter={query}` | Search mods | Max 50 per page |
| `GET /v1/mods/{modId}` | Get mod details | Includes authors, categories |
| `GET /v1/mods/{modId}/files` | List mod files | For download selection |
| `GET /v1/mods/{modId}/files/{fileId}/download-url` | Get CDN URL | Direct download link |

## Response Structures

### Mod Response
```json
{
  "data": {
    "id": 12345,
    "gameId": 432,
    "name": "Just Enough Items (JEI)",
    "slug": "jei",
    "summary": "View Items and Recipes",
    "downloadCount": 1234567,
    "authors": [{"id": 1, "name": "mezz", "url": "..."}],
    "logo": {"thumbnailUrl": "..."},
    "latestFiles": [...],
    "dateModified": "2024-01-01T00:00:00Z"
  }
}
```

### File Response
```json
{
  "data": {
    "id": 54321,
    "modId": 12345,
    "displayName": "jei-1.20.1-15.3.0.4.jar",
    "fileName": "jei-1.20.1-15.3.0.4.jar",
    "fileLength": 1234567,
    "downloadUrl": "https://edge.forgecdn.net/files/...",
    "dependencies": [{"modId": 111, "relationType": 3}]
  }
}
```

## Implementation Structure

```text
internal/source/curseforge/
├── curseforge.go     # ModSource implementation
├── curseforge_test.go
├── client.go         # HTTP client, request/response handling
├── client_test.go
└── types.go          # API response structs
```

## Type Mappings

| CurseForge Field | Domain Field | Notes |
|------------------|--------------|-------|
| `id` | `Mod.ID` | Convert int to string |
| `name` | `Mod.Name` | Direct |
| `summary` | `Mod.Summary` | Direct |
| `authors[0].name` | `Mod.Author` | First author |
| `downloadCount` | `Mod.Downloads` | Use instead of endorsements |
| `logo.thumbnailUrl` | `Mod.PictureURL` | Image URL |
| `dateModified` | `Mod.UpdatedAt` | Parse ISO8601 |
| `latestFiles[0].displayName` | `Mod.Version` | Extract from filename |

## Configuration

### games.yaml
```yaml
games:
  minecraft:
    name: "Minecraft"
    install_path: "~/.minecraft"
    mod_path: "~/.minecraft/mods"
    sources:
      nexusmods: "minecraft"
      curseforge: "432"  # CurseForge game ID

  skyrim-se:
    name: "Skyrim Special Edition"
    install_path: "..."
    sources:
      nexusmods: "skyrimspecialedition"
      # curseforge: "..." # CurseForge ID if available
```

### Auth Token Storage
Store API key in SQLite `auth_tokens` table:
- `source_id`: "curseforge"
- `token_data`: encrypted API key

## CLI Integration

### Auth Commands
```bash
# Login with API key
lmm auth login --source curseforge
# Prompts for API key

# Check auth status
lmm auth status
# Shows: curseforge: authenticated

# Logout
lmm auth logout --source curseforge
```

### Search with Source
```bash
# Search defaults to all configured sources for game
lmm search "jei" --game minecraft

# Filter to specific source
lmm search "jei" --game minecraft --source curseforge
```

## Implementation Steps

### Phase 1: Core Implementation
1. [ ] Create `internal/source/curseforge/types.go` - API response structs
2. [ ] Create `internal/source/curseforge/client.go` - HTTP client
3. [ ] Create `internal/source/curseforge/client_test.go` - Client tests with recorded responses
4. [ ] Create `internal/source/curseforge/curseforge.go` - ModSource interface impl
5. [ ] Create `internal/source/curseforge/curseforge_test.go` - Integration tests

### Phase 2: CLI Integration
6. [ ] Register CurseForge source in `cmd/lmm/main.go`
7. [ ] Update `auth login` to support `--source curseforge`
8. [ ] Update `auth status` to show all sources
9. [ ] Add `--source` flag to search/install commands

### Phase 3: Documentation
10. [ ] Update README.md with CurseForge support
11. [ ] Update docs/configuration.md with curseforge source mapping
12. [ ] Add CurseForge API key instructions

## Testing Strategy

1. **Unit tests** - Mock HTTP client with recorded responses
2. **Integration tests** - Test full ModSource interface
3. **Manual testing** - Real API calls with test games

### Test Data
Use well-known mods for testing:
- Minecraft: JEI (id: 238222), Fabric API (id: 306612)
- Record API responses for reproducible tests

## Error Handling

- API key invalid/expired: `ErrAuthRequired` with helpful message
- Rate limiting (429): Exponential backoff, max 3 retries
- Mod not found (404): `ErrModNotFound`
- Network errors: Wrap with context

## Known Limitations

1. **Game ID mapping** - Users must know CurseForge game IDs
2. **No OAuth** - API key only (simpler but less secure)
3. **Download URLs may expire** - Fetch fresh URL before each download

## Future Enhancements

- Auto-discover CurseForge game IDs from game name
- Support mod dependencies from file data
- Changelog parsing from mod description
