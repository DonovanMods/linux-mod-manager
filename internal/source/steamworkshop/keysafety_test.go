package steamworkshop_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the package-wide guard for the one secret this source
// handles: the user's personal Steam Web API key (W2 review, Critical 1).
// It is the source-level sibling of httpclient's own redaction tests — it
// sweeps EVERY exported entry point that can carry a key and asserts the
// key is in none of the errors they return, so a keyed call added later is
// covered the day it is written rather than the day it leaks.
//
// The two failure shapes are the two that can carry a key: a transport
// failure (whose *url.Error stringifies the RESOLVED url) and a refusal
// whose body echoes the request back.

// theKey is deliberately a string no other fixture in this package uses, so
// a match in an error message can only have come from the credential.
const theKey = "SUPERSECRETKEY-0123456789abcdef"

// keyBearingCall is one exported entry point that runs with a registered
// key, in the shape a caller would render its error.
type keyBearingCall struct {
	name string
	run  func(context.Context, *steamworkshop.Source) error
}

func keyBearingCalls() []keyBearingCall {
	return []keyBearingCall{
		{"ValidateKey", func(ctx context.Context, s *steamworkshop.Source) error {
			return s.ValidateKey(ctx, theKey)
		}},
		{"Search", func(ctx context.Context, s *steamworkshop.Source) error {
			_, err := s.Search(ctx, source.SearchQuery{GameID: "1133870", Query: "cargo"})
			return err
		}},
		{"ResolveCollection", func(ctx context.Context, s *steamworkshop.Source) error {
			_, err := s.ResolveCollection(ctx, "2500900001")
			return err
		}},
		{"GetMod", func(ctx context.Context, s *steamworkshop.Source) error {
			_, err := s.GetMod(ctx, "1133870", "3617086610")
			return err
		}},
		{"DescribeMods", func(ctx context.Context, s *steamworkshop.Source) error {
			_, err := s.DescribeMods(ctx, "1133870", []string{"3617086610"}, true)
			return err
		}},
		{"CheckUpdates", func(ctx context.Context, s *steamworkshop.Source) error {
			_, err := s.CheckUpdates(ctx, []domain.InstalledMod{
				installedItem("3617086610", "7987119735124793734", 1764767935),
			})
			return err
		}},
	}
}

// eachKeyedCall runs every key-bearing entry point against baseURL, each on
// its OWN source so one subtest's failures cannot trip the shared circuit
// breaker and hand the next subtest an error it never really produced.
func eachKeyedCall(t *testing.T, baseURL string, check func(t *testing.T, err error)) {
	t.Helper()
	for _, c := range keyBearingCalls() {
		t.Run(c.name, func(t *testing.T) {
			src := newTestSource(t, baseURL, t.TempDir(), nil)
			src.SetAPIKey(theKey)
			check(t, c.run(context.Background(), src))
		})
	}
}

func TestNoErrorEverCarriesTheAPIKey_TransportFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	eachKeyedCall(t, "http://"+addr, func(t *testing.T, err error) {
		require.Error(t, err, "a closed port must fail")
		assertNoKey(t, err)
	})
}

func TestNoErrorEverCarriesTheAPIKey_AServerThatEchoesTheRequest(t *testing.T) {
	// The adversarial server: a refusal whose body repeats the whole query
	// string, exactly as Valve's own error page quotes the "key="
	// parameter back. 400 rather than 5xx on purpose — a 5xx is retried in
	// the transport and never reaches the body-interpolating branch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("upstream refused: " + r.URL.String()))
	}))
	defer srv.Close()

	eachKeyedCall(t, srv.URL, func(t *testing.T, err error) {
		require.Error(t, err)
		assertNoKey(t, err)
	})
}

func TestNoErrorEverCarriesTheAPIKey_A403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html><body><h1>Forbidden</h1>Please verify your key= parameter (` +
			r.URL.Query().Get("key") + `).</body></html>`))
	}))
	defer srv.Close()

	eachKeyedCall(t, srv.URL, func(t *testing.T, err error) {
		require.Error(t, err)
		assertNoKey(t, err)
	})
}

func assertNoKey(t *testing.T, err error) {
	t.Helper()
	msg := err.Error()
	assert.NotContains(t, msg, theKey, "the Steam Web API key must never appear in an error: %s", msg)
	// The parameter is the tell that a resolved URL was interpolated: no
	// message this package produces has any reason to carry one.
	assert.False(t, strings.Contains(msg, "key="+theKey), "a resolved URL leaked into: %s", msg)
}
