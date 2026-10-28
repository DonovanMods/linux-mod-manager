package nexusmods

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// recordedSortSchema is the shape of testdata/modssort-schema.json - a hand
// recorded introspection of NexusMods' ModsSort input (and the BaseSortValue
// and SortDirection it is built from), the Mod fields the search selects,
// and the `sort` argument of the `mods` query. See that file's own
// "_provenance" block for when and how it was taken.
//
// It is #503's counterpart of recordedSchema: the search now sends a `sort`
// variable and selects `downloads`/`endorsements`, and a name or type the
// live schema does not define would fail EVERY search server-side with
// nothing in the build to notice (#337/#343). It reuses schema_contract_test.go's
// inputObject/inputField/typeRef/enumType, and nothing here touches the
// network.
type recordedSortSchema struct {
	ModsSort    inputObject  `json:"modsSort"`
	BaseSort    inputObject  `json:"baseSort"`
	Direction   enumType     `json:"direction"`
	ModFields   []inputField `json:"modFields"`
	ModsSortArg inputField   `json:"modsSortArg"`
}

func loadRecordedSortSchema(t *testing.T) *recordedSortSchema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "modssort-schema.json"))
	if err != nil {
		t.Fatalf("reading the recorded sort schema: %v", err)
	}
	var s recordedSortSchema
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("parsing the recorded sort schema: %v", err)
	}
	if len(s.ModsSort.InputFields) == 0 || len(s.BaseSort.InputFields) == 0 ||
		len(s.Direction.EnumValues) == 0 || len(s.ModFields) == 0 || s.ModsSortArg.Name == "" {
		t.Fatal("the recorded sort schema is incomplete - the fixture is the whole point of this test")
	}
	return &s
}

// leafName unwraps a NON_NULL/LIST chain to the named type at its end.
func (r typeRef) leafName() string {
	t := &r
	for t.OfType != nil {
		t = t.OfType
	}
	return t.Name
}

// sdl renders a type reference the way a GraphQL variable declaration
// spells it ("[ModsSort!]").
func (r typeRef) sdl() string {
	switch r.Kind {
	case "LIST":
		return "[" + r.OfType.sdl() + "]"
	case "NON_NULL":
		return r.OfType.sdl() + "!"
	}
	return r.Name
}

// captureSearchRequest drives the REAL request builder (SearchMods) against
// an httptest server and returns the whole GraphQL request it sent: the
// query text and the variables.
func captureSearchRequest(t *testing.T, sort domain.SearchSort) (query string, vars map[string]any) {
	t.Helper()
	var captured *struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding the request body: %v", err)
		}
		captured = &req
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"data":{"mods":{"nodes":[]}}}`); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client(), "test-key")
	client.graphqlURL = server.URL
	if _, err := client.SearchMods(context.Background(), "skyrimspecialedition", "armour", "", nil, sort, 20, 0); err != nil {
		t.Fatalf("SearchMods: %v", err)
	}
	if captured == nil {
		t.Fatal("the server captured no request")
	}
	return captured.Query, captured.Variables
}

// TestSearchModsSortMatchesRecordedSchema is the contract for #503's sort:
// every key of every entry of the `sort` variable must be a member of the
// recorded ModsSort, take the input object ModsSort records for it
// (BaseSortValue), carry that object's required `direction`, and use a
// SortDirection value. Every sort the source advertises is driven.
func TestSearchModsSortMatchesRecordedSchema(t *testing.T) {
	schema := loadRecordedSortSchema(t)
	sortMembers := map[string]inputField{}
	for _, f := range schema.ModsSort.InputFields {
		sortMembers[f.Name] = f
	}
	baseMembers := map[string]bool{}
	for _, f := range schema.BaseSort.InputFields {
		baseMembers[f.Name] = true
	}
	directions := map[string]bool{}
	for _, v := range schema.Direction.EnumValues {
		directions[v.Name] = true
	}

	for _, s := range []domain.SearchSort{domain.SortUpdated, domain.SortDownloads, domain.SortPopular} {
		t.Run(string(s), func(t *testing.T) {
			_, vars := captureSearchRequest(t, s)
			entries, ok := vars["sort"].([]any)
			if !ok || len(entries) == 0 {
				t.Fatalf("variables carry no sort list: %#v", vars)
			}
			for i, e := range entries {
				entry, isObj := e.(map[string]any)
				if !isObj {
					t.Fatalf("sort[%d] must be an object, got %T", i, e)
				}
				for key, raw := range entry {
					member, defined := sortMembers[key]
					if !defined {
						t.Errorf("sort[%d] key %q is not a member of the recorded ModsSort", i, key)
						continue
					}
					if member.Type.Name != schema.BaseSort.Name {
						t.Errorf("sort[%d] key %q takes %s, not the %s this test knows how to check", i, key, member.Type.Name, schema.BaseSort.Name)
						continue
					}
					value, isObj := raw.(map[string]any)
					if !isObj {
						t.Errorf("sort[%d][%s] must be an object, got %T", i, key, raw)
						continue
					}
					for _, required := range schema.BaseSort.requiredMembers() {
						if _, present := value[required]; !present {
							t.Errorf("sort[%d][%s] has no %q (it is NON_NULL on %s)", i, key, required, schema.BaseSort.Name)
						}
					}
					for k, v := range value {
						if !baseMembers[k] {
							t.Errorf("sort[%d][%s] sends %q, which %s does not define", i, key, k, schema.BaseSort.Name)
						}
						if str, isStr := v.(string); k == "direction" && (!isStr || !directions[str]) {
							t.Errorf("sort[%d][%s].direction %v is not a member of %s", i, key, v, schema.Direction.Name)
						}
					}
				}
			}
		})
	}
}

// TestSearchQueryDeclaresTheRecordedSortArgument pins the query text against
// the recorded `sort` argument of `mods`: the variable is declared with that
// argument's own type and passed through under its own name. An unset
// variable is simply not sent (no sort), so the declaration is nullable.
func TestSearchQueryDeclaresTheRecordedSortArgument(t *testing.T) {
	schema := loadRecordedSortSchema(t)
	want := "$" + schema.ModsSortArg.Name + ": " + schema.ModsSortArg.Type.sdl()
	query, _ := captureSearchRequest(t, domain.SortUpdated)
	if !strings.Contains(query, want) {
		t.Errorf("the search query must declare %q (the recorded mods(%s:) type)", want, schema.ModsSortArg.Name)
	}
	if !strings.Contains(query, schema.ModsSortArg.Name+": $"+schema.ModsSortArg.Name) {
		t.Errorf("the search query must pass the variable to mods(%s:)", schema.ModsSortArg.Name)
	}
}

// TestSearchQuerySelectsOnlyRecordedModFields checks every field the search
// selects on a node against the recorded Mod type, and the types the Go
// decode relies on for the three #503's sorts key on.
func TestSearchQuerySelectsOnlyRecordedModFields(t *testing.T) {
	schema := loadRecordedSortSchema(t)
	recorded := map[string]inputField{}
	for _, f := range schema.ModFields {
		recorded[f.Name] = f
	}

	selected := selectedNodeFields(t, graphqlSearchQuery)
	for _, name := range selected {
		if _, ok := recorded[name]; !ok {
			t.Errorf("the search selects %q, which the recorded Mod type does not define", name)
		}
	}
	for name, leaf := range map[string]string{"downloads": "Int", "endorsements": "Int", "updatedAt": "DateTime"} {
		field, ok := recorded[name]
		if !ok {
			t.Errorf("the recording has no Mod.%s", name)
			continue
		}
		if got := field.Type.leafName(); got != leaf {
			t.Errorf("Mod.%s is recorded as %s, but the client decodes it as %s", name, got, leaf)
		}
		found := false
		for _, s := range selected {
			found = found || s == name
		}
		if !found {
			t.Errorf("the search must select Mod.%s (it is what a sort keys on)", name)
		}
	}
}

// selectedNodeFields returns the top-level field names selected inside the
// query's `nodes { ... }` block ("uploader { name }" yields "uploader").
func selectedNodeFields(t *testing.T, query string) []string {
	t.Helper()
	_, after, ok := strings.Cut(query, "nodes {")
	if !ok {
		t.Fatal("the search query has no nodes selection")
	}
	var out []string
	depth := 1
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 && depth == 1 {
			out = append(out, word.String())
		}
		word.Reset()
	}
	for _, r := range after {
		switch r {
		case '{':
			flush()
			depth++
		case '}':
			flush()
			depth--
		case ' ', '\n', '\t', '\r':
			flush()
		default:
			word.WriteRune(r)
		}
		if depth == 0 {
			break
		}
	}
	return out
}

// TestSearchModsSendsNoSortForRelevance pins the default: relevance (and the
// empty value) leaves the `sort` variable out entirely, so the upstream's own
// ordering is untouched.
func TestSearchModsSendsNoSortForRelevance(t *testing.T) {
	for _, s := range []domain.SearchSort{"", domain.SortRelevance} {
		_, vars := captureSearchRequest(t, s)
		if _, present := vars["sort"]; present {
			t.Errorf("sort %q must send no sort variable, got %#v", s, vars["sort"])
		}
	}
}
