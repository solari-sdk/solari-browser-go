package solari

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The session id is the credential inside the ws/cdp URLs, so errors must not carry it.
const secretID = "pool-abc:org-123.IDSECRETSIGNATURE"

func TestRedactSessionPath(t *testing.T) {
	cases := map[string]string{
		"/sessions/pool-abc:org-123.IDSECRET":                "/sessions/pool-abc…",
		"GET /sessions/pool-abc:org-123.IDSECRET/replay-url": "GET /sessions/pool-abc…/replay-url",
		"/sessions/short":          "/sessions/short",
		"/sessions":                "/sessions",
		"/profiles/prof_123456789": "/profiles/prof_123456789",
	}
	for in, want := range cases {
		if got := redactSessionPath(in); got != want {
			t.Errorf("redactSessionPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAPIErrorDoesNotPrintTheSessionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"code":"SessionNotFound"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)
	_, err := c.Sessions.Get(context.Background(), secretID)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "IDSECRET") {
		t.Fatalf("error carries the session id: %v", err)
	}
	if !strings.Contains(err.Error(), "/sessions/pool-abc") {
		t.Fatalf("error should still name the session prefix: %v", err)
	}
}

func TestTransportErrorDoesNotPrintTheSessionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens any more: the dial fails with a *url.Error holding the full URL
	c := newTestClient(t, url)
	_, err := c.Sessions.Get(context.Background(), secretID)
	if err == nil {
		t.Fatal("want an error")
	}
	for e := error(err); e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), "IDSECRET") {
			t.Fatalf("error (or a wrapped error) carries the session id: %v", e)
		}
	}
}

func TestProbeFailureDoesNotPrintTheCDPEndpoint(t *testing.T) {
	const dial = `could not dial "ws://127.0.0.1:1/cdp/CDPSECRET": dial tcp 127.0.0.1:1: connect: connection refused`
	err := probeWith(context.Background(), time.Second, time.Second,
		func(context.Context) error { return errors.New(dial) },
		func(context.Context) error { return nil })
	if err == nil {
		t.Fatal("want an error")
	}
	for e := error(err); e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), "CDPSECRET") {
			t.Fatalf("probe error (or a wrapped error) carries the endpoint: %v", e)
		}
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("the host should survive: %v", err)
	}
}

func TestProbeFailureKeepsErrorsIs(t *testing.T) {
	err := probeWith(context.Background(), time.Second, time.Second,
		func(context.Context) error {
			return fmt.Errorf(`could not dial "ws://h/cdp/S": %w`, context.DeadlineExceeded)
		},
		func(context.Context) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is must still see the cause: %v", err)
	}
}

func TestUnexpectedSessionResponseDoesNotPrintValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"wsEndpoint":"wss://gw/ws/VALUESECRET"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)
	_, err := c.Sessions.Create(context.Background(), CreateSessionOptions{})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "VALUESECRET") {
		t.Fatalf("error carries a response value: %v", err)
	}
}

func TestFailedPresignedFetchDoesNotPrintTheSignedURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	u := srv.URL + "/state.json?X-Amz-Signature=SIGSECRET"
	srv.Close()
	c := newTestClient(t, srv.URL)
	_, err := c.Sessions.getPresigned(context.Background(), u, time.Second, "storageState")
	if err == nil {
		t.Fatal("want an error")
	}
	for e := error(err); e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), "SIGSECRET") {
			t.Fatalf("error (or a wrapped error) carries the signed URL: %v", e)
		}
	}
}
