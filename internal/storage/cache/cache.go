package cache

import (
	"fmt"
	"io/fs"
	"io"
	"os"
	"path/filepath"
)

// Cache manages the central mod file cache
type Cache struct {
	basePath   string
	gameScoped bool // when true, basePath is game-specific; omit gameID from ModPath
}

// New creates a new cache manager for the global cache (basePath/gameID/source-mod/version).
func New(basePath string) *Cache {
	return &Cache{basePath: basePath}
}

// NewGameScoped creates a cache for a per-game cache_path.
// Paths are basePath/source-mod/version (no gameID); the base is already game-specific.
func NewGameScoped(basePath string) *Cache {
	return &Cache{basePath: basePath, gameScoped: true}
}

// ModPath returns the path where a mod version's files are stored
func (c *Cache) ModPath(gameID, sourceID, modID, version string) string {
	modKey := fmt.Sprintf("%s-%s", sourceID, modID)
	if c.gameScoped {
		return filepath.Join(c.basePath, modKey, version)
	}
	return filepath.Join(c.basePath, gameID, modKey, version)
}

// Exists checks if a mod version is cached
func (c *Cache) Exists(gameID, sourceID, modID, version string) bool {
	path := c.ModPath(gameID, sourceID, modID, version)
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Store saves a file to the cache
func (c *Cache) Store(gameID, sourceID, modID, version, relativePath string, content []byte) error {
	modPath := c.ModPath(gameID, sourceID, modID, version)
	fullPath := filepath.Join(modPath, relativePath)

	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}

	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		return fmt.Errorf("writing cached file: %w", err)
	}

	return nil
}

// ListFiles returns all files in a cached mod version
func (c *Cache) ListFiles(gameID, sourceID, modID, version string) ([]string, error) {
	modPath := c.ModPath(gameID, sourceID, modID, version)

	var files []string
	err := filepath.WalkDir(modPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip symlinks to avoid traversing outside cache root
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		relPath, err := filepath.Rel(modPath, path)
		if err != nil {
			return err
		}
		files = append(files, relPath)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("listing cached files: %w", err)
	}

	return files, nil
}

// Delete removes a cached mod version
func (c *Cache) Delete(gameID, sourceID, modID, version string) error {
	modPath := c.ModPath(gameID, sourceID, modID, version)
	if err := os.RemoveAll(modPath); err != nil {
		return fmt.Errorf("deleting cached mod: %w", err)
	}
	return nil
}

// GetFilePath returns the full path to a cached file
func (c *Cache) GetFilePath(gameID, sourceID, modID, version, relativePath string) string {
	return filepath.Join(c.ModPath(gameID, sourceID, modID, version), relativePath)
}

// CloneMod copies a cached mod version into another cache.
func (c *Cache) CloneMod(dest *Cache, gameID, sourceID, modID, version string) error {
	files, err := c.ListFiles(gameID, sourceID, modID, version)
	if err != nil {
		return err
	}
	for _, file := range files {
		srcPath := c.GetFilePath(gameID, sourceID, modID, version, file)
		dstPath := dest.GetFilePath(gameID, sourceID, modID, version, file)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return fmt.Errorf("creating destination dir: %w", err)
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

// Size returns the total size of cached files for a mod version
func (c *Cache) Size(gameID, sourceID, modID, version string) (int64, error) {
	modPath := c.ModPath(gameID, sourceID, modID, version)

	var totalSize int64
	err := filepath.WalkDir(modPath, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		totalSize += info.Size()
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("calculating cache size: %w", err)
	}

	return totalSize, nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return fmt.Errorf("creating destination: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copying file: %w", err)
	}
	return nil
}
