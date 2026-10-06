// Package solari is the Go language binding for the Solari Browser platform:
// create, inspect, and release managed remote-browser sessions, and manage the
// stored profiles they attach.
//
// Scope: this SDK is the REST control plane plus browser attachment. The
// reference TypeScript SDK's launch() returns a live Playwright Browser; Go has
// no Playwright, so chromedp drives a session instead. Sessions.Create hands
// back the raw CDP endpoint for Connect, and Sessions.Launch is a thin one-call
// convenience (create, connect, seed cookies, probe, retry, release) that hands
// back a chromedp browser context to drive. See the README.
package solari

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// ClientOptions configure a Client. Nothing is read from the environment —
// pass everything explicitly.
type ClientOptions struct {
	// APIKey authenticates every request. Format: slr_live_<id>_<secret>.
	APIKey string
	// Region routes sessions through a Solari edge. Defaults to RegionUSWest.
	// Ignored when BaseURL is set.
	Region Region
	// BaseURL overrides the region's API origin (staging / self-hosted
	// gateways). When set, Region is ignored.
	BaseURL string
	// HTTPClient overrides the default HTTP client (mainly for tests). When
	// set, TimeoutMs is ignored.
	HTTPClient *http.Client
	// MaxAttempts caps tries per request, counting the first. Default 2.
	MaxAttempts int
	// BackoffMs is the FIXED delay between attempts. Default 500. A pointer so
	// that 0 (no wait — handy for tests) is distinguishable from unset.
	BackoffMs *int
	// TimeoutMs bounds each attempt. Default 90000.
	TimeoutMs int
}

// Client talks the SDK ⇆ Gateway REST API. It is safe for concurrent use.
type Client struct {
	http *httpTransport

	// Sessions manages remote-browser sessions.
	Sessions *Sessions
	// Profiles manages stored browser profiles.
	Profiles *Profiles
	// Proxy exposes managed-egress metadata.
	Proxy *Proxy
}

// NewClient constructs a Client. It fails when APIKey is empty or Region is
// unknown.
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.APIKey == "" {
		return nil, newSolariError("Solari: apiKey is required")
	}

	baseURL := opts.BaseURL
	if baseURL == "" {
		region := opts.Region
		if region == "" {
			region = DefaultRegion
		}
		url, ok := regionURLs[region]
		if !ok {
			return nil, newSolariError(fmt.Sprintf(
				"Solari: unsupported region %q. Supported: %s.",
				region, strings.Join(supportedRegions(), ", ")))
		}
		baseURL = url
	}

	backoffMs := -1
	if opts.BackoffMs != nil {
		backoffMs = *opts.BackoffMs
	}

	ht, err := newHTTPTransport(httpTransportOptions{
		apiKey:      opts.APIKey,
		baseURL:     baseURL,
		httpClient:  opts.HTTPClient,
		maxAttempts: opts.MaxAttempts,
		backoffMs:   backoffMs,
		timeoutMs:   opts.TimeoutMs,
	})
	if err != nil {
		return nil, err
	}

	c := &Client{http: ht}
	c.Sessions = &Sessions{client: c}
	c.Profiles = &Profiles{client: c}
	c.Proxy = &Proxy{client: c}
	return c, nil
}

// BaseURL returns the resolved API origin, without a trailing slash.
func (c *Client) BaseURL() string { return c.http.baseURL }

// supportedRegions lists the known regions, sorted for a stable message.
func supportedRegions() []string {
	out := make([]string, 0, len(regionURLs))
	for r := range regionURLs {
		out = append(out, string(r))
	}
	sort.Strings(out)
	return out
}
