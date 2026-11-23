package core_test

// #674: an .lmmc imported from an https address records the response's
// ETag and Last-Modified, and the collection's revision check asks the
// address with a conditional GET: a 304 is up to date, a 200 is read for
// its version. Without validators it fetches the document, at most once
// per #673's cache window.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	etagV1     = `"v1-abc"`
	modifiedV1 = "Sun, 22 Nov 2026 10:00:00 GMT"
	etagV2     = `W/"v2-def"`
	modifiedV2 = "Sun, 22 Nov 2026 12:00:00 GMT"
)

// urlRevision is the update report's collection status for f's "coll"
// profile, refresh as given.
func (f *updateFixture) urlRevision(t *testing.T, refresh bool) *core.CollectionRevisionStatus {
	t.Helper()
	installed, err := f.svc.GetInstalledMods(context.Background(), "skyrim", "coll")
	require.NoError(t, err)
	report, err := f.svc.CheckGameUpdateReport(context.Background(), f.game, "coll", installed, nil, core.UpdateCheckOptions{Refresh: refresh})
	require.NoError(t, err)
	require.NotNil(t, report.CollectionRevision)
	return report.CollectionRevision
}

// importWithValidators imports version 1 from an address that answers
// with an ETag and a Last-Modified.
func (f *updateFixture) importWithValidators(t *testing.T) {
	t.Helper()
	f.validators.Store(&docValidators{etag: etagV1, lastModified: modifiedV1})
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
}

func TestCollectionURLImport_RecordsTheValidators(t *testing.T) {
	f := newUpdateFixture(t)
	f.importWithValidators(t)

	o := f.profile(t).Collection.Origin
	require.NotNil(t, o)
	assert.Equal(t, etagV1, o.ETag)
	assert.Equal(t, modifiedV1, o.LastModified)
}

func TestCollectionURLRevision_NotModifiedIsUpToDate(t *testing.T) {
	f := newUpdateFixture(t)
	f.importWithValidators(t)
	before := f.docRequestCount()

	got := f.urlRevision(t, false)
	assert.Equal(t, before+1, f.docRequestCount(), "one request")
	req := f.lastDocRequest()
	assert.Equal(t, etagV1, req.Get("If-None-Match"))
	assert.Equal(t, modifiedV1, req.Get("If-Modified-Since"))
	assert.Equal(t, core.CollectionOriginURL, got.Origin)
	assert.True(t, got.Checked)
	assert.False(t, got.Available)
	assert.Equal(t, 1, got.Installed)
	assert.Equal(t, 1, got.Latest)
	assert.Empty(t, got.Error)
}

func TestCollectionURLRevision_ANewerVersionIsAvailable(t *testing.T) {
	f := newUpdateFixture(t)
	f.importWithValidators(t)
	f.validators.Store(&docValidators{etag: etagV2, lastModified: modifiedV2})
	f.serve(urlDocument(t, "coll-1", 2, versionTwo))

	got := f.urlRevision(t, false)
	assert.True(t, got.Checked)
	assert.True(t, got.Available)
	assert.Equal(t, 2, got.Latest)
	assert.Equal(t, 1, got.Installed)

	o := f.profile(t).Collection.Origin
	assert.Equal(t, etagV1, o.ETag, "the check records nothing: the validators are the installed version's until a sync")
}

func TestCollectionURLRevision_AChangedDocumentAtTheSameVersionIsUpToDate(t *testing.T) {
	f := newUpdateFixture(t)
	f.importWithValidators(t)
	f.validators.Store(&docValidators{etag: etagV2})
	f.serve(urlDocument(t, "coll-1", 1, versionTwo)) // touched, not republished

	got := f.urlRevision(t, false)
	assert.True(t, got.Checked)
	assert.False(t, got.Available)
	assert.Equal(t, 1, got.Latest)
}

func TestCollectionURLRevision_AnotherCollectionIsAnError(t *testing.T) {
	f := newUpdateFixture(t)
	f.importWithValidators(t)
	f.validators.Store(&docValidators{etag: etagV2})
	f.serve(urlDocument(t, "coll-2", 5, nil))

	got := f.urlRevision(t, false)
	assert.False(t, got.Checked)
	assert.False(t, got.Available)
	assert.Contains(t, got.Error, "coll-2")
}

// A profile imported before #674 (or from a server sending neither
// header) has no validators: the check fetches the document - once per
// cache window - and says what it found.
func TestCollectionURLRevision_NoValidatorsIsAFullFetchOncePerWindow(t *testing.T) {
	f := newUpdateFixture(t)
	f.importURL(t, urlDocument(t, "coll-1", 1, nil)) // no validators served
	o := f.profile(t).Collection.Origin
	require.Empty(t, o.ETag)
	require.Empty(t, o.LastModified)
	f.serve(urlDocument(t, "coll-1", 2, versionTwo))
	before := f.docRequestCount()

	got := f.urlRevision(t, false)
	assert.True(t, got.Available)
	assert.Equal(t, 2, got.Latest)
	req := f.lastDocRequest()
	assert.Empty(t, req.Get("If-None-Match"), "nothing to send")
	assert.Empty(t, req.Get("If-Modified-Since"))

	f.urlRevision(t, false)
	assert.Equal(t, before+1, f.docRequestCount(), "the window keeps the answer")
	f.urlRevision(t, true)
	assert.Equal(t, before+2, f.docRequestCount(), "a refresh asks again")
}

func TestCollectionURLRevision_AFailureIsReported(t *testing.T) {
	f := newUpdateFixture(t)
	f.importWithValidators(t)
	p := f.profile(t)
	p.Collection.Origin.ImportURL = f.server.URL + "/gone.yaml"
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))

	got := f.urlRevision(t, false)
	assert.False(t, got.Checked)
	assert.Contains(t, got.Error, "404")
}

// A sync from the address records the new version's validators, and
// forgets the kept answer.
func TestCollectionURLSync_RefreshesTheValidators(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	f.importWithValidators(t)
	f.validators.Store(&docValidators{etag: etagV2, lastModified: modifiedV2})
	f.serve(urlDocument(t, "coll-1", 2, versionTwo))
	require.True(t, f.urlRevision(t, false).Available)

	plan := f.planUpdate(t)
	_, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	o := f.profile(t).Collection.Origin
	assert.Equal(t, etagV2, o.ETag)
	assert.Equal(t, modifiedV2, o.LastModified)

	got := f.urlRevision(t, false)
	assert.Equal(t, etagV2, f.lastDocRequest().Get("If-None-Match"), "asked again, with the new validators")
	assert.True(t, got.Checked)
	assert.False(t, got.Available)
	assert.Equal(t, 2, got.Installed)
}

// The validators are this machine's record of the address, like the
// address itself: a document can never set them, and no export carries
// them.
func TestCollectionValidators_AreNeverADocumentsOrAnExports(t *testing.T) {
	f := newUpdateFixture(t)
	f.validators.Store(&docValidators{etag: etagV1})
	f.importURL(t, urlDocument(t, "coll-1", 1, func(doc *domain.CollectionDocument) {
		doc.Info.Origin = &domain.CollectionOrigin{Author: "curator", ETag: `"forged"`, LastModified: "forged"}
	}))
	o := f.profile(t).Collection.Origin
	assert.Equal(t, etagV1, o.ETag, "the response's, never the document's")
	assert.Empty(t, o.LastModified, "the server sent none; the document's is dropped")
	assert.Equal(t, "curator", o.Author)

	export, err := f.svc.ExportCollection(context.Background(), "skyrim", "coll", core.CollectionExportOptions{})
	require.NoError(t, err)
	assert.NotContains(t, string(export.Document), "v1-abc")
	assert.NotContains(t, string(export.Document), "etag")
}
