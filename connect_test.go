package solari

import (
	"net"
	"time"

	"context"
	"errors"
	"github.com/chromedp/chromedp"
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

// The first chromedp.Run dials the endpoint, and chromedp's error quotes the URL it dialled. The URL carries the session
// id (the credential), so the error the caller sees must not.
func TestConnectDialErrorDoesNotPrintTheEndpoint(t *testing.T) {
	const endpoint = "ws://127.0.0.1:1/cdp/SECRETSESSIONTOKEN123?sig=SECRETSIG"
	for name, connect := range map[string]func() (context.Context, context.CancelFunc, error){
		"Connect": func() (context.Context, context.CancelFunc, error) {
			return Connect(context.Background(), &Session{ID: "s1", CDPEndpoint: endpoint})
		},
		"ConnectCDP": func() (context.Context, context.CancelFunc, error) { return ConnectCDP(context.Background(), endpoint) },
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel, err := connect()
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			runCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			defer stop()
			err = chromedp.Run(runCtx)
			if err == nil {
				t.Fatal("dialling a closed port must fail")
			}
			for _, secret := range []string{"SECRETSESSIONTOKEN123", "SECRETSIG", "/cdp/"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error prints %q: %v", secret, err)
				}
			}
			if !strings.Contains(err.Error(), "127.0.0.1:1") {
				t.Errorf("the host should stay for diagnosis: %v", err)
			}
			var serr *SolariError
			if !errors.As(err, &serr) {
				t.Errorf("want a *SolariError, got %T", err)
			}
		})
	}
}

// A connect that is cancelled or runs out of time must still be recognisable with errors.Is, even though the message is scrubbed.
func TestConnectScrubbedErrorKeepsItsCause(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { // accept and hold: the websocket handshake never completes
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	endpoint := "ws://" + ln.Addr().String() + "/cdp/SECRETSESSIONTOKEN123?sig=SECRETSIG"

	run := func(t *testing.T, mkCtx func(context.Context) (context.Context, context.CancelFunc)) error {
		ctx, cancel, err := ConnectCDP(context.Background(), endpoint)
		if err != nil {
			t.Fatal(err)
		}
		defer cancel()
		runCtx, stop := mkCtx(ctx)
		defer stop()
		return chromedp.Run(runCtx)
	}
	check := func(t *testing.T, err error, want error) {
		if err == nil {
			t.Fatal("expected an error")
		}
		if !errors.Is(err, want) {
			t.Errorf("errors.Is(%v, %v) = false: the scrub dropped the cause", err, want)
		}
		for _, secret := range []string{"SECRETSESSIONTOKEN123", "SECRETSIG", "/cdp/"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error prints %q: %v", secret, err)
			}
		}
	}
	t.Run("deadline", func(t *testing.T) {
		err := run(t, func(c context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(c, 400*time.Millisecond)
		})
		check(t, err, context.DeadlineExceeded)
	})
	t.Run("cancelled", func(t *testing.T) {
		err := run(t, func(c context.Context) (context.Context, context.CancelFunc) {
			cc, cancel := context.WithCancel(c)
			time.AfterFunc(200*time.Millisecond, cancel)
			return cc, cancel
		})
		check(t, err, context.Canceled)
	})
}
