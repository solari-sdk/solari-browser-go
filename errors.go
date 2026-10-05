package solari

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

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
	msg := fmt.Sprintf("Solari %s %s failed: %d", method, path, status)
	if body != "" {
		msg += " " + body
	}
	return &SolariError{Message: msg, Status: status, Code: parseErrorCode(raw)}
}
