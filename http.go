package solari

// Shared HTTP transport for the SDK ⇆ Gateway REST API. One place owns auth
// headers, error mapping, retries/backoff, and timeouts.
//
// Retry policy (matches the reference TypeScript SDK): every request is tried
// up to maxAttempts times, waiting a FIXED backoffMs between attempts (not
// exponential). Transport errors and HTTP 502/503/504 are retried, as is any
// IDEMPOTENT request the gateway explicitly marked `"retryable": true` — every
// other status is returned to the caller as-is, including 429.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultMaxAttempts = 2
	defaultBackoffMs   = 500
	defaultTimeoutMs   = 90_000
)

// httpTransport carries out REST calls to the gateway.
type httpTransport struct {
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	maxAttempts int
	backoffMs   int
}

type httpTransportOptions struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	// maxAttempts counts the first try. 0 means default.
	maxAttempts int
	// backoffMs < 0 means "use the default"; 0 disables the wait (tests).
	backoffMs int
	// timeoutMs bounds each attempt. Ignored when httpClient is supplied.
	timeoutMs int
}

func newHTTPTransport(o httpTransportOptions) (*httpTransport, error) {
	if o.apiKey == "" {
		return nil, newSolariError("Solari: apiKey is required")
	}
	if o.baseURL == "" {
		return nil, newSolariError("Solari: baseUrl is required")
	}
	maxAttempts := o.maxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	backoffMs := o.backoffMs
	if backoffMs < 0 {
		backoffMs = defaultBackoffMs
	}
	timeoutMs := o.timeoutMs
	if timeoutMs <= 0 {
		timeoutMs = defaultTimeoutMs
	}
	client := o.httpClient
	if client == nil {
		client = &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	}
	return &httpTransport{
		apiKey:      o.apiKey,
		baseURL:     strings.TrimRight(o.baseURL, "/"),
		httpClient:  client,
		maxAttempts: maxAttempts,
		backoffMs:   backoffMs,
	}, nil
}

// authHeader returns the Authorization value sent on every request.
func (t *httpTransport) authHeader() string { return "Bearer " + t.apiKey }

// httpRequestOptions carry per-request knobs.
type httpRequestOptions struct {
	// idempotencyKey, when set, is sent as `Idempotency-Key` on EVERY attempt
	// of this call and makes the request safe to replay. Minted once by the
	// caller: a key per attempt would make each retry a fresh create.
	idempotencyKey string
	// tolerateNotFound makes a 404 a success (nothing is decoded into out) —
	// used by the idempotent DELETE paths.
	tolerateNotFound bool
	// rejectNotFoundCode carves one error code back out of tolerateNotFound: a
	// 404 whose body carries this code is returned as an error instead of being
	// swallowed. Empty means "tolerate every 404". Set by Sessions.Release so a
	// refused session id is not mistaken for an already-released one; the other
	// idempotent DELETEs (profiles) leave it empty, where a 404 really does mean
	// "already gone".
	rejectNotFoundCode string
}

// isRetryableStatus reports whether a status warrants another attempt. Only the
// gateway's "try again" statuses qualify.
func isRetryableStatus(status int) bool {
	// 507 is listed although THIS gateway does not emit one (censused 2026-09-22:
	// browser emits 501/502/503 only). Desktop's InsufficientCapacity 507 is
	// transient, and a client's correctness must not depend on which gateway build
	// it reaches. Inert today, deliberately — do not remove it as dead code.
	return status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout ||
		status == http.StatusInsufficientStorage
}

// isSafeToReplay reports whether THIS REQUEST may be sent again. Deliberately
// a per-request question, not a per-method one: a POST is not idempotent by
// verb, but a POST carrying an Idempotency-Key is safe to replay because the
// server answers the second copy from the first one's result.
//
// RETRACTED REASON, kept deliberately: this used to be method-only, because
// "the browser API issues no Idempotency-Key, so a re-sent POST /sessions
// could leave a second live session behind". Creates now mint one, so the
// condition was removed rather than worked around.
func isSafeToReplay(method, idempotencyKey string) bool {
	return isIdempotentMethod(method) || idempotencyKey != ""
}

// newIdempotencyKey mints a key identifying a CALL, reused by its retries.
func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("slr-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("slr-%x", b)
}

// isIdempotentMethod reports whether a METHOD is safe to send twice.
func isIdempotentMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodPut:
		return true
	}
	return false
}

// saysRetryable reports whether the gateway explicitly marked this response
// retryable. The flag can appear on a status OUTSIDE the 5xx allowlist — today
// `404 ReplayPending`, where the recording upload is still in flight.
func saysRetryable(raw []byte) bool {
	var body struct {
		Retryable *bool `json:"retryable"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return false // non-JSON body carries no hint
	}
	return body.Retryable != nil && *body.Retryable
}

// request performs a REST call and decodes the JSON response into out (which
// may be nil to discard the body). A nil body sends no request payload at all.
func (t *httpTransport) request(ctx context.Context, method, path string, body interface{}, opts httpRequestOptions, out interface{}) error {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return newSolariError(fmt.Sprintf("Solari %s %s: failed to marshal request body: %v", method, path, err))
		}
		bodyBytes = b
	}

	var lastErr error
	for attempt := 1; ; attempt++ {
		status, raw, err := t.attempt(ctx, method, path, bodyBytes, opts.idempotencyKey)
		switch {
		case err != nil:
			// Transport/network failure — always retryable.
			lastErr = err
		case status >= 200 && status < 300:
			return decodeInto(method, path, raw, out)
		// A 404 tolerated here is swallowed before the retryable-hint case
		// below, so it is never retried. No route needs both today (the
		// hint's live case, 404 ReplayPending, is on GET replay-url, which
		// does not tolerate 404). ORDER IS ALSO LOAD-BEARING against
		// rejectNotFoundCode just below: a rejected 404 fails FAST here,
		// before the retryable-hint case ever sees it — a refused session id
		// is permanently invalid and retrying it just burns attempts.
		case status == http.StatusNotFound && opts.tolerateNotFound:
			if opts.rejectNotFoundCode != "" &&
				parseErrorCode(raw) == opts.rejectNotFoundCode {
				return newHTTPError(method, path, status, raw)
			}
			return nil
		case !isRetryableStatus(status) &&
			!(isSafeToReplay(method, opts.idempotencyKey) && saysRetryable(raw)):
			return newHTTPError(method, path, status, raw)
		default:
			lastErr = newHTTPError(method, path, status, raw)
		}

		if attempt >= t.maxAttempts {
			return t.exhausted(method, path, lastErr)
		}
		if err := t.sleepBackoff(ctx); err != nil {
			return err
		}
	}
}

// attempt performs one round-trip and fully reads the body.
func (t *httpTransport) attempt(ctx context.Context, method, path string, bodyBytes []byte, idempotencyKey string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, bytesReader(bodyBytes))
	if err != nil {
		return 0, nil, &SolariError{
			Message: fmt.Sprintf("Solari %s %s: %v", method, path, err),
			Err:     err,
		}
	}
	req.Header.Set("Authorization", t.authHeader())
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	res, err := t.httpClient.Do(req)
	if err != nil {
		return 0, nil, &SolariError{
			Message: fmt.Sprintf("Solari %s %s: %v", method, path, err),
			Err:     err,
		}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, &SolariError{
			Message: fmt.Sprintf("Solari %s %s: reading body failed: %v", method, path, err),
			Err:     err,
		}
	}
	return res.StatusCode, raw, nil
}

// exhausted wraps the last failure after every attempt was spent. Unlike the
// TypeScript SDK it preserves the last response's Status/Code, so callers can
// still branch on them via errors.As.
func (t *httpTransport) exhausted(method, path string, lastErr error) error {
	out := &SolariError{
		Message: fmt.Sprintf("Solari %s %s: exhausted %d attempts", method, path, t.maxAttempts),
		Err:     lastErr,
	}
	if serr, ok := lastErr.(*SolariError); ok {
		out.Status, out.Code = serr.Status, serr.Code
		if serr.Message != "" {
			out.Message += ": " + serr.Message
		}
	}
	return out
}

// decodeInto unmarshals a success body into out, tolerating an empty body.
func decodeInto(method, path string, raw []byte, out interface{}) error {
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &SolariError{
			Message: fmt.Sprintf("Solari %s %s: decoding response failed: %v", method, path, err),
			Err:     err,
		}
	}
	return nil
}

// sleepBackoff waits the fixed retry delay, respecting ctx cancellation.
func (t *httpTransport) sleepBackoff(ctx context.Context) error {
	if t.backoffMs <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(time.Duration(t.backoffMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// bytesReader returns an io.Reader for b, or nil when b is nil (so a bodyless
// request really sends no body).
func bytesReader(b []byte) io.Reader {
	if b == nil {
		return nil
	}
	return bytes.NewReader(b)
}
