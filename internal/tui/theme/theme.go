package theme

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme contains the shared visual primitives used by TUI views.
type Theme struct {
	Name       string
	Background lipgloss.Color
	Foreground lipgloss.Color
	Accent     lipgloss.Color
	Muted      lipgloss.Color
	Warning    lipgloss.Color
	Danger     lipgloss.Color
	Success    lipgloss.Color

	App        lipgloss.Style
	Title      lipgloss.Style
	Panel      lipgloss.Style
	PanelTitle lipgloss.Style
	Selected   lipgloss.Style
	MutedText  lipgloss.Style
	Help       lipgloss.Style
	Badge      lipgloss.Style
}

// ByName returns a named theme preset.
func ByName(name string) (Theme, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "wizardry":
		return Wizardry(), nil
	case "amber":
		return Amber(), nil
	case "dos":
		return DOS(), nil
	case "green", "phosphor", "green-phosphor":
		return Green(), nil
	default:
		return Theme{}, fmt.Errorf("unknown TUI theme %q", name)
	}
}

func base(name string, foreground, background, accent lipgloss.Color) Theme {
	muted := lipgloss.Color("244")
	warning := lipgloss.Color("11")
	danger := lipgloss.Color("9")
	success := lipgloss.Color("10")

	t := Theme{
		Name:       name,
		Background: background,
		Foreground: foreground,
		Accent:     accent,
		Muted:      muted,
		Warning:    warning,
		Danger:     danger,
		Success:    success,
		App: lipgloss.NewStyle().
			Foreground(foreground).
			Background(background).
			Padding(1, 2),
		Title: lipgloss.NewStyle().
			Foreground(accent).
			Background(background).
			Bold(true),
		Panel: lipgloss.NewStyle().
			Foreground(foreground).
			Background(background).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accent).
			Padding(0, 1),
		PanelTitle: lipgloss.NewStyle().
			Foreground(accent).
			Background(background).
			Bold(true),
		Selected: lipgloss.NewStyle().
			Foreground(background).
			Background(accent).
			Bold(true),
		Badge: lipgloss.NewStyle().
			Foreground(background).
			Background(accent).
			Bold(true).
			Padding(0, 1),
	}

	return t.withMuted(muted)
}

func (t Theme) withMuted(muted lipgloss.Color) Theme {
	t.Muted = muted
	t.MutedText = lipgloss.NewStyle().
		Foreground(muted).
		Background(t.Background)
	t.Help = lipgloss.NewStyle().
		Foreground(muted).
		Background(t.Background)
	return t
}

// Wizardry returns the default RPG party-sheet theme.
func Wizardry() Theme {
	t := base("wizardry", lipgloss.Color("230"), lipgloss.Color("0"), lipgloss.Color("141"))
	t.Warning = lipgloss.Color("215")
	t.Success = lipgloss.Color("150")
	return t
}

// Amber returns a monochrome amber CRT theme.
func Amber() Theme {
	t := base("amber", lipgloss.Color("214"), lipgloss.Color("0"), lipgloss.Color("220"))
	return t.withMuted(lipgloss.Color("172"))
}

// DOS returns a blue DOS utility theme.
func DOS() Theme {
	t := base("dos", lipgloss.Color("15"), lipgloss.Color("18"), lipgloss.Color("51"))
	t.Panel = t.Panel.Border(lipgloss.NormalBorder())
	return t
}

// Green returns a green phosphor terminal theme.
func Green() Theme {
	t := base("green", lipgloss.Color("46"), lipgloss.Color("0"), lipgloss.Color("120"))
	return t.withMuted(lipgloss.Color("70"))
}
