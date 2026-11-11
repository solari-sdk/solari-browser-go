package solari

// Connecting to a session's browser. Kept in its own file so the REST control
// plane above never reaches for chromedp.
//
// Go has no Playwright client, so Session.WSEndpoint (the Playwright wire
// protocol) is unusable here — drive Session.CDPEndpoint over raw CDP instead.

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/chromedp/chromedp"
)

// DefaultDialTimeout bounds the WebSocket handshake of Connect / ConnectCDP: the time from dialling the CDP endpoint to the gateway
// answering the upgrade. chromedp has no dial timeout of its own, so a handshake that gets no answer used to wait for the
// caller's context for ever (a run with no deadline never returned). It does not bound anything after the connection is up: pass a
// context deadline to every chromedp.Run, or health-probe the browser (Launch does).
const DefaultDialTimeout = 30 * time.Second

// defaultDialTimeout is what ConnectCDP (and so Connect) uses; a variable only so a test can lower it.
var defaultDialTimeout = DefaultDialTimeout

// Connect attaches chromedp to a session's remote browser over raw CDP.
//
// The returned context is a chromedp browser context: pass it to chromedp.Run.
// Call the returned cancel func to detach — it tears down the chromedp
// context and its allocator, but does NOT release the Solari session. Release
// that separately with Sessions.Release (the two are independent: a detached
// session keeps running until released or expired).
//
//	sess, err := client.Sessions.Create(ctx, solari.CreateSessionOptions{Stealth: true})
//	if err != nil {
//	    return err
//	}
//	defer client.Sessions.Release(context.Background(), sess.ID)
//
//	browserCtx, cancel, err := solari.Connect(ctx, sess)
//	if err != nil {
//	    return err
//	}
//	defer cancel()
//
//	var title string
//	err = chromedp.Run(browserCtx,
//	    chromedp.Navigate("https://example.com"),
//	    chromedp.Title(&title),
//	)
//
// Nothing is dialed until the first chromedp.Run — a connection failure surfaces
// there, not here.
func Connect(ctx context.Context, session *Session, opts ...chromedp.ContextOption) (context.Context, context.CancelFunc, error) {
	if session == nil {
		return nil, nil, newSolariError("Solari: Connect requires a session")
	}
	if session.CDPEndpoint == "" {
		return nil, nil, newSolariError("Solari: session has no cdpEndpoint to connect to")
	}
	return ConnectCDP(ctx, session.CDPEndpoint, opts...)
}

// ConnectCDP is Connect for a bare CDP endpoint — for callers that persisted an
// endpoint rather than the Session value.
func ConnectCDP(ctx context.Context, cdpEndpoint string, opts ...chromedp.ContextOption) (context.Context, context.CancelFunc, error) {
	return ConnectCDPTimeout(ctx, cdpEndpoint, defaultDialTimeout, opts...)
}

// ConnectCDPTimeout is ConnectCDP with an explicit bound on the WebSocket handshake (DefaultDialTimeout for ConnectCDP). A
// handshake with no answer inside dialTimeout fails the first chromedp.Run with a SolariError that says so. dialTimeout <= 0 means
// no bound of its own (the caller's context still applies).
func ConnectCDPTimeout(ctx context.Context, cdpEndpoint string, dialTimeout time.Duration, opts ...chromedp.ContextOption) (context.Context, context.CancelFunc, error) {
	if cdpEndpoint == "" {
		return nil, nil, newSolariError("Solari: ConnectCDP requires a cdpEndpoint")
	}
	// NoModifyURL: the endpoint is already a complete browser websocket URL, so
	// chromedp must not probe it for /json/version.
	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(ctx, cdpEndpoint, chromedp.NoModifyURL)
	// chromedp's dial error quotes the endpoint (`could not dial "wss://…/cdp/<session id>"`), and the session id is the
	// credential; scrub every error the allocator returns before it reaches the caller's chromedp.Run.
	if c := chromedp.FromContext(allocCtx); c != nil {
		c.Allocator = &scrubbingAllocator{inner: c.Allocator, endpoint: cdpEndpoint, dialTimeout: dialTimeout}
	}
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx, opts...)
	cancel := func() {
		cancelBrowser()
		cancelAlloc()
	}
	return browserCtx, cancel, nil
}

// scrubbingAllocator wraps chromedp's remote allocator so the error of a failed connect carries scheme://host/… instead of
// the dialled URL. The error keeps errors.Is for context.Canceled / context.DeadlineExceeded (scrubbedError.Is).
type scrubbingAllocator struct {
	inner       chromedp.Allocator
	endpoint    string
	dialTimeout time.Duration
}

func (a *scrubbingAllocator) Allocate(ctx context.Context, opts ...chromedp.BrowserOption) (*chromedp.Browser, error) {
	// chromedp dials on the context it is given and keeps the connection on it, so the bound is a timer that cancels that context
	// while the handshake is pending and is disarmed once the browser is up (a cancel func the browser outlives).
	dctx, dcancel := context.WithCancel(ctx)
	var timedOut atomic.Bool
	var timer *time.Timer
	if a.dialTimeout > 0 {
		timer = time.AfterFunc(a.dialTimeout, func() { timedOut.Store(true); dcancel() })
	}
	// The browser lives on dctx; release it when the caller's context ends (a failed dial releases it below).
	go func() { <-dctx.Done(); dcancel() }()
	b, err := a.inner.Allocate(dctx, opts...)
	if timer != nil && !timer.Stop() && timedOut.Load() {
		// The bound fired as the handshake finished: the connection is already cancelled.
		b, err = nil, context.DeadlineExceeded
	}
	if err != nil && timedOut.Load() {
		return nil, &SolariError{
			Message: fmt.Sprintf("Solari: could not connect to the browser: no WebSocket handshake answer within %s (the gateway accepted the connection but did not complete the upgrade); retry, or raise the bound with ConnectCDPTimeout", a.dialTimeout),
			Err:     fmt.Errorf("handshake timeout: %w", context.DeadlineExceeded),
		}
	}
	if err != nil {
		dcancel()
		scrubbed := scrubErr(err, a.endpoint)
		var cause error = scrubbed
		// chromedp dials on its own context, so when the caller's deadline fires its error says "context canceled". Keep the
		// caller's real reason (DeadlineExceeded / Canceled) findable with errors.Is.
		if cerr := ctx.Err(); cerr != nil && !errors.Is(scrubbed, cerr) {
			cause = errors.Join(scrubbed, cerr)
		}
		return nil, &SolariError{Message: "Solari: could not connect to the browser: " + scrubbed.Error(), Err: cause}
	}
	return b, nil
}

func (a *scrubbingAllocator) Wait() { a.inner.Wait() }
