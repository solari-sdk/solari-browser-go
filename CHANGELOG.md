# Changelog

## v0.6.1

- `Sessions.Launch` with the health probe works: the returned `Browser` context is no longer already cancelled. The probe's first `chromedp.Run` used to run on a child context and cancel it on return, which tore down the browser chromedp had allocated on it.
- The probe's connect step now has its own bound (15 s, or `ProbeTimeoutMs` when set); the 2 s default applies to the evaluate step only. Connecting over a cold link took longer than 2 s, so the default probe failed.

## v0.6.0

- `Profiles.Edit` opens the profile editor on an existing profile and returns an
  `*Editor` with `URL`, `ExpiresAt`, `Status`, `SaveAndClose`, `Exit` and
  `WaitForSave`.
- `Profile.Version` is now populated from `Profiles.List`.
