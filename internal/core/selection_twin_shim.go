package core

import "github.com/DonovanMods/linux-mod-manager/internal/domain"

// TwinTestFilterAndSortFiles, TwinTestSelectPrimaryFile, and
// TwinTestSelectFilesForVersion are TEMPORARY exports (Task 4 / #287,
// deleted once cmd/lmm re-points at the real exports below) that let
// cmd/lmm's selection_twin_test.go characterize FilterAndSortFiles/
// PrimaryFile/SelectFilesForVersion against cmd's own
// filterAndSortFiles/selectPrimaryFile/selectFilesToDownload before the cmd
// copies are deleted.

func TwinTestFilterAndSortFiles(files []domain.DownloadableFile, showArchived bool) []domain.DownloadableFile {
	return FilterAndSortFiles(files, showArchived)
}

func TwinTestSelectPrimaryFile(files []domain.DownloadableFile) *domain.DownloadableFile {
	return PrimaryFile(files)
}

func TwinTestSelectFilesForVersion(files []domain.DownloadableFile, storedFileIDs []string, version string) ([]*domain.DownloadableFile, error) {
	return SelectFilesForVersion(files, storedFileIDs, version)
}
