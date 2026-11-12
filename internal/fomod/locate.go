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
// directory; a root-level one wins over wrappers, and two wrappers with
// one each are ErrAmbiguous.
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

	var rootConfig string
	wrapperConfigs := map[string]string{} // lower-case wrapper -> config member
	var wrapperOrder []string
	scripted, omodConfig, omodCRC := false, false, false

	for _, member := range members {
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
				rootConfig = m
				continue
			}
			key := strings.ToLower(prefix)
			if _, ok := wrapperConfigs[key]; !ok {
				wrapperOrder = append(wrapperOrder, key)
				wrapperConfigs[key] = m
			}
		case "script.cs":
			scripted = true
		}
	}

	switch {
	case rootConfig != "":
		return &Installer{ModuleConfig: rootConfig}, nil
	case len(wrapperOrder) == 1:
		cfg := wrapperConfigs[wrapperOrder[0]]
		return &Installer{Root: cfg[:strings.IndexByte(cfg, '/')+1], ModuleConfig: cfg}, nil
	case len(wrapperOrder) > 1:
		return nil, ErrAmbiguous
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
