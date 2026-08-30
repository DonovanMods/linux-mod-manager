package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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
	return snapshotOf(mods), nil
}

// snapshotOf builds the precondition from an ALREADY-READ installed-mod set,
// for a Plan that had to load one anyway (PlanAdopt) - so the plan's own
// views and its staleness precondition come from a single read rather than
// several that could disagree.
func snapshotOf(mods []domain.InstalledMod) installedSnapshot {
	snap := make(installedSnapshot, len(mods))
	for _, m := range mods {
		snap[domain.ModKey(m.SourceID, m.ID)] = fmt.Sprintf("%s|%t", m.Version, m.Enabled)
	}
	return snap
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
