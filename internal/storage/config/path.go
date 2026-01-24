// Package config provides configuration file parsing and validation.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ParseConfigPath validates a config file path and returns the cleaned path if valid.
// It returns an error if:
//   - The path is empty
//   - The path is not absolute
//   - The path contains parent directory traversal (..)
//   - The file does not exist
//   - The path points to a directory instead of a file
//   - The file does not have a .yaml or .yml extension
func ParseConfigPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("config path cannot be empty")
	}

	if !filepath.IsAbs(path) {
		return "", errors.New("config path must be absolute")
	}

	if strings.Contains(path, "..") {
		return "", errors.New("config path contains invalid traversal")
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("config file does not exist")
		}
		return "", err
	}

	if info.IsDir() {
		return "", errors.New("config path is a directory, not a file")
	}

	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".yaml" && ext != ".yml" {
		return "", errors.New("config file must have .yaml or .yml extension")
	}

	return path, nil
}
