package core

// #674 review: the revision check runs inside `lmm update` and the web
// Updates card, so it has its own short deadline - a slow address cannot
// stall them for the import's two minutes.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckURLCollection_HasItsOwnShortDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	old := collectionRevisionCheckTimeout
	collectionRevisionCheckTimeout = 100 * time.Millisecond
	t.Cleanup(func() { collectionRevisionCheckTimeout = old })

	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: srv.Client()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	c := &domain.CollectionInfo{ID: "c", Version: 1, Origin: &domain.CollectionOrigin{ImportURL: srv.URL + "/c.lmmc", ETag: `"v1"`}}
	st := &CollectionRevisionStatus{Installed: 1}

	start := time.Now()
	svc.checkURLCollection(context.Background(), c, c.Origin.ImportURL, st, false)
	assert.Less(t, time.Since(start), 5*time.Second, "the check's deadline, not the import's two minutes")
	assert.False(t, st.Checked)
	assert.Contains(t, st.Error, "did not answer within")
	assert.Less(t, collectionRevisionCheckTimeout, collectionFetchTimeout)
}
