# Changelog

## v0.6.3

- **The gateway's error body no longer carries the session id into error messages.** `GET /sessions/<id>/replay-url` answered `404 ReplayPending` with the full `sessionId` in its body and the error message appended that body. The id is the credential inside the ws/cdp URLs. The body is now redacted (the id of the request path, in any spelling, and every `"sessionId"` field cut to 8 characters); `Code` is still parsed from the raw body.
- **`Connect` / `ConnectCDP`: the first `chromedp.Run` no longer prints the CDP URL on a failed connect.** chromedp's `could not dial "wss://…/cdp/<id>"` is now `Solari: could not connect to the browser: could not dial "wss://host/…"`, a `*SolariError` that keeps `errors.Is(err, context.DeadlineExceeded)` and friends.

## v0.6.2

- **Errors no longer print the session id.** The session id is the credential inside the ws/cdp URLs, but `Sessions.Get` / `Release` / `ReplayURL` errors, the retry-exhausted message and a wrapped `*url.Error` printed it in full (`Solari GET /sessions/<id> failed`). They now carry the first 8 characters of the id only. A `Launch` health-probe failure no longer prints the CDP URL it dialled (`could not dial "ws://…/cdp/<id>"`) and keeps `errors.Is` working on the cause; a failed replay or storage-state download no longer prints the signed URL; "unexpected session response" no longer dumps the response (it holds the id and the endpoints).

## v0.6.1

- `Sessions.Launch` with the health probe works: the returned `Browser` context is no longer already cancelled. The probe's first `chromedp.Run` used to run on a child context and cancel it on return, which tore down the browser chromedp had allocated on it.
- The probe's connect step now has its own bound (15 s, or `ProbeTimeoutMs` when set); the 2 s default applies to the evaluate step only. Connecting over a cold link took longer than 2 s, so the default probe failed.

## v0.6.0

- `Profiles.Edit` opens the profile editor on an existing profile and returns an
  `*Editor` with `URL`, `ExpiresAt`, `Status`, `SaveAndClose`, `Exit` and
  `WaitForSave`.
- `Profile.Version` is now populated from `Profiles.List`.
