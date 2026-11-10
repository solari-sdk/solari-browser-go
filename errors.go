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

var urlTokenRE = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s"'<>)\],]+`)

// redactURL is scheme://host[:port]/… of a URL; <url> for anything that does not parse. A CDP endpoint, a
// signed replay URL and a preview URL carry their credential in the path or query.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<url>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// scrubURLs returns text with each of secrets (a session's ws / cdp endpoints) replaced by its redacted form, then
// every other URL-looking token cut to scheme://host/…: the libraries print the URL they dialled.
func scrubURLs(text string, secrets ...string) string {
	for _, s := range secrets {
		if s != "" {
			text = strings.ReplaceAll(text, s, redactURL(s))
		}
	}
	return urlTokenRE.ReplaceAllStringFunc(text, redactURL)
}

// scrubbedError keeps an error's identity for errors.Is / errors.As-free callers but carries only the scrubbed text:
// it deliberately does not Unwrap, because the wrapped error's text holds the URL.
type scrubbedError struct {
	msg  string
	orig error
}

func (e *scrubbedError) Error() string { return e.msg }

// Is lets errors.Is(err, context.DeadlineExceeded) and friends still work without exposing the original text.
func (e *scrubbedError) Is(target error) bool { return errors.Is(e.orig, target) }

// scrubErr returns err with every URL in its text cut down, or nil for nil.
func scrubErr(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return &scrubbedError{msg: scrubURLs(err.Error(), secrets...), orig: err}
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

var (
	sessionIDFldRE = regexp.MustCompile(`("sessionId"\s*:\s*")([^"]+)(")`)
)

func shortID(id string) string {
	if r := []rune(id); len(r) > 8 {
		return string(r[:8]) + "…"
	}
	return id
}

// redactBody cuts the session id of path (`/sessions/<id>/…`), in any spelling, and every `"sessionId":"…"` field of a response
// body to its first 8 characters. The id is the credential inside the ws/cdp URLs and the gateway's error bodies used to echo
// it (replay-url 404), so it must not reach an error message through the body either.
func redactBody(path, body string) string {
	for _, m := range sessionPathRE.FindAllStringSubmatch(path, -1) {
		raw := m[2]
		if len([]rune(raw)) <= 8 {
			continue
		}
		plain, err := url.PathUnescape(raw)
		if err != nil {
			plain = raw
		}
		for _, form := range []string{raw, url.PathEscape(plain), url.QueryEscape(plain), plain} {
			body = strings.ReplaceAll(body, form, shortID(plain))
		}
	}
	return sessionIDFldRE.ReplaceAllStringFunc(body, func(f string) string {
		sub := sessionIDFldRE.FindStringSubmatch(f)
		return sub[1] + shortID(sub[2]) + sub[3]
	})
}

// newHTTPError builds the error for a non-2xx gateway response, mirroring the
// reference TypeScript SDK's message shape.
func newHTTPError(method, path string, status int, raw []byte) *SolariError {
	body := redactBody(path, strings.TrimSpace(string(raw)))
	if len(body) > maxErrorBodyChars {
		body = body[:maxErrorBodyChars] + "…"
	}
	msg := fmt.Sprintf("Solari %s %s failed: %d", method, redactSessionPath(path), status)
	if body != "" {
		msg += " " + body
	}
	return &SolariError{Message: msg, Status: status, Code: parseErrorCode(raw)}
}
