package fomod

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// OpKind is an install operation's kind.
type OpKind int

// The install operation kinds.
const (
	// OpCopy copies Source to Destination.
	OpCopy OpKind = iota
	// OpMkdir creates the directory Destination.
	OpMkdir
)

// Op is one install operation. Paths are '/'-separated.
type Op struct {
	// Kind is the operation.
	Kind OpKind
	// Source is the archive member, relative to the installer root, spelled
	// as Request.Members spells it; empty for OpMkdir.
	Source string
	// Destination is relative to the mod root. Its case comes from the XML
	// (and, under a <folder>, from the member), as in Vortex.
	Destination string
	// Priority is the winning entry's effective priority: its own priority,
	// offset by -1e9 for required files and +1e9 for conditional installs.
	Priority int64
}

// SourceProblem says why a source matched nothing installable.
type SourceProblem int

// The source problems.
const (
	// SourceNotFound: no member matches the source.
	SourceNotFound SourceProblem = iota
	// SourceIsDirectory: a <file> source names a directory.
	SourceIsDirectory
)

// MissingSource is a <file> or <folder> whose source matched nothing. On a
// fresh install Vortex only warns; whether lmm refuses is the caller's call.
type MissingSource struct {
	// Source is the entry's source, as written.
	Source string
	// Folder is true for a <folder> entry.
	Folder bool
	// Reason says what was wrong.
	Reason SourceProblem
}

type member struct {
	key  string // lower-case
	path string // as given, cleaned
}

type resolver struct {
	byKey   map[string]string
	sorted  []member
	copies  map[string]*Op
	dirs    map[string]*Op
	missing []MissingSource
	warn    func(string)
	count   int
}

func newResolver(members []string, warn func(string)) *resolver {
	r := &resolver{byKey: map[string]string{}, copies: map[string]*Op{}, dirs: map[string]*Op{}, warn: warn}
	for _, m := range members {
		n := normalizeMember(m)
		if n == "" || strings.HasSuffix(n, "/") {
			continue
		}
		clean := collapse(n)
		if clean == "" {
			continue
		}
		key := strings.ToLower(clean)
		// Two members differing only in case: keep one, deterministically.
		if prev, ok := r.byKey[key]; ok && prev <= clean {
			continue
		}
		r.byKey[key] = clean
	}
	for key, p := range r.byKey {
		r.sorted = append(r.sorted, member{key: key, path: p})
	}
	sort.Slice(r.sorted, func(i, j int) bool { return r.sorted[i].key < r.sorted[j].key })
	return r
}

// under returns the members inside directory key (lower-case, no trailing
// '/'); "" is the root.
func (r *resolver) under(key string) []member {
	prefix := key
	if prefix != "" {
		prefix += "/"
	}
	i := sort.Search(len(r.sorted), func(i int) bool { return r.sorted[i].key >= prefix })
	j := i
	for j < len(r.sorted) && strings.HasPrefix(r.sorted[j].key, prefix) {
		j++
	}
	return r.sorted[i:j]
}

// install expands one file-list entry into operations.
func (r *resolver) install(item FileItem, offset int64) error {
	priority := int64(item.Priority) + offset
	source := collapse(normalizeMember(item.Source))
	rawDest := item.Source
	if item.HasDestination {
		rawDest = item.Destination
	}
	dest, err := cleanDest(rawDest)
	if err != nil {
		return err
	}

	if item.Folder {
		srcKey := strings.ToLower(source)
		found := r.under(srcKey)
		if len(found) == 0 {
			r.missing = append(r.missing, MissingSource{Source: item.Source, Folder: true, Reason: SourceNotFound})
			return nil
		}
		depth := 0
		if source != "" {
			depth = strings.Count(source, "/") + 1
		}
		for _, m := range found {
			rel := strings.Join(strings.Split(m.path, "/")[depth:], "/")
			if err := r.addCopy(m.path, dest+"/"+rel, priority); err != nil {
				return err
			}
		}
		return nil
	}

	m, ok := r.byKey[strings.ToLower(source)]
	if !ok || source == "" {
		reason := SourceNotFound
		if source != "" && len(r.under(strings.ToLower(source))) > 0 {
			reason = SourceIsDirectory
		}
		r.missing = append(r.missing, MissingSource{Source: item.Source, Reason: reason})
		return nil
	}
	if strings.HasSuffix(rawDest, "/") || strings.HasSuffix(rawDest, `\`) {
		// Vortex turns a file whose destination ends in a separator into a
		// directory creation, and installs nothing. So does lmm, loudly.
		r.warn(fmt.Sprintf("file %q has destination %q ending in a separator: only the directory is created, the file is not installed (as in Vortex)", item.Source, rawDest))
		return r.addDir(dest)
	}
	if dest == "" {
		dest = path.Base(source)
	}
	return r.addCopy(m, dest, priority)
}

func (r *resolver) bump() error {
	r.count++
	if r.count > MaxOps {
		return fmt.Errorf("%w: more than %d install operations", ErrTooLarge, MaxOps)
	}
	return nil
}

// addCopy records a copy, resolving a destination conflict Vortex's way:
// the higher priority wins; on a tie, the larger source path. Destinations
// collide case-insensitively. Every destination is cleaned here, whatever
// built it: a member name ("x/../../evil" under a <folder>) is as
// untrusted as the XML.
func (r *resolver) addCopy(source, dest string, priority int64) error {
	if err := r.bump(); err != nil {
		return err
	}
	dest, err := cleanDest(dest)
	if err != nil {
		return err
	}
	if dest == "" {
		r.warn(fmt.Sprintf("member %q resolves to the mod root itself: not installed", source))
		return nil
	}
	key := strings.ToLower(dest)
	if old, ok := r.copies[key]; ok {
		if priority < old.Priority || (priority == old.Priority && compareSources(source, old.Source) <= 0) {
			return nil
		}
	}
	r.copies[key] = &Op{Kind: OpCopy, Source: source, Destination: dest, Priority: priority}
	return nil
}

func (r *resolver) addDir(dest string) error {
	if err := r.bump(); err != nil {
		return err
	}
	key := strings.ToLower(dest)
	if _, ok := r.dirs[key]; !ok && dest != "" {
		r.dirs[key] = &Op{Kind: OpMkdir, Destination: dest}
	}
	return nil
}

func (r *resolver) result() []Op {
	out := make([]Op, 0, len(r.copies)+len(r.dirs))
	for _, op := range r.copies {
		out = append(out, *op)
	}
	for _, op := range r.dirs {
		out = append(out, *op)
	}
	sort.Slice(out, func(i, j int) bool {
		ki, kj := lowerKey(out[i].Destination), lowerKey(out[j].Destination)
		if ki != kj {
			return ki < kj
		}
		if out[i].Destination != out[j].Destination {
			return out[i].Destination < out[j].Destination
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// compareSources orders source paths for Vortex's tie rule. Vortex uses
// .NET's culture-aware compare; lmm approximates it case-insensitively,
// falling back to the exact bytes.
func compareSources(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// cleanDest normalises a destination: separators to '/', empty and "."
// segments dropped (so a leading separator is too), ".." resolved. A ".."
// that would leave the mod root is ErrUnsafePath.
func cleanDest(p string) (string, error) {
	var stack []string
	for _, seg := range strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") {
		switch seg {
		case "", ".":
		case "..":
			if len(stack) == 0 {
				return "", fmt.Errorf("%w: %q", ErrUnsafePath, p)
			}
			stack = stack[:len(stack)-1]
		default:
			stack = append(stack, seg)
		}
	}
	return strings.Join(stack, "/"), nil
}

// collapse drops empty and "." segments from a '/'-separated path.
func collapse(p string) string {
	if !strings.Contains(p, "//") && !strings.Contains(p, "./") && !strings.HasSuffix(p, "/") && !strings.HasSuffix(p, "/.") && p != "." {
		return p
	}
	var segs []string
	for _, seg := range strings.Split(p, "/") {
		if seg != "" && seg != "." {
			segs = append(segs, seg)
		}
	}
	return strings.Join(segs, "/")
}

// lowerKey is a path's case-insensitive key: '/'-separated, lower-case.
func lowerKey(p string) string {
	return strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
}

// normalizeKey is a fileDependency path's key: lowerKey without a leading
// "./" or '/'.
func normalizeKey(p string) string {
	return strings.ToLower(normalizeMember(p))
}
