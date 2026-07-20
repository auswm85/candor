<!-- Thanks for contributing to candor! Keep it focused and on-mission. -->

## What & why

<!-- What does this change and why? Link any related issue: Fixes #123 -->

## Checklist

- [ ] `go build ./...` and `go test -race -count=1 ./...` pass
- [ ] `go vet ./...`, `gofmt -l .` (no output), and `golangci-lint run` are clean
- [ ] Added/updated tests for the change
- [ ] Preserves the local-first and fail-open guarantees (no cloud/telemetry; the
      proxy still can't affect the user's real request)
- [ ] Docs/README updated if behavior or flags changed
