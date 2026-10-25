package solari

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// timeoutSeconds, keepAlive and userMetadata go out on create; the metadata comes back on the Session.
func TestCreateSessionTimeoutKeepAliveUserMetadata(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1","userMetadata":{"job":"j-7"}}`))
	}))
	defer srv.Close()

	s, err := newTestClient(t, srv.URL).Sessions.Create(context.Background(), CreateSessionOptions{
		SessionTimeoutMs: 120000,
		KeepAlive:        true,
		UserMetadata:     map[string]interface{}{"job": "j-7"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if body["timeoutSeconds"] != float64(120) || body["keepAlive"] != true {
		t.Errorf("body = %v, want timeoutSeconds 120 and keepAlive true", body)
	}
	if md, _ := body["userMetadata"].(map[string]interface{}); md["job"] != "j-7" {
		t.Errorf("userMetadata not sent: %v", body["userMetadata"])
	}
	if s.UserMetadata["job"] != "j-7" {
		t.Errorf("Session.UserMetadata = %v", s.UserMetadata)
	}
}

// The zero values of the new options add nothing to the body.
func TestCreateSessionNewOptionsOmittedWhenZero(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1"}`))
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv.URL).Sessions.Create(context.Background(), CreateSessionOptions{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(raw) != 0 {
		t.Errorf("a zero create sent a body: %s", raw)
	}
}

// Get lifts the gateway's fields out of the response.
func TestSessionsGetNewFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"s1","status":"active","kind":"stealth","org":"o1","createdAt":"2026-10-24T01:00:00Z","expiresAt":"2026-10-24T02:00:00Z","userMetadata":{"a":"b"},"wsEndpoint":"wss://gw/ws/s1","cdpEndpoint":"wss://gw/cdp/s1"}`))
	}))
	defer srv.Close()

	v, err := newTestClient(t, srv.URL).Sessions.Get(context.Background(), "s1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.Kind != "stealth" || v.Org != "o1" || v.CreatedAt == "" || v.UserMetadata["a"] != "b" || v.CDPEndpoint == "" || v.WSEndpoint == "" {
		t.Errorf("view = %+v", v)
	}
}

func TestSessionsList(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[{"id":"s1","status":"active","kind":"fast","createdAt":"c","expiresAt":"e","userMetadata":{"team":"x"}},{"id":"s2","status":"released"}],"nextCursor":"cur2"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	page, err := c.Sessions.List(context.Background(), ListSessionsOptions{
		Limit:    2,
		Cursor:   "cur1",
		Metadata: map[string]string{"team": "x", "env": "prod"},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotPath != "/sessions" {
		t.Errorf("path = %q", gotPath)
	}
	for _, want := range []string{"limit=2", "cursor=cur1"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	// Pairs are sorted by key and joined by AND.
	if !strings.Contains(gotQuery, "q=user_metadata%5B%27env%27%5D%3A%27prod%27+AND+user_metadata%5B%27team%27%5D%3A%27x%27") {
		t.Errorf("filter not rendered as expected: %q", gotQuery)
	}
	if len(page.Sessions) != 2 || page.Sessions[0].ID != "s1" || page.Sessions[0].UserMetadata["team"] != "x" || page.Sessions[1].Status != "released" {
		t.Errorf("sessions = %+v", page.Sessions)
	}
	if page.NextCursor != "cur2" {
		t.Errorf("NextCursor = %q", page.NextCursor)
	}
}

func TestSessionsListZeroOptionsAndEmptyPage(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[]}`))
	}))
	defer srv.Close()

	page, err := newTestClient(t, srv.URL).Sessions.List(context.Background(), ListSessionsOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotURL != "/sessions" {
		t.Errorf("url = %q, want a bare /sessions", gotURL)
	}
	if page.Sessions == nil || len(page.Sessions) != 0 || page.NextCursor != "" {
		t.Errorf("page = %+v, want an empty non-nil slice and no cursor", page)
	}
}

// A filter the gateway grammar cannot express is refused before any request is sent.
func TestSessionsListRejectsUnexpressibleFilter(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	for name, m := range map[string]map[string]string{
		"quote in value": {"k": "it's"},
		"quote in key":   {"k'": "v"},
		"empty key":      {"": "v"},
	} {
		if _, err := c.Sessions.List(context.Background(), ListSessionsOptions{Metadata: m}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	many := map[string]string{}
	for i := 0; i < 11; i++ {
		many[string(rune('a'+i))] = "v"
	}
	if _, err := c.Sessions.List(context.Background(), ListSessionsOptions{Metadata: many}); err == nil {
		t.Error("11 pairs: want an error")
	}
	if calls != 0 {
		t.Errorf("%d requests were sent, want none", calls)
	}
}

// A gateway 400 on the filter surfaces its code.
func TestSessionsListGatewayValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"Invalid q","code":"ValidationError","field":"q"}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv.URL).Sessions.List(context.Background(), ListSessionsOptions{})
	if !IsCode(err, "ValidationError") {
		t.Errorf("err = %v, want code ValidationError", err)
	}
}

// ReleaseAndWait is Release under the TS/Python name: same request, same 404 rules.
func TestReleaseAndWaitMatchesRelease(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		body    string
		wantErr bool
	}{
		"204 acked":                        {204, "", false},
		"bare 404 is already gone":         {404, `{"error":"Not Found"}`, false},
		"404 InvalidSessionId is an error": {404, `{"error":"Not Found","code":"InvalidSessionId"}`, true},
	} {
		var method, path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method, path = r.Method, r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		err := newTestClient(t, srv.URL).Sessions.ReleaseAndWait(context.Background(), "s1")
		srv.Close()
		if method != http.MethodDelete || path != "/sessions/s1" {
			t.Errorf("%s: request = %s %s, want DELETE /sessions/s1", name, method, path)
		}
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, want error=%v", name, err, tc.wantErr)
		}
		if tc.wantErr && !IsCode(err, CodeInvalidSessionId) {
			t.Errorf("%s: code not surfaced: %v", name, err)
		}
	}
}
