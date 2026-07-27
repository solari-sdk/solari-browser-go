package solari

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// zeroBackoff returns a pointer to 0 — retries with no wait, for tests.
func zeroBackoff() *int { z := 0; return &z }

// newTestClient wires a Client at srv with retries that do not sleep.
func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := NewClient(ClientOptions{APIKey: "slr_live_id_secret", BaseURL: baseURL, BackoffMs: zeroBackoff()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// roundTripFunc lets a test stub the transport layer itself (to simulate
// network errors that httptest cannot).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewClient(t *testing.T) {
	tests := []struct {
		name        string
		opts        ClientOptions
		wantErr     string
		wantBaseURL string
	}{
		{
			name:        "defaults to the us-west region URL",
			opts:        ClientOptions{APIKey: "k"},
			wantBaseURL: "https://api.getsolari.com",
		},
		{
			name:        "explicit region resolves to its URL",
			opts:        ClientOptions{APIKey: "k", Region: RegionUSWest},
			wantBaseURL: "https://api.getsolari.com",
		},
		{
			name:        "baseURL overrides the region",
			opts:        ClientOptions{APIKey: "k", Region: RegionUSWest, BaseURL: "https://gw.staging.example.com"},
			wantBaseURL: "https://gw.staging.example.com",
		},
		{
			name:        "trailing slash is trimmed",
			opts:        ClientOptions{APIKey: "k", BaseURL: "https://gw.example.com/"},
			wantBaseURL: "https://gw.example.com",
		},
		{
			name:    "apiKey is required",
			opts:    ClientOptions{},
			wantErr: "apiKey is required",
		},
		{
			name:    "unknown region is rejected",
			opts:    ClientOptions{APIKey: "k", Region: "eu-north"},
			wantErr: `unsupported region "eu-north"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient(tc.opts)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				var serr *SolariError
				if !errors.As(err, &serr) {
					t.Fatalf("expected *SolariError, got %T", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.BaseURL() != tc.wantBaseURL {
				t.Errorf("BaseURL() = %q, want %q", c.BaseURL(), tc.wantBaseURL)
			}
			if c.Sessions == nil || c.Profiles == nil || c.Proxy == nil {
				t.Error("namespaces must be wired")
			}
		})
	}
}

func TestClientDefaults(t *testing.T) {
	c, err := NewClient(ClientOptions{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if c.http.maxAttempts != 2 {
		t.Errorf("maxAttempts = %d, want 2", c.http.maxAttempts)
	}
	if c.http.backoffMs != 500 {
		t.Errorf("backoffMs = %d, want 500 (fixed delay)", c.http.backoffMs)
	}
	if c.http.httpClient.Timeout.Milliseconds() != 90_000 {
		t.Errorf("timeout = %v, want 90000ms", c.http.httpClient.Timeout)
	}
}

// Every request carries the bearer key and a JSON content type.
func TestAuthAndContentTypeHeadersOnEveryRequest(t *testing.T) {
	type seen struct{ auth, contentType string }
	var got []seen

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, seen{r.Header.Get("Authorization"), r.Header.Get("Content-Type")})
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sessions":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1"}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx := context.Background()
	if _, err := c.Sessions.Create(ctx, CreateSessionOptions{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.Profiles.List(ctx); err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(got))
	}
	for i, s := range got {
		if s.auth != "Bearer slr_live_id_secret" {
			t.Errorf("request %d Authorization = %q", i, s.auth)
		}
		if s.contentType != "application/json" {
			t.Errorf("request %d Content-Type = %q", i, s.contentType)
		}
	}
}

// Only 502/503/504 are retried; every other status returns on the first try.
func TestRetryPolicyByStatus(t *testing.T) {
	tests := []struct {
		status       string
		code         int
		wantAttempts int
	}{
		{"502 bad gateway is retried", 502, 2},
		{"503 unavailable is retried", 503, 2},
		{"504 gateway timeout is retried", 504, 2},
		{"400 bad request is not retried", 400, 1},
		{"401 unauthorized is not retried", 401, 1},
		{"402 payment required is not retried", 402, 1},
		{"429 rate limited is not retried", 429, 1},
		{"500 internal error is not retried", 500, 1},
	}

	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			attempts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(`{"error":"nope"}`))
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			_, err := c.Sessions.Create(context.Background(), CreateSessionOptions{})
			if err == nil {
				t.Fatal("expected an error")
			}
			if attempts != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", attempts, tc.wantAttempts)
			}
			var serr *SolariError
			if !errors.As(err, &serr) {
				t.Fatalf("expected *SolariError, got %T", err)
			}
			if serr.Status != tc.code {
				t.Errorf("Status = %d, want %d", serr.Status, tc.code)
			}
		})
	}
}

// A retryable status that clears on the second attempt succeeds transparently.
func TestRetrySucceedsOnSecondAttempt(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"no capacity"}`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1","cdpEndpoint":"wss://gw/cdp/s1"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	sess, err := c.Sessions.Create(context.Background(), CreateSessionOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if sess.ID != "s1" {
		t.Errorf("session id = %q", sess.ID)
	}
}

// Transport/network errors are retryable too.
func TestRetryOnTransportError(t *testing.T) {
	calls := 0
	stub := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("dial tcp: connection reset by peer")
		}
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(`{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})}

	c, err := NewClient(ClientOptions{
		APIKey:     "k",
		BaseURL:    "https://gw.example.com",
		HTTPClient: stub,
		BackoffMs:  zeroBackoff(),
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := c.Sessions.Create(context.Background(), CreateSessionOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if sess.ID != "s1" {
		t.Errorf("session id = %q", sess.ID)
	}
}

// Exhausting every attempt reports the last failure's status and code.
func TestExhaustedAttemptsPreservesStatusAndCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"code":"BrowserUnhealthy","error":"slot died"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Sessions.Create(context.Background(), CreateSessionOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "exhausted 2 attempts") {
		t.Errorf("error = %q, want it to mention exhausted attempts", err)
	}
	var serr *SolariError
	if !errors.As(err, &serr) {
		t.Fatalf("expected *SolariError, got %T", err)
	}
	if serr.Status != 503 {
		t.Errorf("Status = %d, want 503", serr.Status)
	}
	if serr.Code != CodeBrowserUnhealthy {
		t.Errorf("Code = %q, want %q", serr.Code, CodeBrowserUnhealthy)
	}
	if !IsCode(err, CodeBrowserUnhealthy) {
		t.Error("IsCode should match the wrapped code")
	}
	if serr.Unwrap() == nil {
		t.Error("exhausted error should wrap the last failure")
	}
}

// The gateway's `{"code":...}` is lifted onto SolariError.Code.
func TestErrorCodeParsing(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantCode string
	}{
		{"feature requires plan", 402, `{"code":"FeatureRequiresPlan","error":"stealth is a paid feature"}`, CodeFeatureRequiresPlan},
		{"concurrency limit", 429, `{"code":"ConcurrencyLimitExceeded"}`, CodeConcurrencyLimitExceeded},
		{"plan limit", 402, `{"code":"PlanLimitExceeded"}`, CodePlanLimitExceeded},
		{"browser unhealthy", 500, `{"code":"BrowserUnhealthy"}`, CodeBrowserUnhealthy},
		{"body without a code", 400, `{"error":"bad request"}`, ""},
		{"non-JSON body", 400, `<html>gateway timeout</html>`, ""},
		{"empty body", 400, ``, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			_, err := c.Sessions.Create(context.Background(), CreateSessionOptions{})
			var serr *SolariError
			if !errors.As(err, &serr) {
				t.Fatalf("expected *SolariError, got %T (%v)", err, err)
			}
			if serr.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", serr.Code, tc.wantCode)
			}
			if serr.Status != tc.status {
				t.Errorf("Status = %d, want %d", serr.Status, tc.status)
			}
			if tc.wantCode != "" && !IsCode(err, tc.wantCode) {
				t.Errorf("IsCode(%q) = false", tc.wantCode)
			}
		})
	}
}

// A huge error body is truncated rather than pasted whole into the message.
func TestErrorBodyIsTruncated(t *testing.T) {
	huge := strings.Repeat("x", 4000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(huge))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Sessions.Create(context.Background(), CreateSessionOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(err.Error()) > maxErrorBodyChars+128 {
		t.Errorf("message length = %d, want it truncated near %d", len(err.Error()), maxErrorBodyChars)
	}
}
