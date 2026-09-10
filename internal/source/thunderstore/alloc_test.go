package thunderstore_test

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// largePackageCount is the size of the synthetic community this test builds.
// The real worst case on the site is 50,707 packages, so this is it: the
// whole design rests on that document never being held in memory, and a
// smaller number would not test the claim.
const largePackageCount = 50000

// peakHeapBudget is what "bounded" means here. The document this test
// serves is ~120 MB of JSON and would decode to several times that; both
// output files stream, so nothing accumulates with the size of the
// community and the measured peak is ~6 MB. The budget is set an order of
// magnitude above that - generous enough never to flake on a busy machine,
// and still two orders below anything that could mean "the document is in
// memory".
const peakHeapBudget = 32 << 20

// TestStreamingDecodeKeepsPeakAllocationBounded is the load-bearing
// measurement of this unit. Thunderstore's largest community is 329 MB
// decoded; if indexing it cost that much heap, none of this design would be
// possible. The fixture is GENERATED here and streamed by the handler -
// nothing this large is committed, and nothing this large is ever held by
// the test either.
func TestStreamingDecodeKeepsPeakAllocationBounded(t *testing.T) {
	sandboxEnv(t)
	var served int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Last-Modified", "Wed, 10 Sep 2026 12:00:00 GMT")
		w.Header().Set("Content-Type", "application/json")
		served = writeSyntheticCommunity(w, largePackageCount)
	}))
	defer srv.Close()

	src := thunderstore.New(thunderstore.Options{CacheDir: t.TempDir(), BaseURL: srv.URL})

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	stop := make(chan struct{})
	peaks := make(chan uint64, 1)
	go func() {
		var peak uint64
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				peaks <- peak
				return
			default:
			}
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak {
				peak = m.HeapAlloc
			}
			time.Sleep(time.Millisecond)
		}
	}()

	status, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	close(stop)
	peak := <-peaks

	require.NoError(t, err)
	require.Equal(t, largePackageCount, status.Packages)
	require.Greater(t, served, int64(20<<20), "the synthetic document must be genuinely large")

	t.Logf("document %d MB, peak heap %d MB, budget %d MB",
		served>>20, peak>>20, int64(peakHeapBudget)>>20)
	assert.Less(t, peak, uint64(peakHeapBudget),
		"the decode must stream: peak heap %d MB over a %d MB document", peak>>20, served>>20)
}

// writeSyntheticCommunity streams a plausible community document straight
// to w, one package at a time, and returns how many bytes it wrote. It is
// deliberately a writer rather than a []byte: a test that built the whole
// document in memory to prove the decoder does not would be measuring
// itself.
func writeSyntheticCommunity(w http.ResponseWriter, packages int) int64 {
	bw := bufio.NewWriterSize(w, 128*1024)
	var n int64
	write := func(s string) {
		c, _ := bw.WriteString(s)
		n += int64(c)
	}
	write("[")
	for i := range packages {
		if i > 0 {
			write(",")
		}
		write(syntheticPackage(i))
	}
	write("]")
	_ = bw.Flush()
	return n
}

// syntheticPackage is one package of the generated document, shaped like a
// real one: a handful of versions, each with a description of the length
// Thunderstore descriptions actually run to.
func syntheticPackage(i int) string {
	description := fmt.Sprintf(
		"Package number %d. %s", i,
		"A description of the length these actually run to, with enough words in it that the decoded document is several times the size of the searchable projection this index keeps.")
	versions := ""
	for v := range 4 {
		if v > 0 {
			versions += ","
		}
		versions += fmt.Sprintf(
			`{"version_number":"1.%d.0","description":%q,"icon":"https://cdn.invalid/icon.png","dependencies":["Owner%d-Dep-1.0.0","BepInEx-BepInExPack-5.4.2100"],"download_url":"https://cdn.invalid/d","downloads":%d,"date_created":"2026-09-0%dT10:00:00Z","website_url":"","is_active":true,"uuid4":"00000000-0000-4000-8000-%012d","file_size":%d}`,
			3-v, description, i, 1000*v, v+1, i*10+v, 100000+v)
	}
	return fmt.Sprintf(
		`{"name":"Package%d","full_name":"Owner%d-Package%d","owner":"Owner%d","package_url":"https://cdn.invalid/p","date_created":"2026-01-01T00:00:00Z","date_updated":"2026-09-0%dT10:00:00Z","uuid4":"00000000-0000-4000-8000-%012d","rating_score":%d,"is_pinned":false,"is_deprecated":%t,"has_nsfw_content":false,"categories":["Mods","Client-side"],"versions":[%s]}`,
		i, i, i, i, (i%9)+1, i, i%1000, i%97 == 0, versions)
}
