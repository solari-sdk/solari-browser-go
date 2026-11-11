package solari

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
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
