package core

import "slices"

// collection_plan_summary.go is a converted collection's plan overview
// (#654): the counts a large collection's plan leads with, and the items
// among hundreds that need the user, each with why. Computed once here so
// the CLI's text and the web UI's plan view say the same thing, and kept
// on ConvertedCollection so the import plan and the collection update plan
// both carry it; a re-plan (an optional mod toggled) carries fresh counts.

// CollectionAttentionReason says why a collection item needs the user
// before or after the import. Frontends word each code; a code they do not
// know is shown as is, so later reasons (#657's gone pins) slot in.
type CollectionAttentionReason string

const (
	// CollectionAttentionInstallerQuestions is an item whose installer the
	// collection records no answers for, so it asks at install.
	CollectionAttentionInstallerQuestions CollectionAttentionReason = "installer_questions"
	// CollectionAttentionExternal is an item lmm does not fetch: the user
	// gets it by hand, from the link and the curator's instructions.
	CollectionAttentionExternal CollectionAttentionReason = "external"
	// CollectionAttentionPrerequisite is a loader installed with its own
	// installer, not by lmm (#635): a step for the user.
	CollectionAttentionPrerequisite CollectionAttentionReason = "prerequisite"
	// CollectionAttentionOldFile is an item whose pinned file the source
	// files as an old version or archived: lmm installs that exact file.
	CollectionAttentionOldFile CollectionAttentionReason = "old_file"
	// CollectionAttentionUnsupported is a required item lmm cannot import:
	// it refuses the import (CollectionImport.Unsupported names why).
	CollectionAttentionUnsupported CollectionAttentionReason = "unsupported"
)

// CollectionPlanSummary is a converted collection plan's overview (#654).
// Counts are of ConvertedCollection.Items.
type CollectionPlanSummary struct {
	// Items counts every item.
	Items int `json:"items"`
	// Install counts the items lmm downloads and installs (will_install, and
	// bundled items installed from the collection's own archive), Installed
	// those the profile already has.
	Install   int `json:"install"`
	Installed int `json:"installed"`
	// Optional counts the curator's optional items, and OptionalSelected
	// the ones opted in.
	Optional         int `json:"optional"`
	OptionalSelected int `json:"optional_selected"`
	// InstallerQuestions counts the items to install whose installer asks.
	InstallerQuestions int `json:"installer_questions"`
	// ManualDownload counts the items the account must download from their
	// page (ConvertedCollection.ManualDownload); External the items lmm does
	// not fetch at all, prerequisite steps included.
	ManualDownload int `json:"manual_download"`
	External       int `json:"external"`
	// Unsupported counts the items lmm cannot import: a required one
	// refuses the import, an optional one is skipped.
	Unsupported int `json:"unsupported"`
	// Attention lists the items that need the user, in the collection's
	// order. A free account's manual downloads are counted but not listed:
	// the account is their one reason, which the plan says once.
	Attention []CollectionAttentionItem `json:"attention,omitempty"`
}

// CollectionAttentionItem is one item that needs the user: its
// ConvertedCollectionItem.ID and why.
type CollectionAttentionItem struct {
	ID      string                      `json:"id"`
	Reasons []CollectionAttentionReason `json:"reasons"`
}

// itemFacts is what the summary needs of an item beyond its wire fields.
type itemFacts struct {
	installerAsks bool
	prerequisite  bool
}

// oldFileCategories are the source's file categories for a file it no
// longer offers as current.
var oldFileCategories = []string{"OLD_VERSION", "ARCHIVED"}

// summarise is c's CollectionPlanSummary, from its items' states.
func (c *ConvertedCollection) summarise() *CollectionPlanSummary {
	s := &CollectionPlanSummary{Items: len(c.Items)}
	for _, it := range c.Items {
		facts := c.facts[it.ID]
		if it.Optional {
			s.Optional++
			if it.State != CollectionItemOptionalSkipped && it.State != CollectionItemUnsupported {
				s.OptionalSelected++
			}
		}
		var reasons []CollectionAttentionReason
		toInstall := false
		switch it.State {
		case CollectionItemWillInstall, CollectionItemBundled:
			s.Install++
			toInstall = true
		case CollectionItemInstalled:
			s.Installed++
		case CollectionItemManualDownload:
			s.ManualDownload++
			toInstall = true
		case CollectionItemExternal:
			s.External++
			if facts.prerequisite {
				reasons = append(reasons, CollectionAttentionPrerequisite)
			} else {
				reasons = append(reasons, CollectionAttentionExternal)
			}
		case CollectionItemUnsupported:
			s.Unsupported++
			if !it.Optional {
				reasons = append(reasons, CollectionAttentionUnsupported)
			}
		}
		if toInstall {
			if facts.installerAsks {
				s.InstallerQuestions++
				reasons = append(reasons, CollectionAttentionInstallerQuestions)
			}
			if slices.Contains(oldFileCategories, it.FileCategory) {
				reasons = append(reasons, CollectionAttentionOldFile)
			}
		}
		if len(reasons) > 0 {
			s.Attention = append(s.Attention, CollectionAttentionItem{ID: it.ID, Reasons: reasons})
		}
	}
	return s
}
