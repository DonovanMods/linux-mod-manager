package httpclient_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `lmm serve` re-keys a live source when a user saves a key in Setup
// (#626), so SetAPIKey runs while REST calls are in flight. These tests pin
// the two halves of the fix: the key field is synchronized (the -race run
// is what proves it), and each request reads the key ONCE, so the key it
// sends and the key it redacts from its own errors are the same value.

// Both keys carry characters url.QueryEscape changes, so the escaped form a
// query-parameter client puts on the wire differs from the raw one and both
// must be caught.
const (
	keyA = "key A/one+=" // the key a request is sent with
	keyB = "key B/two+=" // the key the client is re-keyed to mid-flight
)

// requestMethods drives one request through each of the client's request
// methods, so every entry point is held to the same rule.
var requestMethods = []struct {
	name string
	do   func(c *httpclient.Client, t *testing.T) error
}{
	{"DoJSON", func(c *httpclient.Client, t *testing.T) error {
		var out map[string]any
		return c.DoJSON(t.Context(), http.MethodGet, "/thing?x=1", &out)
	}},
	{"DoJSONBody", func(c *httpclient.Client, t *testing.T) error {
		var out map[string]any
		return c.DoJSONBody(t.Context(), http.MethodPost, "/thing", map[string]int{"a": 1}, &out)
	}},
	{"DoForm", func(c *httpclient.Client, t *testing.T) error {
		var out map[string]any
		return c.DoForm(t.Context(), "/thing", url.Values{"a": {"1"}}, &out)
	}},
	{"DoStream", func(c *httpclient.Client, t *testing.T) error {
		resp, err := c.DoStream(t.Context(), http.MethodGet, "/thing", nil)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.Body.Close()
	}},
}

// authModes are the two ways a client forwards its key.
var authModes = []struct {
	name string
	opts func(base string) httpclient.Options
	// sent reads the key the server received, in the form it arrived.
	sent func(r *http.Request) string
}{
	{
		name: "header",
		opts: func(base string) httpclient.Options {
			return httpclient.Options{BaseURL: base, APIKey: keyA, AuthHeader: "apikey", AuthLabel: "Test"}
		},
		sent: func(r *http.Request) string { return r.Header.Get("apikey") },
	},
	{
		name: "query",
		opts: func(base string) httpclient.Options {
			return httpclient.Options{BaseURL: base, APIKey: keyA, AuthQueryParam: "key", AuthLabel: "Test"}
		},
		sent: func(r *http.Request) string { return r.URL.Query().Get("key") },
	},
}

// TestSetAPIKey_ConcurrentWithRequestsIsRaceFree re-keys the client from
// several goroutines while requests through every method run against a
// live server. It asserts little by itself: under -race (CI's test job,
// `make test-race`) an unsynchronized key is a reported data race.
func TestSetAPIKey_ConcurrentWithRequestsIsRaceFree(t *testing.T) {
	for _, mode := range authModes {
		t.Run(mode.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch got := mode.sent(r); got {
				case keyA, keyB:
				default:
					t.Errorf("server received a key that was never set: %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			c := httpclient.New(mode.opts(srv.URL))

			const rounds = 25
			var wg sync.WaitGroup
			for g := range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := range rounds {
						if (g+i)%2 == 0 {
							c.SetAPIKey(keyA)
						} else {
							c.SetAPIKey(keyB)
						}
						assert.True(t, c.IsAuthenticated())
					}
				}()
			}
			for _, m := range requestMethods {
				for range 2 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for range rounds {
							assert.NoError(t, m.do(c, t), m.name)
						}
					}()
				}
			}
			wg.Wait()
		})
	}
}

// TestARequestRedactsTheKeyItSentAfterARekey: a request sent under key A
// fails AFTER the client was re-keyed to B, with an upstream error page
// that echoes the request's key back. The error must redact A - the key
// that request actually carried - in both its raw and its escaped form. A
// redaction that read the client's CURRENT key would look for B, find
// nothing, and hand A to the terminal.
func TestARequestRedactsTheKeyItSentAfterARekey(t *testing.T) {
	for _, mode := range authModes {
		for _, m := range requestMethods {
			t.Run(mode.name+"/"+m.name, func(t *testing.T) {
				var c *httpclient.Client
				var received string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					received = mode.sent(r)
					c.SetAPIKey(keyB) // the user saves a new key mid-request
					w.WriteHeader(http.StatusBadGateway)
					// Echo both shapes: the raw header value and the
					// request URI as it arrived (escaped, for query auth).
					_, _ = fmt.Fprintf(w, "upstream refused key %q at %s", received, r.URL.RequestURI())
				}))
				defer srv.Close()
				c = httpclient.New(mode.opts(srv.URL))

				err := m.do(c, t)
				require.Error(t, err)
				require.Equal(t, keyA, received, "the request was sent under key A")
				msg := err.Error()
				assert.NotContains(t, msg, keyA, "the key the request sent must not leak")
				assert.NotContains(t, msg, url.QueryEscape(keyA), "nor its escaped form")
				assert.NotContains(t, msg, keyB, "the error is about A; B never appears in it")
				assert.Contains(t, msg, "[redacted]", "A is masked, not dropped")
				assert.Contains(t, msg, "status 502", "the error is otherwise the usual one")
			})
		}
	}
}

// TestATransportFailureRedactsTheKeyItSentAfterARekey is the same rule on
// the path that never produces a response: the transport re-keys the
// client and then fails with an error naming the request URL, which for a
// query-parameter client carries key A.
func TestATransportFailureRedactsTheKeyItSentAfterARekey(t *testing.T) {
	for _, m := range requestMethods {
		t.Run(m.name, func(t *testing.T) {
			var c *httpclient.Client
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				c.SetAPIKey(keyB)
				// An error that quotes the URL itself, not via *url.Error
				// (which requestError already unwraps): the redaction is the
				// only thing between this and the terminal.
				return nil, errors.New("proxy refused " + r.URL.String())
			})
			c = httpclient.New(httpclient.Options{
				HTTPClient:     &http.Client{Transport: transport},
				BaseURL:        "http://steam.invalid",
				APIKey:         keyA,
				AuthQueryParam: "key",
				AuthLabel:      "Test",
			})

			err := m.do(c, t)
			require.Error(t, err)
			msg := err.Error()
			assert.NotContains(t, msg, keyA)
			assert.NotContains(t, msg, url.QueryEscape(keyA))
			assert.Contains(t, msg, "key=[redacted]")
			assert.True(t, strings.Contains(msg, "/thing"), "the request is still named: %s", msg)
		})
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
