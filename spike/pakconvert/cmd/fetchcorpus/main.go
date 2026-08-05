// fetchcorpus downloads a spike corpus of Icarus mods (pak + exmodz files)
// from the public Firestore catalog into -dir (OUTSIDE the repo; never
// committed). Manual usage:
//
//	go run ./spike/pakconvert/cmd/fetchcorpus -dir ~/lmm-spike-corpus [-dual 8] [-census-all]
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
	"github.com/DonovanMods/linux-mod-manager/spike/pakconvert"
)

// Named spike targets (design doc "Corpus" section), matched case-insensitively.
var targets = []string{"flooflevelcap", "intreeg", "larkwell", "turret"}

func main() {
	dir := flag.String("dir", "", "corpus directory (required, outside the repo)")
	dual := flag.Int("dual", 8, "number of dual-form (pak+exmodz) mods to fetch")
	censusAll := flag.Bool("census-all", false, "sweep GetModFiles for the WHOLE catalog census")
	flag.Parse()
	if *dir == "" {
		log.Fatal("-dir is required")
	}

	ctx := context.Background()
	src := icarus.New(nil, "projectdaedalus-fb09f") // public catalog, no auth

	// PageSize 2000 >> catalog size (~540) so one page holds everything.
	res, err := src.Search(ctx, source.SearchQuery{PageSize: 2000})
	if err != nil {
		log.Fatalf("catalog search: %v", err)
	}
	fmt.Printf("catalog: %d mods\n", len(res.Mods))

	selected := pakconvert.MatchTargets(res.Mods, targets)
	selectedIDs := map[string]bool{}
	for _, m := range selected {
		selectedIDs[m.ID] = true
	}

	// Deterministic scan order for the dual-form quota and census.
	byID := append([]domain.Mod(nil), res.Mods...)
	sort.Slice(byID, func(i, j int) bool { return byID[i].ID < byID[j].ID })

	var census []pakconvert.CensusEntry
	var manifest pakconvert.Manifest
	dualFound := 0
	for _, m := range byID {
		needDual := dualFound < *dual
		if !*censusAll && !needDual && !selectedIDs[m.ID] {
			continue
		}
		time.Sleep(200 * time.Millisecond) // be polite to the public API
		files, err := src.GetModFiles(ctx, &m)
		if err != nil {
			log.Printf("WARN GetModFiles %s (%s): %v", m.ID, m.Name, err)
			continue
		}
		var pakFile, exmodzFile *domain.DownloadableFile
		for i := range files {
			switch files[i].ID {
			case "pak":
				pakFile = &files[i]
			case "exmodz":
				exmodzFile = &files[i]
			}
		}
		census = append(census, pakconvert.CensusEntry{
			ID: m.ID, Name: m.Name,
			HasPak: pakFile != nil, HasExmodz: exmodzFile != nil,
		})

		isDual := pakFile != nil && exmodzFile != nil
		fetch := selectedIDs[m.ID] || (isDual && dualFound < *dual)
		if !fetch {
			continue
		}
		if isDual && !selectedIDs[m.ID] {
			dualFound++
		}
		cm := pakconvert.CorpusMod{ID: m.ID, Name: m.Name, Version: m.Version, Week: m.Category}
		if pakFile != nil {
			cm.Pak = download(ctx, src, &m, pakFile, *dir)
		}
		if exmodzFile != nil {
			cm.Exmodz = download(ctx, src, &m, exmodzFile, *dir)
		}
		manifest.Mods = append(manifest.Mods, cm)
		fmt.Printf("fetched %s (%s) pak=%q exmodz=%q\n", m.Name, m.ID, cm.Pak, cm.Exmodz)
	}

	if err := pakconvert.SaveJSON(filepath.Join(*dir, "manifest.json"), manifest); err != nil {
		log.Fatalf("writing manifest: %v", err)
	}
	if err := pakconvert.SaveJSON(filepath.Join(*dir, "catalog-census.json"), census); err != nil {
		log.Fatalf("writing census: %v", err)
	}
	fmt.Printf("done: %d mods fetched, %d census rows -> %s\n", len(manifest.Mods), len(census), *dir)
}

// download fetches one file and returns its corpus-relative path ("" on failure).
func download(ctx context.Context, src *icarus.Icarus, m *domain.Mod, f *domain.DownloadableFile, dir string) string {
	url, err := src.GetDownloadURL(ctx, m, f.ID)
	if err != nil {
		log.Printf("WARN GetDownloadURL %s/%s: %v", m.ID, f.ID, err)
		return ""
	}
	rel := filepath.Join(m.ID, f.FileName)
	dest := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		log.Printf("WARN mkdir %s: %v", dest, err)
		return ""
	}
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("WARN GET %s: %v", url, err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("WARN GET %s: status %s", url, resp.Status)
		return ""
	}
	out, err := os.Create(dest)
	if err != nil {
		log.Printf("WARN create %s: %v", dest, err)
		return ""
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		os.Remove(dest)
		log.Printf("WARN saving %s: %v", dest, err)
		return ""
	}
	return rel
}
