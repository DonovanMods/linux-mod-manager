package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mkdir creates dir (and parents) or fails the test.
func mkdir(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0755))
}

// TestResolvePaths pins the resolution order: explicit override, then XDG base
// directories (absolute values only), then the legacy ~/.config and
// ~/.local/share defaults — with a legacy fallback so installs that predate
// XDG support keep finding their files.
func TestResolvePaths(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, home string) Options
		want  func(home string) Paths
	}{
		{
			name: "defaults when no XDG variables are set",
			setup: func(t *testing.T, home string) Options {
				t.Setenv("XDG_CONFIG_HOME", "")
				t.Setenv("XDG_DATA_HOME", "")
				return Options{}
			},
			want: func(home string) Paths {
				return Paths{
					ConfigDir: filepath.Join(home, ".config", "lmm"),
					DataDir:   filepath.Join(home, ".local", "share", "lmm"),
					CacheDir:  filepath.Join(home, ".local", "share", "lmm", "cache"),
				}
			},
		},
		{
			name: "XDG variables win when nothing exists yet",
			setup: func(t *testing.T, home string) Options {
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
				t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
				return Options{}
			},
			want: func(home string) Paths {
				return Paths{
					ConfigDir: filepath.Join(home, "xdg-config", "lmm"),
					DataDir:   filepath.Join(home, "xdg-data", "lmm"),
					CacheDir:  filepath.Join(home, "xdg-data", "lmm", "cache"),
				}
			},
		},
		{
			name: "legacy directories win when they exist and the XDG ones do not",
			setup: func(t *testing.T, home string) Options {
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
				t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
				mkdir(t, filepath.Join(home, ".config", "lmm"))
				mkdir(t, filepath.Join(home, ".local", "share", "lmm"))
				return Options{}
			},
			want: func(home string) Paths {
				return Paths{
					ConfigDir: filepath.Join(home, ".config", "lmm"),
					DataDir:   filepath.Join(home, ".local", "share", "lmm"),
					CacheDir:  filepath.Join(home, ".local", "share", "lmm", "cache"),
				}
			},
		},
		{
			name: "XDG directories win when both exist",
			setup: func(t *testing.T, home string) Options {
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
				t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
				mkdir(t, filepath.Join(home, ".config", "lmm"))
				mkdir(t, filepath.Join(home, ".local", "share", "lmm"))
				mkdir(t, filepath.Join(home, "xdg-config", "lmm"))
				mkdir(t, filepath.Join(home, "xdg-data", "lmm"))
				return Options{}
			},
			want: func(home string) Paths {
				return Paths{
					ConfigDir: filepath.Join(home, "xdg-config", "lmm"),
					DataDir:   filepath.Join(home, "xdg-data", "lmm"),
					CacheDir:  filepath.Join(home, "xdg-data", "lmm", "cache"),
				}
			},
		},
		{
			name: "relative XDG values are ignored per the spec",
			setup: func(t *testing.T, home string) Options {
				t.Setenv("XDG_CONFIG_HOME", "relative/config")
				t.Setenv("XDG_DATA_HOME", "relative/data")
				return Options{}
			},
			want: func(home string) Paths {
				return Paths{
					ConfigDir: filepath.Join(home, ".config", "lmm"),
					DataDir:   filepath.Join(home, ".local", "share", "lmm"),
					CacheDir:  filepath.Join(home, ".local", "share", "lmm", "cache"),
				}
			},
		},
		{
			name: "explicit overrides beat everything",
			setup: func(t *testing.T, home string) Options {
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
				t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
				return Options{
					ConfigDir: filepath.Join(home, "explicit-config"),
					DataDir:   filepath.Join(home, "explicit-data"),
				}
			},
			want: func(home string) Paths {
				return Paths{
					ConfigDir: filepath.Join(home, "explicit-config"),
					DataDir:   filepath.Join(home, "explicit-data"),
					CacheDir:  filepath.Join(home, "explicit-data", "cache"),
				}
			},
		},
		{
			name: "cache_path in config.yaml overrides the cache location",
			setup: func(t *testing.T, home string) Options {
				t.Setenv("XDG_CONFIG_HOME", "")
				t.Setenv("XDG_DATA_HOME", "")
				cfgDir := filepath.Join(home, ".config", "lmm")
				mkdir(t, cfgDir)
				require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"),
					[]byte("cache_path: "+filepath.Join(home, "big-disk", "lmm-cache")+"\n"), 0644))
				return Options{}
			},
			want: func(home string) Paths {
				return Paths{
					ConfigDir: filepath.Join(home, ".config", "lmm"),
					DataDir:   filepath.Join(home, ".local", "share", "lmm"),
					CacheDir:  filepath.Join(home, "big-disk", "lmm-cache"),
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home) // os.UserHomeDir reads $HOME on Linux
			opts := tt.setup(t, home)

			got, err := ResolvePaths(opts)
			require.NoError(t, err)
			assert.Equal(t, tt.want(home), got)
		})
	}
}

// TestResolvePaths_ExplicitDirsDoNotRequireHome pins #277: a fully
// flag-driven invocation (containers, CI) must not need $HOME.
func TestResolvePaths_ExplicitDirsDoNotRequireHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	cfg := t.TempDir()
	data := t.TempDir()

	got, err := ResolvePaths(Options{ConfigDir: cfg, DataDir: data})
	require.NoError(t, err)
	assert.Equal(t, Paths{ConfigDir: cfg, DataDir: data, CacheDir: filepath.Join(data, "cache")}, got)
}
