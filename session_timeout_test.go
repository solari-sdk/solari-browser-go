package solari

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// SessionTimeoutMs is rounded UP to whole seconds; the legacy seconds field still works; the new name wins.
func TestSessionTimeoutSeconds(t *testing.T) {
	cases := []struct {
		name       string
		ms, legacy int
		want       int
		wantErr    bool
	}{
		{"unset", 0, 0, 0, false},
		{"1000 ms is 1 s", 1000, 0, 1, false},
		{"1001 ms rounds up to 2 s", 1001, 0, 2, false},
		{"59999 ms rounds up to 60 s", 59999, 0, 60, false},
		{"60000 ms is 60 s", 60000, 0, 60, false},
		{"1 ms rounds up to 1 s (the gateway then refuses under 60)", 1, 0, 1, false},
		{"legacy seconds only", 0, 90, 90, false},
		{"both set: SessionTimeoutMs wins", 120000, 90, 120, false},
		{"largest ms that fits int32 seconds", math.MaxInt32 * 1000, 0, math.MaxInt32, false},
		{"one ms past int32 seconds", math.MaxInt32*1000 + 1, 0, 0, true},
		{"max int does not overflow", math.MaxInt, 0, 0, true},
		{"negative ms", -1, 0, 0, true},
		{"negative legacy", 0, -5, 0, true},
	}
	for _, c := range cases {
		got, err := sessionTimeoutSeconds(c.ms, c.legacy)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: got (%d, %v), want (%d, err=%v)", c.name, got, err, c.want, c.wantErr)
		}
	}
}

// The wire field stays timeoutSeconds, with the converted value; a negative value sends nothing.
func TestCreateSendsConvertedSessionTimeout(t *testing.T) {
	var body map[string]interface{}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	if _, err := c.Sessions.Create(context.Background(), CreateSessionOptions{SessionTimeoutMs: 61001, TimeoutSeconds: 10}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if body["timeoutSeconds"] != float64(62) {
		t.Errorf("timeoutSeconds = %v, want 62 (61001 ms rounded up; the new name wins over TimeoutSeconds)", body["timeoutSeconds"])
	}
	if _, ok := body["sessionTimeoutMs"]; ok {
		t.Error("the new name must not be sent on the wire")
	}

	calls = 0
	if _, err := c.Sessions.Create(context.Background(), CreateSessionOptions{SessionTimeoutMs: -1}); err == nil {
		t.Error("a negative SessionTimeoutMs must be rejected")
	}
	if calls != 0 {
		t.Errorf("%d requests were sent for a negative value, want none", calls)
	}
}

func TestProbeTimeoutFor(t *testing.T) {
	cases := []struct {
		name    string
		ms      int
		legacy  time.Duration
		want    time.Duration
		wantErr bool
	}{
		{"default", 0, 0, 2 * time.Second, false},
		{"ms", 500, 0, 500 * time.Millisecond, false},
		{"legacy duration", 0, 3 * time.Second, 3 * time.Second, false},
		{"both: ms wins", 500, 3 * time.Second, 500 * time.Millisecond, false},
		{"negative ms", -1, 0, 0, true},
	}
	for _, c := range cases {
		got, err := probeTimeoutFor(c.ms, c.legacy)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: got (%v, %v), want (%v, err=%v)", c.name, got, err, c.want, c.wantErr)
		}
	}
}
