package curseforge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pagedFilesServer answers GET /v1/mods/{id}/files the way CurseForge does:
// at most pageSize files from index, with the pagination block naming the
// total. total files are generated (ids descending, newest first); a total
// larger than the 50-file page cap is served page by page. onRequest, when
// set, runs for every request with its index.
func pagedFilesServer(t *testing.T, total int, requests *atomic.Int32, onRequest func(index int)) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/mods/42/files", r.URL.Path)
		assert.Equal(t, "50", r.URL.Query().Get("pageSize"), "the API's largest page")
		index, err := strconv.Atoi(r.URL.Query().Get("index"))
		if err != nil {
			index = 0
		}
		mu.Lock()
		requests.Add(1)
		mu.Unlock()
		if onRequest != nil {
			onRequest(index)
		}

		var data []File
		for i := index; i < total && i < index+50; i++ {
			id := 1_000_000 - i
			data = append(data, File{ID: id, DisplayName: "Pack-1." + strconv.Itoa(i), FileName: "Pack-1." + strconv.Itoa(i) + ".jar"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PaginatedResponse[[]File]{
			Data:       data,
			Pagination: Pagination{Index: index, PageSize: 50, ResultCount: len(data), TotalCount: total},
		})
	}))
}

func newPagingSource(srv *httptest.Server) *CurseForge {
	cf := New(srv.Client(), "test-api-key")
	cf.client.SetBaseURL(srv.URL)
	return cf
}

// TestCurseForge_GetModFiles_ListsEveryPage: the listing is the mod's whole
// file list, not CurseForge's default first page - a file the update check
// advertised from latestFilesIndexes (or an older version the user asks to
// install) can be anywhere in it. Order is the API's, pages appended, so the
// first file is still the primary one.
func TestCurseForge_GetModFiles_ListsEveryPage(t *testing.T) {
	var requests atomic.Int32
	srv := pagedFilesServer(t, 120, &requests, nil)
	defer srv.Close()

	files, err := newPagingSource(srv).GetModFiles(context.Background(), &domain.Mod{ID: "42"})
	require.NoError(t, err)

	require.Len(t, files, 120)
	assert.EqualValues(t, 3, requests.Load(), "indexes 0, 50, 100")
	assert.Equal(t, "1000000", files[0].ID)
	assert.Equal(t, "999881", files[119].ID, "page order is preserved")
	assert.True(t, files[0].IsPrimary)
	assert.False(t, files[50].IsPrimary, "only the first file is primary")
}

// TestCurseForge_GetModFiles_StopsAtTotalCount: an exactly-full last page does
// not cost another request.
func TestCurseForge_GetModFiles_StopsAtTotalCount(t *testing.T) {
	var requests atomic.Int32
	srv := pagedFilesServer(t, 100, &requests, nil)
	defer srv.Close()

	files, err := newPagingSource(srv).GetModFiles(context.Background(), &domain.Mod{ID: "42"})
	require.NoError(t, err)
	assert.Len(t, files, 100)
	assert.EqualValues(t, 2, requests.Load())
}

// TestCurseForge_GetModFiles_StopsOnAnEmptyPage: a totalCount the server
// cannot back up (it names more files than it serves) ends the walk.
func TestCurseForge_GetModFiles_StopsOnAnEmptyPage(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var data []File
		if r.URL.Query().Get("index") == "0" {
			data = []File{{ID: 1, FileName: "a-1.0.jar"}}
		}
		_ = json.NewEncoder(w).Encode(PaginatedResponse[[]File]{Data: data, Pagination: Pagination{PageSize: 50, TotalCount: 500}})
	}))
	defer srv.Close()

	files, err := newPagingSource(srv).GetModFiles(context.Background(), &domain.Mod{ID: "42"})
	require.NoError(t, err)
	assert.Len(t, files, 1)
	assert.EqualValues(t, 2, requests.Load())
}

// TestCurseForge_GetModFiles_PageCapIsARunawayGuard: a listing that never ends
// is cut at maxModFilePages, returning what was fetched without an error.
func TestCurseForge_GetModFiles_PageCapIsARunawayGuard(t *testing.T) {
	var requests atomic.Int32
	srv := pagedFilesServer(t, 1_000_000, &requests, nil)
	defer srv.Close()

	files, err := newPagingSource(srv).GetModFiles(context.Background(), &domain.Mod{ID: "42"})
	require.NoError(t, err)
	assert.EqualValues(t, maxModFilePages, requests.Load())
	assert.Len(t, files, maxModFilePages*50)
}

// TestCurseForge_GetModFiles_HonoursCancellationBetweenPages: cancelling while
// the walk is under way is the context's error, not a partial listing.
func TestCurseForge_GetModFiles_HonoursCancellationBetweenPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var requests atomic.Int32
	srv := pagedFilesServer(t, 500, &requests, func(index int) {
		if index == 50 {
			cancel()
		}
	})
	defer srv.Close()

	files, err := newPagingSource(srv).GetModFiles(ctx, &domain.Mod{ID: "42"})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, files)
	assert.LessOrEqual(t, requests.Load(), int32(2), "no page after the cancelled one")
}
