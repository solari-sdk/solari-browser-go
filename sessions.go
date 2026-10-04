package solari

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// storageStateFetchTimeout bounds the presigned storage-state download.
const storageStateFetchTimeout = 8 * time.Second

// defaultSessionTTL is assumed when the gateway omits expiresAt.
const defaultSessionTTL = time.Hour

// Sessions manages remote-browser sessions. Obtain it from Client.Sessions.
type Sessions struct {
	client *Client
}

// Create acquires a browser session (POST /sessions). The zero
// CreateSessionOptions requests a plain session and sends no body.
//
// The returned Session carries the upstream WSEndpoint and CDPEndpoint
// directly — pass CDPEndpoint to Connect to drive the browser.
func (s *Sessions) Create(ctx context.Context, opts CreateSessionOptions) (*Session, error) {
	body := map[string]interface{}{}
	if opts.ProfileID != "" {
		body["profileId"] = opts.ProfileID
	}
	if opts.Recording {
		body["recording"] = true
	}
	if opts.Stealth {
		body["stealth"] = true
	}
	if opts.Captcha {
		body["captcha"] = true
	}
	if opts.Proxy != nil {
		body["proxy"] = opts.Proxy
	}
	// An empty options set sends no body at all.
	var payload interface{}
	if len(body) > 0 {
		payload = body
	}

	var data createSessionResponse
	if err := s.client.http.request(ctx, http.MethodPost, "/sessions", payload, httpRequestOptions{}, &data); err != nil {
		return nil, err
	}
	if data.SessionID == "" || data.WSEndpoint == "" {
		return nil, newSolariError("Solari: unexpected session response: " + describe(data))
	}

	cdp := data.CDPEndpoint
	if cdp == "" {
		cdp = deriveCdpFromWs(data.WSEndpoint)
	}
	expiresAt := data.ExpiresAt
	if expiresAt == "" {
		expiresAt = time.Now().UTC().Add(defaultSessionTTL).Format("2006-01-02T15:04:05.000Z07:00")
	}

	session := &Session{
		ID:          data.SessionID,
		WSEndpoint:  data.WSEndpoint,
		CDPEndpoint: cdp,
		ExpiresAt:   expiresAt,
		Proxy:       data.Proxy,
	}

	// A storage state is only meaningful when a profile was attached. nil then
	// means "no profile"; nil with StorageStateAttached means "profile is empty".
	if opts.ProfileID != "" {
		session.StorageStateAttached = true
		if data.StorageStateURL != nil {
			state, err := s.fetchStorageState(ctx, data.StorageStateURL.URL)
			if err != nil {
				return nil, err
			}
			session.StorageState = state
		} else {
			session.StorageState = data.StorageState
		}
	}
	return session, nil
}

// Get fetches the gateway's current view of a session (GET /sessions/:id).
func (s *Sessions) Get(ctx context.Context, id string) (*SessionView, error) {
	var view SessionView
	err := s.client.http.request(ctx, http.MethodGet, "/sessions/"+url.PathEscape(id), nil, httpRequestOptions{}, &view)
	if err != nil {
		return nil, err
	}
	return &view, nil
}

// Release ends a session (DELETE /sessions/:id) and waits for the gateway to
// acknowledge. Idempotent: an already-gone session (404) is not an error.
func (s *Sessions) Release(ctx context.Context, id string) error {
	return s.client.http.request(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(id), nil,
		httpRequestOptions{tolerateNotFound: true}, nil)
}

// ReplayURL returns a presigned link to the session's replay
// (GET /sessions/:id/replay-url). Available ~1-3s after Release, and only for
// sessions created with Recording.
func (s *Sessions) ReplayURL(ctx context.Context, id string) (*ReplayURL, error) {
	// Defaults survive keys the gateway omits.
	res := ReplayURL{ContentEncoding: "gzip"}
	err := s.client.http.request(ctx, http.MethodGet,
		"/sessions/"+url.PathEscape(id)+"/replay-url", nil, httpRequestOptions{}, &res)
	if err != nil {
		return nil, err
	}
	if res.URL == "" {
		return nil, newSolariError("Solari: unexpected replay-url response: " + describe(res))
	}
	return &res, nil
}

// DownloadReplay fetches the session's replay bytes (NDJSON). The bytes may
// or may not actually be gzip despite what was uploaded — GCS decompresses a
// gzip object transparently on an ordinary GET, so check ReplayURL's
// ContentEncoding (now provider-accurate) before attempting to decompress.
func (s *Sessions) DownloadReplay(ctx context.Context, id string) ([]byte, error) {
	link, err := s.ReplayURL(ctx, id)
	if err != nil {
		return nil, err
	}
	raw, err := s.getPresigned(ctx, link.URL, 0, "replay")
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// fetchStorageState downloads a presigned storage state. An empty url means the
// profile exists but holds nothing yet.
func (s *Sessions) fetchStorageState(ctx context.Context, rawURL string) (*StorageState, error) {
	if rawURL == "" {
		return nil, nil
	}
	raw, err := s.getPresigned(ctx, rawURL, storageStateFetchTimeout, "storageState")
	if err != nil {
		return nil, err
	}
	var state StorageState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, &SolariError{
			Message: fmt.Sprintf("Solari: storageState response was not valid JSON: %v", err),
			Err:     err,
		}
	}
	return &state, nil
}

// getPresigned GETs a presigned URL. These are pre-authenticated object-store
// links, so no Authorization header is sent. A zero timeout leaves the bound to
// ctx and the HTTP client.
func (s *Sessions) getPresigned(ctx context.Context, rawURL string, timeout time.Duration, what string) ([]byte, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &SolariError{
			Message: fmt.Sprintf("Solari: failed to fetch %s: %v", what, err),
			Err:     err,
		}
	}
	res, err := s.client.http.httpClient.Do(req)
	if err != nil {
		return nil, &SolariError{
			Message: fmt.Sprintf("Solari: failed to fetch %s: %v", what, err),
			Err:     err,
		}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, &SolariError{
			Message: fmt.Sprintf("Solari: failed to read %s: %v", what, err),
			Err:     err,
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body := strings.TrimSpace(string(raw))
		if len(body) > 256 {
			body = body[:256] + "…"
		}
		return nil, &SolariError{
			Message: fmt.Sprintf("Solari: %s fetch returned %d: %s", what, res.StatusCode, body),
			Status:  res.StatusCode,
		}
	}
	return raw, nil
}

// deriveCdpFromWs turns a Playwright wire endpoint into its raw CDP sibling by
// rewriting /ws/<id> to /cdp/<id>. Anything unrecognized is passed through.
func deriveCdpFromWs(wsEndpoint string) string {
	u, err := url.Parse(wsEndpoint)
	if err != nil {
		return wsEndpoint
	}
	if !strings.HasPrefix(u.Path, "/ws/") {
		return wsEndpoint
	}
	u.Path = "/cdp/" + strings.TrimPrefix(u.Path, "/ws/")
	return u.String()
}

// describe renders a value as JSON for an error message, falling back to Go
// syntax when it will not marshal.
func describe(v interface{}) string {
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%+v", v)
}
