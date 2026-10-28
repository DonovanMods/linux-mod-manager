package thunderstore_test

// Two lmm PROCESSES over one community directory (T1 review #2). The
// in-process lock (Source.communityLock) serialises goroutines inside one
// binary and says nothing about `lmm serve` refreshing while the user
// types `lmm search` - an everyday configuration, and one the TTL makes
// simultaneous rather than merely possible.
//
// The first test here runs two Sources, which is already two lock holders:
// an flock is per open file description, so two descriptors contend
// whether they belong to two processes or one. The second re-executes this
// test binary twice, because a claim about processes deserves one test
// that has some.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two variables a re-executed child reads. Their presence is what
// tells this binary it is a child rather than a test run.
const (
	childEnvCacheDir = "LMM_TEST_THUNDERSTORE_CACHE_DIR"
	childEnvBaseURL  = "LMM_TEST_THUNDERSTORE_BASE_URL"
)

// TestMain runs the child body instead of the test suite when this binary
// was re-executed by TestTwoProcessesCommittingTheSameCommunityLeaveAMatchedPair.
func TestMain(m *testing.M) {
	if cacheDir := os.Getenv(childEnvCacheDir); cacheDir != "" {
		os.Exit(childRefresh(cacheDir, os.Getenv(childEnvBaseURL)))
	}
	os.Exit(m.Run())
}

// childRefresh is the whole body of a re-executed child: one forced
// refresh of the shared community directory.
func childRefresh(cacheDir, baseURL string) int {
	src := thunderstore.New(thunderstore.Options{CacheDir: cacheDir, BaseURL: baseURL})
	if _, err := src.RefreshIndex(context.Background(), testCommunity, true, nil); err != nil {
		fmt.Fprintln(os.Stderr, "child refresh:", err)
		return 1
	}
	return 0
}

// TestASecondProcessWaitsForTheBuildRatherThanRacingIt is the lock stated
// as behaviour rather than as a syscall: while one process is building a
// community's index, a second one that wants the same index waits - and
// when it gets the lock it finds the index already built and asks upstream
// nothing at all. Without the lock the second process downloaded the same
// document concurrently and committed it over the first one's files.
func TestASecondProcessWaitsForTheBuildRatherThanRacingIt(t *testing.T) {
	sandboxEnv(t)
	cacheDir := t.TempDir()

	srv := newBlockingIndexServer(t, syntheticDocument(20, 4))
	first := thunderstore.New(thunderstore.Options{CacheDir: cacheDir, BaseURL: srv.URL})
	second := thunderstore.New(thunderstore.Options{CacheDir: cacheDir, BaseURL: srv.URL})

	firstDone := make(chan error, 1)
	go func() {
		_, err := first.RefreshIndex(t.Context(), testCommunity, false, nil)
		firstDone <- err
	}()

	// The first process is now inside the response body, holding the lock.
	select {
	case <-srv.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the first refresh never reached the server")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := second.RefreshIndex(t.Context(), testCommunity, false, nil)
		secondDone <- err
	}()

	// Give the second process every chance to race. It must not: no second
	// request may reach the server while the first still holds the lock.
	select {
	case err := <-secondDone:
		t.Fatalf("the second refresh finished while the first still held the lock (err=%v)", err)
	case <-time.After(250 * time.Millisecond):
	}
	assert.Equal(t, 1, srv.requests(), "a second process must not fetch the same document concurrently")

	srv.unblock()
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)

	assert.Equal(t, 1, srv.requests(),
		"the second process took the lock, found the index fresh, and asked upstream nothing")
	requireMatchedPair(t, cacheDir)
}

// TestTwoProcessesCommittingTheSameCommunityLeaveAMatchedPair is the same
// property with two real processes, both FORCED past the TTL so each
// genuinely rebuilds. Whatever order they finish in, the directory they
// leave is one build's index.json beside that same build's packages.jsonl:
// every row decodes, and the record it addresses is the package the row
// names.
func TestTwoProcessesCommittingTheSameCommunityLeaveAMatchedPair(t *testing.T) {
	sandboxEnv(t)
	cacheDir := t.TempDir()

	// Two documents of different record widths, alternating per request, so
	// a mismatched pair could not hide behind two builds of equal size.
	srv := newAlternatingIndexServer(t, syntheticDocument(40, 1), syntheticDocument(40, 10))

	children := make([]*exec.Cmd, 2)
	for i := range children {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), childEnvCacheDir+"="+cacheDir, childEnvBaseURL+"="+srv.URL)
		cmd.Stderr = os.Stderr
		children[i] = cmd
		require.NoError(t, cmd.Start())
	}
	for _, cmd := range children {
		require.NoError(t, cmd.Wait(), "a child refresh failed")
	}

	requireMatchedPair(t, cacheDir)
}

// requireMatchedPair is the exact check the review's proof needed and a
// size check cannot make: the directory reads as an index AND every row of
// index.json addresses, in packages.jsonl, a record that decodes and
// carries the full_name the row claims.
func requireMatchedPair(t *testing.T, cacheDir string) {
	t.Helper()
	require.True(t, indexIsPresent(t, cacheDir), "the directory must hold a usable index")

	packages, err := os.ReadFile(filepath.Join(indexDir(cacheDir, testCommunity), "packages.jsonl"))
	require.NoError(t, err)

	rows := readIndexRows(t, cacheDir, testCommunity)
	require.NotEmpty(t, rows)
	for i, row := range rows {
		fullName := rowString(t, row, 0)
		offset, length := rowInt(t, row, 6), rowInt(t, row, 7)
		require.LessOrEqual(t, offset+length, int64(len(packages)),
			"row %d (%s) addresses past the end of packages.jsonl", i, fullName)

		var record struct {
			FullName string `json:"full_name"`
		}
		require.NoError(t, json.Unmarshal(packages[offset:offset+length], &record),
			"row %d (%s) addresses bytes that do not decode", i, fullName)
		require.Equal(t, fullName, record.FullName, "row %d addresses another package's record", i)
	}
}

// blockingIndexServer serves one document but holds the response body open
// until it is unblocked, so a test can hold a build in progress for as long
// as it needs to watch what a second one does.
type blockingIndexServer struct {
	*httptest.Server

	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu sync.Mutex
	n  int
}

func newBlockingIndexServer(t *testing.T, body []byte) *blockingIndexServer {
	t.Helper()
	s := &blockingIndexServer{entered: make(chan struct{}, 8), release: make(chan struct{})}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		s.n++
		s.mu.Unlock()

		w.Header().Set("Last-Modified", "Wed, 10 Sep 2026 12:00:00 GMT")
		w.Header().Set("Content-Type", "application/json")
		// Half the document, then a flush, so the client is genuinely
		// inside the body - and therefore holding the lock - before the
		// test looks.
		half := len(body) / 2
		_, _ = w.Write(body[:half])
		w.(http.Flusher).Flush()
		s.entered <- struct{}{}
		<-s.release
		_, _ = w.Write(body[half:])
	}))
	t.Cleanup(func() {
		s.unblock()
		s.Close()
	})
	return s
}

func (s *blockingIndexServer) unblock() { s.once.Do(func() { close(s.release) }) }

func (s *blockingIndexServer) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// alternatingIndexServer answers each request with a different document, so
// two builds of one community are never byte-identical and a mismatched
// pair cannot pass by coincidence.
type alternatingIndexServer struct {
	*httptest.Server

	mu   sync.Mutex
	docs [][]byte
	n    int
}

func newAlternatingIndexServer(t *testing.T, docs ...[]byte) *alternatingIndexServer {
	t.Helper()
	s := &alternatingIndexServer{docs: docs}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		body := s.docs[s.n%len(s.docs)]
		// A DIFFERENT Last-Modified per document, so a conditional GET
		// from either child is answered with the document, never a 304.
		lastModified := fmt.Sprintf("Wed, 10 Sep 2026 12:00:%02d GMT", s.n%60)
		s.n++
		s.mu.Unlock()

		w.Header().Set("Last-Modified", lastModified)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}
