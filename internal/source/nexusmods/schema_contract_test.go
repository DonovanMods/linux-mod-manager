package nexusmods

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// recordedSchema is the shape of testdata/modsfilter-schema.json - a hand
// recorded introspection of NexusMods' ModsFilter and the two filter-value
// input objects it is built from, plus their operator enums. See that
// file's own "_provenance" block for when and how it was taken.
//
// It exists because #337/#343: the client sent `tagNames` and `categoryId`,
// neither of which ModsFilter defines any more, so EVERY tag- or
// category-filtered NexusMods search failed with "Field is not defined on
// ModsFilter" - a whole flag broken with nothing in the build to notice.
// NOTHING here touches the network: the contract is checked against the
// recording, and the request is driven against an httptest server.
type recordedSchema struct {
	ModsFilter inputObject `json:"modsFilter"`
	Base       inputObject `json:"base"`
	BaseEW     inputObject `json:"baseEW"`
	Ops        enumType    `json:"ops"`
	OpsEW      enumType    `json:"opsEW"`
}

// inputObject is a recorded GraphQL INPUT_OBJECT: ModsFilter itself, and
// the two filter-value objects its list fields take.
type inputObject struct {
	Name        string       `json:"name"`
	InputFields []inputField `json:"inputFields"`
}

type inputField struct {
	Name string  `json:"name"`
	Type typeRef `json:"type"`
}

// typeRef is introspection's recursive type reference: a NON_NULL or LIST
// wrapper nests the type it wraps in OfType, a named type ends the chain.
type typeRef struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	OfType *typeRef `json:"ofType"`
}

type enumType struct {
	Name       string `json:"name"`
	EnumValues []struct {
		Name string `json:"name"`
	} `json:"enumValues"`
}

// requiredMembers names the input object's NON_NULL members - the ones every
// entry the client sends must carry. Read from the recording (review M4)
// rather than hardcoded as "value", so a rename in a re-recorded fixture
// changes what the test demands instead of quietly checking the old name.
func (o inputObject) requiredMembers() []string {
	var out []string
	for _, f := range o.InputFields {
		if f.Type.Kind == "NON_NULL" {
			out = append(out, f.Name)
		}
	}
	return out
}

// operatorMember names the input object's ENUM member and the enum it takes
// ("op", "FilterComparisonOperator"), both read from the recording.
func (o inputObject) operatorMember() (member, enum string) {
	for _, f := range o.InputFields {
		if f.Type.Kind == "ENUM" {
			return f.Name, f.Type.Name
		}
	}
	return "", ""
}

// elementTypeName unwraps a ModsFilter field's LIST-of-NON_NULL-of-X type
// down to X's name, which is the input object each of that field's entries
// must satisfy ("BaseFilterValue", "BaseFilterValueEqualsWildcard", ...).
func (s *recordedSchema) elementTypeName(field string) (string, bool) {
	for _, f := range s.ModsFilter.InputFields {
		if f.Name != field {
			continue
		}
		t := &f.Type
		for t.OfType != nil {
			t = t.OfType
		}
		return t.Name, t.Name != ""
	}
	return "", false
}

// filterValueObjects maps an element type name to the recorded input object
// it names, so the contract below can ask the RECORDING what a given
// ModsFilter field's entries must look like rather than carrying its own
// table of the answer (review M4).
func (s *recordedSchema) filterValueObjects() map[string]inputObject {
	out := map[string]inputObject{}
	for _, o := range []inputObject{s.Base, s.BaseEW} {
		if o.Name != "" {
			out[o.Name] = o
		}
	}
	return out
}

// enumMembers maps an enum type name to its recorded members.
func (s *recordedSchema) enumMembers() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, e := range []enumType{s.Ops, s.OpsEW} {
		if e.Name == "" {
			continue
		}
		members := map[string]bool{}
		for _, v := range e.EnumValues {
			members[v.Name] = true
		}
		out[e.Name] = members
	}
	return out
}

func loadRecordedSchema(t *testing.T) *recordedSchema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "modsfilter-schema.json"))
	if err != nil {
		t.Fatalf("reading the recorded schema: %v", err)
	}
	var s recordedSchema
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("parsing the recorded schema: %v", err)
	}
	if len(s.ModsFilter.InputFields) == 0 || len(s.Ops.EnumValues) == 0 ||
		len(s.Base.InputFields) == 0 || len(s.BaseEW.InputFields) == 0 {
		t.Fatal("the recorded schema is incomplete - the fixture is the whole point of this test")
	}
	return &s
}

// captureSearchVariables drives the REAL request builder (SearchMods, not a
// reimplementation of its filter map) against an httptest server that
// records the GraphQL variables it was sent.
func captureSearchVariables(t *testing.T, category string, tags []string) map[string]any {
	t.Helper()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding the request body: %v", err)
		}
		captured = req.Variables
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"data":{"mods":{"nodes":[]}}}`); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client(), "test-key")
	client.graphqlURL = server.URL
	if _, err := client.SearchMods(context.Background(), "skyrimspecialedition", "armour", category, tags, 20, 0); err != nil {
		t.Fatalf("SearchMods: %v", err)
	}
	if captured == nil {
		t.Fatal("the server captured no variables")
	}
	return captured
}

// TestSearchModsFilterMatchesRecordedSchema is the contract #337/#343 were
// missing: every key SearchMods puts in the ModsFilter map must be a field
// the recorded schema actually defines, and every "op" it sends must be a
// member of the operator enum for THAT field's own value type
// (BaseFilterValueEqualsWildcard accepts a narrower set than
// BaseFilterValue). RED before the fix on `tagNames` and `categoryId`.
func TestSearchModsFilterMatchesRecordedSchema(t *testing.T) {
	schema := loadRecordedSchema(t)
	valueObjects := schema.filterValueObjects()
	enums := schema.enumMembers()

	// Every optional filter exercised at once, so a key that only appears
	// with --category or --tag is covered too.
	vars := captureSearchVariables(t, "Armour", []string{"lore-friendly", "immersive"})
	filter, ok := vars["filter"].(map[string]any)
	if !ok {
		t.Fatalf("variables carry no filter object: %#v", vars)
	}
	if len(filter) != 4 {
		t.Errorf("expected gameDomainName, name, categoryName and tag; got %v", mapKeys(filter))
	}

	for field, raw := range filter {
		elem, defined := schema.elementTypeName(field)
		if !defined {
			t.Errorf("filter key %q is not an inputField of the recorded ModsFilter (fields: %v)", field, schemaFieldNames(schema))
			continue
		}
		valueObject, known := valueObjects[elem]
		if !known {
			t.Errorf("filter key %q takes %s, which this recording has no input object for", field, elem)
			continue
		}
		opMember, opEnum := valueObject.operatorMember()
		allowed, hasEnum := enums[opEnum]
		if !hasEnum {
			t.Errorf("%s's operator member takes %q, which this recording has no enum for", elem, opEnum)
			continue
		}
		entries, isList := raw.([]any)
		if !isList {
			t.Errorf("filter key %q must be a list of filter values, got %T", field, raw)
			continue
		}
		for i, e := range entries {
			entry, isObj := e.(map[string]any)
			if !isObj {
				t.Errorf("filter[%s][%d] must be an object, got %T", field, i, e)
				continue
			}
			for _, required := range valueObject.requiredMembers() {
				if _, present := entry[required]; !present {
					t.Errorf("filter[%s][%d] has no %q (it is NON_NULL on %s)", field, i, required, elem)
				}
			}
			op, _ := entry[opMember].(string)
			if op != "" && !allowed[op] {
				t.Errorf("filter[%s][%d] %s %q is not a member of %s, the enum %s accepts", field, i, opMember, op, opEnum, elem)
			}
		}
	}
}

// TestSearchModsFilterOmitsRetiredFields pins the two names by NAME: they
// are what NexusMods removed, and re-introducing either would break every
// filtered search again with a server-side error no unit test would see.
func TestSearchModsFilterOmitsRetiredFields(t *testing.T) {
	filter, _ := captureSearchVariables(t, "Armour", []string{"lore-friendly"})["filter"].(map[string]any)
	for _, retired := range []string{"tagNames", "categoryId"} {
		if _, present := filter[retired]; present {
			t.Errorf("filter still sends %q, which ModsFilter no longer defines (#337/#343)", retired)
		}
	}
	if _, present := filter["tag"]; !present {
		t.Error("a tag-filtered search must send the schema's own `tag` field")
	}
	if _, present := filter["categoryName"]; !present {
		t.Error("a category-filtered search must send the schema's own `categoryName` field")
	}
}

// An unfiltered search sends neither optional key at all.
func TestSearchModsFilterOmitsUnusedOptionalFilters(t *testing.T) {
	filter, _ := captureSearchVariables(t, "", nil)["filter"].(map[string]any)
	if len(filter) != 2 {
		t.Errorf("an unfiltered search sends only gameDomainName and name; got %v", mapKeys(filter))
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func schemaFieldNames(s *recordedSchema) []string {
	out := make([]string, 0, len(s.ModsFilter.InputFields))
	for _, f := range s.ModsFilter.InputFields {
		out = append(out, f.Name)
	}
	return out
}
