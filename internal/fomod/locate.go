package fomod

import (
	"strings"
)

// Installer is where an archive's FOMOD lives.
type Installer struct {
	// Root is the directory holding fomod/, as the archive spells it, with
	// a trailing '/': "" at the archive root, or one wrapper directory.
	// Every FileItem.Source is relative to it.
	Root string
	// ModuleConfig is the ModuleConfig.xml member's path, '/'-separated.
	ModuleConfig string
}

// Rel returns member's path relative to the installer root, '/'-separated,
// and whether member lies under the root at all. The root prefix matches
// case-insensitively.
func (in *Installer) Rel(member string) (string, bool) {
	m := normalizeMember(member)
	if m == "" || strings.HasSuffix(m, "/") {
		return "", false
	}
	if len(m) < len(in.Root) || !strings.EqualFold(m[:len(in.Root)], in.Root) {
		return "", false
	}
	rel := m[len(in.Root):]
	return rel, rel != ""
}

// Locate finds the FOMOD in an archive from its file name and member
// names, without reading any member. A FOMOD is fomod/ModuleConfig.xml
// (case-insensitively) at the archive root or under exactly one wrapper
// directory. A root-level one wins over wrappers. Two candidates at the
// same level, even ones differing only in case ("fomod/ModuleConfig.xml"
// and "FOMOD/moduleconfig.xml", or wrappers "A/" and "a/"), are
// ErrAmbiguous: which one an archive tool would extract is not knowable.
//
// Members Evaluate would drop as unsafe (Result.UnsafeMembers) are skipped
// here too, so Root is always a safe relative prefix.
//
// It returns (nil, nil) for an archive that is not an installer, and
// ErrScripted for an OMOD (by its .omod name, or its config + .crc members)
// or for fomod/script.cs with no ModuleConfig.xml beside it. An archive
// carrying both script.cs and ModuleConfig.xml is installed from the XML:
// the script is never run either way.
func Locate(archiveName string, members []string) (*Installer, error) {
	if strings.HasSuffix(strings.ToLower(archiveName), ".omod") {
		return nil, ErrScripted
	}

	// Candidates by exact (normalised) member path, so case variants are
	// distinct candidates and a member listed twice is one.
	rootConfigs := map[string]bool{}
	wrapperConfigs := map[string]bool{}
	scripted, omodConfig, omodCRC := false, false, false

	for _, member := range members {
		// A member Evaluate would drop (resolve.go's unsafeMember: a ".."
		// segment, an absolute or drive-letter path, a NUL) is never a
		// candidate either, so it can neither become Root ("../") nor make
		// a sound installer ErrAmbiguous (#614).
		if unsafeMember(member) {
			continue
		}
		m := normalizeMember(member)
		if m == "" || strings.HasSuffix(m, "/") {
			continue
		}
		lower := strings.ToLower(m)
		switch lower {
		case "config":
			omodConfig = true
		case "data.crc", "plugins.crc":
			omodCRC = true
		}

		parts := strings.Split(lower, "/")
		var prefix string
		switch {
		case len(parts) == 2 && parts[0] == "fomod":
		case len(parts) == 3 && parts[1] == "fomod":
			prefix = m[:strings.IndexByte(m, '/')+1]
		default:
			continue
		}
		switch parts[len(parts)-1] {
		case "moduleconfig.xml":
			if prefix == "" {
				rootConfigs[m] = true
			} else {
				wrapperConfigs[m] = true
			}
		case "script.cs":
			scripted = true
		}
	}

	switch {
	case len(rootConfigs) > 1:
		return nil, ErrAmbiguous
	case len(rootConfigs) == 1:
		return &Installer{ModuleConfig: only(rootConfigs)}, nil
	case len(wrapperConfigs) > 1:
		return nil, ErrAmbiguous
	case len(wrapperConfigs) == 1:
		cfg := only(wrapperConfigs)
		return &Installer{Root: cfg[:strings.IndexByte(cfg, '/')+1], ModuleConfig: cfg}, nil
	case scripted, omodConfig && omodCRC:
		return nil, ErrScripted
	}
	return nil, nil
}

// normalizeMember turns an archive member name into '/'-separated form
// without a leading "./" or '/'. A trailing '/' (a directory entry) is kept.
func normalizeMember(member string) string {
	m := strings.ReplaceAll(member, `\`, "/")
	for {
		switch {
		case strings.HasPrefix(m, "./"):
			m = m[2:]
		case strings.HasPrefix(m, "/"):
			m = m[1:]
		default:
			return m
		}
	}
}

// only returns the one key of a single-entry set.
func only(set map[string]bool) string {
	for k := range set {
		return k
	}
	return ""
}
