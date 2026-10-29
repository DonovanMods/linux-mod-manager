package testutil

import (
	"errors"
	"net/http"
	"sync"
)

// githubAPIHost is the host the release check (#568) asks in production.
const githubAPIHost = "api.github.com"

// ErrGitHubRefused is the error a guarded request to api.github.com fails
// with.
var ErrGitHubRefused = errors.New("testutil: the test suite must never reach api.github.com")

// GuardGitHub wraps http.DefaultTransport so that any request to
// api.github.com fails with ErrGitHubRefused before it is sent, and returns
// a function listing every such attempt ("METHOD URL").
//
// lmm's release check is on by default, so a test binary whose code path
// reaches the real checker would otherwise call GitHub from every
// developer's machine and every CI run. A TestMain calls this before
// m.Run and fails the run when the list is not empty: the request is
// refused either way, but a refusal a test swallowed (the check never
// fails a command) would otherwise pass in silence.
func GuardGitHub() func() []string {
	g := &githubGuard{next: http.DefaultTransport}
	http.DefaultTransport = g
	return g.list
}

// githubGuard is the wrapping RoundTripper.
type githubGuard struct {
	next     http.RoundTripper
	mu       sync.Mutex
	attempts []string
}

// RoundTrip refuses api.github.com and passes everything else through.
func (g *githubGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() == githubAPIHost {
		g.mu.Lock()
		g.attempts = append(g.attempts, req.Method+" "+req.URL.String())
		g.mu.Unlock()
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, ErrGitHubRefused
	}
	return g.next.RoundTrip(req)
}

func (g *githubGuard) list() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.attempts...)
}
