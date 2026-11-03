package solari

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
