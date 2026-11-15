package domain

import (
	"maps"
	"slices"
)

// The reasons a collection item is left to do (CollectionTodo.Reason,
// #660). A collection import installs every item it can and records each
// one it could not as a to-do on the profile's reference, with the reason
// and the one action that resolves it; the frontends list them as
// "Finish collection".
const (
	// TodoInstallerQuestions: the item's FOMOD installer needs answers -
	// the collection recorded none, or recorded ones that no longer fit
	// (CollectionTodo.FomodReason says which). Answered, it installs.
	TodoInstallerQuestions = "installer_questions"
	// TodoGonePin: the source no longer offers the file the collection
	// pins, and no current file stands in for it. Skipped, or retried
	// once the source offers it again.
	TodoGonePin = "gone_pin"
	// TodoDownloadFailed: fetching the mod failed (the network, the
	// source's API, a checksum mismatch). Retried, it installs.
	TodoDownloadFailed = "download_failed"
	// TodoManualDownload: the source serves the file only by hand (a free
	// account, or a mod without download links). Downloaded by hand, it
	// installs through `lmm profile fill`.
	TodoManualDownload = "manual_download"
	// TodoUnsupportedArchive: the archive is one lmm cannot extract, or
	// needs a tool that is not installed (CollectionTodo.Detail names it).
	// Retried once the tool is installed.
	TodoUnsupportedArchive = "unsupported_archive"
	// TodoModGone: the source no longer has the mod at all (deleted, or
	// hidden). Skipped - or retried, should it come back.
	TodoModGone = "mod_gone"
	// TodoNotInstalled: the profile lists the item, the collection put it
	// there, and it has no install - with no record of why (an older lmm
	// rewrote the profile, or the import was interrupted). Retried, it
	// installs.
	TodoNotInstalled = "not_installed"
	// TodoRefused: anything else that refused the item (a mod loader the
	// game does not declare, a failed deploy). Detail says why; it can be
	// retried or skipped.
	TodoRefused = "refused"
)

// CollectionTodo is why a collection item is not installed, and what
// resolves it (#660): ModReference.Todo. This machine's record, like
// SubstitutedFor: the profile file keeps it (`todo:`), no export or
// collection document carries it, and a successful install of the item
// clears it.
type CollectionTodo struct {
	// Reason is one of the Todo* reasons.
	Reason string `yaml:"reason" json:"reason"`
	// Name is the mod's name as the import knew it, so the to-do can be
	// listed without asking the source.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Detail is the failure's own text, for the user to read.
	Detail string `yaml:"detail,omitempty" json:"detail,omitempty"`
	// FomodReason is, for TodoInstallerQuestions, why the installer asks:
	// unanswered, stale or archive_mismatch (core's FomodReason*).
	FomodReason string `yaml:"fomod_reason,omitempty" json:"fomod_reason,omitempty"`
	// FileIDs are, for TodoGonePin, the pinned files the source no longer
	// offers.
	FileIDs []string `yaml:"file_ids,omitempty" json:"file_ids,omitempty"`
	// CanSkipFile says, for TodoGonePin, that every gone file is a
	// secondary one: leaving them out installs the rest of the mod.
	CanSkipFile bool `yaml:"can_skip_file,omitempty" json:"can_skip_file,omitzero"`
	// ArchiveMD5 is, for TodoInstallerQuestions, the md5 the collection
	// records for each of the mod's archives, by file id (#609): the
	// archive its recorded choices were made against, so finishing the
	// item still replays them only against that archive.
	ArchiveMD5 map[string]string `yaml:"archive_md5,omitempty" json:"archive_md5,omitempty"`
}

// Clone returns a copy of t that shares nothing with it; nil for nil.
func (t *CollectionTodo) Clone() *CollectionTodo {
	if t == nil {
		return nil
	}
	c := *t
	c.FileIDs = slices.Clone(t.FileIDs)
	c.ArchiveMD5 = maps.Clone(t.ArchiveMD5)
	return &c
}

// Equal reports whether t and o record the same to-do, nil only as nil.
func (t *CollectionTodo) Equal(o *CollectionTodo) bool {
	if t == nil || o == nil {
		return t == nil && o == nil
	}
	return t.Reason == o.Reason && t.Name == o.Name && t.Detail == o.Detail && t.FomodReason == o.FomodReason &&
		slices.Equal(t.FileIDs, o.FileIDs) && t.CanSkipFile == o.CanSkipFile && maps.Equal(t.ArchiveMD5, o.ArchiveMD5)
}
