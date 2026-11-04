package domain

import (
	"strings"
	"unicode/utf8"
)

// SanitizeLine makes a single-line piece of source-supplied text safe to
// print (#594): a mod's name, author, version, summary, category, file
// name or URL arrives from a remote source verbatim, and an ESC sequence
// in it (CSI cursor movement, an OSC 0 title, an OSC 8 link, ESC[2J) would
// otherwise reach the terminal raw. It removes every C0 control
// (U+0000-U+001F), DEL and every C1 control (U+0080-U+009F), turns a tab,
// newline or carriage return (and a CRLF pair) into one space, removes the
// bidi embedding/override/isolate controls (U+202A-U+202E, U+2066-U+2069)
// that spoof display order, and replaces invalid UTF-8 with U+FFFD. Every
// other rune - CJK, emoji, accents - is kept. Clean input is returned
// unchanged, without allocating.
func SanitizeLine(s string) string { return sanitize(s, false) }

// SanitizeText is SanitizeLine for multi-line source text (a description,
// a changelog): it keeps newlines and tabs, turning a CRLF pair or a lone
// carriage return into a newline, and otherwise removes what SanitizeLine
// removes.
func SanitizeText(s string) string { return sanitize(s, true) }

// sanitize is SanitizeLine (multiline false) and SanitizeText (true).
func sanitize(s string, multiline bool) string {
	if isCleanText(s, multiline) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteRune(utf8.RuneError)
		case r == '\r':
			if i < len(s) && s[i] == '\n' {
				i++ // a CRLF pair is one line break
			}
			if multiline {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		case r == '\n' || r == '\t':
			if multiline {
				b.WriteRune(r)
			} else {
				b.WriteByte(' ')
			}
		case isStrippedRune(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isCleanText reports whether sanitize would return s unchanged, so the
// common case costs one scan and no allocation.
func isCleanText(s string, multiline bool) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			return false
		case r == '\n' || r == '\t':
			if !multiline {
				return false
			}
		case r == '\r' || isStrippedRune(r):
			return false
		}
	}
	return true
}

// isStrippedRune is a rune both sanitizers remove outright: the C0
// controls other than tab/newline/carriage return (handled by the caller),
// DEL, the C1 controls, and the bidi embedding, override and isolate
// controls.
func isStrippedRune(r rune) bool {
	switch {
	case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
		return true
	case r >= 0x7f && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// SanitizeSourceText cleans every display field of a mod a source returned
// (#594): SanitizeLine on the single-line fields, SanitizeText on the
// description, and the same on its dependency references' versions. Ids
// (ID, SourceID, GameID, a dependency's ModID) are left alone: they are
// keys, not display text, and rewriting one would point it at a different
// mod. Safe on a nil receiver.
func (m *Mod) SanitizeSourceText() {
	if m == nil {
		return
	}
	for _, p := range []*string{
		&m.Name, &m.Version, &m.Author, &m.AuthorName, &m.Summary,
		&m.Category, &m.CategoryName, &m.PictureURL, &m.SourceURL, &m.DisplayVersion,
	} {
		*p = SanitizeLine(*p)
	}
	m.Description = SanitizeText(m.Description)
	for i := range m.Dependencies {
		m.Dependencies[i].Version = SanitizeLine(m.Dependencies[i].Version)
	}
}

// SanitizeSourceText is Mod.SanitizeSourceText for an installed row, plus
// the row's own version history (PreviousVersion, SkippedVersion), which
// came from the same source. Safe on a nil receiver.
func (m *InstalledMod) SanitizeSourceText() {
	if m == nil {
		return
	}
	m.Mod.SanitizeSourceText()
	m.PreviousVersion = SanitizeLine(m.PreviousVersion)
	m.SkippedVersion = SanitizeLine(m.SkippedVersion)
}

// SanitizeSourceText cleans a source's downloadable file the way
// Mod.SanitizeSourceText cleans a mod: its name, file name, version and
// category with SanitizeLine, its description with SanitizeText. ID and
// SHA256 are left alone. Safe on a nil receiver.
func (f *DownloadableFile) SanitizeSourceText() {
	if f == nil {
		return
	}
	f.Name = SanitizeLine(f.Name)
	f.FileName = SanitizeLine(f.FileName)
	f.Version = SanitizeLine(f.Version)
	f.Category = SanitizeLine(f.Category)
	f.Description = SanitizeText(f.Description)
}

// SanitizeSourceText cleans an update-check answer: the installed mod it
// is about, the new version and the changelog. Safe on a nil receiver.
func (u *Update) SanitizeSourceText() {
	if u == nil {
		return
	}
	u.InstalledMod.SanitizeSourceText()
	u.NewVersion = SanitizeLine(u.NewVersion)
	u.Changelog = SanitizeText(u.Changelog)
}
