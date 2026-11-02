# Development

Checks, CI, releases and tests. See also [CONTRIBUTING.md](../CONTRIBUTING.md). Back to the [README](../README.md).

## Checks

`make ci` runs the checks CI runs: gofmt, `go vet`, golangci-lint,
actionlint, the GoReleaser config check, tests with the race detector,
govulncheck, gitleaks over the git history, and a build. CI additionally
runs `make cover` (the tests with a coverage report), `make e2e`, `make
snapshot` (a release build into `dist/` without publishing) and CodeQL.
Tools run at pinned versions via `go run`, so nothing needs to be
installed.

## CI and releases

GitHub Actions (`.github/workflows`) runs the Makefile targets: lint,
tests on Linux and macOS, coverage, end-to-end tests, govulncheck, gitleaks, and a
release snapshot, plus CodeQL code scanning. Dependencies are kept
current by Dependabot.

Pushing a `v*` tag (e.g. `git tag v0.1.0 && git push origin v0.1.0`)
publishes a GitHub release with GoReleaser: binaries for
Linux/macOS × amd64/arm64, `.deb`/`.rpm`, checksums signed with cosign
(keyless), SBOMs, and a build provenance attestation. A
Homebrew tap (macOS and Linux) can be enabled in `.goreleaser.yaml`.

See [CONTRIBUTING.md](../CONTRIBUTING.md) and [SECURITY.md](../SECURITY.md).

## Testing

```sh
make vet test                   # unit tests (no Ollama needed)
test/e2e/run.sh                 # drive the real TUI in a pseudo-terminal (needs uv, lazygit)
test/e2e/run.sh --ai            # + AI flows against the local Ollama (gemma4:e2b, gemma3:1b;
                                #   E2E_AI_MODEL=<model> for another)
go test -tags live -run Live -v ./internal/ai/   # plan quality eval, real model
```

The live eval runs each scenario several times (`GL_REPOS_LIVE_RUNS`,
`GL_REPOS_LIVE_MODEL`) and fails unless every plan is acceptable: correct
ops and fields, nothing extra, no steps for destructive requests or a clean
tree. Run it after touching the prompt or schema in `internal/ai`.
