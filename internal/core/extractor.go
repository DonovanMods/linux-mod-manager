package core

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Extractor handles archive extraction for mod files
type Extractor struct{}

// NewExtractor creates a new Extractor
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract extracts an archive to the destination directory
// Supports .zip (native), .7z and .rar (via system 7z command)
func (e *Extractor) Extract(archivePath, destDir string) error {
	format := e.DetectFormat(archivePath)
	if format == "" {
		return fmt.Errorf("unsupported archive format: %s", filepath.Ext(archivePath))
	}

	// Create destination directory
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	switch format {
	case "zip":
		return e.extractZip(archivePath, destDir)
	case "7z", "rar":
		return e.extract7z(archivePath, destDir)
	default:
		return fmt.Errorf("unsupported archive format: %s", format)
	}
}

// CanExtract returns true if the extractor can handle the given filename
func (e *Extractor) CanExtract(filename string) bool {
	return e.DetectFormat(filename) != ""
}

// DetectFormat returns the archive format based on filename extension
func (e *Extractor) DetectFormat(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".zip":
		return "zip"
	case ".7z":
		return "7z"
	case ".rar":
		return "rar"
	default:
		return ""
	}
}

// extractZip extracts a ZIP archive using Go's native archive/zip package
func (e *Extractor) extractZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if err := e.extractZipFile(f, destDir); err != nil {
			return err
		}
	}

	return nil
}

// extractZipFile extracts a single file from a ZIP archive
func (e *Extractor) extractZipFile(f *zip.File, destDir string) error {
	// Sanitize the file path to prevent zip slip attacks
	destPath, err := e.sanitizePath(destDir, f.Name)
	if err != nil {
		return err
	}

	// Handle directories
	if f.FileInfo().IsDir() {
		// Use 0755 for directories to ensure we can write files into them
		return os.MkdirAll(destPath, 0755)
	}

	// Create parent directories
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", f.Name, err)
	}

	// Open the source file
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("opening file %s in archive: %w", f.Name, err)
	}
	defer rc.Close()

	// Create the destination file
	outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return fmt.Errorf("creating file %s: %w", destPath, err)
	}
	defer outFile.Close()

	// Copy the contents
	_, err = io.Copy(outFile, rc)
	if err != nil {
		return fmt.Errorf("writing file %s: %w", destPath, err)
	}

	return nil
}

// sanitizePath ensures the extracted file path is within the destination directory
// This prevents "zip slip" attacks where malicious archives contain paths like "../../../etc/passwd"
func (e *Extractor) sanitizePath(destDir, filePath string) (string, error) {
	// Clean the path to remove any . or .. components
	cleanPath := filepath.Clean(filePath)

	// Join with destination directory
	destPath := filepath.Join(destDir, cleanPath)

	// Verify the resulting path is still within destDir
	// This catches cases like filePath = "../../../etc/passwd"
	if !strings.HasPrefix(filepath.Clean(destPath)+string(os.PathSeparator), filepath.Clean(destDir)+string(os.PathSeparator)) {
		// Also check exact match for the destDir itself
		if filepath.Clean(destPath) != filepath.Clean(destDir) {
			return "", fmt.Errorf("path traversal detected: %s", filePath)
		}
	}

	return destPath, nil
}

// extract7z extracts archives using the system 7z command
// This handles .7z and .rar files
func (e *Extractor) extract7z(archivePath, destDir string) error {
	// Check if 7z is available
	_, err := exec.LookPath("7z")
	if err != nil {
		return fmt.Errorf("7z command not found: install p7zip-full to extract .7z and .rar files")
	}

	// Run 7z extraction
	// -y: assume yes to all queries
	// -o: output directory (no space between -o and path)
	cmd := exec.Command("7z", "x", "-y", "-o"+destDir, archivePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("7z extraction failed: %w\nOutput: %s", err, string(output))
	}

	return nil
}
