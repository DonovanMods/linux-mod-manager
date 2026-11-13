package fomod

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocate(t *testing.T) {
	tests := []struct {
		name    string
		archive string
		members []string
		want    *Installer
		wantErr error
	}{
		{
			name:    "root",
			archive: "mod.7z",
			members: []string{"fomod/", "fomod/ModuleConfig.xml", "fomod/info.xml", "Core/a.esp"},
			want:    &Installer{Root: "", ModuleConfig: "fomod/ModuleConfig.xml"},
		},
		{
			name:    "case and backslashes",
			archive: "mod.zip",
			members: []string{`FOMOD\moduleconfig.XML`, `core\a.esp`},
			want:    &Installer{Root: "", ModuleConfig: "FOMOD/moduleconfig.XML"},
		},
		{
			name:    "one wrapper directory",
			archive: "mod.zip",
			members: []string{"Wrapper/fomod/ModuleConfig.xml", "Wrapper/Core/a.esp"},
			want:    &Installer{Root: "Wrapper/", ModuleConfig: "Wrapper/fomod/ModuleConfig.xml"},
		},
		{
			// #614 (F1 re-review): an unsafe member is never a candidate, so
			// it can neither become Root ("../") nor force ErrAmbiguous.
			name:    "a dot-dot wrapper is not a candidate",
			archive: "mod.zip",
			members: []string{"../fomod/ModuleConfig.xml", "Wrapper/fomod/ModuleConfig.xml"},
			want:    &Installer{Root: "Wrapper/", ModuleConfig: "Wrapper/fomod/ModuleConfig.xml"},
		},
		{
			name:    "a drive-letter wrapper is not a candidate",
			archive: "mod.zip",
			members: []string{"C:/fomod/ModuleConfig.xml", `D:\fomod\ModuleConfig.xml`},
		},
		{
			name:    "a dot-dot member does not shadow the root installer",
			archive: "mod.zip",
			members: []string{"fomod/ModuleConfig.xml", "fomod/../FOMOD/moduleconfig.xml"},
			want:    &Installer{Root: "", ModuleConfig: "fomod/ModuleConfig.xml"},
		},
		{
			name:    "an absolute or NUL member is not a candidate",
			archive: "mod.zip",
			members: []string{"/fomod/ModuleConfig.xml", "fomod\x00/ModuleConfig.xml", "W/fomod/ModuleConfig.xml"},
			want:    &Installer{Root: "W/", ModuleConfig: "W/fomod/ModuleConfig.xml"},
		},
		{
			name:    "two levels deep is not an installer",
			archive: "mod.zip",
			members: []string{"a/b/fomod/ModuleConfig.xml"},
		},
		{
			name:    "root wins over a wrapper",
			archive: "mod.zip",
			members: []string{"fomod/ModuleConfig.xml", "Extra/fomod/ModuleConfig.xml"},
			want:    &Installer{Root: "", ModuleConfig: "fomod/ModuleConfig.xml"},
		},
		{
			name:    "two wrappers are ambiguous",
			archive: "mod.zip",
			members: []string{"A/fomod/ModuleConfig.xml", "B/fomod/ModuleConfig.xml"},
			wantErr: ErrAmbiguous,
		},
		{
			name:    "plain archive",
			archive: "mod.zip",
			members: []string{"Data/a.esp", "readme.txt"},
		},
		{
			name:    "scripted",
			archive: "mod.zip",
			members: []string{"fomod/script.cs", "a.esp"},
			wantErr: ErrScripted,
		},
		{
			name:    "scripted under a wrapper",
			archive: "mod.zip",
			members: []string{`W\Fomod\Script.CS`},
			wantErr: ErrScripted,
		},
		{
			// The XML installer is read; the script beside it is never run.
			name:    "script beside an XML installer",
			archive: "mod.zip",
			members: []string{"fomod/script.cs", "fomod/ModuleConfig.xml"},
			want:    &Installer{Root: "", ModuleConfig: "fomod/ModuleConfig.xml"},
		},
		{
			name:    "omod by name",
			archive: "Old Mod.OMOD",
			members: []string{"config", "data"},
			wantErr: ErrScripted,
		},
		{
			name:    "omod by contents",
			archive: "renamed.zip",
			members: []string{"config", "script", "data.crc", "data"},
			wantErr: ErrScripted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Locate(tt.archive, tt.members)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInstaller_Rel(t *testing.T) {
	in := &Installer{Root: "Wrapper/"}
	rel, ok := in.Rel(`wrapper\Core\a.esp`)
	assert.True(t, ok)
	assert.Equal(t, "Core/a.esp", rel)
	_, ok = in.Rel("Other/a.esp")
	assert.False(t, ok)
	_, ok = in.Rel("Wrapper/")
	assert.False(t, ok, "the root itself is not a member under it")

	root := &Installer{}
	rel, ok = root.Rel("./Core/a.esp")
	assert.True(t, ok)
	assert.Equal(t, "Core/a.esp", rel)
}
