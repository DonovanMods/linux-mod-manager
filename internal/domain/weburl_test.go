package domain

import "testing"

func TestSafeWebURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"https", "https://www.nexusmods.com/skyrim/mods/1", "https://www.nexusmods.com/skyrim/mods/1"},
		{"http", "http://example.test/mod", "http://example.test/mod"},
		{"mixed-case scheme", "HTTPS://example.test/mod", "HTTPS://example.test/mod"},
		{"surrounding space is trimmed", "  https://example.test/mod \n", "https://example.test/mod"},
		{"empty", "", ""},
		{"blank", "   ", ""},
		{"javascript", "javascript:alert(1)", ""},
		{"javascript with https lookalike", "javascript://https://example.test/%0aalert(1)", ""},
		{"data", "data:text/html,<script>alert(1)</script>", ""},
		{"file", "file:///etc/passwd", ""},
		{"ftp", "ftp://example.test/mod", ""},
		{"relative path", "/mods/1", ""},
		{"scheme-relative", "//example.test/mod", ""},
		{"bare host", "example.test/mod", ""},
		{"https without host", "https:///mod", ""},
		{"https with opaque part", "https:example.test", ""},
		{"embedded control character", "https://exa\x00mple.test/", ""},
		{"embedded newline", "https://example.test/\nx", ""},
		{"unparseable", "https://[::1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SafeWebURL(tt.raw); got != tt.want {
				t.Errorf("SafeWebURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestModPageURL(t *testing.T) {
	if got := (&Mod{SourceURL: "https://example.test/m"}).PageURL(); got != "https://example.test/m" {
		t.Errorf("PageURL() = %q", got)
	}
	if got := (&Mod{SourceURL: "javascript:alert(1)"}).PageURL(); got != "" {
		t.Errorf("PageURL() of an unsafe URL = %q, want empty", got)
	}
	var nilMod *Mod
	if got := nilMod.PageURL(); got != "" {
		t.Errorf("nil Mod PageURL() = %q, want empty", got)
	}
}
