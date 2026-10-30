package testutil

import (
	"errors"
	"net/http"
	"sync"
)

// githubHosts are the hosts lmm reaches GitHub at in production: the API
// the release check (#568) and `lmm self-update` ask, and the host release
// assets download from (#569).
var githubHosts = map[string]bool{"api.github.com": true, "github.com": true}

// ErrGitHubRefused is the error a guarded request to GitHub fails with.
var ErrGitHubRefused = errors.New("testutil: the test suite must never reach GitHub")

// GuardGitHub wraps http.DefaultTransport so that any request to
// api.github.com or github.com fails with ErrGitHubRefused before it is
// sent, and returns a function listing every such attempt ("METHOD URL").
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

// RoundTrip refuses GitHub and passes everything else through.
func (g *githubGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if githubHosts[req.URL.Hostname()] {
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
