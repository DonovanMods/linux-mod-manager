// Package httpclient: this file is the stall guard (#436).
//
// A request timeout bounds a transfer END TO END, which is the wrong shape
// for a large download: the ceiling has to be long enough for a slow link
// to finish 34 MB, and a transfer that stopped moving a second after it
// began then sits silent for that whole ceiling - indistinguishable, to the
// user, from a hang. What actually identifies a stuck transfer is that no
// bytes are arriving, so this RoundTripper bounds exactly that: the wait
// for the response headers, and then every gap between two reads of the
// body. A download that keeps moving is never cut off, however slow.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrStalled reports a transfer that stopped delivering bytes for longer
// than its idle window. Every StallError matches it with errors.Is.
var ErrStalled = errors.New("the transfer stalled")

// StallError is the stall, with the window that expired, so a message can
// say how long nothing arrived rather than only that something failed.
type StallError struct {
	Timeout time.Duration
}

// Error names the silence and its length.
func (e *StallError) Error() string {
	return fmt.Sprintf("%v: no data received for %s", ErrStalled, e.Timeout)
}

// Is makes errors.Is(err, ErrStalled) true for every StallError.
func (e *StallError) Is(target error) bool { return target == ErrStalled }

// Timer is the part of *time.Timer the guard uses. It is an interface so a
// test can fire the window by hand instead of waiting it out.
type Timer interface {
	Reset(d time.Duration) bool
	Stop() bool
}

// IdleTimeout is an http.RoundTripper that fails a request once no response
// bytes have arrived for Timeout: first while waiting for the headers, then
// between any two reads of the body. The failure is a *StallError, from
// RoundTrip or from the body's Read, whichever was waiting.
//
// It cancels the request through its context, which is what makes a read
// that is blocked inside net/http return at all - and a cancellation the
// CALLER made is still reported as the caller's own context error, never as
// a stall.
//
// A zero Timeout disables the guard entirely.
type IdleTimeout struct {
	// Base performs the request. nil uses http.DefaultTransport.
	Base http.RoundTripper
	// Timeout is the longest the transfer may go without a byte.
	Timeout time.Duration
	// AfterFunc schedules f after d. nil uses time.AfterFunc; a test
	// supplies a timer it fires itself.
	AfterFunc func(d time.Duration, f func()) Timer
}

// RoundTrip implements http.RoundTripper.
func (t *IdleTimeout) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if t.Timeout <= 0 {
		return base.RoundTrip(req)
	}

	ctx, cancel := context.WithCancelCause(req.Context())
	stall := &StallError{Timeout: t.Timeout}
	timer := t.afterFunc(t.Timeout, func() { cancel(stall) })

	resp, err := base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		timer.Stop()
		stalled := errors.Is(context.Cause(ctx), ErrStalled)
		cancel(nil)
		if stalled {
			return nil, stalledError(stall, err)
		}
		return nil, err
	}

	// The headers arrived: the body starts a fresh window.
	timer.Reset(t.Timeout)
	resp.Body = &idleBody{body: resp.Body, ctx: ctx, cancel: cancel, timer: timer, timeout: t.Timeout}
	return resp, nil
}

func (t *IdleTimeout) afterFunc(d time.Duration, f func()) Timer {
	if t.AfterFunc != nil {
		return t.AfterFunc(d, f)
	}
	return time.AfterFunc(d, f)
}

// idleBody re-arms the window on every read that delivers bytes, and turns
// the cancellation the window caused back into the StallError it was.
type idleBody struct {
	body    io.ReadCloser
	ctx     context.Context //nolint:containedctx // the request's own context, owned by this body for its lifetime
	cancel  context.CancelCauseFunc
	timer   Timer
	timeout time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.timer.Reset(b.timeout)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		if cause := context.Cause(b.ctx); errors.Is(cause, ErrStalled) {
			return n, stalledError(cause, err)
		}
	}
	return n, err
}

// stalledError reports a failure the stall window caused. net/http hands
// back the cancellation cause itself on newer runtimes, and a wrapped
// context error on older ones; either way the result names the stall once.
func stalledError(stall, err error) error {
	if errors.Is(err, ErrStalled) {
		return err
	}
	return fmt.Errorf("%w (%w)", stall, err)
}

// Close stops the window and releases the request's context.
func (b *idleBody) Close() error {
	b.timer.Stop()
	err := b.body.Close()
	b.cancel(nil)
	return err
}
