# solari-browser-go

Go language binding for the **Solari Browser** platform: create, inspect, and
release managed remote-browser sessions, and manage the stored profiles they
attach. It behaves identically on the wire to the reference TypeScript SDK
(`@solarisdk/browser`, `sdk/src/index.ts`).

```
go get github.com/solari-sdk/solari-browser-go
```

Module path: `github.com/solari-sdk/solari-browser-go`, package `solari`.
Requires Go 1.23+ and `github.com/chromedp/chromedp`.

> Not to be confused with `github.com/solari-sdk/solari-sandbox-go` — that is
> the **desktop/sandbox** SDK (microVMs, commands, files, code). This one is the
> **browser** platform.

## Scope: control plane + `Connect`

This SDK is the REST control plane plus a `Connect` helper. The TypeScript SDK's
`launch()` returns a live **Playwright** `Browser` — Playwright has no Go client,
so that is deliberately not ported.

Instead, `Sessions.Create` hands back the session's **raw CDP endpoint**, and you
drive the browser with [chromedp](https://github.com/chromedp/chromedp). Both
endpoints are returned exactly as the gateway issued them — unlike the Node SDK,
nothing is rewritten to a loopback proxy.

| Field | Use |
|---|---|
| `Session.CDPEndpoint` | **What you want.** Raw CDP — `Connect`, or chromedp / any CDP client. |
| `Session.WSEndpoint` | Playwright wire protocol. No Go client exists; returned for completeness. |

## Design

- **Context-first.** Every network method takes `ctx context.Context` first.
- **Errors as typed values.** Every failure is a `*SolariError` carrying `Status`
  (HTTP status, or 0 for transport errors) and `Code` (the gateway's error code).
  Match with `errors.As`, or the `IsCode` shorthand.
- **Nothing is read from the environment.** Pass the API key and any overrides
  explicitly to `NewClient`.
- **Retries are conservative.** Each request is tried up to `MaxAttempts` times
  (default 2) with a **fixed** `BackoffMs` delay (default 500ms). Only transport
  errors and HTTP 502/503/504 are retried — notably *not* 429, where retrying
  cannot help.

## Example: create a session → drive it → release

```go
package main

import (
	"context"
	"log"

	"github.com/chromedp/chromedp"
	solari "github.com/solari-sdk/solari-browser-go"
)

func main() {
	ctx := context.Background()

	client, err := solari.NewClient(solari.ClientOptions{
		APIKey: "slr_live_<id>_<secret>",
	})
	if err != nil {
		log.Fatal(err)
	}

	session, err := client.Sessions.Create(ctx, solari.CreateSessionOptions{
		Stealth: true,
		Proxy:   solari.ProxyCountry("us"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Sessions.Release(context.Background(), session.ID)

	browserCtx, cancel, err := solari.Connect(ctx, session)
	if err != nil {
		log.Fatal(err)
	}
	defer cancel()

	var title string
	if err := chromedp.Run(browserCtx,
		chromedp.Navigate("https://example.com"),
		chromedp.Title(&title),
	); err != nil {
		log.Fatal(err)
	}
	log.Println("title:", title)
}
```

`Connect` dials nothing itself — the connection is established on the first
`chromedp.Run`, and failures surface there. `cancel()` detaches the client; it
does **not** release the session, which keeps running until `Sessions.Release`
or its `ExpiresAt` deadline.

## Sessions

```go
session, err := client.Sessions.Create(ctx, solari.CreateSessionOptions{
	ProfileID:  "prof_1",   // attach a stored profile
	Recording:  true,       // record the session for replay
	Stealth:    true,       // runtime stealth shim
	Captcha:    true,       // managed captcha solving (requires Stealth)
	WebBotAuth: true,       // sign requests for Cloudflare Web Bot Auth
	Proxy:      solari.ProxySmart,
})

view, err := client.Sessions.Get(ctx, session.ID)          // current view
err = client.Sessions.Release(ctx, session.ID)             // idempotent
link, err := client.Sessions.ReplayURL(ctx, session.ID)    // presigned replay link
raw, err := client.Sessions.DownloadReplay(ctx, session.ID) // replay bytes (NDJSON)
```

The zero `CreateSessionOptions` is valid and requests a plain session. `Release`
and `Profiles.Delete` tolerate a 404, so they are safe to call twice.

Replays are available ~1-3s after `Release`, and only for sessions created with
`Recording: true`. `DownloadReplay` returns the bytes exactly as stored — gzipped
by default, per `ReplayURL.ContentEncoding`.

### Proxy egress

`CreateSessionOptions.Proxy` takes either a preset or a full request. Managed
egress requires `Stealth: true`.

```go
Proxy: solari.ProxyCountry("gb")  // country shorthand
Proxy: solari.ProxySmart          // gateway picks + escalates per host
Proxy: solari.ProxyOff            // no managed egress

Proxy: solari.ProxyRequest{
	Country:         "us",
	Tier:            solari.TierMobile, // or TierResidential (default) / TierStatic
	ASN:             "21928",           // pin the carrier (T-Mobile)
	Session:         "warm-1",          // sticky egress IP
	SessionDuration: 10,                // minutes (1-30)
	State:           "california",      // US-only
	City:            "los_angeles",     // US-only
}
```

The resolved egress comes back on `Session.Proxy` (`*ResolvedProxyConfig`), and
is nil when no managed proxy was requested. To check what a gateway supports
before asking:

```go
countries, err := client.Proxy.Countries(ctx)
// countries.Enabled, countries.Countries → ["us", "gb", …]
```

## Profiles

A profile is a persisted cookie jar + localStorage — the same shape as
Playwright's `storageState`.

```go
prof, err := client.Profiles.Create(ctx, "shopper")
profiles, err := client.Profiles.List(ctx)
res, err := client.Profiles.Save(ctx, prof.ID, state) // → res.Version, res.SizeBytes
err = client.Profiles.Delete(ctx, prof.ID)            // idempotent
```

Attaching a profile populates the session's storage state. Because Go cannot
express TypeScript's `undefined` vs `null`, the tri-state is carried by two
fields:

| `StorageStateAttached` | `StorageState` | Meaning |
|---|---|---|
| `false` | `nil` | No profile was attached. |
| `true` | `nil` | The profile exists but is empty. |
| `true` | non-nil | The profile's contents. |

`StorageState` preserves top-level keys it does not model (in `Extra`), so a
`Sessions.Create` → `Profiles.Save` round trip never silently drops fields.

## Errors

```go
session, err := client.Sessions.Create(ctx, solari.CreateSessionOptions{Stealth: true})
if err != nil {
	var serr *solari.SolariError
	if errors.As(err, &serr) {
		log.Printf("status=%d code=%s: %s", serr.Status, serr.Code, serr.Message)
	}
	// or, more directly:
	if solari.IsCode(err, solari.CodeConcurrencyLimitExceeded) {
		// at the org's live-session cap — back off and retry later
	}
}
```

Known codes: `CodeFeatureRequiresPlan`, `CodeConcurrencyLimitExceeded`,
`CodePlanLimitExceeded`, `CodeBrowserUnhealthy`. The gateway may add more, so
compare rather than switching exhaustively.

## Client options

| Option | Default | Notes |
|---|---|---|
| `APIKey` | — | Required. Format `slr_live_<id>_<secret>`. |
| `Region` | `RegionUSWest` | More regions coming soon. Ignored when `BaseURL` is set. |
| `BaseURL` | region URL | Override for staging / self-hosted gateways. |
| `HTTPClient` | — | Overrides the default client; `TimeoutMs` is then ignored. |
| `MaxAttempts` | 2 | Tries per request, counting the first. |
| `BackoffMs` | 500 | **Fixed** delay between attempts. Pointer, so 0 is distinguishable from unset. |
| `TimeoutMs` | 90000 | Per-attempt bound. |

`Client` is safe for concurrent use.

## Tests

The suite is fully offline — every case runs against `httptest` servers and
asserts request shapes and response parsing. No API key, gateway, or browser
needed.

```bash
go build ./... && go test ./...
```
