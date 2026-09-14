# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Keyset (cursor) pagination via `Keyset[T]`, with forward and backward paging.
- Offset pagination via `Offset[T]` for page-number interfaces.
- `Allowlist`, declaring in Go which sort orders a client may request.
- `Validate[T]`, a boot-time check of the allowlist against the live schema.
- Typed errors carrying a stable `Code`, matchable with `errors.Is`.
- `ginx`: query-string binding and a single 400 response shape.
- Integration tests against real PostgreSQL, including concurrent writes during
  paging and a query-plan assertion.
