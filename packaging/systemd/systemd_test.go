package systemd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// goreleaserConfig is the release configuration, relative to this package.
const goreleaserConfig = "../../.goreleaser.yaml"

// packagedPath is where every package installs the unit.
const packagedPath = "/usr/lib/systemd/user/" + ServeUnitName

// TestGoreleaserShipsServeUnit keeps lmm-serve.service in every package
// (#567): the nfpm contents (deb, rpm, apk) and the AUR package() both
// install this directory's file to /usr/lib/systemd/user, and neither
// enables it - the user opts in.
func TestGoreleaserShipsServeUnit(t *testing.T) {
	raw, err := os.ReadFile(goreleaserConfig)
	require.NoError(t, err)
	var cfg struct {
		NFPMs []struct {
			Contents []struct {
				Src string `yaml:"src"`
				Dst string `yaml:"dst"`
			} `yaml:"contents"`
			Scripts map[string]string `yaml:"scripts"`
		} `yaml:"nfpms"`
		AURs []struct {
			Package string `yaml:"package"`
			Install string `yaml:"install"`
		} `yaml:"aurs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))

	src := filepath.ToSlash(filepath.Join("packaging", "systemd", ServeUnitName))
	require.NotEmpty(t, cfg.NFPMs)
	for _, n := range cfg.NFPMs {
		found := false
		for _, c := range n.Contents {
			if c.Src == src && c.Dst == packagedPath {
				found = true
			}
		}
		assert.True(t, found, "nfpms contents: src %s, dst %s", src, packagedPath)
		assert.Empty(t, n.Scripts, "no install script: the unit ships disabled")
	}

	require.NotEmpty(t, cfg.AURs)
	for _, a := range cfg.AURs {
		assert.Contains(t, a.Package, `install -Dm644 "./`+src+`" "${pkgdir}`+packagedPath+`"`, "aurs package()")
		assert.Empty(t, a.Install, "no .install script: the unit ships disabled")
		assert.NotContains(t, a.Package, "systemctl", "the unit ships disabled")
	}
}

// TestArchiveCarriesTheUnitSource: the AUR package() installs from the
// release tarball it downloads, so the tarball must carry the unit at the
// path package() names.
func TestArchiveCarriesTheUnitSource(t *testing.T) {
	raw, err := os.ReadFile(goreleaserConfig)
	require.NoError(t, err)
	var cfg struct {
		Archives []struct {
			Files []string `yaml:"files"`
		} `yaml:"archives"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	require.NotEmpty(t, cfg.Archives)
	for _, a := range cfg.Archives {
		assert.Contains(t, strings.Join(a.Files, "\n"), "packaging/systemd/"+ServeUnitName)
	}
}
