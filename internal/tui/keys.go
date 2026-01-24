package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// KeyMap defines keybindings for the TUI
type KeyMap struct {
	mode string
}

// NewKeyMap creates a new keymap for the given mode
func NewKeyMap(mode string) *KeyMap {
	if mode == "" {
		mode = "vim"
	}
	return &KeyMap{mode: mode}
}

// Mode returns the current keybinding mode
func (k *KeyMap) Mode() string {
	return k.mode
}

// IsUp returns true if the key is an "up" navigation key
func (k *KeyMap) IsUp(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeyUp {
		return true
	}
	if k.mode == "vim" && msg.String() == "k" {
		return true
	}
	return false
}

// IsDown returns true if the key is a "down" navigation key
func (k *KeyMap) IsDown(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeyDown {
		return true
	}
	if k.mode == "vim" && msg.String() == "j" {
		return true
	}
	return false
}

// IsLeft returns true if the key is a "left" navigation key
func (k *KeyMap) IsLeft(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeyLeft {
		return true
	}
	if k.mode == "vim" && msg.String() == "h" {
		return true
	}
	return false
}

// IsRight returns true if the key is a "right" navigation key
func (k *KeyMap) IsRight(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeyRight {
		return true
	}
	if k.mode == "vim" && msg.String() == "l" {
		return true
	}
	return false
}

// IsConfirm returns true if the key is a confirm/select key
func (k *KeyMap) IsConfirm(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyEnter || msg.String() == " "
}

// IsCancel returns true if the key is a cancel/back key
func (k *KeyMap) IsCancel(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyEsc
}

// IsQuit returns true if the key is a quit key
func (k *KeyMap) IsQuit(msg tea.KeyMsg) bool {
	return msg.String() == "q" || msg.Type == tea.KeyCtrlC
}

// IsSearch returns true if the key should focus search
func (k *KeyMap) IsSearch(msg tea.KeyMsg) bool {
	return msg.String() == "/"
}

// IsHelp returns true if the key should show help
func (k *KeyMap) IsHelp(msg tea.KeyMsg) bool {
	return msg.String() == "?"
}

// IsDelete returns true if the key is a delete key
func (k *KeyMap) IsDelete(msg tea.KeyMsg) bool {
	return msg.String() == "d" || msg.Type == tea.KeyDelete
}

// IsHome returns true if the key should go to first item
func (k *KeyMap) IsHome(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeyHome {
		return true
	}
	if k.mode == "vim" && msg.String() == "g" {
		return true
	}
	return false
}

// IsEnd returns true if the key should go to last item
func (k *KeyMap) IsEnd(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeyEnd {
		return true
	}
	if k.mode == "vim" && msg.String() == "G" {
		return true
	}
	return false
}

// IsMoveUp returns true if the key should move item up in order
func (k *KeyMap) IsMoveUp(msg tea.KeyMsg) bool {
	return msg.String() == "K"
}

// IsMoveDown returns true if the key should move item down in order
func (k *KeyMap) IsMoveDown(msg tea.KeyMsg) bool {
	return msg.String() == "J"
}

// NavigationHelp returns help text for navigation keys
func (k *KeyMap) NavigationHelp() string {
	if k.mode == "vim" {
		return "j/k: navigate  h/l: change"
	}
	return "↑/↓: navigate  ←/→: change"
}

// FullHelp returns complete help text
func (k *KeyMap) FullHelp() string {
	if k.mode == "vim" {
		return `Navigation:
  j/k     Move down/up
  h/l     Move left/right (in settings)
  g/G     Go to first/last item

Actions:
  enter   Select/Confirm
  space   Toggle/Select
  d       Delete
  /       Search
  ?       Help
  q       Quit

Reorder:
  J/K     Move item down/up`
	}

	return `Navigation:
  ↑/↓     Move up/down
  ←/→     Move left/right (in settings)
  Home    Go to first item
  End     Go to last item

Actions:
  Enter   Select/Confirm
  Space   Toggle/Select
  Delete  Delete
  /       Search
  ?       Help
  q       Quit

Reorder:
  Shift+↓ Move item down
  Shift+↑ Move item up`
}
