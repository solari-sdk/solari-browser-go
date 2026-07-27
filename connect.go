package solari

// Connecting to a session's browser. Kept in its own file so the REST control
// plane above never reaches for chromedp.
//
// Go has no Playwright client, so Session.WSEndpoint (the Playwright wire
// protocol) is unusable here — drive Session.CDPEndpoint over raw CDP instead.

import (
	"context"

	"github.com/chromedp/chromedp"
)

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
	if cdpEndpoint == "" {
		return nil, nil, newSolariError("Solari: ConnectCDP requires a cdpEndpoint")
	}
	// NoModifyURL: the endpoint is already a complete browser websocket URL, so
	// chromedp must not probe it for /json/version.
	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(ctx, cdpEndpoint, chromedp.NoModifyURL)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx, opts...)
	cancel := func() {
		cancelBrowser()
		cancelAlloc()
	}
	return browserCtx, cancel, nil
}
