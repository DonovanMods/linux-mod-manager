package serve_test

// The hook-order ratchet (issue 434's final review, R4). Preact keeps a
// component's hook state in a list indexed by call order, so every render
// must call the same hooks in the same order - which a hook placed after an
// early return, or inside a branch, does not. The library's selection
// announcement did exactly that: its useMemo sat below the "loading" and
// "no mods" returns. It worked only because Preact 10 appends hook slots on
// demand, and a render that takes the other branch can hand one hook's
// state to another.
//
// A line scan, like the other SPA ratchets beside it, because the modules
// are prettier-formatted: a component's own statements are indented two
// spaces and everything nested in them deeper. That is what lets it tell a
// component's own guard clauses from the returns of the functions and
// callbacks the component declares.
import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	// componentStart opens a component: a top-level function whose name is
	// capitalised.
	componentStart = regexp.MustCompile(`^(?:export )?(?:default )?function [A-Z]\w*\(`)
	// guardReturn is a one-line early return at the component's own level.
	guardReturn = regexp.MustCompile(`^  if \(.*\) return\b`)
	// blockOpener opens a block at the component's own level: an if, loop
	// or switch (its condition may run on over several lines), or an else.
	blockOpener = regexp.MustCompile(`^  (?:(?:if|for|while|switch) \(|} else\b)(?:.*[{(])?$`)
	// blockReturn is a return directly inside such a block.
	blockReturn = regexp.MustCompile(`^    return\b`)
	// hookCall is a hook called at the component's level or one below it,
	// as a statement or as a declaration's value.
	hookCall = regexp.MustCompile(`^ {2}(?: {2})?(?:(?:const|let|var) [^=]+= )?use[A-Z]\w*\(`)
)

// hookOrderViolations names every hook in source that a component calls
// after one of its early returns, or inside one of its blocks.
func hookOrderViolations(source string) []string {
	var out []string
	inComponent, inBlock, returned := false, false, false
	for i, line := range strings.Split(source, "\n") {
		if componentStart.MatchString(line) {
			inComponent, inBlock, returned = true, false, false
			continue
		}
		if !inComponent {
			continue
		}
		if line == "}" {
			inComponent = false
			continue
		}

		// A line at the component's own level ends whatever block came
		// before it, except the ") {" that closes a condition written over
		// several lines.
		if len(line) > 2 && strings.HasPrefix(line, "  ") && line[2] != ' ' && line[2] != ')' {
			inBlock = blockOpener.MatchString(line)
		}

		if hookCall.MatchString(line) {
			nested := strings.HasPrefix(line, "    ")
			switch {
			case nested && inBlock:
				out = append(out, fmt.Sprintf("line %d: %s is called inside a branch", i+1, strings.TrimSpace(line)))
			case returned:
				out = append(out, fmt.Sprintf("line %d: %s is called after an early return", i+1, strings.TrimSpace(line)))
			}
		}
		if guardReturn.MatchString(line) || inBlock && blockReturn.MatchString(line) {
			returned = true
		}
	}
	return out
}

// TestHooksAreCalledBeforeEveryEarlyReturn walks every SPA module for a hook
// whose place in the call order can change between renders.
func TestHooksAreCalledBeforeEveryEarlyReturn(t *testing.T) {
	dir := filepath.Join(".", "spa", "app")
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".js" {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, violation := range hookOrderViolations(string(data)) {
			t.Errorf("%s: %s - Preact matches hook state by call order, so call every hook above the component's first return", path, violation)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
}

// TestHookOrderCheck_Reach is the checker's own reach: the shapes it must
// refuse, and the ordinary component code it must leave alone.
func TestHookOrderCheck_Reach(t *testing.T) {
	for _, tc := range []struct {
		name    string
		source  string
		refused bool
	}{
		{name: "a hook after a guard clause", refused: true, source: `export function Card({ rows }) {
  if (!rows) return null;
  const [open, setOpen] = useState(false);
  return view;
}`},
		{name: "a hook after a block that returns", refused: true, source: `function Card({ rows }) {
  if (rows === null) {
    return view;
  }
  const label = useMemo(() => rows.length, [rows]);
  return view;
}`},
		{name: "a hook after a condition over several lines that returns", refused: true, source: `function Card({ rows }) {
  if (
    rows === null ||
    rows.length === 0
  ) {
    return null;
  }
  useEffect(() => {}, []);
  return view;
}`},
		{name: "a hook after an else that returns", refused: true, source: `function Card({ rows }) {
  if (rows) {
    log(rows);
  } else {
    return null;
  }
  const ref = useRef(null);
  return view;
}`},
		{name: "a hook inside a branch", refused: true, source: `function Card({ rows }) {
  if (rows) {
    useEffect(() => {}, []);
  }
  return view;
}`},
		{name: "a hook wrapped onto the next line after a return", refused: true, source: `function Card({ rows }) {
  if (!rows) return null;
  const announcement =
    useMemo(() => rows.length, [rows]);
  return view;
}`},
		{name: "a custom hook after a return", refused: true, source: `function Bar({ open }) {
  if (!open) return null;
  useDismissOnOutsideOrEscape(ref, open, close);
  return view;
}`},

		{name: "hooks above every return", source: `function Card({ rows }) {
  const [open, setOpen] = useState(false);
  const label = useMemo(() => rows?.length, [rows]);
  useEffect(() => {}, []);
  if (!rows) return null;
  return view;
}`},
		{name: "a nested function's own returns", source: `function Card({ rows }) {
  function visible() {
    return rows.filter(Boolean);
  }
  const count = useMemo(() => visible().length, [rows]);
  return view;
}`},
		{name: "a one-line if, then a nested function's return", source: `function Card({ rows }) {
  if (rows) log(rows);
  function visible() {
    return rows.filter(Boolean);
  }
  const count = useMemo(() => visible().length, [rows]);
  return view;
}`},
		{name: "a block that does not return", source: `function Card({ rows }) {
  if (rows) {
    log(rows);
  }
  const count = useMemo(() => rows.length, [rows]);
  return view;
}`},
		{name: "a hook's own callback returning", source: `function Card({ open }) {
  useEffect(() => {
    if (!open) return;
    return () => {};
  }, [open]);
  const ref = useRef(null);
  return view;
}`},
		{name: "a helper, not a component", source: `function visibleRows(rows) {
  if (!rows) return [];
  const [first] = useState(rows);
  return first;
}`},
		{name: "a second component after the first returned early", source: `function A({ x }) {
  if (!x) return null;
  return view;
}

function B() {
  const [y] = useState(0);
  return view;
}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hookOrderViolations(tc.source)
			if tc.refused {
				assert.NotEmpty(t, got, "must be refused:\n%s", tc.source)
			} else {
				assert.Empty(t, got, "must pass:\n%s", tc.source)
			}
		})
	}
}
