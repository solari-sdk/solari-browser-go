package solari

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestProxyCountries(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"countries":["us","gb","de","jp"]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	res, err := c.Proxy.Countries(context.Background())
	if err != nil {
		t.Fatalf("countries: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/proxy/countries" {
		t.Errorf("request = %s %s, want GET /proxy/countries", gotMethod, gotPath)
	}
	if !res.Enabled {
		t.Error("Enabled = false, want true")
	}
	if want := []string{"us", "gb", "de", "jp"}; !reflect.DeepEqual(res.Countries, want) {
		t.Errorf("Countries = %v, want %v", res.Countries, want)
	}
}

// A gateway without proxy credentials reports enabled:false.
func TestProxyCountriesDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":false,"countries":[]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	res, err := c.Proxy.Countries(context.Background())
	if err != nil {
		t.Fatalf("countries: %v", err)
	}
	if res.Enabled {
		t.Error("Enabled = true, want false")
	}
	if len(res.Countries) != 0 {
		t.Errorf("Countries = %v, want empty", res.Countries)
	}
}
