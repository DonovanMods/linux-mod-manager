package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/safeyaml"

	"go.yaml.in/yaml/v3"
)

// profileBackupSuffix names the copy SaveProfile keeps of a profile file it
// had to rewrite whole, beside the file: <name>.yaml.bak. ListProfiles never
// sees it, since it does not end in .yaml.
const profileBackupSuffix = ".bak"

// profileKeys is the order a profile file's top-level keys are written in
// (ProfileConfig's), and refKeys a mod reference's (ModReferenceConfig's).
// A key an in-place save adds goes after the nearest key before it in this
// order, so a file lmm wrote stays exactly as a whole write would have it.
var (
	profileKeys = []string{"name", "game_id", "mods", "link_method", "is_default", "hooks", "overrides"}
	refKeys     = []string{"source_id", "mod_id", "version", "file_ids", "locked", "disabled"}
)

// profileFileHookYAML is ProfileHookConfigYAML as a profile file is written:
// a hook that is not explicit is left out, rather than written as `null`
// (#441). A missing key and a null one decode the same, to "inherit from
// the game".
type profileFileHookYAML struct {
	BeforeAll  *string `yaml:"before_all,omitempty"`
	BeforeEach *string `yaml:"before_each,omitempty"`
	AfterEach  *string `yaml:"after_each,omitempty"`
	AfterAll   *string `yaml:"after_all,omitempty"`
}

type profileFileHooksYAML struct {
	Install   profileFileHookYAML `yaml:"install,omitempty"`
	Uninstall profileFileHookYAML `yaml:"uninstall,omitempty"`
}

// profileFileYAML is the document a profile file is written whole as:
// ProfileConfig's keys, in its order, with the hooks written as
// profileFileHooksYAML.
type profileFileYAML struct {
	Name       string               `yaml:"name"`
	GameID     string               `yaml:"game_id"`
	Mods       []ModReferenceConfig `yaml:"mods"`
	LinkMethod string               `yaml:"link_method,omitempty"`
	IsDefault  bool                 `yaml:"is_default,omitempty"`
	Hooks      profileFileHooksYAML `yaml:"hooks,omitempty"`
	Overrides  map[string]string    `yaml:"overrides,omitempty"`
}

func fileHook(h ProfileHookConfigYAML) profileFileHookYAML {
	return profileFileHookYAML(h)
}

// profileFileOf is the whole document profile is written as.
func profileFileOf(profile *domain.Profile) profileFileYAML {
	hooks := serializeProfileHooks(profile.Hooks, profile.HooksExplicit)
	doc := profileFileYAML{
		Name:      profile.Name,
		GameID:    profile.GameID,
		IsDefault: profile.IsDefault,
		Mods:      refConfigsOf(profile.Mods),
		Hooks:     profileFileHooksYAML{Install: fileHook(hooks.Install), Uninstall: fileHook(hooks.Uninstall)},
		Overrides: overridesOf(profile),
	}
	// Only write link_method if explicitly set: String() never returns "", so
	// assigning it unconditionally defeats `omitempty` and bakes a phantom
	// symlink override into every profile file.
	if profile.LinkMethodExplicit {
		doc.LinkMethod = profile.LinkMethod.String()
	}
	return doc
}

func refConfigsOf(mods []domain.ModReference) []ModReferenceConfig {
	refs := make([]ModReferenceConfig, len(mods))
	for i, m := range mods {
		refs[i] = ModReferenceConfig{
			SourceID: m.SourceID,
			ModID:    m.ModID,
			Version:  m.Version,
			FileIDs:  m.FileIDs,
			Locked:   m.Locked,
			Disabled: m.Disabled,
		}
	}
	return refs
}

func overridesOf(profile *domain.Profile) map[string]string {
	if len(profile.Overrides) == 0 {
		return nil
	}
	overrides := make(map[string]string, len(profile.Overrides))
	for path, content := range profile.Overrides {
		overrides[path] = string(content)
	}
	return overrides
}

// SaveProfile writes profile to its file,
// <configDir>/games/<GameID>/profiles/<Name>.yaml - the file LoadProfile
// read it from, since LoadProfile names a profile for its file (#441).
//
// A file that is already there is edited in place: only the text of what
// changed is replaced, so comments, key order, flow style, blank lines,
// indentation, line endings and an unexpanded `~/` hook path stay as the
// author wrote them (MarkModsDisabled's approach, applied to every change a
// save can make). The edit is checked before anything is written - the new
// text must read back as exactly profile - and a layout it cannot edit that
// way (a flow-style mods list, an alias, a block scalar where a value
// changes) is rewritten whole instead, with the file as it was kept beside
// it as <Name>.yaml.bak. A profile that has not changed is not written at
// all.
//
// Every write replaces the file atomically - a temporary file beside it,
// renamed over it - so a crash or a full disk never leaves half a profile;
// a symlinked file's target is what changes, and a read-only file is
// refused.
func SaveProfile(configDir string, profile *domain.Profile) error {
	path, err := ProfilePath(configDir, profile.GameID, profile.Name)
	if err != nil {
		return err
	}
	_, err = saveProfileFile(path, path, profile)
	return err
}

// SaveRenamedProfile writes profile, just renamed from oldName, to its new
// file, starting from oldName's document so the rename keeps its comments
// and layout; the `name:` inside is updated when it named the old file. The
// old file is left for the caller to remove.
func SaveRenamedProfile(configDir string, profile *domain.Profile, oldName string) error {
	from, err := ProfilePath(configDir, profile.GameID, oldName)
	if err != nil {
		return err
	}
	to, err := ProfilePath(configDir, profile.GameID, profile.Name)
	if err != nil {
		return err
	}
	_, err = saveProfileFile(from, to, profile)
	return err
}

// saveOutcome is how saveProfileFile wrote a profile.
type saveOutcome int

const (
	// savedWhole: there was no file, so the document was written whole.
	savedWhole saveOutcome = iota
	// savedInPlace: the existing file was edited in place (or needed no
	// edit at all).
	savedInPlace
	// savedRewritten: the existing file's layout could not be edited in
	// place, so it was rewritten whole and its old text kept beside it.
	savedRewritten
)

// saveProfileFile writes profile to the file at to, editing the document at
// from (to itself, except for a rename). See SaveProfile.
func saveProfileFile(from, to string, profile *domain.Profile) (saveOutcome, error) {
	base, err := os.ReadFile(from)
	if errors.Is(err, os.ErrNotExist) {
		whole, err := wholeDocument(profile)
		if err != nil {
			return 0, err
		}
		return savedWhole, createProfileFile(to, whole, 0o644)
	}
	if err != nil {
		return 0, fmt.Errorf("reading profile: %w", err)
	}

	fromName := strings.TrimSuffix(filepath.Base(from), ".yaml")
	edited, editErr := editProfileDocument(base, fromName, profile)
	if editErr == nil {
		if from == to {
			if bytes.Equal(edited, base) {
				return savedInPlace, nil
			}
			return savedInPlace, writeFileAtomic(to, edited)
		}
		return savedInPlace, createProfileFileLike(from, to, edited)
	}

	// The layout cannot be edited in place. Rewrite it whole - the save
	// was asked for, and the document itself is right either way - but
	// keep what the author wrote, unless a whole rewrite loses nothing.
	whole, err := wholeDocument(profile)
	if err != nil {
		return 0, fmt.Errorf("%w (and the file's layout does not allow an in-place edit: %v)", err, editErr)
	}
	if err := checkWritable(from); err != nil && from == to {
		return 0, err
	}
	if lossy(base, fromName, profile.GameID) {
		if err := os.WriteFile(from+profileBackupSuffix, base, 0o600); err != nil {
			return 0, fmt.Errorf("keeping a copy of %s before rewriting it: %w", from, err)
		}
	}
	if from == to {
		return savedRewritten, writeFileAtomic(to, whole)
	}
	return savedRewritten, createProfileFileLike(from, to, whole)
}

// ErrProfileUnwritable is returned when a profile cannot be written so that
// it reads back as itself - a value the YAML encoder writes as text its own
// decoder rejects (a hand-edited mod id beginning with a blank line is one
// the fuzzer found). Nothing is written.
var ErrProfileUnwritable = errors.New("the profile cannot be written so that it reads back as itself")

// wholeDocument is profile's whole document, checked to read back as
// profile before anything is written with it.
func wholeDocument(profile *domain.Profile) ([]byte, error) {
	whole, err := yaml.Marshal(profileFileOf(profile))
	if err != nil {
		return nil, fmt.Errorf("marshaling profile: %w", err)
	}
	var cfg ProfileConfig
	if err := safeyaml.Unmarshal(whole, &cfg); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProfileUnwritable, err)
	}
	back, err := profileFromConfig(cfg, profile.GameID, profile.Name)
	if err != nil || !sameProfile(back, profile) {
		return nil, fmt.Errorf("%w: profile %q (game %q)", ErrProfileUnwritable, profile.Name, profile.GameID)
	}
	return whole, nil
}

// lossy reports whether rewriting base - the profile file of gameID named
// name - whole would lose anything but formatting lmm itself would write:
// true unless base already IS its own whole document.
func lossy(base []byte, name, gameID string) bool {
	if len(bytes.TrimSpace(base)) == 0 {
		return false
	}
	var cfg ProfileConfig
	if err := safeyaml.Unmarshal(base, &cfg); err != nil {
		return true
	}
	profile, err := profileFromConfig(cfg, gameID, name)
	if err != nil {
		return true
	}
	whole, err := yaml.Marshal(profileFileOf(profile))
	return err != nil || !bytes.Equal(whole, base)
}

// createProfileFile writes data as a new profile file at path, creating its
// directory, atomically.
func createProfileFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating profiles dir: %w", err)
	}
	return renameIntoPlace(path, data, mode)
}

// createProfileFileLike is createProfileFile with the mode of the file at
// like.
func createProfileFileLike(like, path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(like); err == nil {
		mode = info.Mode().Perm()
	}
	return createProfileFile(path, data, mode)
}

// editProfileDocument returns base - the profile file named fromName -
// edited in place so it reads back as want, or an error when its layout
// does not allow that. See SaveProfile.
func editProfileDocument(base []byte, fromName string, want *domain.Profile) (edited []byte, err error) {
	defer func() {
		// A bug in the editor costs this save its in-place edit, never the
		// save: the caller rewrites the file whole instead.
		if r := recover(); r != nil {
			edited, err = nil, fmt.Errorf("the in-place edit failed: %v", r)
		}
	}()

	var have ProfileConfig
	if err := safeyaml.Unmarshal(base, &have); err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := safeyaml.Unmarshal(base, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, errors.New("the file holds no document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 {
		return nil, errors.New("the document is not a block mapping")
	}
	haveProfile, err := profileFromConfig(have, want.GameID, want.Name)
	if err != nil {
		return nil, err
	}

	e, err := newDocEditor(base, root)
	if err != nil {
		return nil, err
	}
	if have.Name == fromName && want.Name != fromName {
		if err := e.setTop("name", want.Name, false); err != nil {
			return nil, err
		}
	}
	if err := e.editMods(have.Mods, want.Mods); err != nil {
		return nil, err
	}
	if haveProfile.LinkMethodExplicit != want.LinkMethodExplicit || haveProfile.LinkMethod != want.LinkMethod {
		if err := e.setTop("link_method", want.LinkMethod.String(), !want.LinkMethodExplicit); err != nil {
			return nil, err
		}
	}
	if have.IsDefault != want.IsDefault {
		if err := e.setTop("is_default", true, !want.IsDefault); err != nil {
			return nil, err
		}
	}
	if haveProfile.Hooks != want.Hooks || haveProfile.HooksExplicit != want.HooksExplicit {
		doc := profileFileOf(want)
		if err := e.setTopBlock("hooks", struct {
			Hooks profileFileHooksYAML `yaml:"hooks,omitempty"`
		}{doc.Hooks}); err != nil {
			return nil, err
		}
	}
	if !sameOverrides(haveProfile.Overrides, want.Overrides) {
		if err := e.setTopBlock("overrides", struct {
			Overrides map[string]string `yaml:"overrides,omitempty"`
		}{overridesOf(want)}); err != nil {
			return nil, err
		}
	}

	edited, err = e.apply()
	if err != nil {
		return nil, err
	}
	var after ProfileConfig
	if err := safeyaml.Unmarshal(edited, &after); err != nil {
		return nil, fmt.Errorf("the edited text would not parse: %w", err)
	}
	got, err := profileFromConfig(after, want.GameID, want.Name)
	if err != nil {
		return nil, fmt.Errorf("the edited text would not read back: %w", err)
	}
	if !sameProfile(got, want) {
		return nil, errors.New("the edited text would not read back as the saved profile")
	}
	return edited, nil
}

// sameProfile reports whether a and b are the same profile as a file
// records it: every persisted field, with an empty list or map the same as
// none, and a link method compared only when it is explicit.
func sameProfile(a, b *domain.Profile) bool {
	if a.Name != b.Name || a.GameID != b.GameID || a.IsDefault != b.IsDefault ||
		a.LinkMethodExplicit != b.LinkMethodExplicit ||
		(a.LinkMethodExplicit && a.LinkMethod != b.LinkMethod) ||
		a.Hooks != b.Hooks || a.HooksExplicit != b.HooksExplicit ||
		!sameOverrides(a.Overrides, b.Overrides) {
		return false
	}
	return sameRefs(refConfigsOf(a.Mods), b.Mods)
}

func sameOverrides(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for path, content := range a {
		other, ok := b[path]
		if !ok || !bytes.Equal(content, other) {
			return false
		}
	}
	return true
}

// sameRefs reports whether have records exactly want's references, in
// order.
func sameRefs(have []ModReferenceConfig, want []domain.ModReference) bool {
	if len(have) != len(want) {
		return false
	}
	for i, w := range refConfigsOf(want) {
		if !sameRef(have[i], w) {
			return false
		}
	}
	return true
}

func sameRef(a, b ModReferenceConfig) bool {
	return a.SourceID == b.SourceID && a.ModID == b.ModID && a.Version == b.Version &&
		slices.Equal(a.FileIDs, b.FileIDs) && a.Locked == b.Locked && a.Disabled == b.Disabled
}

// docEditor collects the text edits that turn a profile document into the
// one a save wants. Every edit is placed from the document's yaml.Node
// marks; anything it cannot place exactly is an error, and the caller
// rewrites the file whole.
type docEditor struct {
	src   sourceText
	data  []byte
	root  *yaml.Node
	br    string // the line break a new line ends with
	edits []textEdit
	// inserts are pending insertions by offset, each ranked, so that keys
	// added at one place come out in their canonical order.
	inserts map[int][]rankedText
}

type rankedText struct {
	rank int
	text string
}

// newDocEditor prepares to edit data, whose root mapping is root. A
// document whose lines do not all end the same way - LF, CRLF or CR - is
// refused: yaml.v3 also reads NEL, LS and PS as line breaks, which YAML 1.2
// and most editors do not, and a lone CR among CRLFs is one break to yaml.v3
// and none to an editor, so a line added or removed next to one would not
// read the same to every reader (MarkModsDisabled draws the same line).
func newDocEditor(data []byte, root *yaml.Node) (*docEditor, error) {
	src := newSourceText(data)
	br := ""
	for _, l := range src.lines {
		switch {
		case l.eol == "":
		case l.eol != "\n" && l.eol != "\r\n" && l.eol != "\r":
			return nil, fmt.Errorf("the file has a %q line break", l.eol)
		case br == "":
			br = l.eol
		case l.eol != br:
			return nil, errors.New("the file's lines do not all end the same way")
		}
	}
	if br == "" {
		br = "\n"
	}
	return &docEditor{src: src, data: data, root: root, br: br, inserts: map[int][]rankedText{}}, nil
}

// apply returns the document with every collected edit made.
func (e *docEditor) apply() ([]byte, error) {
	edits := slices.Clone(e.edits)
	for offset, texts := range e.inserts {
		sort.SliceStable(texts, func(i, j int) bool { return texts[i].rank < texts[j].rank })
		var b strings.Builder
		for _, t := range texts {
			b.WriteString(t.text)
		}
		edits = append(edits, textEdit{offset: offset, text: b.String()})
	}
	return applyEdits(e.data, edits)
}

func (e *docEditor) insert(offset, rank int, text string) {
	e.inserts[offset] = append(e.inserts[offset], rankedText{rank: rank, text: text})
}

// lineStart is the offset line (1-based) starts at; lineAfter the offset
// just past its line break (its end, for a last line with none).
func (e *docEditor) lineStart(line int) int { return e.src.lines[line-1].start }

func (e *docEditor) lineAfter(line int) int {
	l := e.src.lines[line-1]
	return l.end + len(l.eol)
}

// endsWithBreak reports whether the text before offset ends a line.
func (e *docEditor) endsWithBreak(offset int) bool {
	return offset == 0 || breakLenBefore(e.data, offset) > 0
}

// onlySpacesBefore reports whether nothing but spaces precedes offset on
// its line.
func (e *docEditor) onlySpacesBefore(offset int) bool {
	line := e.src.lineOf(offset)
	if line < 1 {
		return false
	}
	return strings.Trim(string(e.data[e.lineStart(line):offset]), " ") == ""
}

// nodeOffset is the byte offset of n's mark.
func (e *docEditor) nodeOffset(n *yaml.Node) (int, error) {
	offset, ok := e.src.offset(n.Line, n.Column)
	if !ok {
		return 0, fmt.Errorf("a node at line %d could not be located", n.Line)
	}
	return offset, nil
}

// lastLineOf is the last line value (the value of key) occupies - key's own
// line for an empty value, whose mark is not always where it is.
func (e *docEditor) lastLineOf(key, value *yaml.Node) (int, error) {
	if isEmptyScalar(value) {
		return key.Line, nil
	}
	line, ok := e.src.lastLine(value)
	if !ok {
		return 0, fmt.Errorf("the end of %q's value could not be located", key.Value)
	}
	return max(line, key.Line), nil
}

// scalarText is v as a single-line YAML scalar, formatted as a whole write
// formats it.
func scalarText(v any) (string, error) {
	out, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	text := strings.TrimSuffix(string(out), "\n")
	if strings.ContainsAny(text, "\r\n") {
		return "", fmt.Errorf("%q does not fit on one line", v)
	}
	return text, nil
}

// replaceScalar replaces the text of the scalar node n with text, keeping
// everything around it - a line comment after it included.
func (e *docEditor) replaceScalar(n *yaml.Node, text string) error {
	if n.Kind != yaml.ScalarNode || n.Anchor != "" || n.Style&yaml.TaggedStyle != 0 {
		return errors.New("the value is not a plain scalar")
	}
	offset, err := e.nodeOffset(n)
	if err != nil {
		return err
	}
	var length int
	switch n.Style {
	case 0:
		if n.Value == "" || !bytes.HasPrefix(e.data[offset:], []byte(n.Value)) {
			return errors.New("the value's text could not be located")
		}
		length = len(n.Value)
	case yaml.DoubleQuotedStyle, yaml.SingleQuotedStyle:
		end, ok := quotedEnd(e.data, offset)
		if !ok {
			return errors.New("the value's closing quote could not be located")
		}
		length = end + 1 - offset
	default:
		return errors.New("the value is a block scalar")
	}
	if e.src.lineOf(offset) != e.src.lineOf(offset+length-1) {
		return errors.New("the value spans lines")
	}
	e.edits = append(e.edits, textEdit{offset: offset, length: length, text: text})
	return nil
}

// topEntry returns the top-level key named key, its value and its index in
// the root mapping, or index -1.
func (e *docEditor) topEntry(key string) (*yaml.Node, *yaml.Node, int) {
	return mappingEntry(e.root, key)
}

func mappingEntry(m *yaml.Node, key string) (*yaml.Node, *yaml.Node, int) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; k.Kind == yaml.ScalarNode && k.Value == key {
			return k, m.Content[i+1], i
		}
	}
	return nil, nil, -1
}

// topSpan is the bytes the top-level entry at index i of the root mapping
// occupies: from the start of its key's line to the end of the last line
// before the next top-level key that is not blank or a column-0 comment -
// which belong to the next key, or to the document.
func (e *docEditor) topSpan(i int) (start, end int, err error) {
	key := e.root.Content[i]
	if key.Column != 1 {
		return 0, 0, fmt.Errorf("the key %q does not start its line", key.Value)
	}
	endLine := len(e.src.lines)
	if i+2 < len(e.root.Content) {
		endLine = e.root.Content[i+2].Line - 1
	}
	for endLine > key.Line {
		text := e.data[e.lineStart(endLine):e.src.lines[endLine-1].end]
		if len(bytes.TrimSpace(text)) != 0 && text[0] != '#' {
			break
		}
		endLine--
	}
	if value := e.root.Content[i+1]; !isEmptyScalar(value) {
		if last, ok := e.src.lastLine(value); ok && last > endLine {
			return 0, 0, fmt.Errorf("the value of %q runs past the next key", key.Value)
		}
	}
	return e.lineStart(key.Line), e.lineAfter(endLine), nil
}

// insertTop inserts text - whole lines, each ending in a line break - as
// the top-level entry key, after the nearest entry before it in
// profileKeys' order (or before the nearest one after it).
func (e *docEditor) insertTop(key, text string) error {
	rank := slices.Index(profileKeys, key)
	for r := rank - 1; r >= 0; r-- {
		if _, _, i := e.topEntry(profileKeys[r]); i >= 0 {
			_, end, err := e.topSpan(i)
			if err != nil {
				return err
			}
			if !e.endsWithBreak(end) {
				text = e.br + text
			}
			e.insert(end, rank, text)
			return nil
		}
	}
	for r := rank + 1; r < len(profileKeys); r++ {
		if k, _, i := e.topEntry(profileKeys[r]); i >= 0 {
			if k.Column != 1 {
				return fmt.Errorf("the key %q does not start its line", k.Value)
			}
			e.insert(e.lineStart(k.Line), rank, text)
			return nil
		}
	}
	end := len(e.data)
	if !e.endsWithBreak(end) {
		text = e.br + text
	}
	e.insert(end, rank, text)
	return nil
}

// setTop gives the top-level key the scalar value, or removes the key.
func (e *docEditor) setTop(key string, value any, remove bool) error {
	k, v, i := e.topEntry(key)
	if remove {
		if i < 0 {
			return nil
		}
		start, end, err := e.topSpan(i)
		if err != nil {
			return err
		}
		e.edits = append(e.edits, textEdit{offset: start, length: end - start})
		return nil
	}
	text, err := scalarText(value)
	if err != nil {
		return err
	}
	if i < 0 {
		return e.insertTop(key, key+": "+text+e.br)
	}
	if isEmptyScalar(v) {
		return e.setTopBlockText(i, k, key+": "+text+e.br)
	}
	return e.replaceScalar(v, text)
}

// setTopBlock replaces the top-level key's whole entry with value's
// encoding - a struct holding just that key - or removes it when value
// encodes to nothing.
func (e *docEditor) setTopBlock(key string, value any) error {
	out, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	text := string(out)
	if strings.TrimSpace(text) == "{}" {
		text = ""
	}
	if e.br != "\n" {
		text = strings.ReplaceAll(text, "\n", e.br)
	}
	k, _, i := e.topEntry(key)
	if i < 0 {
		if text == "" {
			return nil
		}
		return e.insertTop(key, text)
	}
	return e.setTopBlockText(i, k, text)
}

// setTopBlockText replaces the top-level entry at index i with text.
func (e *docEditor) setTopBlockText(i int, key *yaml.Node, text string) error {
	start, end, err := e.topSpan(i)
	if err != nil {
		return err
	}
	if text != "" && !e.endsWithBreak(end) {
		text = strings.TrimSuffix(text, e.br)
	}
	e.edits = append(e.edits, textEdit{offset: start, length: end - start, text: text})
	return nil
}

// modItem is one reference of a block mods list, as text: chunk is the
// bytes from the end of the previous reference (or this one's own line, for
// the first) to the end of this one's last line - so the comments and blank
// lines above a reference travel with it.
type modItem struct {
	node       *yaml.Node
	chunkStart int
	end        int // just past the item's last line
	lastLine   int
	dashCol    int
	keyCol     int
}

// editMods edits the mods list from have to want: references are matched by
// source and mod id (the n-th copy of a mod to its n-th copy), a matched
// reference keeps its text and has only its changed keys edited, the list
// is put in want's order, and a reference want does not list goes - with
// the comments above it.
func (e *docEditor) editMods(have []ModReferenceConfig, want []domain.ModReference) error {
	if sameRefs(have, want) {
		return nil
	}
	k, v, i := e.topEntry("mods")
	if i < 0 {
		items, err := e.newItems(want, 4, 2)
		if err != nil {
			return err
		}
		return e.insertTop("mods", "mods:"+e.br+items)
	}
	if len(want) == 0 {
		return e.setTopBlockText(i, k, "mods: []"+e.br)
	}
	if v.Kind != yaml.SequenceNode || v.Style&yaml.FlowStyle != 0 || len(v.Content) == 0 {
		if !isEmptyScalar(v) && (v.Kind != yaml.SequenceNode || len(v.Content) != 0) {
			return errors.New("the mods list is not a block sequence")
		}
		items, err := e.newItems(want, 4, 2)
		if err != nil {
			return err
		}
		return e.setTopBlockText(i, k, "mods:"+e.br+items)
	}
	if len(v.Content) != len(have) {
		return errors.New("the mods list is not a plain sequence")
	}

	items := make([]modItem, len(v.Content))
	for j, node := range v.Content {
		item, err := e.modItem(node)
		if err != nil {
			return err
		}
		if j == 0 {
			item.chunkStart = e.lineStart(e.src.lineOf(item.chunkStart))
		} else {
			if item.dashCol != items[0].dashCol || e.src.lineOf(item.chunkStart) <= items[j-1].lastLine {
				return errors.New("the mods list's references are not laid out one below another")
			}
			item.chunkStart = items[j-1].end
		}
		items[j] = item
	}
	regionStart, regionEnd := items[0].chunkStart, items[len(items)-1].end

	// The n-th reference to a mod in want takes the n-th one in have.
	byKey := make(map[string][]int)
	for j, ref := range have {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		byKey[key] = append(byKey[key], j)
	}
	var out strings.Builder
	for _, ref := range refConfigsOf(want) {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if matches := byKey[key]; len(matches) > 0 {
			j := matches[0]
			byKey[key] = matches[1:]
			chunk, err := e.editItem(items[j], have[j], ref)
			if err != nil {
				return err
			}
			out.WriteString(chunk)
			continue
		}
		text, err := e.newItems([]domain.ModReference{refOf(ref)}, items[0].dashCol, items[0].keyCol-items[0].dashCol)
		if err != nil {
			return err
		}
		out.WriteString(text)
	}
	text := out.String()
	if !e.endsWithBreak(regionEnd) {
		text = strings.TrimSuffix(text, e.br)
	}
	e.edits = append(e.edits, textEdit{offset: regionStart, length: regionEnd - regionStart, text: text})
	return nil
}

func refOf(c ModReferenceConfig) domain.ModReference {
	return domain.ModReference{
		SourceID: c.SourceID, ModID: c.ModID, Version: c.Version,
		FileIDs: c.FileIDs, Locked: c.Locked, Disabled: c.Disabled,
	}
}

// modItem locates the reference node of a block mods list.
func (e *docEditor) modItem(node *yaml.Node) (modItem, error) {
	if node.Kind != yaml.MappingNode || node.Anchor != "" || len(node.Content) == 0 {
		return modItem{}, errors.New("a reference is not a plain mapping")
	}
	offset, err := e.nodeOffset(node)
	if err != nil {
		return modItem{}, err
	}
	dash := offset
	for dash > 0 {
		if c := e.data[dash-1]; c == ' ' || c == '\t' {
			dash--
			continue
		}
		if n := breakLenBefore(e.data, dash); n > 0 {
			dash -= n
			continue
		}
		break
	}
	if dash == 0 || e.data[dash-1] != '-' {
		return modItem{}, errors.New("a reference's `-` could not be located")
	}
	dash--
	if !e.onlySpacesBefore(dash) {
		return modItem{}, errors.New("a reference's `-` does not start its line")
	}
	last, ok := e.src.lastLine(node)
	if !ok {
		return modItem{}, errors.New("a reference's end could not be located")
	}
	keyCol := node.Column - 1
	if node.Style&yaml.FlowStyle == 0 {
		keyCol = node.Content[0].Column - 1
	}
	dashLine := e.src.lineOf(dash)
	return modItem{
		node:       node,
		chunkStart: dash,
		end:        e.lineAfter(last),
		lastLine:   last,
		dashCol:    dash - e.lineStart(dashLine),
		keyCol:     keyCol,
	}, nil
}

// newItems is refs as block references, each line ending in a break,
// formatted as a whole write formats them and indented to a list whose `-`
// is at dashCol with keys keyOffset columns after it.
func (e *docEditor) newItems(refs []domain.ModReference, dashCol, keyOffset int) (string, error) {
	if keyOffset < 2 {
		return "", errors.New("the mods list's keys are too close to their `-`")
	}
	out, err := yaml.Marshal(refConfigsOf(refs))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for line := range strings.Lines(string(out)) {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, "- "):
			b.WriteString(strings.Repeat(" ", dashCol) + "-" + strings.Repeat(" ", keyOffset-1) + line[2:])
		case strings.HasPrefix(line, "  "):
			b.WriteString(strings.Repeat(" ", dashCol+keyOffset) + line[2:])
		default:
			return "", fmt.Errorf("unexpected reference text %q", line)
		}
		b.WriteString(e.br)
	}
	return b.String(), nil
}

// editItem returns item's chunk with the keys that differ between have and
// want edited, ending in a line break.
func (e *docEditor) editItem(item modItem, have, want ModReferenceConfig) (string, error) {
	sub := &docEditor{src: e.src, data: e.data, root: item.node, br: e.br, inserts: map[int][]rankedText{}}
	if sameRef(have, want) {
		return e.chunk(item, nil)
	}
	flow := item.node.Style&yaml.FlowStyle != 0
	for rank, key := range refKeys {
		var wantText string
		var zero bool
		var err error
		switch key {
		case "source_id", "mod_id":
			continue
		case "version":
			if have.Version == want.Version {
				continue
			}
			zero = want.Version == ""
			wantText, err = scalarText(want.Version)
		case "file_ids":
			if slices.Equal(have.FileIDs, want.FileIDs) {
				continue
			}
			zero = len(want.FileIDs) == 0
			wantText, err = e.fileIDsText(want.FileIDs, !flow, item.keyCol)
		case "locked":
			if have.Locked == want.Locked {
				continue
			}
			zero = !want.Locked
			wantText = "true"
		case "disabled":
			if have.Disabled == want.Disabled {
				continue
			}
			zero = !want.Disabled
			wantText = "true"
		}
		if err != nil {
			return "", err
		}
		if err := sub.setItemKey(item, rank, key, wantText, zero, flow); err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
	}
	return e.chunk(item, sub)
}

// fileIDsText is file_ids' whole entry, `file_ids: [...]`, or - in a block
// reference - the key followed by one `- id` line each, as a whole write
// writes it.
func (e *docEditor) fileIDsText(ids []string, block bool, keyCol int) (string, error) {
	texts := make([]string, len(ids))
	for i, id := range ids {
		text, err := scalarText(id)
		if err != nil {
			return "", err
		}
		texts[i] = text
	}
	if !block || len(ids) == 0 {
		return "file_ids: [" + strings.Join(texts, ", ") + "]", nil
	}
	var b strings.Builder
	b.WriteString("file_ids:")
	for _, text := range texts {
		b.WriteString(e.br + strings.Repeat(" ", keyCol+2) + "- " + text)
	}
	return b.String(), nil
}

// setItemKey gives the reference's key the text wantText (for file_ids,
// the whole entry), or removes it when zero.
func (e *docEditor) setItemKey(item modItem, rank int, key, wantText string, zero, flow bool) error {
	m := item.node
	k, v, i := mappingEntry(m, key)
	entryText := key + ": " + wantText
	if key == "file_ids" {
		entryText = wantText
	}
	if i < 0 {
		if zero {
			return nil
		}
		return e.insertItemKey(item, rank, entryText, flow)
	}

	if zero {
		if flow || i == 0 {
			// A key a line cannot be taken from: its value becomes the one
			// that reads as absent.
			zeroText := map[string]string{"version": `""`, "file_ids": "file_ids: []", "locked": "false", "disabled": "false"}[key]
			if key == "file_ids" {
				return e.replaceEntry(k, v, zeroText)
			}
			if isEmptyScalar(v) || v.Kind != yaml.ScalarNode {
				return e.replaceEntry(k, v, key+": "+zeroText)
			}
			return e.replaceScalar(v, zeroText)
		}
		keyOffset, err := e.nodeOffset(k)
		if err != nil {
			return err
		}
		if k.Column-1 != item.keyCol || !e.onlySpacesBefore(keyOffset) {
			return errors.New("the key does not start its line")
		}
		last, err := e.lastLineOf(k, v)
		if err != nil {
			return err
		}
		start, end := e.lineStart(k.Line), e.lineAfter(last)
		if end == len(e.data) && !e.endsWithBreak(end) {
			// The reference's last line ends the file: take the break
			// before it instead of leaving an empty line.
			start -= breakLenBefore(e.data, start)
		}
		e.edits = append(e.edits, textEdit{offset: start, length: end - start})
		return nil
	}

	if key != "file_ids" && v.Kind == yaml.ScalarNode && !isEmptyScalar(v) {
		return e.replaceScalar(v, wantText)
	}
	if key == "file_ids" && flow {
		entryText = strings.ReplaceAll(entryText, e.br, " ")
	}
	return e.replaceEntry(k, v, entryText)
}

// replaceEntry replaces a mapping entry - from its key to the end of its
// value's last line - with text.
func (e *docEditor) replaceEntry(k, v *yaml.Node, text string) error {
	start, err := e.nodeOffset(k)
	if err != nil {
		return err
	}
	if k.Style != 0 {
		return errors.New("the key is quoted")
	}
	last, err := e.lastLineOf(k, v)
	if err != nil {
		return err
	}
	end, _, ok := e.src.lineEnd(last)
	if !ok {
		return errors.New("the value's last line could not be located")
	}
	if v.Kind != yaml.ScalarNode && v.Style&yaml.FlowStyle != 0 {
		// A flow value can end mid-line, before more of a flow reference.
		open, err := e.nodeOffset(v)
		if err != nil {
			return err
		}
		close, ok := e.src.matchingClose(open)
		if !ok {
			return errors.New("the value's closing bracket could not be located")
		}
		end = close + 1
	} else if v.Kind == yaml.ScalarNode && !isEmptyScalar(v) {
		// Stop at the scalar's own end, so what follows it on the line - a
		// comma and the rest of a flow reference - stays.
		sub := &docEditor{src: e.src, data: e.data}
		if err := sub.replaceScalar(v, ""); err != nil {
			return err
		}
		end = sub.edits[0].offset + sub.edits[0].length
	} else if isEmptyScalar(v) {
		// `key:` with nothing after it: only the key and its colon go.
		colon := bytes.IndexByte(e.data[start:end], ':')
		if colon < 0 {
			return errors.New("the key's colon could not be located")
		}
		end = start + colon + 1
		if rest := strings.TrimSpace(string(e.data[end:e.src.lines[k.Line-1].end])); rest != "" && !strings.HasPrefix(rest, "#") {
			return errors.New("the empty value is followed by more text")
		}
	}
	e.edits = append(e.edits, textEdit{offset: start, length: end - start, text: text})
	return nil
}

// insertItemKey adds entryText to the reference, after the nearest key
// before it in refKeys' order - at the end of a flow reference.
func (e *docEditor) insertItemKey(item modItem, rank int, entryText string, flow bool) error {
	m := item.node
	if flow {
		start, err := e.nodeOffset(m)
		if err != nil {
			return err
		}
		end, ok := e.src.matchingClose(start)
		if !ok {
			return errors.New("the reference's closing brace could not be located")
		}
		last := e.src.lastNonSpace(start, end)
		text := ", " + entryText
		if e.data[last] == ',' || e.data[last] == '{' {
			text = " " + entryText
			if e.data[last] == ',' {
				text += ","
			}
		}
		e.insert(last+1, rank, text)
		return nil
	}
	anchorLine := -1
	for r := rank - 1; r >= 0 && anchorLine < 0; r-- {
		if k, v, i := mappingEntry(m, refKeys[r]); i >= 0 {
			last, err := e.lastLineOf(k, v)
			if err != nil {
				return err
			}
			anchorLine = last
		}
	}
	if anchorLine < 0 {
		anchorLine = item.lastLine
	}
	offset, lineBreak, ok := e.src.lineEnd(anchorLine)
	if !ok {
		return errors.New("the reference's line does not end in a break a new line can repeat")
	}
	text := strings.ReplaceAll(entryText, e.br, lineBreak)
	e.insert(offset, rank, lineBreak+strings.Repeat(" ", item.keyCol)+text)
	return nil
}

// chunk returns item's chunk with sub's edits - all of them inside it -
// made, ending in a line break.
func (e *docEditor) chunk(item modItem, sub *docEditor) (string, error) {
	text := e.data[item.chunkStart:item.end]
	if sub != nil {
		edited, err := sub.apply()
		if err != nil {
			return "", err
		}
		// sub edited the whole document; the chunk moved by whatever its
		// edits added or removed before its own end.
		delta := len(edited) - len(e.data)
		if item.end+delta < item.chunkStart || item.end+delta > len(edited) {
			return "", errors.New("an edit reached outside its reference")
		}
		for _, ed := range sub.edits {
			if ed.offset < item.chunkStart || ed.offset+ed.length > item.end {
				return "", errors.New("an edit reached outside its reference")
			}
		}
		for offset := range sub.inserts {
			if offset < item.chunkStart || offset > item.end {
				return "", errors.New("an edit reached outside its reference")
			}
		}
		text = edited[item.chunkStart : item.end+delta]
	}
	out := string(text)
	if breakLenBefore([]byte(out), len(out)) == 0 {
		out += e.br
	}
	return out, nil
}
