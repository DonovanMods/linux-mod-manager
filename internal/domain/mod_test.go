package domain

import "testing"

func TestSourceLocal_IsExpectedValue(t *testing.T) {
	if SourceLocal != "local" {
		t.Errorf("SourceLocal = %q, want %q", SourceLocal, "local")
	}
}
