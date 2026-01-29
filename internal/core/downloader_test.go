package core_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloader_Download_ReturnsChecksum(t *testing.T) {
	content := []byte("test file content for checksum")
	// MD5 of "test file content for checksum" = 658a93464f955290e4b8ecd8fc1d3df7
	expectedChecksum := "658a93464f955290e4b8ecd8fc1d3df7"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "test.txt")

	result, err := downloader.Download(context.Background(), server.URL, destPath, nil)
	require.NoError(t, err)

	assert.Equal(t, destPath, result.Path)
	assert.Equal(t, int64(len(content)), result.Size)
	assert.Equal(t, expectedChecksum, result.Checksum)
	assert.Len(t, result.Checksum, 32) // MD5 produces 32 hex chars
}

func TestDownloader_Download(t *testing.T) {
	// Create test content
	content := []byte("test file content")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "17")
		w.Write(content)
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "test.txt")

	var progressCalls []core.DownloadProgress
	progressFn := func(p core.DownloadProgress) {
		progressCalls = append(progressCalls, p)
	}

	_, err := downloader.Download(context.Background(), server.URL, destPath, progressFn)
	require.NoError(t, err)

	// Verify file was created
	data, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, content, data)

	// Verify progress was called
	assert.NotEmpty(t, progressCalls)
	// Last call should show 100%
	lastProgress := progressCalls[len(progressCalls)-1]
	assert.Equal(t, int64(17), lastProgress.Downloaded)
}

func TestDownloader_Download_CancelledContext(t *testing.T) {
	// Create a slow server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		// Write slowly so we can cancel
		for i := 0; i < 1000; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
				w.Write(make([]byte, 1000))
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "test.txt")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := downloader.Download(ctx, server.URL, destPath, nil)
	assert.Error(t, err)
}

func TestDownloader_Download_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "test.txt")

	_, err := downloader.Download(context.Background(), server.URL, destPath, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestDownloader_Download_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "test.txt")

	_, err := downloader.Download(context.Background(), server.URL, destPath, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestDownloader_Download_RetriesOnTransientError(t *testing.T) {
	content := []byte("ok after retries")
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Write(content)
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "test.txt")

	result, err := downloader.Download(context.Background(), server.URL, destPath, nil)
	require.NoError(t, err)
	assert.Equal(t, destPath, result.Path)
	assert.Equal(t, int64(len(content)), result.Size)
	data, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, content, data)
	assert.Equal(t, 3, attempt)
}

// TestDownloader_Download_RetriesExhausted verifies that when all retries fail (e.g. server always 5xx), download returns error.
func TestDownloader_Download_RetriesExhausted(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "test.txt")

	_, err := downloader.Download(context.Background(), server.URL, destPath, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
	// Should have tried defaultMaxAttempts (3) times
	assert.GreaterOrEqual(t, attempt, 3)
}

func TestDownloader_Download_CreatesDirectories(t *testing.T) {
	content := []byte("test content")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "nested", "dir", "test.txt")

	_, err := downloader.Download(context.Background(), server.URL, destPath, nil)
	require.NoError(t, err)

	data, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestDownloader_Download_ProgressTracking(t *testing.T) {
	// Create content that will be sent in chunks
	content := make([]byte, 1000)
	for i := range content {
		content[i] = byte(i % 256)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write(content)
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "test.bin")

	var lastProgress core.DownloadProgress
	progressFn := func(p core.DownloadProgress) {
		lastProgress = p
	}

	_, err := downloader.Download(context.Background(), server.URL, destPath, progressFn)
	require.NoError(t, err)

	assert.Equal(t, int64(1000), lastProgress.TotalBytes)
	assert.Equal(t, int64(1000), lastProgress.Downloaded)
	assert.InDelta(t, 100.0, lastProgress.Percentage, 0.1)
}

func TestDownloader_Download_UnknownContentLength(t *testing.T) {
	content := []byte("test content")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't set Content-Length header
		w.Write(content)
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "test.txt")

	var progressCalls []core.DownloadProgress
	progressFn := func(p core.DownloadProgress) {
		progressCalls = append(progressCalls, p)
	}

	_, err := downloader.Download(context.Background(), server.URL, destPath, progressFn)
	require.NoError(t, err)

	// Verify file was created
	data, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestDownloader_Download_CustomHTTPClient(t *testing.T) {
	content := []byte("custom client test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify custom header is set
		assert.Equal(t, "TestAgent", r.Header.Get("User-Agent"))
		w.Write(content)
	}))
	defer server.Close()

	customClient := &http.Client{
		Transport: &testRoundTripper{
			header: "TestAgent",
			rt:     http.DefaultTransport,
		},
	}

	downloader := core.NewDownloader(customClient)
	destPath := filepath.Join(t.TempDir(), "test.txt")

	_, err := downloader.Download(context.Background(), server.URL, destPath, nil)
	require.NoError(t, err)

	data, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

type testRoundTripper struct {
	header string
	rt     http.RoundTripper
}

func (t *testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", t.header)
	return t.rt.RoundTrip(req)
}

func TestDownloadProgress_Percentage(t *testing.T) {
	tests := []struct {
		name       string
		total      int64
		downloaded int64
		expected   float64
	}{
		{"zero total", 0, 0, 0},
		{"50 percent", 100, 50, 50.0},
		{"100 percent", 100, 100, 100.0},
		{"partial", 1000, 333, 33.3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := core.DownloadProgress{
				TotalBytes: tt.total,
				Downloaded: tt.downloaded,
			}
			if tt.total > 0 {
				p.Percentage = float64(p.Downloaded) / float64(p.TotalBytes) * 100
			}
			assert.InDelta(t, tt.expected, p.Percentage, 0.1)
		})
	}
}

func TestDownloader_Download_ReaderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("partial"))
		// Connection will close before all content is sent
		// This simulates a partial read
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "test.txt")

	_, err := downloader.Download(context.Background(), server.URL, destPath, nil)
	// Should still work (partial download) or error - depends on implementation
	// The key is that it doesn't panic
	_ = err
}

func TestNewDownloader(t *testing.T) {
	// With nil client
	d1 := core.NewDownloader(nil)
	assert.NotNil(t, d1)

	// With custom client
	client := &http.Client{}
	d2 := core.NewDownloader(client)
	assert.NotNil(t, d2)
}

func TestDownloader_Download_WriteTempFirst(t *testing.T) {
	content := []byte("test content for atomic write")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	destPath := filepath.Join(t.TempDir(), "final.txt")

	// Write something to the destination first
	err := os.WriteFile(destPath, []byte("old content"), 0644)
	require.NoError(t, err)

	_, err = downloader.Download(context.Background(), server.URL, destPath, nil)
	require.NoError(t, err)

	// Verify new content replaced old
	data, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func BenchmarkDownloader_Download(b *testing.B) {
	// Create 1MB of content
	content := make([]byte, 1024*1024)
	for i := range content {
		content[i] = byte(i % 256)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		io.Copy(w, &contentReader{content: content, pos: 0})
	}))
	defer server.Close()

	downloader := core.NewDownloader(nil)
	tempDir := b.TempDir()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		destPath := filepath.Join(tempDir, "test"+string(rune(i))+".bin")
		_, _ = downloader.Download(context.Background(), server.URL, destPath, nil)
	}
}

type contentReader struct {
	content []byte
	pos     int
}

func (r *contentReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.content) {
		return 0, io.EOF
	}
	n = copy(p, r.content[r.pos:])
	r.pos += n
	return n, nil
}
