package solari

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// A gateway that accepts the TCP connection and the HTTP upgrade request but never answers it. chromedp has no dial timeout, so
// before the bound existed the first Run waited for the caller's context (for ever when it had no deadline): the 8-minute
// session_connect hang of the 2026-11-11 release-gate run.
func TestConnectCDPHandshakeWithNoAnswerFailsAtTheBound(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // held open, never answered
		}
	}()

	// The test's own guard: without the bound the Run below would wait until here, and the elapsed-time check fails instead of
	// the test hanging.
	parent, stop := context.WithTimeout(context.Background(), 6*time.Second)
	defer stop()
	bctx, cancel, err := ConnectCDPTimeout(parent, "ws://"+ln.Addr().String()+"/cdp/SECRET-SESSION-ID", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	start := time.Now()
	err = chromedp.Run(bctx, chromedp.Navigate("about:blank"))
	if err == nil {
		t.Fatal("Run succeeded against a gateway that never answers the upgrade")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("Run took %s; want it to fail at the 300ms handshake bound (err: %v)", took, err)
	}
	if !strings.Contains(err.Error(), "no WebSocket handshake answer within 300ms") {
		t.Fatalf("error does not say the handshake timed out: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) is false: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET-SESSION-ID") {
		t.Fatalf("error carries the session id: %v", err)
	}
	var se *SolariError
	if !errors.As(err, &se) {
		t.Fatalf("error is not a *SolariError: %T", err)
	}
}

// A zero bound is "no bound of its own": the caller's deadline still ends the Run, and says so (no handshake-timeout message).
func TestConnectCDPZeroBoundLeavesTheCallersDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	parent, stop := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer stop()
	bctx, cancel, err := ConnectCDPTimeout(parent, "ws://"+ln.Addr().String()+"/cdp/x", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	err = chromedp.Run(bctx, chromedp.Navigate("about:blank"))
	if err == nil || strings.Contains(err.Error(), "no WebSocket handshake answer") {
		t.Fatalf("want the caller's deadline error, got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(DeadlineExceeded) is false: %v", err)
	}
}

func hungListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	return ln.Addr().String()
}

// Connect / ConnectCDP are the calls users make: they must carry the default bound. The default is lowered for the test.
func TestConnectAndConnectCDPUseTheDefaultBound(t *testing.T) {
	old := defaultDialTimeout
	defaultDialTimeout = 300 * time.Millisecond
	defer func() { defaultDialTimeout = old }()
	addr := hungListener(t)
	for name, connect := range map[string]func(ctx context.Context) (context.Context, context.CancelFunc, error){
		"ConnectCDP": func(ctx context.Context) (context.Context, context.CancelFunc, error) {
			return ConnectCDP(ctx, "ws://"+addr+"/cdp/SECRET-SESSION-ID")
		},
		"Connect": func(ctx context.Context) (context.Context, context.CancelFunc, error) {
			return Connect(ctx, &Session{CDPEndpoint: "ws://" + addr + "/cdp/SECRET-SESSION-ID"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			parent, stop := context.WithTimeout(context.Background(), 6*time.Second)
			defer stop()
			bctx, cancel, err := connect(parent)
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			start := time.Now()
			err = chromedp.Run(bctx, chromedp.Navigate("about:blank"))
			if err == nil || time.Since(start) > 3*time.Second || !strings.Contains(err.Error(), "no WebSocket handshake answer") {
				t.Fatalf("want the handshake bound after ~300ms, got %v after %s", err, time.Since(start))
			}
		})
	}
}

// The bound is for the handshake only: a gateway that completes the upgrade and then stays up must not be cut off when the bound
// passes (a timer that is not disarmed after the dial would kill every healthy session at the bound).
func TestHealthyConnectionSurvivesPastTheBound(t *testing.T) {
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			return
		}
		defer conn.Close()
		// Read until the client goes away; answer nothing (like a browser that is up and quiet).
		for {
			if _, _, err := wsutil.ReadClientData(conn); err != nil {
				close(closed)
				return
			}
		}
	}))
	defer srv.Close()

	parent, stop := context.WithCancel(context.Background())
	defer stop()
	bctx, cancel, err := ConnectCDPTimeout(parent, "ws"+strings.TrimPrefix(srv.URL, "http")+"/cdp/x", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	runDone := make(chan struct{})
	go func() { defer close(runDone); _ = chromedp.Run(bctx) }() // dials, then waits on the silent peer
	select {
	case <-closed:
		t.Fatal("the connection was closed after the handshake bound although the handshake had succeeded")
	case <-time.After(1200 * time.Millisecond): // 4x the bound
	}
	if bctx.Err() != nil {
		t.Fatalf("browser context ended after the bound: %v", bctx.Err())
	}
	cancel()
	<-runDone
}

// The shipped default is the documented 30 s and is what ConnectCDP reads (the tests above lower the variable, so pin it here).
func TestShippedDefaultDialBoundIs30Seconds(t *testing.T) {
	if DefaultDialTimeout != 30*time.Second || defaultDialTimeout != DefaultDialTimeout {
		t.Fatalf("DefaultDialTimeout = %s, defaultDialTimeout = %s; want both 30s", DefaultDialTimeout, defaultDialTimeout)
	}
}
