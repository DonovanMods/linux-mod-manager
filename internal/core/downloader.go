package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// DownloadProgress represents the current state of a download
type DownloadProgress struct {
	TotalBytes int64   // Total size in bytes (0 if unknown)
	Downloaded int64   // Bytes downloaded so far
	Percentage float64 // Completion percentage (0-100)
}

// ProgressFunc is called periodically during download with progress updates
type ProgressFunc func(DownloadProgress)

// Downloader handles HTTP file downloads with progress tracking
type Downloader struct {
	httpClient *http.Client
}

// NewDownloader creates a new Downloader with the given HTTP client
// If httpClient is nil, http.DefaultClient is used
func NewDownloader(httpClient *http.Client) *Downloader {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Downloader{
		httpClient: httpClient,
	}
}

// Download fetches a file from the URL and saves it to destPath
// Progress updates are sent to the optional progressFn callback
func (d *Downloader) Download(ctx context.Context, url, destPath string, progressFn ProgressFunc) error {
	// Create the request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	// Execute the request
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP error: %d %s", resp.StatusCode, resp.Status)
	}

	// Create destination directory if needed
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	// Create a temporary file first for atomic write
	tempPath := destPath + ".tmp"
	file, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer func() {
		file.Close()
		os.Remove(tempPath) // Clean up temp file on error
	}()

	// Get content length if available
	totalBytes := resp.ContentLength

	// Create a progress tracking reader
	reader := &progressReader{
		reader:     resp.Body,
		totalBytes: totalBytes,
		progressFn: progressFn,
	}

	// Copy the data
	_, err = io.Copy(file, reader)
	if err != nil {
		return fmt.Errorf("downloading file: %w", err)
	}

	// Close the file before renaming
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing file: %w", err)
	}

	// Atomically move temp file to final destination
	if err := os.Rename(tempPath, destPath); err != nil {
		return fmt.Errorf("renaming file: %w", err)
	}

	return nil
}

// progressReader wraps an io.Reader to track download progress
type progressReader struct {
	reader     io.Reader
	totalBytes int64
	downloaded int64
	progressFn ProgressFunc
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.downloaded += int64(n)
		if r.progressFn != nil {
			progress := DownloadProgress{
				TotalBytes: r.totalBytes,
				Downloaded: r.downloaded,
			}
			if r.totalBytes > 0 {
				progress.Percentage = float64(r.downloaded) / float64(r.totalBytes) * 100
			}
			r.progressFn(progress)
		}
	}
	return n, err
}
