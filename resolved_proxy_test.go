package solari

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// REGRESSION GUARD for the shipped fix. ResolvedProxyConfig used to carry
// Server/Username/Password. The gateway stopped sending them (they disclose the
// proxy vendor's account), so the bindings parsed empty strings forever — a
// contract that was not just dead but actively misleading: a caller could read
// sess.Proxy.Server, get "", and conclude no proxy was applied.
//
// The existing session tests assert the fields that ARE present, so re-adding a
// credential field breaks nothing. This asserts the ABSENCE, which is the whole
// content of the fix.
func TestResolvedProxyConfigCarriesNoCredentials(t *testing.T) {
	rt := reflect.TypeOf(ResolvedProxyConfig{})

	var names, tags []string
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		names = append(names, f.Name)
		tags = append(tags, strings.Split(f.Tag.Get("json"), ",")[0])
	}

	// Exact set, not a denylist: a field named `Creds` or `Upstream` is the same
	// disclosure under a name no denylist would have predicted.
	wantNames := []string{"TimezoneID", "Country", "Tier"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("ResolvedProxyConfig fields = %v, want exactly %v.\n"+
			"This type is a COARSE CONFIRMATION: egress is applied server-side, so a "+
			"caller never dials the proxy and must never receive its address or account.",
			names, wantNames)
	}
	wantTags := []string{"timezoneId", "country", "tier"}
	if !reflect.DeepEqual(tags, wantTags) {
		t.Fatalf("ResolvedProxyConfig json tags = %v, want exactly %v", tags, wantTags)
	}
}

// A gateway that still emitted the old keys must not resurrect them through the
// decoder — and, more importantly, must not make the struct lie by half-filling.
func TestResolvedProxyConfigIgnoresLegacyCredentialKeys(t *testing.T) {
	const wire = `{
		"timezoneId": "America/Los_Angeles",
		"country": "us",
		"tier": "mobile",
		"server": "http://resi.vendor.example:8000",
		"username": "acct-12345",
		"password": "hunter2"
	}`

	var got ResolvedProxyConfig
	if err := json.Unmarshal([]byte(wire), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TimezoneID != "America/Los_Angeles" || got.Country != "us" || got.Tier != TierMobile {
		t.Fatalf("resolved proxy = %+v", got)
	}

	// Round-trip: nothing a caller can serialise carries the vendor account.
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{"resi.vendor.example", "acct-12345", "hunter2", "server", "username", "password"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("re-encoded resolved proxy leaks %q: %s", leak, b)
		}
	}
}
