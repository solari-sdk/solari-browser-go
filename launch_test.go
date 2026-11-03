package solari

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

func TestIsTransientRetryPolicy(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"transport (status 0)", &SolariError{Status: 0}, true},
		{"500", &SolariError{Status: 500}, true},
		{"503", &SolariError{Status: 503}, true},
		{"400 not retryable", &SolariError{Status: 400}, false},
		{"404 not retryable", &SolariError{Status: 404}, false},
		{"429 not retryable", &SolariError{Status: 429}, false},
		{"non-solari error retried", errors.New("boom"), true},
	}
	for _, c := range cases {
		if got := isTransient(c.err); got != c.want {
			t.Errorf("%s: isTransient = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMapSameSite(t *testing.T) {
	if mapSameSite("Strict") != network.CookieSameSiteStrict {
		t.Error("Strict")
	}
	if mapSameSite("Lax") != network.CookieSameSiteLax {
		t.Error("Lax")
	}
	if mapSameSite("None") != network.CookieSameSiteNone {
		t.Error("None")
	}
	if mapSameSite("") != "" || mapSameSite("weird") != "" {
		t.Error("empty/unknown should map to empty (default)")
	}
}

// The probe's connect step must run on the caller's browser context itself: chromedp ties the browser it allocates to
// the context of the first Run, so connecting on a child that is cancelled afterwards hands back a dead context.
func TestProbeConnectsOnTheBrowserContextAndLeavesItAlive(t *testing.T) {
	type key struct{}
	browserCtx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "browser"))
	defer cancel()
	var connectCtx, evalCtx context.Context
	err := probeWith(browserCtx, time.Second, time.Second,
		func(ctx context.Context) error { connectCtx = ctx; return nil },
		func(ctx context.Context) error { evalCtx = ctx; return nil })
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if connectCtx != browserCtx {
		t.Error("connect must run on browserCtx itself, not a child that is cancelled when the probe returns")
	}
	if evalCtx == browserCtx {
		t.Error("evaluate should run on a bounded child context")
	}
	if browserCtx.Err() != nil {
		t.Errorf("browser context was cancelled by the probe: %v", browserCtx.Err())
	}
	if evalCtx.Err() == nil {
		t.Error("the evaluate context should be released after the probe")
	}
}

func TestProbeFailsWhenConnectIsTooSlowOrBroken(t *testing.T) {
	ok := func(context.Context) error { return nil }
	hang := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	boom := func(context.Context) error { return errors.New("boom") }

	ctx, cancel := context.WithCancel(context.Background())
	if err := probeWith(ctx, 20*time.Millisecond, time.Second, hang, ok); err == nil {
		t.Error("a connect slower than the bound must fail the probe")
	}
	cancel() // what Launch does next: it ends the connect goroutine
	if err := probeWith(context.Background(), time.Second, time.Second, boom, ok); err == nil {
		t.Error("a connect error must fail the probe")
	}
	if err := probeWith(context.Background(), time.Second, time.Second, ok, boom); err == nil {
		t.Error("an evaluate error must fail the probe")
	}
	var serr *SolariError
	if err := probeWith(context.Background(), time.Second, 20*time.Millisecond, ok, hang); !errors.As(err, &serr) {
		t.Errorf("a slow evaluate must fail the probe with a SolariError, got %v", err)
	}
}

func TestProbeTimeoutExplicitAlsoBoundsConnect(t *testing.T) {
	if _, explicit, _ := probeTimeoutFor(0, 0); explicit {
		t.Error("the default is not explicit")
	}
	if _, explicit, _ := probeTimeoutFor(500, 0); !explicit {
		t.Error("ProbeTimeoutMs is explicit")
	}
}
