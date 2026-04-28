package theme

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestByNameReturnsPresets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
	}{
		{name: "", want: "wizardry"},
		{name: "wizardry", want: "wizardry"},
		{name: "amber", want: "amber"},
		{name: "dos", want: "dos"},
		{name: "green", want: "green"},
		{name: "phosphor", want: "green"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
