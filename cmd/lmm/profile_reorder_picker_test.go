package main

import (
	"context"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #254's picker. Every prompt loop here is driven through an injected
// io.Reader, per the install-picker precedent - no TTY is needed, and none
// of these tests may ever touch os.Stdin.

// withInteractiveReorder turns -i on for one test and puts it back
// afterwards - the flag is a package-level cobra binding, like every other
// flag these tests set.
func withInteractiveReorder(t *testing.T) {
	t.Helper()
	profileReorderInteract = true
	t.Cleanup(func() { profileReorderInteract = false })
}

// setupPickerProfile seeds a three-mod profile whose load order is
// alpha, beta, gamma - the order every case below permutes.
func setupPickerProfile(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := setupDoProfileSwitchTest(t)
	for _, id := range []string{"alpha", "beta", "gamma"} {
		require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
			Mod:          domain.Mod{ID: id, SourceID: "src1", Name: strings.ToUpper(id[:1]) + id[1:] + " Mod", Version: "1.0", GameID: game.ID},
			ProfileName:  "default",
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
		}))
		addProfileMod(t, svc, game, "default", "src1", id)
	}
	return svc, game
}

func modIDs(p *domain.Profile) []string {
	ids := make([]string, 0, len(p.Mods))
	for _, ref := range p.Mods {
		ids = append(ids, ref.ModID)
	}
	return ids
}

// TestReorderPositions is the parser's own table. It exists chiefly to pin
// the two rules parseRangeSelection gets deliberately WRONG for this job:
// the typed order is preserved, and a duplicate is an error rather than a
// silent dedupe (#254's trap).
func TestReorderPositions(t *testing.T) {
	tests := []struct {
		name  string
		input string
		max   int
		want  []int
		err   string
	}{
		{name: "typed order is preserved", input: "1,3,2", max: 3, want: []int{1, 3, 2}},
		{name: "single position", input: "2", max: 3, want: []int{2}},
		{name: "partial list is allowed", input: "3", max: 3, want: []int{3}},
		{name: "spaces are tolerated", input: " 3 , 1 ", max: 3, want: []int{3, 1}},
		{name: "hyphen range expands ascending", input: "2-4,1", max: 4, want: []int{2, 3, 4, 1}},
		{name: "dotted range expands ascending", input: "2..4,1", max: 4, want: []int{2, 3, 4, 1}},
		{name: "duplicate is rejected", input: "1,3,3", max: 3, err: "listed more than once"},
		{name: "duplicate across a range is rejected", input: "2-3,3", max: 3, err: "listed more than once"},
		{name: "out of range high", input: "1,4", max: 3, err: "out of range (1-3)"},
		{name: "out of range zero", input: "0", max: 3, err: "out of range (1-3)"},
		{name: "not a number", input: "1,x", max: 3, err: `"x" is not a position number`},
		{name: "descending range", input: "3-1", max: 3, err: "must be ascending"},
		{name: "empty entry", input: "1,,2", max: 3, err: "empty entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reorderPositions(tt.input, tt.max)
			if tt.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestPromptReorderFrom_RetriesOnOneSharedReader is the install picker's
// documented trap, guarded here: the reader must be created ONCE, so the
// second line survives the first attempt's rejection. A per-attempt
// bufio.Reader would buffer both lines on the first read and throw the
// second away with the discarded Reader, and this test would hang or fail.
func TestPromptReorderFrom_RetriesOnOneSharedReader(t *testing.T) {
	refs := []domain.ModReference{
		{SourceID: "src1", ModID: "alpha"},
		{SourceID: "src1", ModID: "beta"},
		{SourceID: "src1", ModID: "gamma"},
	}
	names := map[string]string{"src1:alpha": "Alpha Mod", "src1:beta": "Beta Mod", "src1:gamma": "Gamma Mod"}

	var keys []string
	out := captureStdout(t, func() error {
		var err error
		keys, err = promptReorderFrom(strings.NewReader("1,3,3\n1,3,2\n"), "default", refs, names)
		return err
	})

	assert.Equal(t, []string{"src1:alpha", "src1:gamma", "src1:beta"}, keys)
	assert.Contains(t, out, "Invalid order: position 3 is listed more than once")
	assert.Contains(t, out, "1  alpha   Alpha Mod")
}

func TestPromptReorderFrom_EmptyKeepsTheOrder(t *testing.T) {
	refs := []domain.ModReference{{SourceID: "src1", ModID: "alpha"}}
	var keys []string
	_ = captureStdout(t, func() error {
		var err error
		keys, err = promptReorderFrom(strings.NewReader("\n"), "default", refs, nil)
		return err
	})
	assert.Nil(t, keys, "an empty line means keep the order, not reorder to nothing")
}

func TestPromptReorderFrom_QCancels(t *testing.T) {
	refs := []domain.ModReference{{SourceID: "src1", ModID: "alpha"}}
	var err error
	_ = captureStdout(t, func() error {
		_, err = promptReorderFrom(strings.NewReader("q\n"), "default", refs, nil)
		return nil
	})
	assert.ErrorIs(t, err, ErrCancelled)
}

// TestPromptReorderFrom_UnusableStdinFailsCleanly is #254's own open
// question answered: -i with nothing to read must fail, not hang and not
// half-read.
func TestPromptReorderFrom_UnusableStdinFailsCleanly(t *testing.T) {
	refs := []domain.ModReference{{SourceID: "src1", ModID: "alpha"}}
	var err error
	_ = captureStdout(t, func() error {
		_, err = promptReorderFrom(strings.NewReader(""), "default", refs, nil)
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading input")
}

// TestDoProfileReorder_InteractiveWritesTheTypedOrder is the acceptance
// criterion: a reorder with no mod ID typed anywhere.
func TestDoProfileReorder_InteractiveWritesTheTypedOrder(t *testing.T) {
	svc, game := setupPickerProfile(t)
	withInteractiveReorder(t)

	out := captureStdout(t, func() error {
		return doProfileReorder(context.Background(), svc, game, nil, strings.NewReader("1,3,2\n"))
	})

	assert.Contains(t, out, "✓ Load order updated for profile default.")
	assert.Equal(t, []string{"alpha", "gamma", "beta"}, modIDs(reloadProfile(t, svc, game, "default")),
		"the typed order is honored exactly - 1,3,2 must not come back as 1,2,3")
}

// TestDoProfileReorder_InteractivePartialAppendsTheRest pins the
// partial-input rule the prompt's help line promises, which is the arg
// form's rule too (core.ResolveReorder).
func TestDoProfileReorder_InteractivePartialAppendsTheRest(t *testing.T) {
	svc, game := setupPickerProfile(t)
	withInteractiveReorder(t)

	_ = captureStdout(t, func() error {
		return doProfileReorder(context.Background(), svc, game, nil, strings.NewReader("3\n"))
	})

	assert.Equal(t, []string{"gamma", "alpha", "beta"}, modIDs(reloadProfile(t, svc, game, "default")))
}

func TestDoProfileReorder_InteractiveEmptyWritesNothing(t *testing.T) {
	svc, game := setupPickerProfile(t)
	withInteractiveReorder(t)

	out := captureStdout(t, func() error {
		return doProfileReorder(context.Background(), svc, game, nil, strings.NewReader("\n"))
	})

	assert.Contains(t, out, "Load order unchanged.")
	assert.NotContains(t, out, "✓ Load order updated")
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, modIDs(reloadProfile(t, svc, game, "default")))
}

func TestDoProfileReorder_InteractiveCancelWritesNothing(t *testing.T) {
	svc, game := setupPickerProfile(t)
	withInteractiveReorder(t)

	var err error
	_ = captureStdout(t, func() error {
		err = doProfileReorder(context.Background(), svc, game, nil, strings.NewReader("q\n"))
		return nil
	})

	assert.ErrorIs(t, err, ErrCancelled)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, modIDs(reloadProfile(t, svc, game, "default")))
}

// TestDoProfileReorder_InteractiveUnderJSONIsRefused is Ruling 2: --json
// never reads stdin, so the picker refuses rather than blocking on a prompt
// nobody will answer.
func TestDoProfileReorder_InteractiveUnderJSONIsRefused(t *testing.T) {
	svc, game := setupPickerProfile(t)
	withInteractiveReorder(t)
	withJSONOutput(t)

	err := doProfileReorder(context.Background(), svc, game, nil, strings.NewReader("1,3,2\n"))
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInteractiveOnly)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, modIDs(reloadProfile(t, svc, game, "default")))
}

func TestDoProfileReorder_InteractiveWithArgsIsRefused(t *testing.T) {
	svc, game := setupPickerProfile(t)
	withInteractiveReorder(t)

	err := doProfileReorder(context.Background(), svc, game, []string{"gamma"}, strings.NewReader("1,3,2\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--interactive takes no mod IDs")
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, modIDs(reloadProfile(t, svc, game, "default")))
}

// TestDoProfileReorder_BareStillPrintsOnly is #254's back-compatibility
// bar: the picker lives behind -i, and a bare invocation must still print
// the table and nothing else.
func TestDoProfileReorder_BareStillPrintsOnly(t *testing.T) {
	svc, game := setupPickerProfile(t)

	out := captureStdout(t, func() error {
		return doProfileReorder(context.Background(), svc, game, nil, nil)
	})

	assert.Equal(t, "Load order for default (first = lowest priority):\n"+
		"#  MOD_ID  NAME\n"+
		"1  alpha   Alpha Mod\n"+
		"2  beta    Beta Mod\n"+
		"3  gamma   Gamma Mod\n", out)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, modIDs(reloadProfile(t, svc, game, "default")))
}
