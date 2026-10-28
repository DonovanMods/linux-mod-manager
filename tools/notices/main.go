// Command notices regenerates THIRD_PARTY_NOTICES (`make notices`, #522).
//
// It must run from the repository root (or be pointed at it with -root).
// Standard library only; see internal/notices for what it reads.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/notices"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	doc, err := notices.Generate(context.Background(), root)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, notices.FileName), doc, 0o644)
}
