// profile_reorder.go holds `lmm profile reorder -i`'s interactive picker
// (#254): the numbered prompt that lets a user reorder a profile's load
// order by POSITION instead of by mod ID.
//
// Why it exists: the arg form (`lmm profile reorder 12345 67890 11111`)
// requires knowing every mod's ID up front, in the order you want them,
// before you type anything - so in practice you ran the bare command to
// print the table, copied the IDs out by hand, and retyped them all. The
// web UI has a drag handle; this is the CLI's answer to the same gesture.
//
// It follows the install file picker's precedent exactly (cmd/lmm/install
// .go): rows printed by a small formatter, then a prompt loop with ONE
// bufio.Reader created before the loop and shared across every attempt -
// re-wrapping the underlying reader per attempt silently discards whatever
// that attempt had buffered past its line, which is the trap install.go's
// own comment documents.
//
// It does NOT reuse parseRangeSelection. That parser SORTS and DEDUPES its
// result, because for file selection the order the user typed is
// irrelevant. Here the typed order IS the entire payload: reusing it would
// turn "1,3,2" back into "1,2,3" and make the command a silent no-op.
// parseReorderSelection below is its own parser for exactly that reason.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// reorderPositions turns a typed permutation line into the profile-relative
// positions it names, in the order they were typed.
//
// Accepted: a comma-separated list of 1-based positions and ascending
// ranges ("2", "1,3,2", "2-5,1", "2..5,1"). Ranges expand in ascending
// order and exist for the one case that is genuinely painful without them -
// moving a block, or moving one entry to the END of a long list, which
// otherwise means retyping every other position.
//
// Rejected, each with its own message: a non-number, a position outside
// 1..max, and a DUPLICATE. Duplicates are an error rather than deduplicated
// because "1,3,3" is a typo about ordering, and quietly accepting it would
// produce an order the user did not ask for (#254's own trap note).
//
// A PARTIAL list is allowed and is not an error: the positions named come
// first, in the typed order, and every position not named is appended
// afterwards in its current relative order. That is exactly what the
// existing arg form does (core.ResolveReorder), which is what makes the
// two forms consistent - and it is stated in the prompt's help line,
// because silently completing a partial permutation is the sort of thing
// that surprises people.
func reorderPositions(input string, max int) ([]int, error) {
	var positions []int
	seen := make(map[int]bool, max)

	add := func(n int) error {
		if n < 1 || n > max {
			return fmt.Errorf("position %d is out of range (1-%d)", n, max)
		}
		if seen[n] {
			return fmt.Errorf("position %d is listed more than once", n)
		}
		seen[n] = true
		positions = append(positions, n)
		return nil
	}

	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("empty entry in the list")
		}

		lo, hi, isRange := cutReorderRange(part)
		if !isRange {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("%q is not a position number", part)
			}
			if err := add(n); err != nil {
				return nil, err
			}
			continue
		}

		start, err := strconv.Atoi(lo)
		if err != nil {
			return nil, fmt.Errorf("%q is not a position number", lo)
		}
		end, err := strconv.Atoi(hi)
		if err != nil {
			return nil, fmt.Errorf("%q is not a position number", hi)
		}
		if end < start {
			return nil, fmt.Errorf("range %q must be ascending", part)
		}
		for n := start; n <= end; n++ {
			if err := add(n); err != nil {
				return nil, err
			}
		}
	}

	if len(positions) == 0 {
		return nil, errors.New("no positions given")
	}
	return positions, nil
}

// cutReorderRange splits "2-5" or "2..5" into its ends. The ".." form is
// tried first so "2..5" is never mistaken for a "2." to "5" hyphen split.
func cutReorderRange(part string) (lo, hi string, ok bool) {
	if lo, hi, ok = strings.Cut(part, ".."); ok {
		return strings.TrimSpace(lo), strings.TrimSpace(hi), true
	}
	if lo, hi, ok = strings.Cut(part, "-"); ok {
		return strings.TrimSpace(lo), strings.TrimSpace(hi), true
	}
	return "", "", false
}

// printLoadOrderRows prints the numbered load order the picker reorders -
// the same "#/MOD_ID/NAME" columns the bare `lmm profile reorder` readout
// uses, so the two views of one profile never disagree. names is keyed by
// domain.ModKey; a ref with no installed row falls back to "(unknown)",
// matching the readout.
func printLoadOrderRows(w io.Writer, refs []domain.ModReference, names map[string]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "#\tMOD_ID\tNAME"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	for i, ref := range refs {
		name := names[domain.ModKey(ref.SourceID, ref.ModID)]
		if name == "" {
			name = "(unknown)"
		}
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\n", i+1, ref.ModID, name); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	return tw.Flush()
}

// promptReorder is promptReorderFrom over os.Stdin.
func promptReorder(profileName string, refs []domain.ModReference, names map[string]string) ([]string, error) {
	return promptReorderFrom(os.Stdin, profileName, refs, names)
}

// promptReorderFrom prints refs as a numbered load order and reads ONE
// permutation line from r, retrying on invalid input, and returns the mod
// keys ("<source-id>:<mod-id>") in the order the user typed - ready to hand
// straight to core.ResolveReorder, which resolves them against the profile
// and appends whatever went unmentioned.
//
// Threaded in as an io.Reader (rather than reading os.Stdin directly) for
// the same reason promptMultiSelectionFrom is: the loop is then unit
// -testable with no TTY at all.
//
// Empty input returns a nil selection, meaning "keep the current order" -
// the caller writes nothing. "q"/"Q" returns ErrCancelled. A read failure
// (a closed or empty stdin - `-i` with nothing piped in) is returned as-is
// rather than retried, so the command fails cleanly instead of spinning.
func promptReorderFrom(r io.Reader, profileName string, refs []domain.ModReference, names map[string]string) ([]string, error) {
	fmt.Printf("\nLoad order for %s (first = lowest priority):\n\n", profileName)
	if err := printLoadOrderRows(os.Stdout, refs, names); err != nil {
		return nil, err
	}
	fmt.Println("\nEnter the positions in the order you want them (e.g. 1,3,2 or 2-5,1).")
	fmt.Println("Positions you leave out keep their current relative order at the end.")

	// Created ONCE and shared across every retry below: re-wrapping r in a
	// fresh bufio.Reader per attempt silently drops whatever that attempt
	// had buffered past the line it returned (see this file's doc comment,
	// and cmd/lmm/install.go's own note on the same trap).
	reader := bufio.NewReader(r)
	for {
		fmt.Printf("\nNew order (Enter to keep, q to cancel): ")
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return nil, fmt.Errorf("reading input: %w", err)
		}

		line = strings.TrimSpace(line)
		switch line {
		case "":
			return nil, nil
		case "q", "Q":
			return nil, ErrCancelled
		}

		positions, err := reorderPositions(line, len(refs))
		if err != nil {
			fmt.Printf("Invalid order: %v\n", err)
			continue
		}

		keys := make([]string, 0, len(positions))
		for _, n := range positions {
			ref := refs[n-1]
			keys = append(keys, domain.ModKey(ref.SourceID, ref.ModID))
		}
		return keys, nil
	}
}
