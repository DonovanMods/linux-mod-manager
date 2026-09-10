// Package steamworkshop: this file is the on-disk metadata cache.
//
// The spike's finding was that Valve documents no rate limit for the
// keyless endpoint and reserves the right to change it without notice, so
// caching and backoff are mandatory rather than an optimisation. This is
// the caching half; transport.go is the backoff half.
package steamworkshop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// positiveTTL is how long a successfully-described item is served from
	// disk. Six hours: a Workshop item the user is subscribed to changes
	// rarely, and an update lmm learns about six hours late costs nothing -
	// Steam applies it itself at the next game launch either way.
	positiveTTL = 6 * time.Hour
	// negativeTTL is how long a `result != 1` answer is remembered. One
	// hour, so a delisted item the user still has subscribed cannot hammer
	// the API on every single update check, while a re-listed item comes
	// back the same day.
	negativeTTL = time.Hour
)

// metaCacheDirName is the cache root's subdirectory. The "_" prefix is
// unreachable as a game slug (core.DeriveGameID never emits one), so this
// tree can never collide with the game-scoped mod cache sharing the root.
const metaCacheDirName = "_steamworkshop"

// metaCache stores one GetPublishedFileDetails row per file id under
// <cacheDir>/_steamworkshop/meta/<fileid>.json.
//
// Every failure is silent by design: the cache is an optimisation, and an
// unwritable cache directory must slow lmm down, never break it. A
// zero-value cacheDir disables it entirely (still correct, just chattier).
type metaCache struct {
	dir string
	now func() time.Time

	// mu guards concurrent get/put from one update check's batches. The
	// files themselves are whole-file writes, so a torn read is not
	// possible across processes either.
	mu sync.Mutex
}

func newMetaCache(cacheDir string, now func() time.Time) *metaCache {
	if cacheDir == "" {
		return &metaCache{now: now}
	}
	return &metaCache{dir: filepath.Join(cacheDir, metaCacheDirName, "meta"), now: now}
}

// cacheEntry is the on-disk envelope: the row verbatim plus when it was
// fetched, so the TTL is judged here rather than trusted from the file's
// mtime (which a backup restore or a copy would move).
type cacheEntry struct {
	FetchedAt int64       `json:"fetched_at"`
	Details   itemDetails `json:"details"`
}

// path returns the cache file for fileID, or "" when caching is disabled or
// the id is not a plain published-file id. The id comes from a profile and
// from Steam's own manifest; refusing anything with a path separator or a
// dot keeps a hostile value from escaping the cache tree.
func (c *metaCache) path(fileID string) string {
	if c.dir == "" || fileID == "" {
		return ""
	}
	if strings.ContainsAny(fileID, `/\.`) {
		return ""
	}
	return filepath.Join(c.dir, fileID+".json")
}

// get returns the cached row for fileID when it is still within its TTL -
// six hours for an item Valve described, one hour for one it refused.
func (c *metaCache) get(fileID string) (itemDetails, bool) {
	path := c.path(fileID)
	if path == "" {
		return itemDetails{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return itemDetails{}, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return itemDetails{}, false
	}
	ttl := positiveTTL
	if !entry.Details.available() {
		ttl = negativeTTL
	}
	age := c.now().Sub(time.Unix(entry.FetchedAt, 0))
	if age < 0 || age >= ttl {
		return itemDetails{}, false
	}
	return entry.Details, true
}

// put records d, whether or not Valve described it: a `result != 1` answer
// is exactly the one worth remembering, so a dead item stops costing a
// request per check.
func (c *metaCache) put(d itemDetails) {
	path := c.path(d.PublishedFileID)
	if path == "" {
		return
	}
	data, err := json.Marshal(cacheEntry{FetchedAt: c.now().Unix(), Details: d})
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// Write-then-rename so a concurrent reader never sees a half-written
	// entry and a crash mid-write leaves the previous answer intact.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".meta-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
	}
}
