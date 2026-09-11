package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ErrStalePlan is returned by every Apply whose plan was computed against an
// installed-mod set that has since changed. The frontend re-plans.
var ErrStalePlan = errors.New("plan is stale: installed mods changed since it was computed")

// installedSnapshot is the precondition a Plan records and an Apply
// re-derives: the set of (source_id, mod_id, version, enabled) for the
// profile at plan time. Unexported, json:"-" wherever a Plan embeds one.
type installedSnapshot map[string]string // key "source:id" -> "version|enabled"

// currentInstalledSnapshot builds gameID/profileName's current installed-mod
// snapshot, keyed by domain.ModKey (source:id), so a later checkPlanFresh
// can detect any version, enabled-state, addition, or removal since a Plan
// was computed.
func (s *Service) currentInstalledSnapshot(ctx context.Context, gameID, profileName string) (installedSnapshot, error) {
	mods, err := s.GetInstalledMods(ctx, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading installed mods: %w", err)
	}
	return s.snapshotOf(gameID, mods)
}

// AdapterPreconditionError is the typed error a frontend branches on when a
// game's adapter refuses a flow before it starts (#353) - a loader that has
// to be installed first, a game directory that is not what the adapter
// expects.
//
// The translation lives in core, not in internal/adapter, for the reason
// design §1 gives: Details() is the `--json` envelope's extension point,
// and keeping its implementation here keeps the wire contract and
// cmd/lmm/details_coverage_test.go's ledger in the packages that already
// own them - and keeps internal/adapter importing nothing it does not need.
type AdapterPreconditionError struct {
	// GameID is the game whose adapter refused.
	GameID string
	// Adapter is the adapter's ID.
	Adapter string
	// Reason is the adapter's own message, which names the remedy.
	Reason string
}

// Error implements error.
func (e *AdapterPreconditionError) Error() string {
	return fmt.Sprintf("game %q: adapter %q refused: %s", e.GameID, e.Adapter, e.Reason)
}

// Unwrap reports the sentinel every adapter refusal carries, so a caller can
// errors.Is it without knowing this type.
func (e *AdapterPreconditionError) Unwrap() error { return adapter.ErrPreconditionUnmet }

// Details implements the --json error envelope's extension point, naming
// the game, the adapter and the remedy the adapter gave.
func (e *AdapterPreconditionError) Details() any {
	return struct {
		GameID  string `json:"game_id"`
		Adapter string `json:"adapter"`
		Reason  string `json:"reason"`
	}{GameID: e.GameID, Adapter: e.Adapter, Reason: e.Reason}
}

// checkAdapterPreconditions asks gameID's adapter whether the flow about to
// run on mods may proceed, wrapping a refusal into AdapterPreconditionError.
func (s *Service) checkAdapterPreconditions(gameID string, mods []domain.InstalledMod) error {
	game, ok := s.game(gameID)
	if !ok {
		// Not this check's problem: every flow resolves its game, and the
		// one that did not would report a better error than this could.
		return nil
	}
	a, err := s.AdapterFor(game)
	if err != nil {
		return err
	}
	if err := adapter.CheckPreconditions(a, game, mods); err != nil {
		return &AdapterPreconditionError{GameID: gameID, Adapter: a.ID(), Reason: err.Error()}
	}
	return nil
}

// snapshotOf builds the precondition from an ALREADY-READ installed-mod set,
// for a Plan that had to load one anyway (PlanAdopt) - so the plan's own
// views and its staleness precondition come from a single read rather than
// several that could disagree.
//
// #353: it is ALSO where the game adapter's precondition is checked, which
// is why it is a method taking a gameID rather than a free function. Every
// installedSnapshot in core is built here - by currentInstalledSnapshot for
// the Plans that re-read the set, and directly by the eight that already
// hold it - so a Plan cannot acquire its freshness precondition without the
// adapter having had its say. Checking in only one of the two constructors
// is exactly the bug this shape closes (I4): `lmm deploy` used to render a
// clean plan that its own Apply then refused.
//
// An adapter with no Preconditioner - every adapter U1 ships - makes the
// check a nil return.
func (s *Service) snapshotOf(gameID string, mods []domain.InstalledMod) (installedSnapshot, error) {
	if err := s.checkAdapterPreconditions(gameID, mods); err != nil {
		return nil, err
	}
	snap := make(installedSnapshot, len(mods))
	for _, m := range mods {
		snap[domain.ModKey(m.SourceID, m.ID)] = fmt.Sprintf("%s|%t", m.Version, m.Enabled)
	}
	return snap, nil
}

// checkPlanFresh re-derives gameID/profileName's CURRENT installed-mod
// snapshot and compares it against want (a Plan's recorded precondition),
// returning nil when they match and a wrapped ErrStalePlan otherwise. Called
// as the first statement inside each Apply's private twin, after beginOp.
func (s *Service) checkPlanFresh(ctx context.Context, gameID, profileName string, want installedSnapshot) error {
	got, err := s.currentInstalledSnapshot(ctx, gameID, profileName)
	if err != nil {
		return err
	}
	if !maps.Equal(got, want) {
		return fmt.Errorf("%w: %s/%s", ErrStalePlan, gameID, profileName)
	}
	return nil
}

// isDeployedNow reports whether the game-dir-relative path f currently
// exists under game.ModPath. A removal-direction union names everything a
// mod COULD have deployed; only what is on disk right now is what an
// undeploy would actually touch (Task 24 review, Minor #1). Lstat, not
// Stat - a dangling symlink is still a deployed path to remove.
func isDeployedNow(game *domain.Game, f string) bool {
	_, err := os.Lstat(filepath.Join(game.ModPath, f))
	return err == nil
}

// uninstallHookNames lists the uninstall.* hooks a pass would run, in run
// order - the vocabulary shared by `lmm uninstall`, `lmm purge`, and a
// `lmm deploy --purge` pass. Only configured hooks are named, and SkipHooks
// (the CLI's --no-hooks) suppresses every one of them.
func uninstallHookNames(hooks *ResolvedHooks, skipHooks bool) []string {
	if skipHooks {
		return nil
	}
	var names []string
	for _, h := range []struct{ name, command string }{
		{"uninstall.before_all", hooks.GetUninstallBeforeAll()},
		{"uninstall.before_each", hooks.GetUninstallBeforeEach()},
		{"uninstall.after_each", hooks.GetUninstallAfterEach()},
		{"uninstall.after_all", hooks.GetUninstallAfterAll()},
	} {
		if h.command != "" {
			names = append(names, h.name)
		}
	}
	return names
}

// MergedArtifactEffect is what a flow would do to a profile's merged
// artifact - the single compiled file every exmodz/converted-pak mod on a
// DeployCompile game reaches the game directory through (#197). It is the
// half of an uninstall's or a purge's consequences that the plan's own mod
// and file lists cannot express: those name per-mod deployments, while the
// merged artifact belongs to the profile as a whole.
//
// A nil *MergedArtifactEffect means "no merged-artifact consequence": the
// game does not deploy by compilation, or the flow would leave the artifact
// exactly as it is. Ruling 8 (v2 Phase 3): before this, both `uninstall
// --dry-run` and `purge --dry-run` announced the effect on EVERY compile
// game, whether or not anything would actually change.
type MergedArtifactEffect struct {
	// Action is MergedArtifactResync or MergedArtifactRemove.
	Action MergedArtifactAction `json:"action"`

	// Path is the artifact's game-dir-relative path - the compile source's
	// own MergedArtifactName (#256), the same value DeployResult.
	// MergedArtifact carries.
	Path string `json:"path"`
}

// MergedArtifactAction is MergedArtifactEffect.Action's type - a plain
// string on the wire (json:"action"), but typed here so a stray literal like
// "resyncc" cannot compile into the switch in cmd/lmm/uninstall.go, matching
// the package's other typed-enum pattern (UpdateStatus).
type MergedArtifactAction string

// MergedArtifactResync/MergedArtifactRemove are MergedArtifactAction's two
// values: the artifact is rebuilt from the merge sources that remain (which
// includes generating or redeploying a missing one), or it leaves the game
// directory entirely.
const (
	MergedArtifactResync MergedArtifactAction = "resync"
	MergedArtifactRemove MergedArtifactAction = "remove"
)

// String returns the action's wire name.
func (a MergedArtifactAction) String() string { return string(a) }

// MarshalText implements encoding.TextMarshaler.
func (a MergedArtifactAction) MarshalText() ([]byte, error) { return []byte(a), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (a *MergedArtifactAction) UnmarshalText(b []byte) error {
	switch MergedArtifactAction(b) {
	case MergedArtifactResync, MergedArtifactRemove:
		*a = MergedArtifactAction(b)
		return nil
	default:
		return fmt.Errorf("unknown merged artifact action %q", b)
	}
}
