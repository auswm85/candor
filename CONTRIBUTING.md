# Contributing to candor

Thanks for your interest. candor is deliberately a small, focused, local-first
tool — a single Go binary that tracks live LLM spend via a transparent proxy.
Contributions that keep it simple and on-mission are very welcome.

## Ground rules

- **Stay local-first.** No cloud services, telemetry, accounts, or anything that
  sends usage data off the machine. See `SECURITY.md` for the trust model.
- **Never break the request.** The proxy is a passive observer. Usage tapping is
  fail-open and panic-isolated so it can never affect the user's real LLM call —
  keep it that way.
- **Discuss big changes first.** For anything beyond a bug fix or small
  improvement, please open an issue before writing a lot of code, so we can agree
  the direction fits the project.

## Development setup

Requires **Go 1.26+**. No CGO, no external services.

```sh
go build ./cmd/candor            # build the binary
go run  ./cmd/candor             # open the dashboard (hosts the proxy)
```

## Before you open a PR

Please make sure the full gate passes locally — CI runs the same on
Linux/macOS/Windows:

```sh
go build ./...
go test -race -count=1 ./...     # all tests, race detector on
go vet ./...
gofmt -l .                       # must print nothing
golangci-lint run                # config in .golangci.yml (needs golangci-lint v2)
```

- Add tests for new behavior; keep coverage from regressing.
- Match the surrounding code's style and comment density.
- Keep commits focused; a clear one-line summary is enough (no required trailer).

## Reporting bugs / requesting features

Use the issue templates. For bugs, include your OS, the provider/harness
involved, and steps to reproduce. For security issues, follow `SECURITY.md`
instead of filing a public issue.

## License

By contributing, you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
