---
about: a single http.Client.Timeout shared across a small metadata request and a large download bounds the whole response body read, not just connection setup, so it silently kills a slow-but-legitimate download
saw:
  - internal/update/github.go
  - internal/update/install.go
  - internal/update/update.go
  - tests/update_test.go
---

`internal/update`'s `defaultClient()` originally returned
`&http.Client{Timeout: 10 * time.Second}`, and both `Check` (a small GitHub
API JSON request) and `Apply` (downloading a multi-megabyte release archive
plus checksums.txt) shared that one client. `http.Client.Timeout` is not a
connect-only or headers-only timeout — it bounds the entire request/response
cycle including reading the body — so any download slower than roughly
`payload_size / Timeout` (≈400KB/s for a 4MB archive at 10s) fails with
`context deadline exceeded (Client.Timeout ... while reading body)`, even
though nothing is actually broken.

A value sized for a fast JSON response is not automatically safe for a
payload that can be orders of magnitude larger. `Check` still uses
`defaultClient()`'s 10s `Timeout` (untouched — a caller-supplied context,
e.g. `cmd/gt/main.go`'s 3s/15s wraps, already bounds it tighter in
production). `Apply` now uses a client with **no** `Client.Timeout` at all
(`defaultDownloadClient()`, `install.go`) and instead scopes each download
request's own `context.WithTimeout` via `Options.DownloadTimeout` (default 5
minutes, `update.go`), decoupling the two budgets entirely.

Also confirmed while fixing this: `Options.withDefaults()` can no longer
default `HTTPClient` itself, since which default client is correct (10s vs.
no-Timeout) depends on which exported function (`Check` vs. `Apply`) is
calling it — that decision moved into `Check`/`Apply` directly rather than
staying in the shared `withDefaults()` method.
