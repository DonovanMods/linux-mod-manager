package core

import "github.com/DonovanMods/linux-mod-manager/internal/domain"

// TwinTestFilterAndSortFiles, TwinTestSelectPrimaryFile, and
// TwinTestSelectFilesForVersion are TEMPORARY exports (Task 4 / #287,
// deleted before this task's final commit) that let cmd/lmm's
// selection_twin_test.go characterize the still-unexported
// filterAndSortInstallFiles/selectVersionedDeployFiles against cmd's own
// filterAndSortFiles/selectFilesToDownload before either side moves. Not
// part of the public API - see selection.go for the real exported surface
// this task produces.

func TwinTestFilterAndSortFiles(files []domain.DownloadableFile, showArchived bool) []domain.DownloadableFile {
	return filterAndSortInstallFiles(files, showArchived)
}

func TwinTestSelectPrimaryFile(files []domain.DownloadableFile) *domain.DownloadableFile {
	if len(files) == 0 {
		return nil
	}
	for i := range files {
		if files[i].IsPrimary {
			return &files[i]
		}
	}
	return &files[0]
}

func TwinTestSelectFilesForVersion(files []domain.DownloadableFile, storedFileIDs []string, version string) ([]*domain.DownloadableFile, error) {
	got, _, err := selectVersionedDeployFiles(files, version, storedFileIDs, false)
	return got, err
}
