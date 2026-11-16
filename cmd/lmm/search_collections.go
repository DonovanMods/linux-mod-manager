package main

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// doSearchCollections is `lmm search --collections` (#651): one page of the
// game's collections from core.SearchCollections, as a table whose URL
// column is what `lmm profile import --nexus-collection` takes, or the
// core.CollectionSearchReport document under --json.
func doSearchCollections(ctx context.Context, service *core.Service, game *domain.Game, query string) error {
	report, err := service.SearchCollections(ctx, game, query, core.CollectionSearchOptions{
		SourceID: searchSource,
		Sort:     domain.SearchSort(searchSort),
		PageSize: searchPageSize(searchLimit),
	})
	if err != nil {
		if core.IsCollectionSearchUnsupported(err) {
			return err
		}
		return fmt.Errorf("collection search failed: %w", err)
	}
	if jsonOutput {
		return emitJSON(report)
	}
	if len(report.Collections) == 0 {
		fmt.Println("No collections found.")
		return nil
	}

	var buf bytes.Buffer
	w := newDisplayWidthTableWriter(&buf)
	if _, err := fmt.Fprintln(w, "NAME\tAUTHOR\tENDORSEMENTS\tMODS\tREVISION\tURL\t"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintln(w, "----\t------\t------------\t----\t--------\t---\t"); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}
	for _, c := range report.Collections {
		adult := ""
		if c.Adult {
			adult = "[adult]"
		}
		cells := []string{
			truncate(c.Name, 40),
			truncate(c.Author, 20),
			strconv.Itoa(c.Endorsements),
			strconv.Itoa(c.ModCount),
			strconv.Itoa(c.Revision),
			c.URL,
			adult,
		}
		if _, err := fmt.Fprintln(w, strings.Join(cells, "\t")); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}
	if err := printTable(&buf, 2, nil); err != nil {
		return fmt.Errorf("writing table: %w", err)
	}
	fmt.Printf("\n%d of %d collections. Import one as a new profile: lmm profile import --nexus-collection <url>\n",
		len(report.Collections), report.TotalResults)
	return nil
}
