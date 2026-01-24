package cache

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Cache manages the central mod file cache
type Cache struct {
	basePath string
}

// New creates a new cache manager
func New(basePath string) *Cache {
	return &Cache{basePath: basePath}
}

// ModPath returns the path where a mod version's files are stored
func (c *Cache) ModPath(gameID, sourceID, modID, version string) string {
	return filepath.Join(c.basePath, gameID, fmt.Sprintf("%s-%s", sourceID, modID), version)
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
