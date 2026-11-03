package solari

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var sessionPathRE = regexp.MustCompile(`(/sessions/)([^/?#\s"]+)`)

// redactSessionPath cuts the session id in every `/sessions/<id>` of s to its first 8 characters. The
// session id is the credential inside the ws/cdp URLs, so it must not appear in error text or logs.
func redactSessionPath(s string) string {
	return sessionPathRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := sessionPathRE.FindStringSubmatch(m)
		id := sub[2]
		if r := []rune(id); len(r) > 8 {
			id = string(r[:8]) + "…"
		}
		return sub[1] + id
	})
}

// redactErr returns err with the URL of a *url.Error (which net/http fills with the full request URL)
// redacted, so neither its text nor an unwrapped copy carries the session id.
func redactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return &url.Error{Op: ue.Op, URL: redactSessionPath(ue.URL), Err: ue.Err}
	}
	return err
}

// Error codes the gateway returns in a `{"code": ...}` error body. They are
// plain strings (not a closed enum) — the gateway may add more, so compare with
// IsCode rather than switching exhaustively.
const (
	// CodeFeatureRequiresPlan — the requested feature (stealth, captcha,
	// managed proxy, …) is not enabled for the org's plan.
	CodeFeatureRequiresPlan = "FeatureRequiresPlan"
	// CodeConcurrencyLimitExceeded — the org is at its live-session cap.
	CodeConcurrencyLimitExceeded = "ConcurrencyLimitExceeded"
	// CodePlanLimitExceeded — a plan quota (minutes, profiles, …) is spent.
	CodePlanLimitExceeded = "PlanLimitExceeded"
	// CodeBrowserUnhealthy — the acquired browser failed its health check.
	CodeBrowserUnhealthy = "BrowserUnhealthy"
	// CodeInvalidSessionId — the gateway refused a session id (malformed,
	// forged, or another org's) and acted on nothing. Only meaningful on a
	// 404; see Sessions.Release for why that is not blanket-success.
	CodeInvalidSessionId = "InvalidSessionId"
	// CodeProxyTierUnavailable — POST /sessions 503: a proxy tier you
	// explicitly requested cannot be served right now. No session was
	// created. The client does not retry it.
	CodeProxyTierUnavailable = "ProxyTierUnavailable"
)

// SolariError is the single error type the SDK produces. Match it with
// errors.As; inspect Status for the HTTP status (0 when the failure was not an
// HTTP response) and Code for the gateway's error code (empty when the body
// carried none).
//
//	var serr *solari.SolariError
//	if errors.As(err, &serr) && serr.Code == solari.CodeConcurrencyLimitExceeded {
//	    // back off and retry later
//	}
type SolariError struct {
	// Message is the human-readable description.
	Message string
	// Status is the HTTP status code, or 0 for transport/validation errors.
	Status int
	// Code is the gateway's `{"code": ...}` value, if any.
	Code string
	// Err is the underlying cause, if any. Exposed via errors.Unwrap.
	Err error
}

func (e *SolariError) Error() string { return e.Message }

func (e *SolariError) Unwrap() error { return e.Err }

func newSolariError(msg string) *SolariError { return &SolariError{Message: msg} }

// IsCode reports whether err (or anything it wraps) is a *SolariError carrying
// the given gateway error code.
func IsCode(err error, code string) bool {
	var serr *SolariError
	if errors.As(err, &serr) {
		return serr.Code == code
	}
	return false
}

// maxErrorBodyChars bounds how much of an error response body is folded into
// the error message — anti-bot vendors and proxies love to answer with whole
// HTML pages.
const maxErrorBodyChars = 512

// parseErrorCode pulls `{"code": "..."}` out of an error response body. A body
// that is absent, non-JSON, or lacks a string `code` yields "".
func parseErrorCode(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var parsed struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	return parsed.Code
}

// newHTTPError builds the error for a non-2xx gateway response, mirroring the
// reference TypeScript SDK's message shape.
func newHTTPError(method, path string, status int, raw []byte) *SolariError {
	body := strings.TrimSpace(string(raw))
	if len(body) > maxErrorBodyChars {
		body = body[:maxErrorBodyChars] + "…"
	}
	msg := fmt.Sprintf("Solari %s %s failed: %d", method, redactSessionPath(path), status)
	if body != "" {
		msg += " " + body
	}
	return &SolariError{Message: msg, Status: status, Code: parseErrorCode(raw)}
}
