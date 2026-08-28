package domain

// HookConfig defines scripts for a single operation type (install or uninstall)
type HookConfig struct {
	BeforeAll  string `yaml:"before_all" json:"before_all,omitempty"`
	BeforeEach string `yaml:"before_each" json:"before_each,omitempty"`
	AfterEach  string `yaml:"after_each" json:"after_each,omitempty"`
	AfterAll   string `yaml:"after_all" json:"after_all,omitempty"`
}

// IsEmpty returns true if no hooks are configured
func (h HookConfig) IsEmpty() bool {
	return h.BeforeAll == "" && h.BeforeEach == "" && h.AfterEach == "" && h.AfterAll == ""
}

// GameHooks contains all hooks for a game
type GameHooks struct {
	Install   HookConfig `yaml:"install" json:"install"`
	Uninstall HookConfig `yaml:"uninstall" json:"uninstall"`
}

// IsEmpty returns true if no hooks are configured
func (h GameHooks) IsEmpty() bool {
	return h.Install.IsEmpty() && h.Uninstall.IsEmpty()
}
