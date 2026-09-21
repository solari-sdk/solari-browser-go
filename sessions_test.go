package solari

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A fully-specified create sends every key, with the proxy as a nested object.
func TestCreateSessionRequestShape(t *testing.T) {
	var gotMethod, gotPath string
	var body map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1","cdpEndpoint":"wss://gw/cdp/s1","expiresAt":"2026-07-16T12:00:00Z"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Sessions.Create(context.Background(), CreateSessionOptions{
		Recording: true,
		Stealth:   true,
		Captcha:   true,
		Proxy: ProxyRequest{
			Country:         "us",
			Tier:            TierMobile,
			ASN:             "21928",
			Session:         "warm-1",
			SessionDuration: 10,
			State:           "california",
			City:            "los_angeles",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/sessions" {
		t.Errorf("request = %s %s, want POST /sessions", gotMethod, gotPath)
	}
	for _, k := range []string{"recording", "stealth", "captcha"} {
		if body[k] != true {
			t.Errorf("body[%q] = %v, want true", k, body[k])
		}
	}
	proxy, ok := body["proxy"].(map[string]interface{})
	if !ok {
		t.Fatalf("body[proxy] = %#v, want an object", body["proxy"])
	}
	want := map[string]interface{}{
		"country": "us", "tier": "mobile", "asn": "21928",
		"session": "warm-1", "sessionDuration": float64(10),
		"state": "california", "city": "los_angeles",
	}
	for k, v := range want {
		if proxy[k] != v {
			t.Errorf("proxy[%q] = %#v, want %#v", k, proxy[k], v)
		}
	}
}

// Falsy options are omitted; an empty options set sends no body at all.
func TestCreateSessionOmitsFalsyKeys(t *testing.T) {
	tests := []struct {
		name     string
		opts     CreateSessionOptions
		wantBody string // "" means: no body bytes at all
	}{
		{
			name:     "zero options send no body",
			opts:     CreateSessionOptions{},
			wantBody: "",
		},
		{
			name:     "false flags are omitted",
			opts:     CreateSessionOptions{Recording: false, Stealth: false, Captcha: false},
			wantBody: "",
		},
		{
			name:     "only the set keys are sent",
			opts:     CreateSessionOptions{Stealth: true},
			wantBody: `{"stealth":true}`,
		},
		{
			name:     "empty profileId is omitted",
			opts:     CreateSessionOptions{ProfileID: "", Recording: true},
			wantBody: `{"recording":true}`,
		},
		{
			name:     "profileId is sent when set",
			opts:     CreateSessionOptions{ProfileID: "prof_1"},
			wantBody: `{"profileId":"prof_1"}`,
		},
		{
			name:     "a country preset proxy is sent as a bare string",
			opts:     CreateSessionOptions{Proxy: ProxyCountry("gb")},
			wantBody: `{"proxy":"gb"}`,
		},
		{
			name:     "the smart preset is sent as a bare string",
			opts:     CreateSessionOptions{Proxy: ProxySmart},
			wantBody: `{"proxy":"smart"}`,
		},
		{
			name:     "the off preset is sent as a bare string",
			opts:     CreateSessionOptions{Proxy: ProxyOff},
			wantBody: `{"proxy":"off"}`,
		},
		{
			name:     "an empty ProxyRequest sends an empty object",
			opts:     CreateSessionOptions{Proxy: ProxyRequest{}},
			wantBody: `{"proxy":{}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var raw []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(201)
				_, _ = w.Write([]byte(`{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1"}`))
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			if _, err := c.Sessions.Create(context.Background(), tc.opts); err != nil {
				t.Fatalf("create: %v", err)
			}

			if tc.wantBody == "" {
				if len(raw) != 0 {
					t.Fatalf("body = %q, want no body at all", raw)
				}
				return
			}
			// Compare as JSON — map key order is not guaranteed.
			var got, want interface{}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("body %q is not JSON: %v", raw, err)
			}
			if err := json.Unmarshal([]byte(tc.wantBody), &want); err != nil {
				t.Fatal(err)
			}
			if !jsonEqual(got, want) {
				t.Errorf("body = %s, want %s", raw, tc.wantBody)
			}
		})
	}
}

// The upstream ws/cdp endpoints are handed back verbatim — no local proxy.
func TestCreateSessionReturnsUpstreamEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{
			"sessionId": "org1:pool2:sess3",
			"wsEndpoint": "wss://api.getsolari.com/ws/org1:pool2:sess3",
			"cdpEndpoint": "wss://api.getsolari.com/cdp/org1:pool2:sess3",
			"expiresAt": "2026-07-16T12:00:00.000Z",
			"proxy": {
				"server": "http://gate.decodo.com:7000",
				"username": "user-1",
				"password": "pw",
				"timezoneId": "America/Los_Angeles",
				"country": "us",
				"tier": "mobile"
			}
		}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	sess, err := c.Sessions.Create(context.Background(), CreateSessionOptions{Stealth: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if sess.ID != "org1:pool2:sess3" {
		t.Errorf("ID = %q", sess.ID)
	}
	if sess.WSEndpoint != "wss://api.getsolari.com/ws/org1:pool2:sess3" {
		t.Errorf("WSEndpoint = %q, want the upstream endpoint verbatim", sess.WSEndpoint)
	}
	if sess.CDPEndpoint != "wss://api.getsolari.com/cdp/org1:pool2:sess3" {
		t.Errorf("CDPEndpoint = %q, want the upstream endpoint verbatim", sess.CDPEndpoint)
	}
	if strings.Contains(sess.WSEndpoint, "127.0.0.1") || strings.Contains(sess.WSEndpoint, "localhost") {
		t.Error("endpoints must not be rewritten to a local proxy")
	}
	if sess.ExpiresAt != "2026-07-16T12:00:00.000Z" {
		t.Errorf("ExpiresAt = %q", sess.ExpiresAt)
	}
	if sess.Proxy == nil {
		t.Fatal("Proxy should be populated")
	}
	if sess.Proxy.Tier != TierMobile || sess.Proxy.TimezoneID != "America/Los_Angeles" {
		t.Errorf("Proxy = %+v", *sess.Proxy)
	}
	if sess.StorageState != nil || sess.StorageStateAttached {
		t.Error("no profile was attached; StorageState must stay nil and unattached")
	}
}

// A response without a proxy leaves Session.Proxy nil.
func TestCreateSessionWithoutProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1","cdpEndpoint":"wss://gw/cdp/s1"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	sess, err := c.Sessions.Create(context.Background(), CreateSessionOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sess.Proxy != nil {
		t.Errorf("Proxy = %+v, want nil", *sess.Proxy)
	}
}

// A missing cdpEndpoint is derived from the ws endpoint.
func TestCreateSessionDerivesCdpEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"sessionId":"s1","wsEndpoint":"wss://api.getsolari.com/ws/s1?tok=abc"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	sess, err := c.Sessions.Create(context.Background(), CreateSessionOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := "wss://api.getsolari.com/cdp/s1?tok=abc"
	if sess.CDPEndpoint != want {
		t.Errorf("CDPEndpoint = %q, want %q", sess.CDPEndpoint, want)
	}
}

func TestDeriveCdpFromWs(t *testing.T) {
	tests := []struct {
		name string
		ws   string
		want string
	}{
		{"rewrites the /ws/ path", "wss://api.getsolari.com/ws/s1", "wss://api.getsolari.com/cdp/s1"},
		{"keeps the query string", "wss://gw/ws/s1?tok=abc", "wss://gw/cdp/s1?tok=abc"},
		{"keeps a composite id", "wss://gw/ws/org1:pool2:sess3", "wss://gw/cdp/org1:pool2:sess3"},
		{"handles a ws:// scheme", "ws://localhost:4000/ws/s1", "ws://localhost:4000/cdp/s1"},
		{"passes through a non-/ws/ path", "wss://gw/socket/s1", "wss://gw/socket/s1"},
		{"passes through an already-cdp path", "wss://gw/cdp/s1", "wss://gw/cdp/s1"},
		{"passes through an unparseable endpoint", "://nonsense", "://nonsense"},
		{"passes through an empty string", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveCdpFromWs(tc.ws); got != tc.want {
				t.Errorf("deriveCdpFromWs(%q) = %q, want %q", tc.ws, got, tc.want)
			}
		})
	}
}

// A response missing sessionId/wsEndpoint is rejected.
func TestCreateSessionUnexpectedResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"no sessionId", `{"wsEndpoint":"wss://gw/ws/s1"}`},
		{"no wsEndpoint", `{"sessionId":"s1"}`},
		{"empty object", `{}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(201)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			_, err := c.Sessions.Create(context.Background(), CreateSessionOptions{})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "unexpected session response") {
				t.Errorf("error = %q", err)
			}
		})
	}
}

// A missing expiresAt falls back to now + 1h, ISO 8601 UTC.
func TestCreateSessionDefaultsExpiresAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	sess, err := c.Sessions.Create(context.Background(), CreateSessionOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := time.Parse(time.RFC3339, sess.ExpiresAt)
	if err != nil {
		t.Fatalf("ExpiresAt %q is not ISO 8601: %v", sess.ExpiresAt, err)
	}
	if d := time.Until(got); d < 55*time.Minute || d > 65*time.Minute {
		t.Errorf("ExpiresAt is %v out, want ~1h", d)
	}
	if !strings.HasSuffix(sess.ExpiresAt, "Z") {
		t.Errorf("ExpiresAt = %q, want a UTC (Z) timestamp", sess.ExpiresAt)
	}
}

// storageState: nil without a profile, fetched from the presigned URL with one,
// and nil-but-attached when the profile is empty.
func TestCreateSessionStorageState(t *testing.T) {
	const stateJSON = `{"cookies":[{"name":"sid","value":"abc","domain":".example.com","secure":true,"sameSite":"Lax"}],"origins":[{"origin":"https://example.com","localStorage":[{"name":"k","value":"v"}]}]}`

	tests := []struct {
		name          string
		profileID     string
		respBody      string
		wantFetched   bool
		wantAttached  bool
		wantStateNil  bool
		wantCookieVal string
	}{
		{
			name:         "no profile means no storage state and no fetch",
			profileID:    "",
			respBody:     `{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1","storageStateUrl":{"url":"%s","expiresInSeconds":300}}`,
			wantFetched:  false,
			wantAttached: false,
			wantStateNil: true,
		},
		{
			name:          "a presigned url is fetched",
			profileID:     "prof_1",
			respBody:      `{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1","storageStateUrl":{"url":"%s","expiresInSeconds":300}}`,
			wantFetched:   true,
			wantAttached:  true,
			wantCookieVal: "abc",
		},
		{
			name:         "a null presigned url means an empty profile",
			profileID:    "prof_1",
			respBody:     `{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1","storageStateUrl":{"url":null}}`,
			wantFetched:  false,
			wantAttached: true,
			wantStateNil: true,
		},
		{
			name:          "an inline storageState is used as-is",
			profileID:     "prof_1",
			respBody:      `{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1","storageState":` + stateJSON + `}`,
			wantFetched:   false,
			wantAttached:  true,
			wantCookieVal: "abc",
		},
		{
			name:         "no storage state at all means an empty profile",
			profileID:    "prof_1",
			respBody:     `{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1"}`,
			wantFetched:  false,
			wantAttached: true,
			wantStateNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fetched := false
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/presigned-storage-state" {
					fetched = true
					// A presigned link is pre-authenticated: no bearer token.
					if r.Header.Get("Authorization") != "" {
						t.Errorf("presigned fetch sent an Authorization header")
					}
					_, _ = w.Write([]byte(stateJSON))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(201)
				body := tc.respBody
				if strings.Contains(body, "%s") {
					body = strings.Replace(body, "%s", srv.URL+"/presigned-storage-state", 1)
				}
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			sess, err := c.Sessions.Create(context.Background(), CreateSessionOptions{ProfileID: tc.profileID})
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			if fetched != tc.wantFetched {
				t.Errorf("presigned fetched = %v, want %v", fetched, tc.wantFetched)
			}
			if sess.StorageStateAttached != tc.wantAttached {
				t.Errorf("StorageStateAttached = %v, want %v", sess.StorageStateAttached, tc.wantAttached)
			}
			if tc.wantStateNil {
				if sess.StorageState != nil {
					t.Errorf("StorageState = %+v, want nil", *sess.StorageState)
				}
				return
			}
			if sess.StorageState == nil {
				t.Fatal("StorageState = nil, want it populated")
			}
			if len(sess.StorageState.Cookies) != 1 || sess.StorageState.Cookies[0].Value != tc.wantCookieVal {
				t.Errorf("Cookies = %+v", sess.StorageState.Cookies)
			}
			if len(sess.StorageState.Origins) != 1 || sess.StorageState.Origins[0].Origin != "https://example.com" {
				t.Errorf("Origins = %+v", sess.StorageState.Origins)
			}
		})
	}
}

// A failing presigned fetch surfaces as a SolariError carrying its status.
func TestCreateSessionStorageStateFetchFails(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/presigned" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`<Error>AccessDenied</Error>`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"sessionId":"s1","wsEndpoint":"wss://gw/ws/s1","storageStateUrl":{"url":"` + srv.URL + `/presigned"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Sessions.Create(context.Background(), CreateSessionOptions{ProfileID: "prof_1"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "storageState fetch returned 403") {
		t.Errorf("error = %q", err)
	}
}

func TestSessionsGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sessions/org1:pool2:sess3" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"org1:pool2:sess3","status":"running","expiresAt":"2026-07-16T12:00:00Z","slot":4}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	view, err := c.Sessions.Get(context.Background(), "org1:pool2:sess3")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if view.ID != "org1:pool2:sess3" || view.Status != "running" {
		t.Errorf("view = %+v", *view)
	}
	// Pool-versioned fields survive in Raw.
	if !strings.Contains(string(view.Raw), `"slot":4`) {
		t.Errorf("Raw = %s, want the full body", view.Raw)
	}
}

// Release is idempotent: a 404 is a success.
func TestSessionsRelease(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"204 succeeds", 204, false},
		{"200 succeeds", 200, false},
		{"404 is tolerated", 404, false},
		{"400 fails", 400, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			err := c.Sessions.Release(context.Background(), "s1")
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("release: %v", err)
			}
			if gotMethod != http.MethodDelete || gotPath != "/sessions/s1" {
				t.Errorf("request = %s %s, want DELETE /sessions/s1", gotMethod, gotPath)
			}
		})
	}
}

// replay-url fills in its documented defaults.
func TestSessionsReplayURL(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantExpires int
		wantEnc     string
		wantErr     bool
	}{
		{
			name:        "defaults when the gateway omits the optional keys",
			body:        `{"url":"https://s3/replay.ndjson.gz"}`,
			wantExpires: 0,
			wantEnc:     "gzip",
		},
		{
			name:        "explicit values win",
			body:        `{"url":"https://s3/replay.ndjson","expiresInSeconds":900,"contentEncoding":"identity"}`,
			wantExpires: 900,
			wantEnc:     "identity",
		},
		{
			name:    "a response without a url is rejected",
			body:    `{"expiresInSeconds":900}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/sessions/s1/replay-url" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			link, err := c.Sessions.ReplayURL(context.Background(), "s1")
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				if !strings.Contains(err.Error(), "unexpected replay-url response") {
					t.Errorf("error = %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("replay-url: %v", err)
			}
			if link.ExpiresInSeconds != tc.wantExpires {
				t.Errorf("ExpiresInSeconds = %d, want %d", link.ExpiresInSeconds, tc.wantExpires)
			}
			if link.ContentEncoding != tc.wantEnc {
				t.Errorf("ContentEncoding = %q, want %q", link.ContentEncoding, tc.wantEnc)
			}
		})
	}
}

func TestSessionsDownloadReplay(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blob" {
			_, _ = w.Write([]byte("{\"t\":1}\n{\"t\":2}\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"` + srv.URL + `/blob","contentEncoding":"identity"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	raw, err := c.Sessions.DownloadReplay(context.Background(), "s1")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(raw) != "{\"t\":1}\n{\"t\":2}\n" {
		t.Errorf("replay = %q", raw)
	}
}

// jsonEqual compares two decoded JSON values structurally.
func jsonEqual(a, b interface{}) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ab) == string(bb)
}
