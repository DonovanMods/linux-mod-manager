package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		setup   func(t *testing.T) string // returns path to use
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid absolute path to existing file",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				path := filepath.Join(dir, "config.yaml")
				if err := os.WriteFile(path, []byte("test: value"), 0644); err != nil {
					t.Fatalf("failed to create test file: %v", err)
				}
				return path
			},
			wantErr: false,
		},
		{
			name:    "empty path",
			path:    "",
			wantErr: true,
			errMsg:  "config path cannot be empty",
		},
		{
			name:    "relative path",
			path:    "config.yaml",
			wantErr: true,
			errMsg:  "config path must be absolute",
		},
		{
			name:    "path with parent directory traversal",
			path:    "/etc/../etc/config.yaml",
			wantErr: true,
			errMsg:  "config path contains invalid traversal",
		},
		{
			name: "path to non-existent file",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				return filepath.Join(dir, "nonexistent.yaml")
			},
			wantErr: true,
			errMsg:  "config file does not exist",
		},
		{
			name: "path to directory instead of file",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: true,
			errMsg:  "config path is a directory, not a file",
		},
		{
			name: "path with unsupported extension",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				path := filepath.Join(dir, "config.txt")
				if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
					t.Fatalf("failed to create test file: %v", err)
				}
				return path
			},
			wantErr: true,
			errMsg:  "config file must have .yaml or .yml extension",
		},
		{
			name: "valid path with .yml extension",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				path := filepath.Join(dir, "config.yml")
				if err := os.WriteFile(path, []byte("test: value"), 0644); err != nil {
					t.Fatalf("failed to create test file: %v", err)
				}
				return path
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path
			if tt.setup != nil {
				path = tt.setup(t)
			}

			got, err := ParseConfigPath(path)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseConfigPath(%q) expected error, got nil", path)
					return
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("ParseConfigPath(%q) error = %q, want %q", path, err.Error(), tt.errMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("ParseConfigPath(%q) unexpected error: %v", path, err)
				return
			}

			if got != path {
				t.Errorf("ParseConfigPath(%q) = %q, want %q", path, got, path)
			}
		})
	}
}
