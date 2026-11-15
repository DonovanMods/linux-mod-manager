package main

// profile_finish.go is `lmm profile finish` (#660): a collection profile's
// items left to do - every item its import could not install, each with
// why and the one action that resolves it - listed, and resolved. Core
// keeps the list on the profile and resolves it
// (CollectionTodos/PlanCollectionFinish/ApplyCollectionFinish); this file
// parses, prints and answers installer questions.
//
// The name: the web UI's place for this is "Finish collection" (owner
// ruling), and the command sits beside `lmm profile fill`, which it hands a
// manual download to.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var profileFinishCmd = &cobra.Command{
	Use:   "finish <profile>",
	Short: "List and resolve a collection profile's items left to do",
	Long: `Finish a profile imported from a collection.

A collection import installs every item it can. Each item it could not
install stays in the profile, in its place in the load order, as an item
left to do: why it is not installed, and what resolves it. With no flags,
this command lists them, with the command that resolves each. Deploy,
switch and apply still work on an unfinished collection, with a warning:
the game may not behave as the curator intended.

The actions (a mod is named by its id, or as source:id):
  --retry <mod>       try the install again; "all" retries every item. An
                      installer (FOMOD) that asks is answered at the
                      terminal, or with --fomod-choices / --fomod-defaults.
  --use-current <mod> a pinned file NexusMods no longer offers: install the
                      mod's current main file instead.
  --skip-file <mod>   a gone optional file: leave it out, install the rest.
  --skip <mod>        leave the mod out. The skip is recorded, so a
                      collection update does not add it back silently.
  --from <folder>     install the items you downloaded by hand from that
                      folder (as lmm profile fill does).

An archive an earlier attempt downloaded and kept is installed without
downloading it again. Each item resolves on its own: one that fails again
keeps its place on the list with the new reason.

Examples:
  lmm profile finish "Skyrim Plus" --game skyrim-se
  lmm profile finish "Skyrim Plus" --game skyrim-se --retry all
  lmm profile finish "Skyrim Plus" --game skyrim-se --retry 4044 --fomod-defaults
  lmm profile finish "Skyrim Plus" --game skyrim-se --use-current 266
  lmm profile finish "Skyrim Plus" --game skyrim-se --skip 999
  lmm profile finish "Skyrim Plus" --game skyrim-se --from ~/Downloads`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileFinish,
}

var (
	profileFinishRetry      []string
	profileFinishUseCurrent []string
	profileFinishSkipFile   []string
	profileFinishSkip       []string
	profileFinishFrom       string
	profileFinishDryRun     bool
	profileFinishYes        bool
)

func init() {
	profileCmd.AddCommand(profileFinishCmd)
	f := profileFinishCmd.Flags()
	f.StringSliceVar(&profileFinishRetry, "retry", nil, `retry these items' install ("all" for every item)`)
	f.StringSliceVar(&profileFinishUseCurrent, "use-current", nil, "install these gone pins' mods at their current main file")
	f.StringSliceVar(&profileFinishSkipFile, "skip-file", nil, "leave these mods' gone optional files out and install the rest")
	f.StringSliceVar(&profileFinishSkip, "skip", nil, "leave these mods out of the profile (the skip is recorded)")
	f.StringVar(&profileFinishFrom, "from", "", "install the items downloaded by hand from this folder (lmm profile fill)")
	f.BoolVar(&profileFinishDryRun, "dry-run", false, "print what the finish would do without changing anything")
	f.BoolVarP(&profileFinishYes, "yes", "y", false, "auto-confirm installing from --from's folder")
	addFomodFlags(profileFinishCmd)
}

func runProfileFinish(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileFinish(ctx, service, game, args[0])
	})
}

// finishActionsUsed reports whether any action flag was given.
func finishActionsUsed() bool {
	return len(profileFinishRetry)+len(profileFinishUseCurrent)+len(profileFinishSkipFile)+len(profileFinishSkip) > 0
}

func doProfileFinish(ctx context.Context, service *core.Service, game *domain.Game, profileName string) error {
	if profileFinishFrom != "" {
		if finishActionsUsed() {
			return errors.New("--from installs your downloads on its own; run the other actions separately")
		}
		profileFillYes, profileFillDryRun = profileFinishYes, profileFinishDryRun
		return doProfileFill(ctx, service, game, profileName, []string{profileFinishFrom})
	}
	report, err := service.CollectionTodos(ctx, game, profileName)
	if err != nil {
		return err
	}
	if !finishActionsUsed() {
		if jsonOutput {
			return emitJSON(report)
		}
		printTodoReport(report)
		return nil
	}

	req, err := finishRequest(report)
	if err != nil {
		return err
	}
	plan, err := service.PlanCollectionFinish(ctx, game, req)
	if err != nil {
		return err
	}
	if profileFinishDryRun {
		if jsonOutput {
			return emitJSON(plan)
		}
		printFinishPlan(plan)
		fmt.Println("Dry run: nothing was changed.")
		return nil
	}
	if !jsonOutput {
		printFinishPlan(plan)
	}

	fa, err := newFomodAnswerer(service)
	if err != nil {
		return err
	}
	first := true
	result, err := applyAnsweringFomod(fa, func(answers map[string]domain.FomodChoices) (*core.CollectionFinishResult, error) {
		if !first {
			// A round of answers: plan again, for the items still left
			// (the ones that installed in the last round are not).
			again, perr := service.CollectionTodos(ctx, game, profileName)
			if perr != nil {
				return nil, perr
			}
			if plan, perr = service.PlanCollectionFinish(ctx, game, narrowFinish(req, again.Todos)); perr != nil {
				return nil, perr
			}
		}
		first = false
		return service.ApplyCollectionFinish(ctx, game, plan, core.CollectionFinishOptions{Fomod: answers}, quietSink(finishProgress(plan)))
	}, nil)
	if jsonOutput {
		if err != nil {
			return err
		}
		return emitJSON(result)
	}
	var incomplete *core.CollectionFinishIncompleteError
	if err != nil && !errors.As(err, &incomplete) {
		return err
	}
	printFinishResult(profileName, result)
	if err != nil {
		return fmt.Errorf("%w", ErrReported)
	}
	return nil
}

// finishRequest is the action flags as a core request, each mod named by
// its id resolved against the profile's to-do list.
func finishRequest(report *core.CollectionTodoReport) (core.CollectionFinishRequest, error) {
	req := core.CollectionFinishRequest{Profile: report.Profile}
	resolve := func(flag string, names []string) ([]string, error) {
		var out []string
		for _, name := range names {
			if flag == "--retry" && name == "all" {
				req.RetryAll = true
				continue
			}
			key, err := todoKey(report.Todos, name)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", flag, name, err)
			}
			out = append(out, key)
		}
		return out, nil
	}
	var err error
	if req.Retry, err = resolve("--retry", profileFinishRetry); err != nil {
		return req, err
	}
	if req.UseCurrent, err = resolve("--use-current", profileFinishUseCurrent); err != nil {
		return req, err
	}
	if req.SkipFile, err = resolve("--skip-file", profileFinishSkipFile); err != nil {
		return req, err
	}
	if req.Skip, err = resolve("--skip", profileFinishSkip); err != nil {
		return req, err
	}
	return req, nil
}

// todoKey resolves a mod named on the command line - its id, or
// source:id - to the key of the to-do it names.
func todoKey(todos []core.CollectionTodoItem, name string) (string, error) {
	var hits []string
	for _, t := range todos {
		if t.Key() == name || t.ModID == name {
			hits = append(hits, t.Key())
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", fmt.Errorf("%w: it is not on the profile's list of items to finish", core.ErrBadFinishChoice)
	}
	return "", fmt.Errorf("%w: it names %s; name it as source:id", core.ErrBadFinishChoice, strings.Join(hits, " and "))
}

// narrowFinish is req for the items of todos alone: the ones a round of
// answers left to do.
func narrowFinish(req core.CollectionFinishRequest, todos []core.CollectionTodoItem) core.CollectionFinishRequest {
	left := map[string]bool{}
	for _, t := range todos {
		left[t.Key()] = true
	}
	keep := func(keys []string) []string {
		return slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return !left[k] })
	}
	req.Retry, req.UseCurrent, req.SkipFile, req.Skip = keep(req.Retry), keep(req.UseCurrent), keep(req.SkipFile), keep(req.Skip)
	return req
}

// finishProgress prints a finish's progress lines.
func finishProgress(plan *core.CollectionFinishPlan) func(core.Event) {
	return func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.ImportModInstalling:
			fmt.Printf("  Installing %s:%s...\n", p.SourceID, p.ModID)
		case core.ImportDownloadReused:
			fmt.Printf("    %s\n", p.Detail)
		case core.ImportModFailed:
			fmt.Printf("    Error: %s\n", p.Detail)
		case core.ImportModInstalled:
			if plan.RecordedOnly {
				fmt.Printf("    ✓ Recorded: %s\n", p.ModName)
			} else {
				fmt.Printf("    ✓ Installed: %s\n", p.ModName)
			}
		}
	}
}

// todoName is an item as a line names it: its name and key.
func todoName(t core.CollectionTodoItem) string {
	if t.Name == "" {
		return t.Key()
	}
	return fmt.Sprintf("%s (%s)", t.Name, t.Key())
}

// todoCommand is the command that resolves an item of profile.
func todoCommand(profile string, t core.CollectionTodoItem) string {
	base := fmt.Sprintf("lmm profile finish %q", profile)
	switch t.Action {
	case core.TodoActionAnswerInstaller:
		return fmt.Sprintf("%s --retry %s", base, t.ModID)
	case core.TodoActionChooseFile:
		if t.CanSkipFile {
			return fmt.Sprintf("%s --skip-file %s (or --skip %s)", base, t.ModID, t.ModID)
		}
		return fmt.Sprintf("%s --use-current %s (or --skip %s)", base, t.ModID, t.ModID)
	case core.TodoActionDownload:
		return fmt.Sprintf("%s --from <download-folder>", base)
	}
	return fmt.Sprintf("%s --retry %s", base, t.ModID)
}

// printTodos lists items: each with why, the remedy, and its command.
func printTodos(profile string, todos []core.CollectionTodoItem) {
	for _, t := range todos {
		fmt.Printf("  ! %s: %s\n", todoName(t), t.Remedy)
		if t.Detail != "" {
			fmt.Printf("      %s\n", t.Detail)
		}
		fmt.Printf("      %s\n", todoCommand(profile, t))
	}
}

// printTodoReport is the list with no action flags.
func printTodoReport(report *core.CollectionTodoReport) {
	if len(report.Todos) == 0 {
		fmt.Printf("Collection %s is finished: nothing is left to do.\n", report.Profile)
		return
	}
	fmt.Printf("Finish collection %s: %d of %d need you\n", report.Profile, len(report.Todos), report.Total)
	printTodos(report.Profile, report.Todos)
	fmt.Println("Or retry every item with:")
	fmt.Printf("lmm profile finish %q --retry all\n", report.Profile)
}

// printFinishPlan says what the finish will do.
func printFinishPlan(plan *core.CollectionFinishPlan) {
	if plan.NoChanges {
		fmt.Println("Nothing to do.")
		return
	}
	for _, it := range plan.Install {
		switch it.Choice {
		case core.FinishUseCurrent:
			fmt.Printf("  ↓ %s: install its current main file %s\n", todoName(it.Todo), describeCurrentFile(it.Current))
		case core.FinishSkipFile:
			fmt.Printf("  ↓ %s: install it without its gone files\n", todoName(it.Todo))
		default:
			fmt.Printf("  ↓ %s: retry\n", todoName(it.Todo))
		}
	}
	for _, t := range plan.Skip {
		fmt.Printf("  - %s: skip\n", todoName(t))
	}
	if plan.ReusableDownloads > 0 {
		fmt.Printf("Reusing %d downloaded file(s) from an earlier attempt.\n", plan.ReusableDownloads)
	}
}

// printFinishResult says what the finish did and what is left, its last
// line the next command to run.
func printFinishResult(profile string, result *core.CollectionFinishResult) {
	installed := result.Installed + result.Recorded
	fmt.Printf("\nResolved %d item(s)", installed+len(result.Skipped))
	if len(result.Skipped) > 0 {
		fmt.Printf(" (%d skipped)", len(result.Skipped))
	}
	fmt.Println(".")
	if result.ReusedDownloads > 0 {
		fmt.Printf("Reused %d downloaded file(s).\n", result.ReusedDownloads)
	}
	for _, w := range result.Warnings {
		fmt.Printf("Warning: %s\n", w)
	}
	printCollectionLeft(profile, result.Total, result.Todos)
}

// printCollectionLeft says how many items need the user and lists them,
// ending with the command to run next; or that the collection is finished.
func printCollectionLeft(profile string, total int, todos []core.CollectionTodoItem) {
	if len(todos) == 0 {
		fmt.Printf("Collection %s is finished: all %d mods installed.\n", profile, total)
		return
	}
	fmt.Printf("%d of %d need you:\n", len(todos), total)
	printTodos(profile, todos)
	fmt.Println("Finish the collection with:")
	fmt.Printf("lmm profile finish %q\n", profile)
}

// printCollectionImportSummary is a collection import's outcome (#660):
// "Imported M mods", or "Imported N of M mods; K need you" with each item
// left to do and the command to run next.
func printCollectionImportSummary(result *core.ProfileImportResult) {
	verb := "Imported"
	if result.RecordedOnly {
		verb = "Recorded"
	}
	if len(result.Todos) == 0 {
		fmt.Printf("%s %d mod%s.\n", verb, result.Total, plural(result.Total))
		return
	}
	fmt.Printf("%s %d of %d mod%s; %d need you:\n", verb, result.Total-len(result.Todos), result.Total, plural(result.Total), len(result.Todos))
	printTodos(result.ProfileName, result.Todos)
	fmt.Println("Finish the collection with:")
	fmt.Printf("lmm profile finish %q\n", result.ProfileName)
}

// printIncomplete warns that a deploy, switch, apply or launch runs on an
// unfinished collection (#660 ruling 3): it runs, and the game may not
// behave as the curator intended. Nothing for nil.
func printIncomplete(u *core.CollectionIncomplete) {
	if u == nil || jsonOutput {
		return
	}
	fmt.Fprintf(os.Stderr, "Warning: %s\n", u.Warning)
}

// finishInstallersAfterImport answers a collection import's installer
// to-dos with the answers fa has from --fomod-choices or --fomod-defaults
// (#660), once the import has installed everything else, and folds what
// it installed into result: the items it resolved leave Failures and
// Todos.
func finishInstallersAfterImport(ctx context.Context, service *core.Service, game *domain.Game, fa *fomodAnswerer, result *core.ProfileImportResult) error {
	var keys []string
	for _, t := range result.Todos {
		if t.Reason == domain.TodoInstallerQuestions {
			keys = append(keys, t.Key())
		}
	}
	if len(keys) == 0 {
		return nil
	}
	req := core.CollectionFinishRequest{Profile: result.ProfileName, Retry: keys}
	var plan *core.CollectionFinishPlan
	fin, err := applyAnsweringFomod(fa, func(answers map[string]domain.FomodChoices) (*core.CollectionFinishResult, error) {
		report, perr := service.CollectionTodos(ctx, game, result.ProfileName)
		if perr != nil {
			return nil, perr
		}
		if plan, perr = service.PlanCollectionFinish(ctx, game, narrowFinish(req, report.Todos)); perr != nil {
			return nil, perr
		}
		return service.ApplyCollectionFinish(ctx, game, plan, core.CollectionFinishOptions{Fomod: answers}, nil)
	}, nil)
	var incomplete *core.CollectionFinishIncompleteError
	if err != nil && !errors.As(err, &incomplete) && fin == nil {
		return err
	}
	if fin == nil {
		return nil
	}
	left := map[string]bool{}
	for _, t := range fin.Todos {
		left[t.Key()] = true
	}
	result.Installed += fin.Installed
	result.Recorded += fin.Recorded
	result.ReusedDownloads += fin.ReusedDownloads
	result.Todos, result.Total = fin.Todos, fin.Total
	result.Failures = slices.DeleteFunc(result.Failures, func(f core.ItemFailure) bool { return !left[domain.ModKey(f.SourceID, f.ModID)] })
	result.Failed = len(result.Failures)
	return nil
}
