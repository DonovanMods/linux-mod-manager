package serve

// Internal (package serve) test for the graceful-shutdown drain behaviour
// (docs/plans/2026-08-30-serve-design.md / -impl.md Task 3: "ctx cancel ->
// server drains with a bounded grace"). Exercises serveGraceful directly
// against a bespoke *http.Server rather than a full Server+mux, so the test
// doesn't depend on any route or middleware.

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServeGraceful_InFlightRequestCompletesAfterCancel proves that
// cancelling ctx mid-request does not abort a handler already running: the
// handler blocks until told to proceed, ctx is cancelled while it is still
// blocked, and the client must still observe the full 200 response.
func TestServeGraceful_InFlightRequestCompletesAfterCancel(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})

	httpSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			<-proceed
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("done"))
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	var serveErr error
	go func() {
		defer wg.Done()
		serveErr = serveGraceful(ctx, httpSrv, ln, 5*time.Second)
	}()

	var respErr error
	var status int
	respDone := make(chan struct{})
	go func() {
		defer close(respDone)
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			respErr = err
			return
		}
		defer resp.Body.Close()
		status = resp.StatusCode
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}

	// Cancel while the handler is still blocked in-flight, then let it
	// finish shortly after - the request must still complete successfully.
	cancel()
	time.Sleep(20 * time.Millisecond)
	close(proceed)

	select {
	case <-respDone:
	case <-time.After(5 * time.Second):
		t.Fatal("client never got a response")
	}

	require.NoError(t, respErr)
	assert.Equal(t, http.StatusOK, status)

	wg.Wait()
	assert.NoError(t, serveErr)
}

// TestServeGraceful_ExpiredGracePeriodStillReturns proves the grace period
// is bounded: a handler that never finishes does not hang serveGraceful
// forever - Shutdown's deadline forces it to return.
func TestServeGraceful_ExpiredGracePeriodStillReturns(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	httpSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-block
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- serveGraceful(ctx, httpSrv, ln, 50*time.Millisecond) }()

	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveGraceful did not return within the bounded grace period")
	}
}
