package solari

import (
	"errors"
	"testing"

	"github.com/chromedp/cdproto/network"
)

func TestIsTransientRetryPolicy(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"transport (status 0)", &SolariError{Status: 0}, true},
		{"500", &SolariError{Status: 500}, true},
		{"503", &SolariError{Status: 503}, true},
		{"400 not retryable", &SolariError{Status: 400}, false},
		{"404 not retryable", &SolariError{Status: 404}, false},
		{"429 not retryable", &SolariError{Status: 429}, false},
		{"non-solari error retried", errors.New("boom"), true},
	}
	for _, c := range cases {
		if got := isTransient(c.err); got != c.want {
			t.Errorf("%s: isTransient = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMapSameSite(t *testing.T) {
	if mapSameSite("Strict") != network.CookieSameSiteStrict {
		t.Error("Strict")
	}
	if mapSameSite("Lax") != network.CookieSameSiteLax {
		t.Error("Lax")
	}
	if mapSameSite("None") != network.CookieSameSiteNone {
		t.Error("None")
	}
	if mapSameSite("") != "" || mapSameSite("weird") != "" {
		t.Error("empty/unknown should map to empty (default)")
	}
}
