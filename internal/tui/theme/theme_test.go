package theme

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestByNameReturnsPresets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		caseName string
		name     string
		want     string
	}{
		{caseName: "default", name: "", want: "wizardry"},
		{caseName: "wizardry", name: "wizardry", want: "wizardry"},
		{caseName: "amber", name: "amber", want: "amber"},
		{caseName: "dos", name: "dos", want: "dos"},
		{caseName: "green", name: "green", want: "green"},
		{caseName: "phosphor alias", name: "phosphor", want: "green"},
	}

	for _, tt := range tests {
		t.Run(tt.caseName, func(t *testing.T) {
			theme, err := ByName(tt.name)
			require.NoError(t, err)
			require.Equal(t, tt.want, theme.Name)
		})
	}
}

func TestByNameRejectsUnknownTheme(t *testing.T) {
	t.Parallel()

	_, err := ByName("cursed-rainbow")
	require.Error(t, err)
}
