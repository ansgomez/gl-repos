# AGENTS.md

Guidelines for agents working on gl-repos.

## Layout

- `main.go` — flag parsing, `-list` mode, starts the TUI; `setup.go` —
  `-init` (writes the embedded `config.example.toml`) and `-doctor`.
- `internal/doctor` — the read-only setup checks of `-doctor`. Every
  check has a timeout and prints only sanitized text; ssh and Ollama are
  injected (`Env`), and the e2e suite uses fake `glab`/`gh`/`ssh` and a
  local fake Ollama. Never point it at real servers from tests.
- `internal/scan` — finds repositories up to a depth.
- `internal/gitx` — reads status (`git status --porcelain=v2 --branch`) and
  runs fetch/pull/push/clone by shelling out to `git`. No libgit2/go-git.
  Depends on `internal/forge` for `CloneURL`, `CheckArgs` and
  `HasCredentials` (so `forge` must never import `gitx`).
- `internal/gitx/steps.go` — allow-listed plan steps (the only things the
  AI can run) and `Describe`, the repo summary sent to the model.
- `internal/ai` — Ollama `/api/chat` client with JSON-schema output. Never
  executes anything.
- `internal/forge` — `Forge` interface with GitLab (`glab`) and GitHub
  (`gh`): project lists, search, user, create commands. Always through the
  CLIs (reuse their logins; never handle tokens). Create commands run in
  an empty temp dir, with `GITLAB_HOST`/`GH_HOST` set to the profile's
  host. Every CLI call's stdout and stderr are capped at `MaxOutput`
  (128 MB), past which the CLI is killed (`ErrTooLarge`).
- `internal/safe` — sanitizes untrusted text for the terminal and quotes
  arguments for display. Every string from git, a forge or the model goes
  through it before it is rendered, and so does every path (the root, a
  repository, a destination: `showPath` in `tui/text.go`), since a
  folder name is data too; `TestNoEscapeFromDataInAnyMode` renders every
  mode with such names.
- `internal/config` — TOML config; flags in `main.go` override it.
- `internal/tui` — Bubble Tea model (v1 API):
  - `tui.go` state, keys, background jobs and the quit lifetime
  - `view.go` rendering; `input.go` minimal text field
  - `plan.go` AI flow; `clone.go` clone box; `group.go` GitLab group clone;
    `clonejob.go` clone rows (progress, retries, cancel, `claimDir`)
  - `agent.go` agent picker; `profile.go` active profile + badge;
    `create.go` new/publish
- `test/e2e` — drives the built binary in a pseudo-terminal (pyte via
  uv) against fixture repos and fake `glab`/`gh`/`ssh`.
- `README.md` — the short overview (pitch, quick start, most-used keys,
  Documentation index); `docs/*.md` — the user docs, one topic per file
  (`getting-started`, `install`, `usage`, `profiles`, `repositories`,
  `agents-and-ai`, `configuration`, `troubleshooting`, `security`,
  `development`, `linux`), shipped in the release archives (`.goreleaser.yaml`).
- `docs/demo/` — the README screencasts (`docs/demo-dark.gif`,
  `docs/demo-light.gif`): `setup.sh`
  builds a sandbox HOME of made-up repositories with a fake `glab`,
  `ssh` and Ollama; `demo.tape` is the VHS script. `make demo` records
  it again; do that when the list or a flow it shows changes.

## Conventions

- Go, `gofmt`; keep deps minimal (charmbracelet stack only).
- All git calls go through `gitx.command` so they stay non-interactive.
  Never let git prompt: the TUI owns the terminal.
- Network/state-changing actions are opt-in per keypress; anything that
  publishes (push) asks for confirmation. Pulls are `--ff-only`.
- AI plans are always shown as exact git commands and run only on `y`.
  Add new ops to `gitx.StepOps` + `Step.Args` only if non-destructive.
  The default request (empty prompt) stays local (`ai.localOnly` drops
  push/pull/fetch); it is told by `ai.Call.Default` (`defaultRequest`
  in `plan.go`), never by comparing the request's text with
  `ai.DefaultPrompt`. Prompt changes are checked against a real model with
  `go test -tags live -run Live ./internal/ai/` (every scenario 3/3).
- The list's key help (`listHelp`) names every key, most used first; it
  wraps onto up to `listHelpLines` lines, so add new keys there.
- Ollama requests size `num_ctx` dynamically and cap `num_predict`; never
  default to a large fixed context (a 64k context on an 8B model can
  exhaust the RAM of a typical laptop). Default model `gemma4:e2b`, small
  enough for 8–16 GB machines (`gemma4:e4b` for better plans with more RAM).
- Every Ollama request must stay recoverable: memory guard before load,
  streaming watchdog (load/stall/total timeouts), `esc` cancels, unload
  on failure only for models gl-repos loaded. Keep these when changing
  `internal/ai`; the tests in `ai_test.go` cover each path.
- When testing against a real Ollama, check free memory and `/api/ps`
  first; prefer a resident model or `gemma3:1b`.
- The active profile decides which forge `c`/`n`/`u` use; it must stay
  visible (badge) and switching needs `y`. Never mix profiles in one flow.
- Shortcuts: distinct lowercase keys for new actions; no case-only pairs.
- Tests use fake `glab`/`gh` (`test/e2e/fake_*.sh`); never create or query
  projects on real servers from tests.
- Security invariants (see `docs/security.md` and SECURITY.md): sanitize remote text; forge
  project paths must pass `safePath`; a clone only removes a directory it
  created (`claimDir`); git clones only a `forge.CloneURL` (built by
  `ParseCloneURL`; URL-looking input that does not parse is refused,
  never searched; `IsURL`, `ParseCloneURL` and `HostFromURL` tell the
  forms apart with one classifier, `scpLike` in `cloneurl.go`, so never
  classify a clone source elsewhere, and the rebuilt URL is the checked
  input, never a cleaned one), `gitx.command` and `forge.cliCommand` refuse any
  argument with URL credentials (`forge.CheckArgs`), `Search` refuses
  clone sources (`IsURL`, the same classifier as the clone box) and
  credential-shaped text, and every destination folder passes
  `gitx.CheckDest`; the credential shapes are defined once, in
  `forge.edit` (`creds.go`), which `RedactURLs`, `MaskURLs` and
  `HasCredentials` share, so what is shown redacted is what is refused
  (never add a display-only or a check-only shape); which URL schemes
  have a plain user (ssh-like) and which a secret one (every other) is
  one table, `forge.plainUser`, read by `credentials` (display) and
  `urlUser` (`ParseCloneURL`), so never test a scheme name elsewhere;
  every message is redacted by `scrub` at the top of `Update` (add new
  message types there, and a new shown `Status` field to `scrubStatus`;
  `-list` redacts in `printList`), and a redacted value is never
  compared with a raw one: a value that is executed or checked later
  stays raw in the message and is refused by shape, and the one value
  that is both shown and compared, the risky config keys, is put in its
  shown form (`shownRisks`) by `scrub` and by `checkRisks` alike (never
  compare a scrubbed field with fresh data without it); a new credential
  finding becomes a fixture in `forge.credentialShapes` and the TUI's
  `secretShapes`, which every guard test and `TestNoSecretInAnyMode`
  run; project lists are cleaned in `saveCache`, not by callers; cache
  an Ollama model name is checked once, by `ai.CheckModel` (`ai.model`
  in the config, the names `/api/ps` reports before `rememberSize`
  keeps them, and `loadSizes`), and `ai.url` passes `AI.validate` (no
  user, query, fragment, `;`, `%` or `forge.HasCredentials` shape);
  a host is checked once, by `forge.ValidHost` (the config's `host`,
  `glab auth status` in `loggedInHosts`, remotes in `HostFromURL`),
  before it is stored, shown, named in a cache file or passed as
  `--hostname`, so never take a host from a CLI's output elsewhere; cache
  files are read, written and removed only through `forge.ReadCacheFile`,
  `WriteCacheFile` and `RemoveCacheFile` (`cachefile.go`: the directory is checked and opened
  once by `openCacheDir`, on a read as on a write, and every file
  operation is relative to it (`os.Root`), so nothing there is followed,
  a planted symlink is removed, a symlinked cache directory refused; use
  them for any new file gl-repos keeps there, never a path operation of
  your own); errors never
  quote a query, URL or destination; AI steps run only if their args
  equal the confirmed ones (`PlanArgs`/`RunSteps`); never strip the
  confirm step.
  `n`/`u` create only subgroups of existing groups, after a warning in
  the review; the review shows every command in full (wrapped, not cut),
  and runs exactly the plan it showed: the steps are built once when the
  review is entered (`enterReview`, `createState.steps`) and `y` runs
  that snapshot, never a rebuild (the user name, a host or the ssh/https
  choice can arrive meanwhile).
  Listing never runs a repository's own config programs: read-only git
  calls go through `gitx.readOnly` (its filter drivers off), `command`
  sets `core.fsmonitor=false` and `log.showSignature=false`, status and
  diff use `--ignore-submodules=dirty`, and every git call sets
  `fetch.recurseSubmodules=false` and `submodule.recurse=false`, so
  fetch, pull and push never recurse into submodules (a submodule whose
  git dir is in the work tree, `lib/.git`, is not checked and must not
  be touched; `TestActionsDoNotRecurseIntoSubmodules`). Only the
  confirmed fetch, pull, push and clone (`command` with `network`) may
  contact a remote: every other call gets `noLazyFetch`
  (`GIT_NO_LAZY_FETCH=1`, `GIT_ALLOW_PROTOCOL=none`), so a partial
  clone's lazy fetch of a missing object never runs while listing
  (`TestListingNeverLazyFetches`); a read that needs the object fails
  instead. A repository's own config
  (`.git/config`, its includes, `config.worktree`, its submodules'
  configs below `.git/modules`; never the user's global or system scope) is checked against
  an allowlist, not a denylist: a key is fine only if it is on
  `gitx.allowedKey` (what a normal clone or user writes and what cannot
  run a program, redirect a connection or change what push and fetch
  send, write or delete) or passes one of the few value checks in
  `gitx.riskOf` (`remote.<name>.url` without a transport helper, a
  `remote.<name>.fetch` refspec below `refs/remotes/<name>/`, a boolean
  `core.fsmonitor`/`pager.*`, a `submodule.<name>.update` mode) and,
  for a remote's `url`/`pushurl`, is no local path into the
  repository's own folder or git dir (`insideRepo`: `push: remote.<name>.url`); every
  other key is listed, by name for a program, else as `network:`,
  `push:`, `fetch:` or `config: <key>`. Never add a key to the allowlist
  that names a program, a URL, a proxy, TLS or header setting, a refspec
  or a push/fetch behaviour; `TestNormalConfigsClean` must stay green
  for every clone variant. Repositories with such keys
  (`Status.Risky`) are marked `!`; anything else that runs git there
  asks `y` first and re-checks with `checkRisks` right before it runs.
  The AI plan view wraps and scrolls; `y` needs every line seen.
- Concurrency: git operations share `Model.sem` (`-jobs` slots), clones
  have `cloneSlots` of the same size. Everything that changes a repository
  runs under `Model.life` (cancelled on quit, counted in its WaitGroup).
- Never do slow work (git, network, stat of many files) inside `Update`
  or `View`; run it as a `tea.Cmd` and deliver the result as a message.
- Docs follow the code: a new key, flag or config setting goes into
  `docs/usage.md` (keys and flags tables), `docs/configuration.md` (the
  reference table) and `config.example.toml`, which
  must load to exactly `config.Default()` (tested); the README keeps a
  short table of the most-used keys. The README Quick start,
  `docs/getting-started.md` and `docs/troubleshooting.md` quote error
  messages verbatim; update them when a message changes. The README is
  the short overview; the details live in `docs/*.md`, one topic per
  file, each listed in the README's Documentation index. Placeholders only (`gitlab.example.com`,
  `acme/tools`); the one exception is the README badges, which link the
  project's real GitHub Actions runs and releases.
- Tests are hermetic: no network, no user git config (`GIT_CONFIG_GLOBAL`
  is /dev/null in unit and e2e tests), caches in `GL_REPOS_CACHE_DIR`;
  e2e runs keep only an allowlist of environment variables (`KEEP_ENV`
  in `tdrive.py`) with their own HOME/XDG dirs, and a PATH without
  glab/gh uses `git_only()`.

## Commits and publishing

- Commit only as the GitHub noreply address
  (`<id>+<user>@users.noreply.github.com`), set as `user.email` in this
  repository; never a work, institutional or personal address.
- Run `make publish-check` before every commit and every push, and do
  not commit or push while it fails. It checks `user.email` and every
  commit not yet on a remote for the noreply address, runs gitleaks, and
  greps the files and unpushed commits (messages and diffs) for the patterns in the untracked
  `.git/info/private-patterns` (one extended regexp per line).
- Nothing may reveal the author's employer, institution, colleagues,
  private servers, hostnames, projects, paths or issue numbers: not in
  code, tests, fixtures, docs, file names, images or commit messages. Use
  the placeholders above. Keep the patterns themselves out of the
  repository: naming them would leak them.

## Checks

```sh
make ci                    # the CI checks: gofmt, vet, golangci-lint, actionlint, goreleaser check, race tests, govulncheck, gitleaks, build
make e2e                   # TUI end-to-end (pyte via uv, needs lazygit); make e2e-ai adds Ollama flows
go test -tags live -run Live -v ./internal/ai/   # after any prompt/schema change
```

## CI, releases, tooling

- GitHub Actions (`.github/workflows/`) runs only `make` targets: add a
  check to the Makefile first, then call it from the workflow.
- Tools (golangci-lint, govulncheck, gitleaks, goreleaser, …) are pinned
  in the Makefile and run via `go run`; bump versions there only. uv
  (the e2e driver) is pinned by setup-uv's `version` in
  `.github/workflows/ci.yml`; never install it with `curl | sh`.
  `.golangci.yml` configures lint; keep `make lint` at 0 issues.
- Releases: push a `v*` tag; GoReleaser (`.goreleaser.yaml`) builds
  Linux/macOS × amd64/arm64, deb/rpm, SBOMs, and signs checksums with
  cosign (keyless). Validate config changes with `make release-check
  snapshot`. Update CHANGELOG.md before tagging.
- `go.mod` pins a patched `toolchain`; keep govulncheck clean by bumping
  it (and `golang.org/x/*`) when advisories appear.
- Dependabot opens update pull requests; CI must
  pass before merging them.

- Add unit tests for parsing, scanning and plan validation.
- Add an e2e check (`test/e2e/test_*.py`) for every new key or mode.
- Model behaviour is only proven by the live eval: the schema's property
  order and all-required step fields are what make gemma4:e4b fill
  `branch`/`include_untracked` and finish plans (3/12 → 40/40). Don't
  reorder or relax them without re-running it. The default gemma4:e2b
  passes 33/33 only with the one re-ask in `Propose` when a plan stages
  and says it commits but has no commit step (`missingCommit`); keep it.
