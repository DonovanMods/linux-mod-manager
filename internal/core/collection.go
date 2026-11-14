// collection.go holds what the lmm collection format's reader and writer
// share (#621; design docs/plans/2026-11-10-collections-design.md §12, spec
// docs/collection-format.md): telling a collection document from a
// version-0 profile export, the feature vocabulary of `requires:`, and the
// gate that refuses a document needing a feature this lmm cannot replay.
package core

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// CollectionFeatureExternal is the feature a document with external items
// requires: items lmm lists for the user to fetch by hand, never fetches.
const CollectionFeatureExternal = "external"

// CollectionFeatureFomod is the feature a document with recorded FOMOD
// installer choices requires (#614): an item's `fomod` block, which an
// importer must replay - run the installer with those choices - rather
// than deploy the archive raw.
const CollectionFeatureFomod = "fomod"

// supportedCollectionFeatures is this lmm's supported set: the features a
// collection document may require and still import. A later story adds a
// feature by adding its name here, and that is the whole release
// mechanism - an older lmm refuses what it cannot replay instead of
// half-installing it.
var supportedCollectionFeatures = map[string]bool{
	CollectionFeatureExternal:    true,
	CollectionFeatureFomod:       true,
	CollectionFeaturePluginOrder: true,
	// #634: a mod's choices recorded per file (an entry's fomod_files).
	CollectionFeatureFomodMultiFile: true,
	// #623: an author's own files, carried in the container.
	CollectionFeatureBundled: true,
}

// SupportedCollectionFeatures lists the features this lmm can import, in
// name order.
func SupportedCollectionFeatures() []string {
	out := make([]string, 0, len(supportedCollectionFeatures))
	for name := range supportedCollectionFeatures {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// supportedExcept is SupportedCollectionFeatures without any feature the
// refusal names: a converted source collection can be refused for a feature
// the document format supports but its converter does not carry yet (#607:
// plugin_order until C6), and a refusal must never also call it supported.
func supportedExcept(refused []UnsupportedCollectionFeature) []string {
	out := SupportedCollectionFeatures()
	return slices.DeleteFunc(out, func(name string) bool {
		return slices.ContainsFunc(refused, func(f UnsupportedCollectionFeature) bool { return f.Feature == name })
	})
}

// deriveCollectionRequires is the features doc's own content needs. The
// exporter writes exactly this (`requires:` is never hand-written), and the
// importer gates on its union with what the document declares, so a
// hand-edited document cannot under-declare a feature lmm can see.
func deriveCollectionRequires(doc *domain.CollectionDocument) []string {
	var features []string
	if len(doc.External) > 0 {
		features = append(features, CollectionFeatureExternal)
	}
	for _, item := range doc.Items {
		if !item.Ref.FomodRecord().IsZero() {
			features = append(features, CollectionFeatureFomod)
			break
		}
	}
	// #634: choices recorded per file need an importer that replays them
	// per file; an older lmm, which would read the entry as recording
	// nothing, refuses the feature instead.
	for _, item := range doc.Items {
		if item.Ref.FomodRecord().PerFile() {
			features = append(features, CollectionFeatureFomodMultiFile)
			break
		}
	}
	// #607: a stated mod type deploys only where the game's adapter routes
	// it, so a document relying on one says which.
	for _, item := range doc.Items {
		if t := item.Ref.ModType; t != "" && !slices.Contains(features, CollectionFeatureModTypePrefix+t) {
			features = append(features, CollectionFeatureModTypePrefix+t)
		}
	}
	if !doc.Plugins.IsZero() {
		features = append(features, CollectionFeaturePluginOrder)
	}
	// #620: each file load order needs an importer whose game writes it;
	// an older lmm, or a game without it, refuses rather than drop it.
	for _, id := range doc.LoadOrders.IDs() {
		features = append(features, CollectionFeatureFileLoadOrderPrefix+id)
	}
	// #623: bundled items need an importer that installs files from the
	// container; an older lmm, which ignores files/, refuses the feature.
	for _, item := range doc.Items {
		if item.Ref.SourceID == domain.SourceCollection {
			features = append(features, CollectionFeatureBundled)
			break
		}
	}
	sort.Strings(features)
	return features
}

// IsCollectionDocument reports whether data is an lmm collection - a
// `.lmmc` container, or a YAML document with an `lmm_collection:`
// key - rather than a version-0 profile export, which keeps its own import
// path unchanged. A document that does not parse is not one: the version-0
// path reports the parse error as it always has.
func IsCollectionDocument(data []byte) bool {
	if IsCollectionContainer(data) {
		return true
	}
	// A document that HAS the key claims the format, whatever its value
	// (#621 review S5): "1" quoted, or 0, is an invalid collection that
	// PlanCollectionImport refuses - never silently a version-0 export.
	_, present, _ := config.CollectionFormatKey(data)
	return present
}

// ErrCollectionFormatTooNew is a collection document written in a format
// version this lmm does not read: a newer lmm made it.
var ErrCollectionFormatTooNew = errors.New("the collection was made by a newer version of lmm")

// UnsupportedCollectionFeature is one feature a collection requires that
// this lmm cannot replay.
type UnsupportedCollectionFeature struct {
	// Feature is the feature's name, as `requires:` spells it.
	Feature string `json:"feature"`
	// Items names the items that raise it, when it is an item's rather
	// than the whole collection's.
	Items []string `json:"items,omitempty"`
}

// CollectionUnsupportedError refuses a collection import that needs
// features outside this lmm's supported set (design §3, §12). Nothing is
// written: the plan renders what lmm would need, and Apply refuses with
// this error. There is no override - an import that skips what it cannot
// replay is the "looks like it worked" outcome the gate exists to prevent.
type CollectionUnsupportedError struct {
	// Unsupported lists every missing feature, in name order.
	Unsupported []UnsupportedCollectionFeature `json:"unsupported"`
	// Supported is this lmm's supported set, for the message's remedy.
	Supported []string `json:"supported"`
}

// Error implements error.
func (e *CollectionUnsupportedError) Error() string {
	names := make([]string, len(e.Unsupported))
	for i, f := range e.Unsupported {
		names[i] = f.Feature
	}
	return fmt.Sprintf("this collection needs %s, which this version of lmm cannot import; update lmm and try again",
		strings.Join(names, ", "))
}

// Details implements the --json error envelope's extension point: the
// missing features, each with the items raising it, and the supported set.
func (e *CollectionUnsupportedError) Details() any { return e }

// IsCollectionUnsupported reports whether err is a collection import
// refused by the feature gate.
func IsCollectionUnsupported(err error) bool {
	var target *CollectionUnsupportedError
	return errors.As(err, &target)
}

// unsupportedFeatures is the gate: every feature in declared or derived
// that the supported set lacks, in name order. routes says whether the
// importing game routes a stated mod type (mod_type:<type>, #607); nil
// routes none. loadOrders says whether its adapter writes a file load
// order (file_load_order:<id>, #620); nil writes none.
func unsupportedFeatures(declared, derived []string, routes func(modType string) bool, loadOrders func(id string) bool) []UnsupportedCollectionFeature {
	var names []string
	for _, name := range slices.Concat(declared, derived) {
		name = strings.TrimSpace(name)
		if name == "" || supportedCollectionFeatures[name] || slices.Contains(names, name) {
			continue
		}
		if t, ok := strings.CutPrefix(name, CollectionFeatureModTypePrefix); ok && t != "" && routes != nil && routes(t) {
			continue
		}
		if id, ok := strings.CutPrefix(name, CollectionFeatureFileLoadOrderPrefix); ok && id != "" && loadOrders != nil && loadOrders(id) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]UnsupportedCollectionFeature, len(names))
	for i, name := range names {
		out[i] = UnsupportedCollectionFeature{Feature: name}
	}
	return out
}

// listHooks lists a profile's explicitly set hooks, in the order they run.
func listHooks(hooks domain.GameHooks, explicit domain.GameHooksExplicit) []ImportHook {
	var out []ImportHook
	add := func(set bool, name, command string) {
		if set {
			out = append(out, ImportHook{Hook: name, Command: command})
		}
	}
	add(explicit.Install.BeforeAll, "install.before_all", hooks.Install.BeforeAll)
	add(explicit.Install.BeforeEach, "install.before_each", hooks.Install.BeforeEach)
	add(explicit.Install.AfterEach, "install.after_each", hooks.Install.AfterEach)
	add(explicit.Install.AfterAll, "install.after_all", hooks.Install.AfterAll)
	add(explicit.Uninstall.BeforeAll, "uninstall.before_all", hooks.Uninstall.BeforeAll)
	add(explicit.Uninstall.BeforeEach, "uninstall.before_each", hooks.Uninstall.BeforeEach)
	add(explicit.Uninstall.AfterEach, "uninstall.after_each", hooks.Uninstall.AfterEach)
	add(explicit.Uninstall.AfterAll, "uninstall.after_all", hooks.Uninstall.AfterAll)
	return out
}
