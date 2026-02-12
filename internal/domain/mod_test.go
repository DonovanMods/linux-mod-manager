package domain

import "testing"

func TestSourceLocal_IsExpectedValue(t *testing.T) {
	if SourceLocal != "local" {
		t.Errorf("SourceLocal = %q, want %q", SourceLocal, "local")
	}
}

func TestModKey(t *testing.T) {
	tests := []struct {
		sourceID string
		modID    string
		want     string
	}{
		{"nexusmods", "12345", "nexusmods:12345"},
		{"curseforge", "abc", "curseforge:abc"},
		{"local", "uuid-here", "local:uuid-here"},
		{"", "", ":"},
	}

	for _, tt := range tests {
		got := ModKey(tt.sourceID, tt.modID)
		if got != tt.want {
			t.Errorf("ModKey(%q, %q) = %q, want %q", tt.sourceID, tt.modID, got, tt.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1   string
		v2   string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"1.0", "1.0.0", 0},
		{"2.0", "1.9.9", 1},
		{"v1.2.3", "1.2.3", 0},
		{"V1.2.3", "1.2.3", 0},
		{"1.0.0-beta", "1.0.0", 0},
		{"1.2", "1.10", -1},
		{"", "", 0},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.v1, tt.v2)
		if got != tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
		}
	}
}

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		current string
		new     string
		want    bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.0.1", "1.0.0", false},
		{"1.0.0", "1.0.0", false},
		{"1.0", "2.0", true},
	}

	for _, tt := range tests {
		got := IsNewerVersion(tt.current, tt.new)
		if got != tt.want {
			t.Errorf("IsNewerVersion(%q, %q) = %v, want %v", tt.current, tt.new, got, tt.want)
		}
	}
}
