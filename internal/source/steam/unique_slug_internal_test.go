package steam

import "testing"

// Unit 9 gate, Minor 9: uniqueSlug disambiguated exactly ONCE and its doc
// comment claimed "one round is always enough" because Steam guarantees the
// app id is unique. The app id is unique, but the disambiguated STRING is
// not: a curated known-games entry (or an earlier candidate) can already
// hold "half-life-70" or "app-70", and the second round never happened - so
// uniqueSlug happily handed back a slug that was already taken, which is
// the one thing it exists to prevent.
func TestUniqueSlug_LoopsUntilTheSlugIsActuallyFree(t *testing.T) {
	tests := []struct {
		name  string
		mod   string
		appID string
		taken map[string]bool
		want  string
	}{
		{
			name:  "free slug is used as is",
			mod:   "Half-Life",
			appID: "70",
			taken: map[string]bool{},
			want:  "half-life",
		},
		{
			name:  "taken slug falls back to the app-id form",
			mod:   "Half-Life",
			appID: "70",
			taken: map[string]bool{"half-life": true},
			want:  "half-life-70",
		},
		{
			name:  "the app-id form itself can be taken",
			mod:   "Half-Life",
			appID: "70",
			taken: map[string]bool{"half-life": true, "half-life-70": true},
			want:  "half-life-70-2",
		},
		{
			name:  "and so can its successors",
			mod:   "Half-Life",
			appID: "70",
			taken: map[string]bool{"half-life": true, "half-life-70": true, "half-life-70-2": true},
			want:  "half-life-70-3",
		},
		{
			name:  "a name deriving to nothing falls to app-<id>",
			mod:   "!!!",
			appID: "70",
			taken: map[string]bool{},
			want:  "app-70",
		},
		{
			name:  "including when that is taken too",
			mod:   "!!!",
			appID: "70",
			taken: map[string]bool{"app-70": true},
			want:  "app-70-2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uniqueSlug(tt.mod, tt.appID, tt.taken); got != tt.want {
				t.Errorf("uniqueSlug(%q, %q) = %q, want %q", tt.mod, tt.appID, got, tt.want)
			}
		})
	}
}
