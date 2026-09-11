package core_test

// The adapter seam and the BepInEx normaliser in the SAME import (#411,
// #358/#359).
//
// Both rewrite an extracted archive's layout, at every seam that ingests
// one, and the plan has to predict what the ingest will do. That only holds
// if the two run in one fixed order on one member list: #358's normaliser
// first, against the archive as the user packaged it, then the game's
// adapter, against what normalisation produced. This file is the pin.

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prefixStub moves every member under one extra directory, keeping the rest
// of the path intact. Unlike byNameStub it PRESERVES structure, which is
// what makes the running order visible: "adapted/BepInEx/plugins/x.dll"
// only happens if the BepInEx normaliser ran first.
type prefixStub struct{}

func (prefixStub) ID() string    { return "prefix" }
func (prefixStub) Label() string { return "Prefix" }

func (prefixStub) NormalizeArchive(req adapter.NormalizeRequest) (adapter.Layout, error) {
	rewrites := make(map[string]string, len(req.Members))
	for _, m := range req.Members {
		rewrites[m] = "adapted/" + m
	}
	return adapter.NewLayout("prefix", rewrites), nil
}

// TestPlanImportArchive_AgreesWithIngestForALoaderGame drives every BepInEx
// archive shape through a game that declares the loader, with the identity
// adapter and with a rewriting one, and asserts the two properties that must
// both survive the normaliser and the adapter running together:
//
//   - the plan's file list is exactly what the ingest caches, which is the
//     seam's whole invariant; and
//   - the paths are the adapter's rewrite OF the normaliser's output, not
//     the other way round - so the plan a BepInEx user confirms is the tree
//     their game directory ends up with.
//
// It also covers the loader-rooted shape planIngestCorpus can no longer
// carry: #359 refuses one into a game declaring no loader, which is every
// game that corpus builds.
func TestPlanImportArchive_AgreesWithIngestForALoaderGame(t *testing.T) {
	// want is the NORMALISED tree - what #358 alone produces. A rewriting
	// adapter's expectation is derived from it, which is the point: the
	// adapter sees normalisation's output, never the raw archive.
	cases := map[string]struct {
		members []string
		want    []string
	}{
		"wrapped loader pack": {
			members: []string{"MyPack/BepInEx/plugins/Foo.dll", "MyPack/manifest.json"},
			want:    []string{"BepInEx/plugins/Foo.dll"},
		},
		"loader rooted": {
			members: []string{"BepInEx/plugins/Foo.dll", "BepInEx/config/Foo.cfg"},
			want:    []string{"BepInEx/config/Foo.cfg", "BepInEx/plugins/Foo.dll"},
		},
		"bare plugins root": {
			members: []string{"plugins/Foo.dll"},
			want:    []string{"BepInEx/plugins/Foo.dll"},
		},
		"loose root dll": {
			members: []string{"Foo.dll"},
			want:    []string{"BepInEx/plugins/Flat-2.0/Foo.dll"},
		},
	}

	for _, ad := range []adapter.GameAdapter{adapter.Generic{}, prefixStub{}} {
		t.Run(ad.ID(), func(t *testing.T) {
			for name, tc := range cases {
				t.Run(name, func(t *testing.T) {
					svc, game := newImportArchiveTestService(t)
					svc.RegisterAdapter(ad)
					game.Adapter = ad.ID()
					game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx}
					require.NoError(t, svc.SaveGame(context.Background(), game))

					files := make(map[string]string, len(tc.members))
					for _, m := range tc.members {
						files[m] = "content of " + m
					}
					archivePath := filepath.Join(t.TempDir(), "Flat-2.0.zip")
					createImportTestZip(t, archivePath, files)

					want := tc.want
					if ad.ID() == (prefixStub{}).ID() {
						want = make([]string, len(tc.want))
						for i, p := range tc.want {
							want[i] = "adapted/" + p
						}
					}

					plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
					require.NoError(t, err)
					assert.Equal(t, want, plan.Files,
						"the plan must promise the adapter's rewrite of the NORMALISED tree")

					result, err := svc.ApplyImportArchive(context.Background(), game, "default", plan, core.ImportArchiveOptions{}, nil)
					require.NoError(t, err)

					cached, err := svc.GetGameCache(game).ListFiles(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
					require.NoError(t, err)
					slices.Sort(cached)
					assert.Equal(t, plan.Files, cached,
						"the plan's file list must equal what the ingest actually cached")
				})
			}
		})
	}
}
