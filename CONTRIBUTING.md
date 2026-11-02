# Contributing

Thanks for helping! Bug reports, ideas and pull requests are welcome.

## Before you start

- For larger changes, open an issue first so we can agree on the approach.
- Read [AGENTS.md](AGENTS.md): it describes the layout and the rules the
  code follows (non-interactive git, confirmation before anything that
  publishes, sanitized remote text, allow-listed AI steps).

## Development

```sh
make build        # ./gl-repos
make ci           # the CI checks (gofmt, vet, lint, actionlint, goreleaser check, race tests, govulncheck, gitleaks, build)
make e2e          # drive the real TUI in a pseudo-terminal (needs uv, git, lazygit)
make snapshot     # release build into dist/ without publishing
```

Requirements: Go (the version in `go.mod`; the `go` command fetches it
automatically), git; for `make e2e` also [uv](https://docs.astral.sh/uv/)
and lazygit.

## Pull requests

- Keep changes focused; one topic per request.
- Add tests: unit tests for logic, an e2e check (`test/e2e/test_*.py`) for
  every new key or screen.
- Update the docs and CHANGELOG.md (under "Unreleased") when behaviour
  changes: the key table (`docs/usage.md`, and the short one in
  README.md), the config reference (`docs/configuration.md`), and the
  Quick start (README.md, `docs/getting-started.md`) and
  `docs/troubleshooting.md`, which quote messages as gl-repos prints them.
  README.md is the short overview; the details live in `docs/`. `make demo` records the
  README screencast again from made-up repositories (needs Go, git,
  python3 and [VHS](https://github.com/charmbracelet/vhs)).
- Never put real hostnames, group/project paths, usernames or tokens in
  code, tests or docs; use `gitlab.example.com`, `example.edu`, `acme/…`
  and the like. The README badges are the one exception: they link the
  project's own GitHub Actions runs and releases.
- Commit messages: a short imperative subject ("Add …", "Fix …"), body
  explaining why.

## Releases (maintainers)

Update CHANGELOG.md, then tag: `git tag v0.1.0 && git push origin v0.1.0`.
CI builds, signs and publishes the release (see [docs/development.md](docs/development.md#ci-and-releases)).
