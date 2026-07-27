package solari

// Shared HTTP transport for the SDK ⇆ Gateway REST API. One place owns auth
// headers, error mapping, retries/backoff, and timeouts.
//
// Retry policy (matches the reference TypeScript SDK): every request is tried
// up to maxAttempts times, waiting a FIXED backoffMs between attempts (not
// exponential). Only transport errors and HTTP 502/503/504 are retried — every
// other status is returned to the caller as-is, including 429.

import (
	"bytes"
	"context"
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
	// tolerateNotFound makes a 404 a success (nothing is decoded into out) —
	// used by the idempotent DELETE paths.
	tolerateNotFound bool
}

// isRetryableStatus reports whether a status warrants another attempt. Only the
// gateway's "try again" statuses qualify.
func isRetryableStatus(status int) bool {
	return status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
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
		status, raw, err := t.attempt(ctx, method, path, bodyBytes)
		switch {
		case err != nil:
			// Transport/network failure — always retryable.
			lastErr = err
		case status >= 200 && status < 300:
			return decodeInto(method, path, raw, out)
		case status == http.StatusNotFound && opts.tolerateNotFound:
			return nil
		case !isRetryableStatus(status):
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
func (t *httpTransport) attempt(ctx context.Context, method, path string, bodyBytes []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, bytesReader(bodyBytes))
	if err != nil {
		return 0, nil, &SolariError{
			Message: fmt.Sprintf("Solari %s %s: %v", method, path, err),
			Err:     err,
		}
	}
	req.Header.Set("Authorization", t.authHeader())
	req.Header.Set("Content-Type", "application/json")

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
