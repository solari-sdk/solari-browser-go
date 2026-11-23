package solari

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// A jail kind with no capacity answers 503 NoCapacity with Retry-After: 10 (PARITY B17). The SDK waits that long before the next
// attempt instead of its own fixed backoff (zero in these tests), and every attempt of one create carries the same Idempotency-Key.
func retryAfterServer(t *testing.T, retryAfter string) (*httptest.Server, func() ([]time.Time, []string)) {
	t.Helper()
	var mu sync.Mutex
	var at []time.Time
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := len(at) + 1
		at = append(at, time.Now())
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"no capacity","code":"NoCapacity"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sessionId":"s-1","wsEndpoint":"ws://127.0.0.1:1/ws"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() ([]time.Time, []string) { mu.Lock(); defer mu.Unlock(); return at, keys }
}

func TestRetryAfterIsHonouredOnA503(t *testing.T) {
	srv, seen := retryAfterServer(t, "1")
	c := newTestClient(t, srv.URL)
	if _, err := c.Sessions.Create(context.Background(), CreateSessionOptions{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	at, keys := seen()
	if len(at) != 2 {
		t.Fatalf("attempts = %d, want 2", len(at))
	}
	if gap := at[1].Sub(at[0]); gap < 900*time.Millisecond {
		t.Errorf("the retry came after %v; Retry-After: 1 asks for 1s", gap)
	}
	if keys[0] == "" || keys[0] != keys[1] {
		t.Errorf("Idempotency-Key must be the same on every attempt: %q vs %q", keys[0], keys[1])
	}
}

func TestA503WithoutRetryAfterKeepsTheBackoff(t *testing.T) {
	srv, seen := retryAfterServer(t, "")
	c := newTestClient(t, srv.URL)
	if _, err := c.Sessions.Create(context.Background(), CreateSessionOptions{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	at, _ := seen()
	if gap := at[1].Sub(at[0]); gap > 800*time.Millisecond {
		t.Errorf("no Retry-After: the retry came after %v", gap)
	}
}

func TestRetryAfterDelayUnusableValuesFallBack(t *testing.T) {
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	backoff := 500 * time.Millisecond
	for _, h := range []string{"", "soon", "-5", "Wed, 21 Oct 2015 07:28:00 GMT"} {
		if got := retryAfterDelay(h, backoff, now); got != backoff {
			t.Errorf("Retry-After %q: delay = %v, want the backoff %v", h, got, backoff)
		}
	}
	if got := retryAfterDelay("10", backoff, now); got != 10*time.Second {
		t.Errorf("10 -> %v", got)
	}
	if got := retryAfterDelay("3600", backoff, now); got != maxRetryAfter {
		t.Errorf("3600 -> %v, want the cap %v", got, maxRetryAfter)
	}
	if got := retryAfterDelay(now.Add(7*time.Second).Format(http.TimeFormat), backoff, now); got != 7*time.Second {
		t.Errorf("HTTP-date +7s -> %v", got)
	}
}
