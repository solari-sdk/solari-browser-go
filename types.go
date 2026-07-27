package solari

import "encoding/json"

// Region selects the Solari edge a session is routed through. More regions are
// coming soon.
type Region string

// RegionUSWest is the default (and currently only) region.
const RegionUSWest Region = "us-west"

// DefaultRegion is used when ClientOptions.Region is empty.
const DefaultRegion = RegionUSWest

// regionURLs maps a region to its API origin.
var regionURLs = map[Region]string{
	RegionUSWest: "https://api.getsolari.com",
}

// ── proxy ────────────────────────────────────────────────────────────────

// ProxyTier selects the egress pool for a managed proxy.
type ProxyTier string

const (
	// TierResidential is the default rotating residential pool.
	TierResidential ProxyTier = "residential"
	// TierStatic pins a fixed ISP IP.
	TierStatic ProxyTier = "static"
	// TierMobile egresses from carrier (CGNAT) IPs.
	TierMobile ProxyTier = "mobile"
)

// ProxySpec is what CreateSessionOptions.Proxy accepts: either a ProxyPreset
// (a bare country code, ProxyOff, or ProxySmart) or a ProxyRequest for the
// fully-specified form. It is a closed interface — the wire only accepts these.
type ProxySpec interface {
	isProxySpec()
}

// ProxyPreset is the shorthand string form of a proxy spec: a lowercase
// ISO-3166-1 alpha-2 country code (see ProxyCountry), ProxyOff, or ProxySmart.
type ProxyPreset string

const (
	// ProxyOff disables managed egress for the session.
	ProxyOff ProxyPreset = "off"
	// ProxySmart lets the gateway pick and escalate the egress per host.
	ProxySmart ProxyPreset = "smart"
)

func (ProxyPreset) isProxySpec() {}

// ProxyCountry is the shorthand for `proxy: "us"` — egress from a country's
// default (residential) pool. cc is an ISO-3166-1 alpha-2 code, lowercase.
func ProxyCountry(cc string) ProxyPreset { return ProxyPreset(cc) }

// ProxyRequest is the fully-specified managed-proxy egress request. Requires
// Stealth on the session.
type ProxyRequest struct {
	// Country is an ISO-3166-1 alpha-2 code, lowercase. Defaults to "us".
	Country string `json:"country,omitempty"`
	// Tier selects the egress pool. Defaults to TierResidential.
	Tier ProxyTier `json:"tier,omitempty"`
	// ASN pins egress to a specific autonomous system (e.g. "20057" for
	// AT&T Mobility).
	ASN string `json:"asn,omitempty"`
	// Session is a sticky-session id (alphanumeric + dash, <= 32 chars). Pins
	// the egress IP for SessionDuration minutes.
	Session string `json:"session,omitempty"`
	// SessionDuration is the sticky lifetime in minutes (1-30, default 10).
	// Only meaningful together with Session.
	SessionDuration int `json:"sessionDuration,omitempty"`
	// State narrows US egress to a state (e.g. "california").
	State string `json:"state,omitempty"`
	// City narrows US egress to a city (e.g. "los_angeles").
	City string `json:"city,omitempty"`
}

func (ProxyRequest) isProxySpec() {}

// ResolvedProxyConfig is the proxy the gateway actually assigned. Present on a
// Session only when managed egress was requested.
type ResolvedProxyConfig struct {
	Server     string    `json:"server"`
	Username   string    `json:"username"`
	Password   string    `json:"password"`
	TimezoneID string    `json:"timezoneId"`
	Country    string    `json:"country"`
	Tier       ProxyTier `json:"tier,omitempty"`
}

// ProxyCountries is the response of Proxy.Countries.
type ProxyCountries struct {
	// Enabled reports whether managed proxy credentials are configured on the
	// gateway. When false, proxy requests will fail regardless of country.
	Enabled bool `json:"enabled"`
	// Countries lists the supported ISO-3166-1 alpha-2 egress countries.
	Countries []string `json:"countries"`
}

// ── storage state ────────────────────────────────────────────────────────

// Cookie is one entry of a StorageState's cookie jar.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	// SameSite is one of "Strict", "Lax", "None".
	SameSite string `json:"sameSite,omitempty"`
}

// LocalStorageEntry is a single localStorage key/value pair.
type LocalStorageEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Origin carries an origin's localStorage contents.
type Origin struct {
	Origin       string              `json:"origin"`
	LocalStorage []LocalStorageEntry `json:"localStorage,omitempty"`
}

// StorageState is a browser profile's persisted cookies + localStorage — the
// same shape Playwright's `context.storageState()` produces.
//
// Unknown top-level keys are preserved in Extra so that a
// Sessions.Create → Profiles.Save round-trip never silently drops fields the
// gateway added.
type StorageState struct {
	Cookies []Cookie `json:"cookies,omitempty"`
	Origins []Origin `json:"origins,omitempty"`
	// Extra holds any top-level keys other than cookies/origins.
	Extra map[string]json.RawMessage `json:"-"`
}

// MarshalJSON folds Extra back into the top-level object.
func (s StorageState) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(s.Extra)+2)
	for k, v := range s.Extra {
		out[k] = v
	}
	if s.Cookies != nil {
		b, err := json.Marshal(s.Cookies)
		if err != nil {
			return nil, err
		}
		out["cookies"] = b
	}
	if s.Origins != nil {
		b, err := json.Marshal(s.Origins)
		if err != nil {
			return nil, err
		}
		out["origins"] = b
	}
	return json.Marshal(out)
}

// UnmarshalJSON splits the known keys out and retains the rest in Extra.
func (s *StorageState) UnmarshalJSON(raw []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	s.Cookies, s.Origins, s.Extra = nil, nil, nil
	if v, ok := m["cookies"]; ok {
		if err := json.Unmarshal(v, &s.Cookies); err != nil {
			return err
		}
		delete(m, "cookies")
	}
	if v, ok := m["origins"]; ok {
		if err := json.Unmarshal(v, &s.Origins); err != nil {
			return err
		}
		delete(m, "origins")
	}
	if len(m) > 0 {
		s.Extra = m
	}
	return nil
}

// ── sessions ─────────────────────────────────────────────────────────────

// CreateSessionOptions configure Sessions.Create. The zero value is valid and
// sends no body at all.
type CreateSessionOptions struct {
	// ProfileID attaches a stored profile (cookies + localStorage) and makes
	// the created Session carry its StorageState.
	ProfileID string
	// Recording enables session recording. Off by default.
	Recording bool
	// Stealth enables the runtime stealth shim. Off by default.
	Stealth bool
	// Captcha enables managed captcha solving. Requires Stealth.
	Captcha bool
	// WebBotAuth opts in to Cloudflare Web Bot Auth — every outbound HTTP
	// request is signed with an Ed25519 key registered to Solari's verified
	// bot directory. Independent of Stealth; silently inert when the acquired
	// slot has no signing key configured.
	WebBotAuth bool
	// Proxy requests managed egress. Requires Stealth. Accepts a ProxyPreset
	// (ProxyCountry("gb"), ProxySmart, ProxyOff) or a ProxyRequest.
	Proxy ProxySpec
}

// Session is a live remote-browser session.
type Session struct {
	// ID is the composite session id used by every other Sessions method.
	ID string
	// WSEndpoint is the upstream Playwright wire-protocol endpoint. Go has no
	// Playwright client — see CDPEndpoint / Connect.
	WSEndpoint string
	// CDPEndpoint is the upstream raw CDP endpoint. Pass it to Connect (or to
	// chromedp.NewRemoteAllocator directly).
	CDPEndpoint string
	// ExpiresAt is the plan-tier deadline (ISO 8601 UTC); the session
	// auto-releases at this point.
	ExpiresAt string
	// StorageState is the attached profile's contents. It is nil both when no
	// profile was attached and when the attached profile is empty — use
	// StorageStateAttached to tell those apart.
	StorageState *StorageState
	// StorageStateAttached reports whether a profile was attached to this
	// session. When true and StorageState is nil, the profile exists but is
	// empty.
	StorageStateAttached bool
	// Proxy is the resolved egress. Non-nil only when a managed proxy was
	// requested.
	Proxy *ResolvedProxyConfig
}

// SessionView is the gateway's current view of a session (Sessions.Get). The
// gateway proxies this straight from the pool host, so the payload is
// pool-versioned: the commonly-present fields are lifted out and the full body
// is retained in Raw.
type SessionView struct {
	ID        string `json:"id,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Status    string `json:"status,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	// Raw is the complete response body as received.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes the known fields and retains the whole body in Raw.
func (v *SessionView) UnmarshalJSON(raw []byte) error {
	type alias SessionView
	var a alias
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	*v = SessionView(a)
	v.Raw = append(json.RawMessage(nil), raw...)
	return nil
}

// ReplayURL is a presigned link to a session's recording.
type ReplayURL struct {
	URL              string `json:"url"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
	// ContentEncoding of the object at URL. Defaults to "gzip".
	ContentEncoding string `json:"contentEncoding"`
}

// ── profiles ─────────────────────────────────────────────────────────────

// Profile is a stored browser profile.
type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SaveResult is the outcome of Profiles.Save.
type SaveResult struct {
	Version   int `json:"version"`
	SizeBytes int `json:"sizeBytes"`
}

// ── wire-only shapes ─────────────────────────────────────────────────────

// createSessionResponse is the raw POST /sessions body.
type createSessionResponse struct {
	SessionID       string               `json:"sessionId"`
	WSEndpoint      string               `json:"wsEndpoint"`
	CDPEndpoint     string               `json:"cdpEndpoint"`
	ExpiresAt       string               `json:"expiresAt"`
	StorageStateURL *storageStateURL     `json:"storageStateUrl"`
	StorageState    *StorageState        `json:"storageState"`
	Proxy           *ResolvedProxyConfig `json:"proxy"`
}

// storageStateURL is the presigned pointer to a profile's storage state.
type storageStateURL struct {
	URL              string `json:"url"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
}
