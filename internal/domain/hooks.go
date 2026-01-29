package domain

// HookConfig defines scripts for a single operation type (install or uninstall)
type HookConfig struct {
	BeforeAll  string `yaml:"before_all"`
	BeforeEach string `yaml:"before_each"`
	AfterEach  string `yaml:"after_each"`
	AfterAll   string `yaml:"after_all"`
}

// IsEmpty returns true if no hooks are configured
func (h HookConfig) IsEmpty() bool {
	return h.BeforeAll == "" && h.BeforeEach == "" && h.AfterEach == "" && h.AfterAll == ""
}

// GameHooks contains all hooks for a game
type GameHooks struct {
	Install   HookConfig `yaml:"install"`
	Uninstall HookConfig `yaml:"uninstall"`
}

// IsEmpty returns true if no hooks are configured
func (h GameHooks) IsEmpty() bool {
	return h.Install.IsEmpty() && h.Uninstall.IsEmpty()
}
