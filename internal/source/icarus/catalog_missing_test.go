package icarus

// #539: the Project Daedalus catalog recreated mods under new document IDs,
// so an installed mod's old ID answers HTTP 404. That is the source API's
// own "this mod does not exist" classification, and it must survive as
// domain.ErrModNotFound - not an opaque HTTP error - so the update check
// can report the mod as gone instead of failing the whole source.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// catalogServer answers a single-document GET from catalog, a 404 with
// Firestore's own NOT_FOUND body for an id listed in gone, and a 500 for
// one listed in broken.
func catalogServer(t *testing.T, catalog map[string]map[string]any, gone, broken []string) *Icarus {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := path.Base(r.URL.Path)
		for _, g := range gone {
			if g == id {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Document not found.","status":"NOT_FOUND"}}`))
				return
			}
		}
		for _, b := range broken {
			if b == id {
				http.Error(w, "backend unavailable", http.StatusInternalServerError)
				return
			}
		}
		fields, ok := catalog[id]
		if !ok {
			t.Errorf("unexpected document request for %q", id)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"name":   "projects/p/databases/(default)/documents/mods/" + id,
			"fields": fields,
		})
	}))
	t.Cleanup(srv.Close)
	src := New(srv.Client(), "test-project")
	src.firestore.baseURL = srv.URL
	return src
}

func TestIcarus_GetMod_404IsErrModNotFound(t *testing.T) {
	src := catalogServer(t, nil, []string{"dLs3nvmWj5uOPnXxezGe"}, nil)

	_, err := src.GetMod(context.Background(), "icarus", "dLs3nvmWj5uOPnXxezGe")
	if !errors.Is(err, domain.ErrModNotFound) {
		t.Fatalf("GetMod on a 404 = %v, want errors.Is domain.ErrModNotFound", err)
	}
	if !strings.Contains(err.Error(), "dLs3nvmWj5uOPnXxezGe") {
		t.Errorf("error %q does not name the mod", err)
	}
	if strings.Contains(err.Error(), "NOT_FOUND") {
		t.Errorf("error %q carries Firestore's raw body; a 404 is classified, not dumped", err)
	}
}

func TestIcarus_GetModFiles_404IsErrModNotFound(t *testing.T) {
	src := catalogServer(t, nil, []string{"gone"}, nil)

	_, err := src.GetModFiles(context.Background(), &domain.Mod{ID: "gone"})
	if !errors.Is(err, domain.ErrModNotFound) {
		t.Fatalf("GetModFiles on a 404 = %v, want errors.Is domain.ErrModNotFound", err)
	}
}

// Any other failure keeps today's shape: not ErrModNotFound, and the body
// still in the message for diagnosis.
func TestIcarus_GetMod_500IsNotErrModNotFound(t *testing.T) {
	src := catalogServer(t, nil, nil, []string{"broken"})

	_, err := src.GetMod(context.Background(), "icarus", "broken")
	if err == nil || errors.Is(err, domain.ErrModNotFound) {
		t.Fatalf("GetMod on a 500 = %v, want a non-ErrModNotFound error", err)
	}
	if !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "backend unavailable") {
		t.Errorf("error %q lost the status or body", err)
	}
}

// One vanished mod among several: the rest are still checked, the vanished
// one is reported per mod as a *source.ModNotFoundError, and every failure
// is reported - not just the first.
func TestIcarus_CheckUpdates_GoneModDoesNotHideTheRest(t *testing.T) {
	catalog := map[string]map[string]any{
		"abc": {"name": map[string]any{"stringValue": "Bear Mount"}, "version": map[string]any{"stringValue": "3.3"}},
		"def": {"name": map[string]any{"stringValue": "Wolf Pack"}, "version": map[string]any{"stringValue": "1.0"}},
	}
	src := catalogServer(t, catalog, []string{"gone1", "gone2"}, []string{"broken"})

	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "gone1", Name: "Old One", Version: "1.0"}},
		{Mod: domain.Mod{ID: "abc", Name: "Bear Mount", Version: "3.2"}},
		{Mod: domain.Mod{ID: "broken", Name: "Flaky", Version: "1.0"}},
		{Mod: domain.Mod{ID: "gone2", Name: "Old Two", Version: "1.0"}},
		{Mod: domain.Mod{ID: "def", Name: "Wolf Pack", Version: "1.0"}},
	}

	updates, err := src.CheckUpdates(context.Background(), installed)
	if len(updates) != 1 || updates[0].InstalledMod.ID != "abc" || updates[0].NewVersion != "3.3" {
		t.Fatalf("updates = %+v, want exactly one update for abc -> 3.3", updates)
	}
	if err == nil {
		t.Fatal("CheckUpdates returned no error; the gone and broken mods must be reported")
	}

	missing := map[string]bool{}
	var walk func(error)
	walk = func(e error) {
		var nf *source.ModNotFoundError
		if errors.As(e, &nf) && e == error(nf) {
			missing[nf.ModID] = true
			return
		}
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, c := range u.Unwrap() {
				walk(c)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	if !missing["gone1"] || !missing["gone2"] || len(missing) != 2 {
		t.Errorf("per-mod ModNotFoundErrors = %v, want exactly gone1 and gone2", missing)
	}
	for _, want := range []string{"Old One", "Old Two", "Flaky", "HTTP 500"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not report %q - every failure is named, not just the first", err, want)
		}
	}
}
