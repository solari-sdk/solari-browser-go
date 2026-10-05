package solari

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The gateway marks `404 ReplayPending` retryable: the recording upload is
// still in flight. An idempotent GET must honour that hint even though 404 is
// nowhere near the 5xx allowlist.
func TestRetryableHintRetriesIdempotentGet(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"replay still uploading","code":"ReplayPending","retryable":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"url":"https://s3/replay.ndjson.gz"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	link, err := c.Sessions.ReplayURL(context.Background(), "s1")
	if err != nil {
		t.Fatalf("replay-url: %v", err)
	}
	if link.URL != "https://s3/replay.ndjson.gz" {
		t.Errorf("URL = %q", link.URL)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("attempts = %d, want 2 (the hint must be honoured)", got)
	}
}

// A 404 WITHOUT the hint is terminal — the flag is what changes the outcome,
// not the status. Without this the test above would pass on a blanket
// "retry every 404" bug.
func TestNoHintMeansNoRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no replay","code":"ReplayUnavailable"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if _, err := c.Sessions.ReplayURL(context.Background(), "s1"); err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("attempts = %d, want 1 (a terminal 404 must not be retried)", got)
	}
}

// RETRACTED REASON, kept deliberately. This asserted the opposite, because
// "the browser API issues no Idempotency-Key, so a re-sent POST /sessions could
// leave a second live session behind". Creates now mint a key, so the gateway
// answers the retry from the first result instead of creating twice — the
// condition was removed, not worked around.
//
// This is the case the gateway-side guard produces: a duplicate arriving while
// the first create is still running is answered 409 + retryable:true, and the
// SDK must retry rather than error on a session that is about to exist.
func TestRetryableHintHonouredOnKeyedCreate(t *testing.T) {
	var calls int32
	var firstKey, secondKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			firstKey = r.Header.Get("Idempotency-Key")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"in progress","retryable":true}`))
			return
		}
		secondKey = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sessionId":"s-1","wsEndpoint":"ws://127.0.0.1:1/ws"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if _, err := c.Sessions.Create(context.Background(), CreateSessionOptions{}); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("attempts = %d, want 2 (the hint must be honoured on a keyed create)", got)
	}
	if firstKey == "" {
		t.Error("create sent no Idempotency-Key")
	}
	if firstKey != secondKey {
		t.Errorf("key changed between attempts (%q -> %q); a per-attempt key fixes nothing", firstKey, secondKey)
	}
}

// 507 is in the allowlist although this gateway emits none (censused
// 2026-09-22). Asserted so a "dead code" cleanup reddens a test rather than
// silently making correctness depend on which gateway build is reached.
func TestIsRetryableStatusIncludes507(t *testing.T) {
	for _, s := range []int{502, 503, 504, 507} {
		if !isRetryableStatus(s) {
			t.Fatalf("expected %d to be retryable", s)
		}
	}
	// Negative control: permanent statuses stay out.
	for _, s := range []int{500, 501, 429, 400} {
		if isRetryableStatus(s) {
			t.Fatalf("expected %d NOT to be retryable", s)
		}
	}
}
