# Changelog

All notable changes to this project are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the
project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-11-01

First public release.

### Added

- Repository dashboard: every git repository under a folder with its
  status, ahead/behind counts and the comparison with the default branch;
  `fetch_on_start` reads them fresh, sorted by name, attention or recency.
- Fetch, pull and push per repository, a commit graph (`l`), and lazygit,
  a shell or a coding agent (`[[agent]]`: claude, codex, kimi by default)
  opened in the repository.
- Profiles for GitLab (`glab`) and GitHub (`gh`): search and clone
  projects, clone whole GitLab groups, create (`n`) and publish (`u`)
  repositories with a visibility menu, group autocomplete and the
  creation of missing subgroups, every command shown before `y`.
  `default_group` prefills the owner; `exclude` hides groups everywhere,
  also from the cache and from pasted URLs.
- Local AI assistant (Ollama, `gemma4:e2b` by default): a request in plain
  words becomes a git plan that is checked against a fresh status and
  runs only after `y`; an empty request commits and never touches the
  remote. A memory guard refuses models that do not fit in free RAM.
- Configuration in `config.toml` (`-config`, `GL_REPOS_CONFIG`) with
  `config.example.toml` listing every setting; validation names the
  file and key, explains misplaced keys and `[profile]` written with
  single brackets. `gl-repos -init` writes the commented example config.
- `gl-repos -doctor` checks the setup read-only, one line per check with
  a fix: config, git (2.31 or newer), tool, agents, each profile's CLI,
  host, login and token scopes, where the token is kept, ssh to each
  host, and Ollama with the configured model.
- `gl-repos -list` prints the dashboard once, for scripts.
- Security by design: URL credentials are hidden wherever text is shown
  and refused as arguments; clone URLs are rebuilt from their parsed
  parts; a repository's own git config is checked against an allowlist
  of what a normal clone or user writes, and one with any other key (a
  program, a proxy or TLS setting, a URL rewrite, a pushurl or another
  push or fetch setting that changes what a bare push or fetch sends,
  writes or deletes) is marked `!`, with the key named as `network:`,
  `push:`, `fetch:` or `config:`, and asks before anything runs there;
  fetch, pull and push never recurse into submodules;
  caches are private files in a private directory, never read or
  written through a symlink; every glab and gh call's output is capped
  at 128 MB.
- Releases for Linux/macOS × amd64/arm64 as archives, `.deb` and `.rpm`,
  with an SBOM and checksums signed with cosign (keyless).

[Unreleased]: https://github.com/ansgomez/gl-repos/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/ansgomez/gl-repos/releases/tag/v0.1.0
