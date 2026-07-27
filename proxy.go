package solari

import (
	"context"
	"net/http"
)

// Proxy exposes managed-egress metadata. Obtain it from Client.Proxy.
type Proxy struct {
	client *Client
}

// Countries lists the egress countries this gateway supports, and whether
// managed proxying is configured at all (GET /proxy/countries). Useful to gate
// a region picker, or to check before sending CreateSessionOptions.Proxy.
func (p *Proxy) Countries(ctx context.Context) (*ProxyCountries, error) {
	var out ProxyCountries
	if err := p.client.http.request(ctx, http.MethodGet, "/proxy/countries", nil, httpRequestOptions{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
