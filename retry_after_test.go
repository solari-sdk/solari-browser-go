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
	if got := retryAfterDelay("0", backoff, now); got != backoff {
		t.Errorf("Retry-After: 0 -> %v, want the backoff (never an immediate retry)", got)
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
	if got := retryAfterDelay(now.Add(time.Hour).Format(http.TimeFormat), backoff, now); got != maxRetryAfter {
		t.Errorf("HTTP-date +1h -> %v, want the cap", got)
	}
}

// The delay does not stick: a 503 with Retry-After, then a connection error, then success. The wait after the connection error is the
// plain backoff, not the old Retry-After.
func TestRetryAfterDoesNotStickAfterATransportError(t *testing.T) {
	var mu sync.Mutex
	var at []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		at = append(at, time.Now())
		n := len(at)
		mu.Unlock()
		switch n {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"no capacity","code":"NoCapacity"}`))
		case 2:
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_, _ = conn.Write([]byte("not http\r\n\r\n")) // a malformed reply: a transport error the HTTP client does not replay by itself
			_ = conn.Close()
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sessionId":"s-1","wsEndpoint":"ws://127.0.0.1:1/ws"}`))
		}
	}))
	defer srv.Close()
	c, err := NewClient(ClientOptions{APIKey: "slr_live_id_secret", BaseURL: srv.URL, BackoffMs: intPtr(10), MaxAttempts: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Sessions.Create(context.Background(), CreateSessionOptions{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(at) != 3 {
		t.Fatalf("attempts = %d, want 3", len(at))
	}
	if g := at[1].Sub(at[0]); g < 900*time.Millisecond {
		t.Errorf("the 503's Retry-After was not honoured (%v)", g)
	}
	if g := at[2].Sub(at[1]); g > 800*time.Millisecond {
		t.Errorf("after the connection error the wait was %v: the plain backoff, not the old Retry-After", g)
	}
}

func intPtr(n int) *int { return &n }
