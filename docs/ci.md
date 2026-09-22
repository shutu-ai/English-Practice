# CI quality gates

The repository's minimal workflow runs on every push and pull request with a
fixed Go 1.25 toolchain:

1. `go test -mod=mod ./...`
2. `go vet ./...`
3. `go build -mod=mod ./...`

The runner uses the Go module cache. Tests use deterministic local evaluators
or in-process mock HTTP providers; no real API key, provider, user database,
generated executable, log, or secret is committed or required.

The frontend job runs the mock SpeechSynthesis tests, `node --check` static
checks, TypeScript typecheck, and `npm run build`. The checked-in `web/dist` bundle is served by the
Go app. There is no committed npm lockfile yet, so dependency resolution uses
the declared package ranges; adopt a lockfile when deterministic frontend
dependency resolution is required.
