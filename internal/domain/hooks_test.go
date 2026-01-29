package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHookConfig_IsEmpty(t *testing.T) {
	tests := []struct {
		name     string
		config   HookConfig
		expected bool
	}{
		{"all empty", HookConfig{}, true},
		{"has before_all", HookConfig{BeforeAll: "/path/to/script"}, false},
		{"has after_each", HookConfig{AfterEach: "/path/to/script"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config.IsEmpty())
		})
	}
}

func TestGameHooks_IsEmpty(t *testing.T) {
	tests := []struct {
		name     string
		hooks    GameHooks
		expected bool
	}{
		{"all empty", GameHooks{}, true},
		{"has install hook", GameHooks{Install: HookConfig{BeforeAll: "/path"}}, false},
		{"has uninstall hook", GameHooks{Uninstall: HookConfig{AfterAll: "/path"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.hooks.IsEmpty())
		})
	}
}
