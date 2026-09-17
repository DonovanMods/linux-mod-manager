package config

import "github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

// saveOutcome is which of its three paths a save took, for the internal
// tests that pin each one.
type saveOutcome int

const (
	// savedWhole: there was no file, so the document was written whole.
	savedWhole saveOutcome = iota
	// savedInPlace: the existing file was edited in place (or needed no
	// edit at all).
	savedInPlace
	// savedRewritten: the existing file's layout could not be edited in
	// place, so it was rewritten whole.
	savedRewritten
)

// saveProfileFile plans and writes one save, reporting the path it took.
func saveProfileFile(from, to string, profile *domain.Profile) (saveOutcome, error) {
	save, err := planProfileSave(from, to, profile)
	if err != nil {
		return 0, err
	}
	_, err = save.write()
	switch {
	case save.create:
		return savedWhole, err
	case save.rewrite:
		return savedRewritten, err
	default:
		return savedInPlace, err
	}
}
