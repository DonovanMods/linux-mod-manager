package serve

// The CSRF refusal, route by route. The guard itself is one middleware,
// tested in middleware_test.go; these tests pin that every state-changing
// /api/v1 route actually sits behind it (a route registered without wrap
// would not), and that a refused request leaves no trace.

import (
	"crypto/sha256"
	"encoding/json/v2"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registeredRoute is one method-qualified pattern routes.go hands the mux.
type registeredRoute struct{ method, pattern string }

// unsafeAPIRoutes reads every state-changing /api/v1 pattern out of
// routes.go itself. net/http's ServeMux cannot list what it holds, and
// reading the source means a route added there is in this test's table
// without anyone remembering to add it.
func unsafeAPIRoutes(t *testing.T) []registeredRoute {
	t.Helper()
	src, err := os.ReadFile("routes.go")
	require.NoError(t, err)
	re := regexp.MustCompile(`s\.mux\.Handle\("([A-Z]+) (/api/v1/[^"]*)"`)
	var out []registeredRoute
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		if unsafeMethod(m[1]) {
			out = append(out, registeredRoute{method: m[1], pattern: m[2]})
		}
	}
	return out
}

// fillWildcards turns a mux pattern into a concrete path. The values only
// need to be plausible: the guard refuses before any handler reads them.
func fillWildcards(pattern string) string {
	values := map[string]string{
		"{source}": fixtureSourceID,
		"{id}":     "m1",
		"{name}":   activeProfile,
		"{kind}":   "deploy",
	}
	return regexp.MustCompile(`\{[a-z]+\}`).ReplaceAllStringFunc(pattern, func(w string) string {
		if v, ok := values[w]; ok {
			return v
		}
		return "x"
	})
}

// treeDigest maps every entry under each root to its content (a file's
// hash, a link's target, or a directory marker), so two digests compare
// equal exactly when nothing under the roots was written, removed or
// relinked.
func treeDigest(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			switch {
			case d.Type()&fs.ModeSymlink != 0:
				target, err := os.Readlink(path)
				out[path] = "link:" + target
				return err
			case d.IsDir():
				out[path] = "dir"
			default:
				data, err := os.ReadFile(path)
				sum := sha256.Sum256(data)
				out[path] = string(sum[:])
				return err
			}
			return nil
		}))
	}
	return out
}

// TestAPI_EveryUnsafeRouteRefusesWithoutCSRF holds EVERY state-changing
// /api/v1 route to the guard: no token is a 403 in the {"error","details"}
// JSON envelope every other /api/v1 failure uses (epic live review M1 -
// not a bare text/plain http.Error), and the request writes nothing: no
// config, profile, cache or game-directory change, no plan, upload or job.
func TestAPI_EveryUnsafeRouteRefusesWithoutCSRF(t *testing.T) {
	routes := unsafeAPIRoutes(t)
	// A sanity floor, so a routes.go reshuffle that defeats the regexp
	// fails here instead of passing an empty table.
	require.Greater(t, len(routes), 30)
	assert.Contains(t, routes, registeredRoute{http.MethodPost, "/api/v1/plans/{kind}"})
	assert.Contains(t, routes, registeredRoute{http.MethodPost, "/api/v1/jobs"})

	s, svc, game := newProfilesFixtureServer(t)
	roots := []string{svc.ConfigDir(), svc.CacheDir(), game.InstallPath, game.ModPath}

	for _, route := range routes {
		t.Run(route.method+" "+route.pattern, func(t *testing.T) {
			before := treeDigest(t, roots...)

			rec := doAPIWithoutCSRF(s, route.method, scoped(fillWildcards(route.pattern), game), `{}`)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			var envelope apiErrorEnvelope
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope, json.RejectUnknownMembers(true)))
			assert.Contains(t, envelope.Error, "CSRF")

			assert.Equal(t, before, treeDigest(t, roots...), "a refused request must write nothing")
			assert.Zero(t, s.plans.len(), "a refused request must issue no plan")
			assert.Zero(t, s.uploads.len(), "a refused request must stage no upload")
			assert.Empty(t, s.jobs.list(), "a refused request must start no job")
		})
	}
}

// csrfRefusal is one refused request and the end state that proves it
// did nothing - the half of a route's state (a DB row, a fake source's
// record, a plan's single use) the tree digest above cannot see.
type csrfRefusal struct {
	s           *Server
	method      string
	target      string
	body        string
	contentType string
	unchanged   func(t *testing.T)
}

// TestAPI_RefusedWithoutCSRF_ChangesNothing: for the routes whose effect
// lives somewhere other than a file, a refused request leaves that effect
// undone.
func TestAPI_RefusedWithoutCSRF_ChangesNothing(t *testing.T) {
	profileCRUD := func(method, target, body string) func(t *testing.T) csrfRefusal {
		return func(t *testing.T) csrfRefusal {
			s, svc, game := newProfilesFixtureServer(t)
			return csrfRefusal{s: s, method: method, target: scoped(target, game), body: body,
				unchanged: func(t *testing.T) {
					names, err := svc.NewProfileManager().ListNames(t.Context(), game.ID)
					require.NoError(t, err)
					assert.ElementsMatch(t, []string{activeProfile, switchTargetProfile, applyTargetProfile}, names,
						"a refused request must not have changed the profile set")
				}}
		}
	}
	toggle := func(action string) func(t *testing.T) csrfRefusal {
		return func(t *testing.T) csrfRefusal {
			s, svc, game := newFlowFixtureServer(t)
			return csrfRefusal{s: s, method: http.MethodPost,
				target: scoped("/api/v1/mods/"+fixtureSourceID+"/m1/"+action, game),
				unchanged: func(t *testing.T) {
					mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
					require.NoError(t, err)
					assert.True(t, mod.Enabled)
				}}
		}
	}

	tests := []struct {
		name    string
		arrange func(t *testing.T) csrfRefusal
	}{
		{"install plan installs nothing", func(t *testing.T) csrfRefusal {
			s, svc, game, _ := newInstallFixtureServer(t)
			return csrfRefusal{s: s, method: http.MethodPost, target: scoped("/api/v1/plans/install", game),
				body: installPlanBody(installModID),
				unchanged: func(t *testing.T) {
					_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, installModID, game.ID, "default")
					require.ErrorIs(t, err, domain.ErrModNotFound)
				}}
		}},
		{"enable leaves the mod as it was", toggle("enable")},
		{"disable leaves the mod enabled", toggle("disable")},
		{"reorder keeps the load order", func(t *testing.T) csrfRefusal {
			s, svc, game := newProfilesFixtureServer(t)
			return csrfRefusal{s: s, method: http.MethodPost, target: reorderTarget(game), body: `{"ids":["p3","p1"]}`,
				unchanged: func(t *testing.T) {
					stored, err := svc.NewProfileManager().Get(t.Context(), game.ID, activeProfile)
					require.NoError(t, err)
					assert.Equal(t, "p1", stored.Mods[0].ModID)
				}}
		}},
		{"job start leaves the plan unconsumed", func(t *testing.T) csrfRefusal {
			s, _ := newDeployFixtureServer(t)
			id := planDeployFixture(t, s)
			return csrfRefusal{s: s, method: http.MethodPost, target: "/api/v1/jobs", body: `{"plan_id":"` + string(id) + `"}`,
				unchanged: func(t *testing.T) { assert.Equal(t, 1, s.plans.len()) }}
		}},
		{"verify_fix plan repairs nothing", func(t *testing.T) csrfRefusal {
			s, _, game := newFlowFixtureServer(t)
			stray := strayDeployment(t, s, game, "stray.pak")
			return csrfRefusal{s: s, method: http.MethodPost, target: scoped("/api/v1/plans/verify_fix", game),
				unchanged: func(t *testing.T) {
					_, err := os.Lstat(stray)
					require.NoError(t, err)
				}}
		}},
		{"launch runs nothing", func(t *testing.T) csrfRefusal {
			s, _, game, f := newLaunchServer(t)
			deployFixtureProfile(t, s, game)
			return csrfRefusal{s: s, method: http.MethodPost, target: launchPath(game), body: `{}`,
				unchanged: func(t *testing.T) { assert.Empty(t, f.recorded()) }}
		}},
		{"a multipart upload stages nothing", func(t *testing.T) csrfRefusal {
			s, _ := newDeployFixtureServer(t)
			body, contentType := multipartUpload(t, uploadFormField, "x.zip", []byte("x"))
			return csrfRefusal{s: s, method: http.MethodPost, target: "/api/v1/uploads", body: body, contentType: contentType,
				unchanged: func(t *testing.T) { assert.Equal(t, 0, s.uploads.len()) }}
		}},
		{"index refresh forces no rebuild", func(t *testing.T) csrfRefusal {
			s, _, game, src := newIndexFixtureServer(t)
			return csrfRefusal{s: s, method: http.MethodPost, target: indexPath(game.ID), body: `{"refresh":true}`,
				unchanged: func(t *testing.T) { assert.False(t, src.forced) }}
		}},
		{"index prune removes nothing", func(t *testing.T) csrfRefusal {
			s, _, _, src := newIndexFixtureServer(t)
			return csrfRefusal{s: s, method: http.MethodPost, target: "/api/v1/indexes/prune", body: `{"all":true}`,
				unchanged: func(t *testing.T) { assert.Empty(t, src.removed) }}
		}},
		{"profile create", profileCRUD(http.MethodPost, "/api/v1/profiles", `{"name":"survival"}`)},
		{"profile delete", profileCRUD(http.MethodDelete, "/api/v1/profiles/"+switchTargetProfile, "")},
		{"profile set-default", profileCRUD(http.MethodPost, "/api/v1/profiles/"+switchTargetProfile+"/set-default", "")},
		{"profile rename", profileCRUD(http.MethodPost, "/api/v1/profiles/"+activeProfile+"/rename", `{"name":"survival"}`)},
		{"profile duplicate", profileCRUD(http.MethodPost, "/api/v1/profiles/"+activeProfile+"/duplicate", `{"name":"survival"}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.arrange(t)
			req := apiRequest(c.s, c.method, c.target, c.body)
			req.Header.Del(csrfHeaderName)
			if c.contentType != "" {
				req.Header.Set("Content-Type", c.contentType)
			}
			rec := httptest.NewRecorder()
			c.s.Handler().ServeHTTP(rec, req)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "CSRF")
			c.unchanged(t)
		})
	}
}
