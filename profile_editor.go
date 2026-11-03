package solari

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultWaitForSavePoll is how often WaitForSave checks the profile list.
const DefaultWaitForSavePoll = 2 * time.Second

// EditorStatus is the state of a profile editor.
type EditorStatus string

const (
	EditorIdle      EditorStatus = "idle"
	EditorLaunching EditorStatus = "launching"
	EditorEditing   EditorStatus = "editing"
	EditorSaving    EditorStatus = "saving"
	EditorError     EditorStatus = "error"
)

// EditorState is the result of Editor.Status.
type EditorState struct {
	EditorStatus EditorStatus `json:"editorStatus"`
	Version      int          `json:"version"`
}

// Editor is a handle on a profile editor opened by Profiles.Edit. Send the
// person to URL; it is valid until ExpiresAt (about 30 minutes). There is no
// save without closing: SaveAndClose stores the profile and ends the editor,
// Exit ends it without saving.
//
// An expired or used link makes every call fail with a *SolariError of status
// 404; a link whose editor is not running fails with status 409.
type Editor struct {
	profiles *Profiles
	// ProfileID is the profile being edited.
	ProfileID string
	// HandoffID identifies this editor link (sent as X-Handoff-Id).
	HandoffID string
	// URL is the editor link to open in a browser.
	URL string
	// ExpiresAt is when URL stops working.
	ExpiresAt time.Time
	// StartVersion is the profile's version when the editor was opened.
	StartVersion int
	// PollInterval overrides how often WaitForSave polls. Zero means
	// DefaultWaitForSavePoll.
	PollInterval time.Duration
}

type loginHandoff struct {
	HandoffID string          `json:"handoffId"`
	URL       string          `json:"url"`
	ExpiresAt json.RawMessage `json:"expiresAt"`
	Version   int             `json:"version"`
	ProfileID string          `json:"profileId"`
}

// parseExpiresAt accepts an ISO-8601 string or epoch milliseconds.
func parseExpiresAt(raw json.RawMessage) (time.Time, error) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return time.Time{}, fmt.Errorf("missing expiresAt")
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.UnixMilli(ms), nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// Edit opens an editor on an existing profile (POST /profiles/:id/login-handoff).
// reason is optional free text; pass "" to omit it.
func (p *Profiles) Edit(ctx context.Context, id, reason string) (*Editor, error) {
	var body map[string]interface{}
	if reason != "" {
		body = map[string]interface{}{"reason": reason}
	} else {
		body = map[string]interface{}{}
	}
	var out loginHandoff
	path := "/profiles/" + url.PathEscape(id) + "/login-handoff"
	if err := p.client.http.request(ctx, http.MethodPost, path, body, httpRequestOptions{}, &out); err != nil {
		return nil, err
	}
	exp, err := parseExpiresAt(out.ExpiresAt)
	if err != nil {
		return nil, &SolariError{Message: fmt.Sprintf("Solari POST %s: bad expiresAt: %v", path, err), Err: err}
	}
	pid := out.ProfileID
	if pid == "" {
		pid = id
	}
	return &Editor{profiles: p, ProfileID: pid, HandoffID: out.HandoffID, URL: out.URL,
		ExpiresAt: exp, StartVersion: out.Version}, nil
}

func (e *Editor) call(ctx context.Context, method, suffix string, out interface{}) error {
	return e.callOpts(ctx, method, suffix, out, false)
}

func (e *Editor) callOpts(ctx context.Context, method, suffix string, out interface{}, noRetry bool) error {
	path := "/profiles/" + url.PathEscape(e.ProfileID) + "/editor" + suffix
	return e.profiles.client.http.request(ctx, method, path, nil,
		httpRequestOptions{headers: map[string]string{"X-Handoff-Id": e.HandoffID}, noRetry: noRetry}, out)
}

// Status reports the editor's state (GET /profiles/:id/editor).
func (e *Editor) Status(ctx context.Context) (*EditorState, error) {
	var out EditorState
	if err := e.call(ctx, http.MethodGet, "", &out); err != nil {
		return nil, err
	}
	// The gateway reports null before the editor page has been opened.
	if out.EditorStatus == "" {
		out.EditorStatus = EditorIdle
	}
	return &out, nil
}

// SaveAndClose stores the editor's browser state in the profile and closes the
// editor (POST /profiles/:id/editor/save). It returns the new version. A 502 means
// the save failed and the editor went to error: it is not retried automatically;
// call Exit and start a new Edit (a second save gets 409).
func (e *Editor) SaveAndClose(ctx context.Context) (int, error) {
	var out struct {
		Version int `json:"version"`
	}
	if err := e.callOpts(ctx, http.MethodPost, "/save", &out, true); err != nil {
		return 0, err
	}
	return out.Version, nil
}

// Exit closes the editor without saving (DELETE /profiles/:id/editor).
func (e *Editor) Exit(ctx context.Context) error {
	return e.callOpts(ctx, http.MethodDelete, "", nil, true)
}

// WaitForSave polls Profiles.List until the profile's version exceeds
// StartVersion — someone saved from the editor page — and returns that
// version. It fails with a *SolariError of status 408 when timeout passes or
// the link expires first. A timeout of zero waits until the link expires.
func (e *Editor) WaitForSave(ctx context.Context, timeout time.Duration) (int, error) {
	deadline := e.ExpiresAt
	if timeout > 0 {
		if d := time.Now().Add(timeout); d.Before(deadline) {
			deadline = d
		}
	}
	poll := e.PollInterval
	if poll <= 0 {
		poll = DefaultWaitForSavePoll
	}
	for {
		list, err := e.profiles.List(ctx)
		if err != nil {
			return 0, err
		}
		for _, pr := range list {
			if pr.ID == e.ProfileID && pr.Version > e.StartVersion {
				return pr.Version, nil
			}
		}
		if !time.Now().Add(poll).Before(deadline) {
			return 0, &SolariError{Status: http.StatusRequestTimeout,
				Message: fmt.Sprintf("Solari: timed out waiting for profile %s to be saved", e.ProfileID)}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(poll):
		}
	}
}
