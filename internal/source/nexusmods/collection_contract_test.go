package nexusmods

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"
)

// collectionSchema is testdata/collection-schema.json: a hand-recorded
// introspection (see its _provenance) of the object types
// collectionRevisionQuery reads, each field by its named type, and the
// arguments Query.collectionRevision takes. It is to the collection query
// what modsfilter-schema.json is to the search filter (#337/#343): a
// NexusMods schema change fails this test, offline, instead of a user's
// import.
type collectionSchema struct {
	Types map[string]map[string]string `json:"types"`
	Query map[string]struct {
		Type string            `json:"type"`
		Args map[string]string `json:"args"`
	} `json:"query"`
}

// TestCollectionRevisionQuery_MatchesRecordedSchema walks every selection of
// the query the client sends and checks each field exists, on the type the
// recording says the enclosing field returns, and that the root field's
// arguments are ones collectionRevision takes with the variable's type.
func TestCollectionRevisionQuery_MatchesRecordedSchema(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "collection-schema.json"))
	require.NoError(t, err)
	var schema collectionSchema
	require.NoError(t, json.Unmarshal(data, &schema))
	root, ok := schema.Query["collectionRevision"]
	require.True(t, ok, "the recording must hold Query.collectionRevision")
	require.NotEmpty(t, schema.Types[root.Type])

	toks := gqlTokens(collectionRevisionQuery)
	// The operation header: query Name($var: Type, ...) {
	vars := map[string]string{}
	i := 0
	for toks[i] != "(" {
		i++
	}
	for i++; toks[i] != ")"; i++ {
		if strings.HasPrefix(toks[i], "$") {
			vars[toks[i]] = toks[i+2]
			i += 2
		}
	}
	i++
	require.Equal(t, "{", toks[i])
	i++
	require.Equal(t, "collectionRevision", toks[i])
	i++
	require.Equal(t, "(", toks[i])
	for i++; toks[i] != ")"; i++ {
		arg := toks[i]
		want, ok := root.Args[arg]
		require.True(t, ok, "collectionRevision takes no argument %q", arg)
		require.Equal(t, ":", toks[i+1])
		v := toks[i+2]
		require.Equal(t, want, vars[v], "argument %s is a %s; the query declares %s as %s", arg, want, v, vars[v])
		i += 2
	}
	i++
	walkSelection(t, &schema, root.Type, toks, &i)
}

// walkSelection checks the selection set starting at toks[*i] ("{") against
// typ, recursing into each field that has a selection of its own.
func walkSelection(t *testing.T, schema *collectionSchema, typ string, toks []string, i *int) {
	t.Helper()
	fields, ok := schema.Types[typ]
	require.True(t, ok, "the recording has no type %s; re-record it", typ)
	require.Equal(t, "{", toks[*i])
	for *i++; toks[*i] != "}"; *i++ {
		name := toks[*i]
		fieldType, ok := fields[name]
		require.True(t, ok, "%s has no field %q in the recorded schema", typ, name)
		if *i+1 < len(toks) && toks[*i+1] == "{" {
			*i++
			walkSelection(t, schema, fieldType, toks, i)
		}
	}
}

// gqlTokens splits a GraphQL document into names, variables and
// punctuation: enough of the grammar for the queries this package writes.
func gqlTokens(doc string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range doc {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$' || r == '!':
			cur.WriteRune(r)
		case strings.ContainsRune("{}():,[]", r):
			flush()
			if r != ',' {
				out = append(out, string(r))
			}
		default:
			flush()
		}
	}
	flush()
	return out
}
