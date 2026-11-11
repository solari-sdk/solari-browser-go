package solari

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// fakeBrowser is a CDP endpoint that answers the browser-level commands chromedp's first Run sends, records every method, and
// reports `pages` page targets from Target.getTargets (it announces none on Target.setDiscoverTargets, like the pooled fast-pool
// sessions that left the first Run waiting for ever). pages < 0 makes Target.getTargets answer with an error. createURLs returns the
// url of every Target.createTarget received.
func fakeBrowser(t *testing.T, pages int) (endpoint string, methods func() []string, createURLs func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen, urls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			msg, op, err := wsutil.ReadClientData(conn)
			if err != nil {
				return
			}
			var req struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
				Params struct {
					URL string `json:"url"`
				} `json:"params"`
			}
			if json.Unmarshal(msg, &req) != nil {
				continue
			}
			mu.Lock()
			seen = append(seen, req.Method)
			if req.Method == "Target.createTarget" {
				urls = append(urls, req.Params.URL)
			}
			mu.Unlock()
			result := `{}`
			switch req.Method {
			case "Target.getTargets":
				if pages < 0 {
					_ = wsutil.WriteServerMessage(conn, op, []byte(`{"id":`+itoa(req.ID)+`,"error":{"code":-32000,"message":"no targets for you"}}`))
					continue
				}
				infos := []string{}
				for i := 0; i < pages; i++ {
					infos = append(infos, `{"targetId":"P1","type":"page","title":"","url":"about:blank","attached":false,"canAccessOpener":false}`)
				}
				result = `{"targetInfos":[` + strings.Join(infos, ",") + `]}`
			case "Target.createTarget":
				result = `{"targetId":"NEW"}`
			}
			_ = wsutil.WriteServerMessage(conn, op, []byte(`{"id":`+itoa(req.ID)+`,"result":`+result+`}`))
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/cdp/x", func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), seen...)
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), urls...)
		}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func runFirst(t *testing.T, endpoint string) {
	t.Helper()
	parent, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	bctx, cancel, err := ConnectCDP(parent, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	_ = chromedp.Run(bctx) // the fake never completes the attach; only the methods sent before that matter
}

func contains(ms []string, m string) bool {
	for _, x := range ms {
		if x == m {
			return true
		}
	}
	return false
}

// A browser that reports no page target: chromedp's first Run waited for one for ever. The SDK opens a page.
func TestConnectOpensAPageWhenTheBrowserHasNone(t *testing.T) {
	endpoint, methods, urls := fakeBrowser(t, 0)
	runFirst(t, endpoint)
	if ms := methods(); !contains(ms, "Target.getTargets") || !contains(ms, "Target.createTarget") {
		t.Fatalf("a browser with no page must get Target.createTarget after Target.getTargets; methods sent: %v", ms)
	}
	if u := urls(); len(u) != 1 || u[0] != "about:blank" {
		t.Fatalf("the page opened must be about:blank, got %v", u)
	}
}

// If the SDK cannot learn the browser's targets it does nothing extra (chromedp then behaves as it did before): no page is
// opened on a guess.
func TestConnectDoesNothingWhenGetTargetsFails(t *testing.T) {
	endpoint, methods, _ := fakeBrowser(t, -1)
	runFirst(t, endpoint)
	ms := methods()
	if !contains(ms, "Target.getTargets") {
		t.Fatalf("the SDK did not ask for the targets: %v", ms)
	}
	if contains(ms, "Target.createTarget") {
		t.Fatalf("Target.getTargets failed; Target.createTarget must not be sent: %v", ms)
	}
}

// A browser that already has a page is left alone (no stray extra tab).
func TestConnectLeavesABrowserThatHasAPage(t *testing.T) {
	endpoint, methods, _ := fakeBrowser(t, 1)
	runFirst(t, endpoint)
	ms := methods()
	if !contains(ms, "Target.getTargets") {
		t.Fatalf("the SDK did not look for a page: %v", ms)
	}
	if contains(ms, "Target.createTarget") {
		t.Fatalf("a page already existed; Target.createTarget must not be sent: %v", ms)
	}
}
