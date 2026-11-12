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
	// Source is the archive member exactly as Request.Members spells it
	// (separators, "./" and case included), so the caller can look it up;
	// empty for OpMkdir.
	Source string
	// Destination is relative to the mod root. Its case comes from the XML
	// (and, under a <folder>, from the member), as in Vortex.
	Destination string
	// Priority is the winning entry's effective priority: its own priority,
	// offset by -1e9 for required files and +1e9 for conditional installs.
	// For OpMkdir it is the priority of the entry that asked for it.
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
	key   string // lower-case, cleaned
	clean string // '/'-separated, cleaned
	orig  string // exactly as Request.Members spells it
}

// entry is an op plus the cleaned source its conflicts compare.
type entry struct {
	op  Op
	cmp string
}

type resolver struct {
	byKey   map[string]member
	sorted  []member
	copies  map[string]*entry
	dirs    map[string]*entry
	missing []MissingSource
	unsafe  []string
	warn    func(string)
	count   int
}

func newResolver(members []string, warn func(string)) *resolver {
	r := &resolver{byKey: map[string]member{}, copies: map[string]*entry{}, dirs: map[string]*entry{}, warn: warn}
	for _, m := range members {
		if unsafeMember(m) {
			r.unsafe = append(r.unsafe, m)
			continue
		}
		n := normalizeMember(m)
		if n == "" || strings.HasSuffix(n, "/") {
			continue
		}
		clean := collapse(n)
		if clean == "" {
			continue
		}
		key := strings.ToLower(clean)
		// Members that clean to one key (differing in case or in "./" and
		// separators): keep one, deterministically.
		if prev, ok := r.byKey[key]; ok && (prev.clean < clean || (prev.clean == clean && prev.orig <= m)) {
			continue
		}
		r.byKey[key] = member{key: key, clean: clean, orig: m}
	}
	for _, m := range r.byKey {
		r.sorted = append(r.sorted, m)
	}
	sort.Slice(r.sorted, func(i, j int) bool { return r.sorted[i].key < r.sorted[j].key })
	return r
}

// unsafeMember reports a member name no install may read from: one with a
// ".." segment, an absolute or drive-letter path, or a NUL. Core's
// extractor refuses such members already; this package does not rely on it.
func unsafeMember(m string) bool {
	if strings.ContainsRune(m, 0) {
		return true
	}
	p := strings.ReplaceAll(m, `\`, "/")
	if strings.HasPrefix(p, "/") || hasDriveLetter(p) {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// hasDriveLetter reports a path starting "X:" (a Windows drive).
func hasDriveLetter(p string) bool {
	return len(p) >= 2 && p[1] == ':' && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z'))
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
			rel := strings.Join(strings.Split(m.clean, "/")[depth:], "/")
			if err := r.addCopy(m, dest+"/"+rel, priority); err != nil {
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
		return r.addDir(dest, priority, m.clean)
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
// collide case-insensitively. The destination is cleaned again here as a
// backstop; members that could climb were dropped by newResolver.
func (r *resolver) addCopy(m member, dest string, priority int64) error {
	if err := r.bump(); err != nil {
		return err
	}
	dest, err := cleanDest(dest)
	if err != nil {
		return err
	}
	if dest == "" {
		r.warn(fmt.Sprintf("member %q resolves to the mod root itself: not installed", m.orig))
		return nil
	}
	key := strings.ToLower(dest)
	if old, ok := r.copies[key]; ok && !beats(priority, m.clean, old.op.Priority, old.cmp) {
		return nil
	}
	r.copies[key] = &entry{op: Op{Kind: OpCopy, Source: m.orig, Destination: dest, Priority: priority}, cmp: m.clean}
	return nil
}

// addDir records a directory creation. Its priority and source take part
// only in structural conflicts (see result).
func (r *resolver) addDir(dest string, priority int64, source string) error {
	if err := r.bump(); err != nil {
		return err
	}
	key := strings.ToLower(dest)
	if old, ok := r.dirs[key]; dest == "" || (ok && !beats(priority, source, old.op.Priority, old.cmp)) {
		return nil
	}
	r.dirs[key] = &entry{op: Op{Kind: OpMkdir, Destination: dest, Priority: priority}, cmp: source}
	return nil
}

// beats is Vortex's conflict rule: the higher priority wins, and a tie
// goes to the larger source path.
func beats(priority int64, source string, oldPriority int64, oldSource string) bool {
	if priority != oldPriority {
		return priority > oldPriority
	}
	return compareSources(source, oldSource) > 0
}

// result returns the operations, sorted, after resolving structural
// conflicts: a copy to "a" cannot coexist with a directory creation at "a"
// or any operation under "a/". Copies are taken shortest path first; each
// one that conflicts is weighed against the strongest operation it
// conflicts with by the same rule as a destination conflict, and the loser
// side is dropped with a warning.
func (r *resolver) result() []Op {
	type node struct {
		key  string
		e    *entry
		dead bool
	}
	nodes := make([]*node, 0, len(r.copies)+len(r.dirs))
	for k, e := range r.copies {
		nodes = append(nodes, &node{key: k, e: e})
	}
	for k, e := range r.dirs {
		nodes = append(nodes, &node{key: k, e: e})
	}
	// Sorted by key, a path's descendants follow it contiguously; at an
	// equal key the directory op (OpMkdir) sorts after the copy.
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].key != nodes[j].key {
			return nodes[i].key < nodes[j].key
		}
		return nodes[i].e.op.Kind < nodes[j].e.op.Kind
	})
	for i, n := range nodes {
		if n.dead || n.e.op.Kind != OpCopy {
			continue
		}
		// Rivals: a directory op at the same key (right after the copy),
		// then everything under key+"/", found by binary search so keys
		// sorting between "a" and "a/" ("a-1", "a.txt") are never walked.
		var rivals []*node
		if i+1 < len(nodes) && nodes[i+1].key == n.key && !nodes[i+1].dead {
			rivals = append(rivals, nodes[i+1])
		}
		prefix := n.key + "/"
		for j := sort.Search(len(nodes), func(j int) bool { return nodes[j].key >= prefix }); j < len(nodes) && strings.HasPrefix(nodes[j].key, prefix); j++ {
			if !nodes[j].dead {
				rivals = append(rivals, nodes[j])
			}
		}
		if len(rivals) == 0 {
			continue
		}
		strongest := rivals[0]
		for _, o := range rivals[1:] {
			if beats(o.e.op.Priority, o.e.cmp, strongest.e.op.Priority, strongest.e.cmp) {
				strongest = o
			}
		}
		if beats(n.e.op.Priority, n.e.cmp, strongest.e.op.Priority, strongest.e.cmp) {
			for _, o := range rivals {
				o.dead = true
				r.warn(fmt.Sprintf("%q is installed as a file, so %q cannot be: dropped", n.e.op.Destination, o.e.op.Destination))
			}
			continue
		}
		n.dead = true
		r.warn(fmt.Sprintf("%q is needed as a directory (for %q), so the file there is dropped", n.e.op.Destination, strongest.e.op.Destination))
	}

	out := make([]Op, 0, len(nodes))
	for _, n := range nodes {
		if !n.dead {
			out = append(out, n.e.op)
		}
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
// segments dropped (so a single leading separator is too), ".." resolved.
// ErrUnsafePath refuses a ".." that would leave the mod root, a NUL, a
// UNC-style leading "\\" or "//", and a first segment with a drive letter
// ("C:..."): none of them is relative to the mod root on Windows, where
// the paths were written.
func cleanDest(p string) (string, error) {
	slashed := strings.ReplaceAll(p, `\`, "/")
	if strings.ContainsRune(p, 0) || strings.HasPrefix(slashed, "//") {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, p)
	}
	var stack []string
	for _, seg := range strings.Split(slashed, "/") {
		if len(stack) == 0 && hasDriveLetter(seg) {
			return "", fmt.Errorf("%w: %q", ErrUnsafePath, p)
		}
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
