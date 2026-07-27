package solari

import (
	"context"
	"net/http"
	"net/url"
)

// Profiles manages stored browser profiles — the cookies + localStorage a
// session can attach with CreateSessionOptions.ProfileID. Obtain it from
// Client.Profiles.
type Profiles struct {
	client *Client
}

// List returns every profile owned by the org (GET /profiles).
func (p *Profiles) List(ctx context.Context) ([]Profile, error) {
	var out []Profile
	if err := p.client.http.request(ctx, http.MethodGet, "/profiles", nil, httpRequestOptions{}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Create makes an empty profile (POST /profiles).
func (p *Profiles) Create(ctx context.Context, name string) (*Profile, error) {
	var out Profile
	body := map[string]interface{}{"name": name}
	if err := p.client.http.request(ctx, http.MethodPost, "/profiles", body, httpRequestOptions{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete removes a profile (DELETE /profiles/:id). Idempotent: an
// already-gone profile (404) is not an error.
func (p *Profiles) Delete(ctx context.Context, id string) error {
	return p.client.http.request(ctx, http.MethodDelete, "/profiles/"+url.PathEscape(id), nil,
		httpRequestOptions{tolerateNotFound: true}, nil)
}

// Save overwrites a profile's contents (POST /profiles/:id/save), returning the
// new version and stored size.
func (p *Profiles) Save(ctx context.Context, id string, storageState StorageState) (*SaveResult, error) {
	var out SaveResult
	body := map[string]interface{}{"storageState": storageState}
	err := p.client.http.request(ctx, http.MethodPost, "/profiles/"+url.PathEscape(id)+"/save", body,
		httpRequestOptions{}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
