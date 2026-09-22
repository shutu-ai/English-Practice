# CI quality gates

The repository's minimal workflow runs on every push and pull request with a
fixed Go 1.25 toolchain:

1. `go test -mod=mod ./...`
2. `go vet ./...`
3. `go build -mod=mod ./...`

The runner uses the Go module cache. Tests use deterministic local evaluators
or in-process mock HTTP providers; no real API key, provider, user database,
generated executable, log, or secret is committed or required.

The frontend bundle is checked in and served directly. A frontend CI job is
currently omitted because there is no committed npm lockfile, so dependency
resolution would make the core gate network-sensitive. Add a locked frontend
job when the package manager lockfile is adopted.
