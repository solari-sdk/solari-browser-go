package solari

// One-call launch: create -> connect -> seed -> probe -> retry -> release.
//
// This is the Go analogue of the TypeScript Solari.launch() convenience, and
// like the Rust one it is deliberately a THIN wrapper: it folds the six
// mechanical steps of a resilient session start into one call and hands back
// the same *chromedp browser context* you would get from Connect, for you to
// drive with chromedp.Run. It adds NO page/context object model of its own —
// driving the browser is chromedp's job, exactly as with Connect.
//
// What it does that Connect does not:
//   - creates the session for you (one call instead of Create-then-Connect),
//   - seeds an attached profile's cookies into the browser,
//   - health-probes the browser and (optionally) retries the whole start on a
//     transient failure, releasing the dead session between attempts,
//   - releases the Solari session on Close.
//
// Because Connect is lazy (nothing is dialed until the first chromedp.Run), the
// probe is what actually verifies the connection — enable it (the default when
// Retries > 0) to fail fast on a dead slot instead of at your first Run.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// LaunchOptions configures Sessions.Launch. It carries the session-create
// options plus the resilience knobs (Retries, Probe) — and nothing else. This
// is not a place to grow browser configuration; drive the returned context with
// chromedp directly.
type LaunchOptions struct {
	// Create is how the underlying session is created (stealth, proxy, …).
	Create CreateSessionOptions
	// Retries is how many extra times to retry the whole create+connect+probe
	// on a transient failure. 0 (the default) means no retry.
	Retries int
	// Probe forces the post-connect health probe on or off. nil (the default)
	// means "probe iff Retries > 0", matching the TS SDK.
	Probe *bool
	// ProbeTimeoutMs bounds each step of the health probe (connecting, then
	// evaluating), in milliseconds. 0 defaults to 2000 for the evaluate and 15000
	// for the connect. Negative is an error.
	ProbeTimeoutMs int
	// ProbeTimeout is the same bound as a time.Duration.
	//
	// Deprecated: use ProbeTimeoutMs. When both are set ProbeTimeoutMs wins.
	ProbeTimeout time.Duration
}

// LaunchedSession is a launched session: the chromedp browser context plus the
// Solari session it is attached to. Call Close to detach and release.
//
// Drive Browser with chromedp.Run. This type intentionally exposes no
// page/target helpers of its own.
type LaunchedSession struct {
	// Browser is the chromedp browser context — pass it to chromedp.Run.
	Browser context.Context

	session  *Session
	sessions *Sessions
	cancel   context.CancelFunc
	released bool
}

// Session returns the underlying Solari session (id, endpoints, expiry, proxy).
func (l *LaunchedSession) Session() *Session { return l.session }

// Close detaches chromedp and releases the Solari session. Idempotent; safe to
// defer. Unlike the TS/Rust handles there is no implicit release on GC — Go has
// no reliable finalizer for this, so Close (or a defer) is required for a
// prompt release; otherwise the session lingers until its plan-tier expiry.
func (l *LaunchedSession) Close(ctx context.Context) error {
	if l.released {
		return nil
	}
	l.released = true
	if l.cancel != nil {
		l.cancel()
	}
	return l.sessions.Release(ctx, l.session.ID)
}

// Launch creates a session, connects chromedp to it, seeds an attached
// profile's cookies, health-probes, and (optionally) retries the whole start —
// in one call.
//
// The returned LaunchedSession must be Closed to release the session promptly.
// See the file docs for the scope boundary: this is a thin convenience over
// Connect, not a browser object model.
//
// Profile seeding is COOKIES ONLY. localStorage is not seeded: Playwright (the
// TS/Python path) restores it for free, but chromedp has no equivalent and
// doing it here would mean navigating to each origin, which exceeds a thin
// wrapper. Seed it yourself against Browser if you need it.
func (s *Sessions) Launch(ctx context.Context, opts LaunchOptions) (*LaunchedSession, error) {
	wantProbe := opts.Retries > 0
	if opts.Probe != nil {
		wantProbe = *opts.Probe
	}
	probeTimeout, explicit, err := probeTimeoutFor(opts.ProbeTimeoutMs, opts.ProbeTimeout)
	if err != nil {
		return nil, err
	}

	connectTimeout := defaultConnectTimeout
	if explicit {
		connectTimeout = probeTimeout
	}

	for attempt := 0; ; attempt++ {
		session, err := s.Create(ctx, opts.Create)
		if err != nil {
			if attempt < opts.Retries && isTransient(err) {
				continue
			}
			return nil, err
		}

		browserCtx, cancel, err := Connect(ctx, session)
		if err != nil {
			_ = s.Release(context.Background(), session.ID)
			if attempt < opts.Retries && isTransient(err) {
				continue
			}
			return nil, err
		}

		// Seed cookies from an attached profile, best-effort.
		if session.StorageState != nil {
			seedCookies(browserCtx, session.StorageState)
		}

		if wantProbe {
			if err := probe(browserCtx, connectTimeout, probeTimeout); err != nil {
				cancel()
				_ = s.Release(context.Background(), session.ID)
				if attempt < opts.Retries {
					continue // a probe failure is inherently "unhealthy, retry"
				}
				return nil, err
			}
		}

		return &LaunchedSession{
			Browser:  browserCtx,
			session:  session,
			sessions: s,
			cancel:   cancel,
		}, nil
	}
}

// isTransient reports whether a create/connect failure is worth retrying (as
// opposed to a config or auth error, which will fail again identically).
//
// Status == 0 covers two distinct origins that SolariError does not
// distinguish: a transport failure (no HTTP response at all -- retryable)
// and a locally-constructed validation error from newSolariError, e.g.
// Connect's "session has no cdpEndpoint to connect to" (not retryable --
// the same input reproduces it every time). Treating both as transient is a
// known, accepted gap: today every newSolariError call Connect/ConnectCDP can
// reach is guarded by validation earlier in Create, so the gap is currently
// unreachable through any live caller. A future direct call to
// Connect/ConnectCDP with a malformed Session, or a relaxation of Create's
// validation, would hit it for real -- re-examine this if either changes.
func isTransient(err error) bool {
	var serr *SolariError
	if errors.As(err, &serr) {
		return serr.Status == 0 || serr.Status >= 500
	}
	return true
}

// defaultConnectTimeout bounds the connect step of the probe when ProbeTimeoutMs is not set: the first chromedp.Run
// dials the gateway, upgrades to a websocket and opens a target, which on a cold link takes well over the 2 s the
// evaluate step is given.
const defaultConnectTimeout = 15 * time.Second

// probe opens the connection (chromedp is lazy until the first Run), evaluates
// `1`, and confirms the browser answers within the timeout. A failure means the
// slot is unhealthy — the caller retries.
//
// The connect step runs on browserCtx ITSELF, never on a child context:
// chromedp ties the browser it allocates on the first Run to that Run's context,
// so a child (WithTimeout) context that is then cancelled would tear the browser
// down and hand the caller a dead context. Only the evaluate runs on a child.
func probe(browserCtx context.Context, connectTimeout, evalTimeout time.Duration) error {
	return probeWith(browserCtx, connectTimeout, evalTimeout,
		func(ctx context.Context) error { return chromedp.Run(ctx) },
		func(ctx context.Context) error {
			var n int
			return chromedp.Run(ctx, chromedp.Evaluate(`1`, &n))
		})
}

func probeWith(browserCtx context.Context, connectTimeout, evalTimeout time.Duration, connect, eval func(context.Context) error) error {
	fail := func(what string, err error) error {
		scrubbed := scrubErr(err)
		return &SolariError{Message: "Solari: browser health probe " + what + ": " + scrubbed.Error(), Err: scrubbed}
	}
	done := make(chan error, 1)
	go func() { done <- connect(browserCtx) }()
	select {
	case err := <-done:
		if err != nil {
			return fail("failed", err)
		}
	case <-time.After(connectTimeout):
		// The caller cancels browserCtx next, which also ends the connect goroutine.
		return fail("failed", fmt.Errorf("connecting took longer than %s", connectTimeout))
	case <-browserCtx.Done():
		return fail("failed", browserCtx.Err())
	}
	ctx, cancel := context.WithTimeout(browserCtx, evalTimeout)
	defer cancel()
	if err := eval(ctx); err != nil {
		return fail("failed", err)
	}
	return nil
}

// seedCookies applies a profile's cookies to the live browser. Best-effort: a
// cookie the browser will not accept is skipped rather than failing the launch.
func seedCookies(browserCtx context.Context, state *StorageState) {
	if state == nil || len(state.Cookies) == 0 {
		return
	}
	actions := make([]chromedp.Action, 0, len(state.Cookies))
	for _, c := range state.Cookies {
		p := network.SetCookie(c.Name, c.Value)
		if c.Domain != "" {
			p = p.WithDomain(c.Domain)
		}
		if c.Path != "" {
			p = p.WithPath(c.Path)
		}
		if c.Expires != 0 {
			exp := cdp.TimeSinceEpoch(time.Unix(int64(c.Expires), 0))
			p = p.WithExpires(&exp)
		}
		if c.HTTPOnly {
			p = p.WithHTTPOnly(true)
		}
		if c.Secure {
			p = p.WithSecure(true)
		}
		if ss := mapSameSite(c.SameSite); ss != "" {
			p = p.WithSameSite(ss)
		}
		actions = append(actions, p)
	}
	_ = chromedp.Run(browserCtx, actions...)
}

func mapSameSite(s string) network.CookieSameSite {
	switch s {
	case "Strict":
		return network.CookieSameSiteStrict
	case "Lax":
		return network.CookieSameSiteLax
	case "None":
		return network.CookieSameSiteNone
	default:
		return ""
	}
}

// probeTimeoutFor resolves the health-probe bound: ProbeTimeoutMs, else the
// deprecated ProbeTimeout, else 2s. A negative millisecond value is refused.
// The bool reports whether the caller chose the value, in which case it bounds the connect step too.
func probeTimeoutFor(ms int, legacy time.Duration) (time.Duration, bool, error) {
	if ms < 0 {
		return 0, false, newSolariError("Solari: ProbeTimeoutMs must not be negative")
	}
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond, true, nil
	}
	if legacy > 0 {
		return legacy, true, nil
	}
	return 2 * time.Second, false, nil
}
