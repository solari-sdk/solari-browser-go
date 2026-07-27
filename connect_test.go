package solari

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Connect validates its input before touching the network — a live dial needs a
// real browser and is out of scope for the offline suite.
func TestConnectValidation(t *testing.T) {
	tests := []struct {
		name    string
		session *Session
		wantErr string
	}{
		{"nil session", nil, "Connect requires a session"},
		{"session without a cdpEndpoint", &Session{ID: "s1"}, "no cdpEndpoint"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel, err := Connect(context.Background(), tc.session)
			if err == nil {
				cancel()
				t.Fatal("expected an error")
			}
			if ctx != nil || cancel != nil {
				t.Error("a failed Connect must return no context and no cancel")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
			var serr *SolariError
			if !errors.As(err, &serr) {
				t.Fatalf("expected *SolariError, got %T", err)
			}
		})
	}
}

func TestConnectCDPValidation(t *testing.T) {
	ctx, cancel, err := ConnectCDP(context.Background(), "")
	if err == nil {
		cancel()
		t.Fatal("expected an error")
	}
	if ctx != nil || cancel != nil {
		t.Error("a failed ConnectCDP must return no context and no cancel")
	}
	if !strings.Contains(err.Error(), "requires a cdpEndpoint") {
		t.Errorf("error = %q", err)
	}
}

// A well-formed endpoint yields a live chromedp context and a cancel that does
// not panic. Nothing is dialed until the first chromedp.Run.
func TestConnectReturnsBrowserContext(t *testing.T) {
	browserCtx, cancel, err := Connect(context.Background(), &Session{
		ID:          "s1",
		CDPEndpoint: "wss://api.getsolari.com/cdp/s1",
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if browserCtx == nil || cancel == nil {
		t.Fatal("expected a context and a cancel")
	}
	if browserCtx.Err() != nil {
		t.Errorf("context is already done: %v", browserCtx.Err())
	}
	cancel()
	if browserCtx.Err() == nil {
		t.Error("cancel should tear the context down")
	}
}
