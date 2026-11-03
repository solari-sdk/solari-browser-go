# Changelog

## v0.6.0

- `Profiles.Edit` opens the profile editor on an existing profile and returns an
  `*Editor` with `URL`, `ExpiresAt`, `Status`, `SaveAndClose`, `Exit` and
  `WaitForSave`.
- `Profile.Version` is now populated from `Profiles.List`.
