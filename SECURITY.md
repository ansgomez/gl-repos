# Security policy

## Supported versions

Only the latest release receives fixes.

## Reporting a vulnerability

Please do **not** open a public issue. Report privately instead:

- GitHub: *Security → Report a vulnerability* (private advisory)

Include what you found, how to reproduce it, and the impact. You'll get an
answer within a week; fixes are released as soon as possible and credited
unless you prefer otherwise.

## Scope

gl-repos runs git, glab, gh, lazygit, your shell and coding agents on your
behalf, and talks to an Ollama server. Particularly relevant:

- text from repositories or servers that could reach the terminal
  unsanitized (escape sequences),
- anything that runs or publishes without the confirmation step,
- paths from a server escaping the chosen folder, or deleting user data,
- AI-proposed steps escaping the allow-list.

See [docs/security.md](docs/security.md) for the design: the trust model
with its allowlist, the keys that mark a repository, hooks and the
unattended fetch.

## Credentials: threat model

gl-repos never reads, stores or sends forge tokens; glab, gh, ssh and
git's credential helpers do. What it guards against is a credential the
user (or the AI assistant, or a server) puts *into text* gl-repos
handles: a token in a pasted clone URL, in git's or a CLI's output, in
a server's reply. Each guarantee is enforced at one place, so a new
display or a new command cannot forget it:

| Invariant | Enforced at |
| --- | --- |
| git gets only clone URLs that were parsed and rebuilt from checked parts | `forge.ParseCloneURL` → `forge.CloneURL` (the only type `gitx.Clone`/`CloneProgress` take) |
| one definition of a credential shape: what the screen hides is what is refused | `forge.edit` in `creds.go` (`RedactURLs`, `MaskURLs` and `HasCredentials` share it) |
| one scheme policy for a URL's user: a plain name only for ssh-like schemes, a secret for every other (http, https, git, unknown) | `forge.plainUser` in `creds.go` (`credentials` for display and `HasCredentials`, `urlUser` in `ParseCloneURL`, `StripCredentials`) |
| no argument of git, glab or gh carries URL credentials | `gitx.command`, `forge.cliCommand` (`forge.CheckArgs`) |
| search text that is a clone source (by the clone box's own classifier) or any credential is never sent | `GitLab.Search`, `GitHub.Search` (`searchable`: `IsURL`, `HasCredentials`, `ErrNotSearched`) |
| no folder name holds a credential | `gitx.CheckDest` (every clone, group clone and new-repository destination) |
| text from git, CLIs, servers and the model is shown without credentials (also branch, upstream and default-branch names) | `scrub` at the top of the TUI's `Update` (`scrubStatus` for a status); `printList` for `-list`; the `-doctor` output sink; `fatal` in `main.go` |
| a repository's own config (`.git/config`, its includes, `config.worktree`, its submodules' configs below `.git/modules`) is checked key by key against an allowlist of what a normal clone or user writes and what cannot run a program, redirect a connection or change what push and fetch send, write or delete (the list: [docs/security.md](docs/security.md#the-allowlist)) | `allowedKey`/`riskOf`/`insideRepo` in `gitx/trust.go` (`ConfigRisks`) |
| every other key (a program, `http.proxy`, `http.sslVerify=false`, `url.<base>.insteadOf`, `remote.<name>.pushurl`, `remote.pushDefault`, `branch.<name>.pushRemote`, `push.*`, `fetch.*`, `remote.<name>.mirror`, a mirror's fetch refspec, an unknown key) marks the repository | `riskOf` in `gitx/trust.go` (`ConfigRisks`) |
| a legacy remote file (`.git/remotes/*`, `.git/branches/*`) marks the repository | `addLayout` in `gitx/trust.go` (`ConfigRisks`) |
| a marked repository's config decides nothing that runs, connects or deletes refs without a `y` | `checkRisks` in the TUI's `trust.go` (`startAction`, `startFetchOnStart`, `runExec`, the plan and publish) |
| every action, the unattended startup fetch included, re-reads the config right before it runs | `checkRisks` in the TUI's `trust.go` (`startFetchOnStart` leaves a marked repository alone) |
| the tool, agents and the shell run without the inherited `GIT_DIR`/`GIT_WORK_TREE` | `gitx.Env` (`repoCommand` in the TUI's `tui.go`) |
| fetch, pull and push never recurse into submodules (`-c fetch.recurseSubmodules=false -c submodule.recurse=false` on every git call), so a submodule whose git directory is in the work tree, which is not checked, is never touched | the `-c` settings in `gitx.command` |
| a fetch runs none of the repository's hooks (`-c core.hooksPath=/dev/null`) | the `-c` settings in `gitx.command` (fetch only) |
| no git call but a confirmed fetch, pull, push or clone contacts a remote: a partial clone's lazy fetch of a missing object, which would connect with the repository's own config while it is listed, is switched off (`GIT_NO_LAZY_FETCH=1`, git 2.45+, and `GIT_ALLOW_PROTOCOL=none` for any git, inherited by the fetch git would run and ahead of the repository's `protocol.allow`), and such a read fails instead | `noLazyFetch` in `gitx.command` (`TestListingNeverLazyFetches`) |
| a redacted value is never compared with a raw one: what is executed or checked stays raw and is refused when it has a shape; what is both shown and compared (risky config keys) is compared in the shown form on both sides | `scrub` leaves plan steps, project URLs, paths and the user name alone; `shownRisks` in `trust.go` (`checkRisks` redacts the fresh keys the same way) |
| the cache holds only cleaned projects (parsed clone URLs, redacted descriptions), none in an excluded group, and stays private | `cleanProject` in `saveCache` and `loadCache`, `Keep`/`KeepGroups` in `loadCache`; `writeCache` |
| the size cache holds only plain Ollama model names, and the configured model and server URL are plain (the name is in every request and message; the URL is every request's prefix and printed by `-doctor`) | `ai.CheckModel` in `rememberSize` and `loadSizes` and for `ai.model`; `AI.validate` for `ai.url` (no user, query, fragment, `;`, `%` or credential shape, by `forge.HasCredentials`) |
| nothing in the cache directory is followed: the directory is checked (a real directory, not a symlink) and opened once, on a read as on a write, and every file operation is relative to that open directory (`os.Root`: a path that would leave it, through a symlink too, is refused by the kernel); a planted symlink at a cache file is removed and no cache, a file is read and tightened through the descriptor that was checked | `forge.ReadCacheFile`, `forge.WriteCacheFile`, `forge.RemoveCacheFile`, `openCacheDir` in `cachefile.go` (project lists and the Ollama size cache) |
| a server's user name and group visibility are plain values | `userName` in `Whoami`, `knownVisibility` in `group` |
| a host is a plain host name (a DNS name or bracketed IPv6 address, an optional port) before it is stored, shown, named in a cache file or given to glab or gh as `--hostname`, wherever it comes from: the config, `glab auth status` or a remote | `forge.ValidHost` (the config's `host` settings, `loggedInHosts` for `LoggedInHosts`, `HostFromURL` by the same `urlHost`/`scpHost`) |
| no error message quotes a typed query, URL or destination | `ErrBadCloneURL`, `ErrURLCredentials`, `ErrNotSearched`, `ErrNotFound`, `ErrDestCredentials` |

### Refused clone URLs

Refused, never cleaned, so a URL is never half
used, and never searched on the forge instead:

- an http(s) or git:// URL with any user or password
  (`https://oauth2:<token>@…`, `https://<token>@…`, `git://<token>@…`);
- an ssh URL with a password (also an empty one), or with a user that
  is not a plain name;
- the scheme-less `oauth2:<token>@host/…`;
- `?`, `#`, `;` or `%` anywhere (`?private_token=`, `?%70rivate_token=`,
  `#access_token=`, `;private_token=`);
- a project path that is not plain names separated by single slashes
  (so also a home-relative `~user/…` path and a doubled slash, which
  git would send on as they are), a host that is not a DNS name or
  bracketed IPv6 address, other schemes (`ext::`, `fd::`, `ftp://`),
  control characters, a leading `-`;
- a bare name (`tools.git`), also when a server sends it as a clone
  URL: a local path starts with `/`, `./` or `../`.

### Accepted clone URLs

- `https://host[:port]/group/…/project[.git]` (also `http`,
  `git`),
- `ssh://[user@]host[:port]/path` (also `git+ssh`, `ssh+git`),
- `[user@]host:path[.git]` (git's scp-like form, also with an absolute
  path, `host:/srv/git/project`, which is kept as such),
- `file:///path`
  and local paths (absolute, or starting with `./` or `../`; a bare name
  is a search).

The forms are told apart as git does, by one classifier
(`scpLike` in `cloneurl.go`) that `IsURL` (search or clone source?),
`ParseCloneURL` and `HostFromURL` share: `://` is a URL, a colon before
the first slash is scp-like (`gitlab.example.com:acme/tools` is a clone
source, not a search), text with a space is a phrase (`fix: parser` is
searched). The rebuilt URL is the input with at most one trailing slash
dropped and the scheme lower-cased, never another repository.

### Credential shapes

One list, `forge.edit`:

- any user or password of
  a URL whose scheme is not ssh-like (`plainUser`: http, https, git, and
  any other or unknown scheme, `ftp://<token>@…` too),
- a password of an
  ssh-like URL (`ssh`, `git+ssh`, `ssh+git`, and the scp-like form, whose
  user is a plain name: git logs in as it),
- a scheme-less
  `user:secret@host` (also after a slash, `fix/oauth2:<token>@host`),
- and
  a parameter whose name says token, key,
  secret, password, auth, signature, credential or session after `?`,
  `&`, `#` or `;`, also percent-encoded (`?%70rivate_token=`,
  `?x=1%26private_token=`, `%3Fprivate_token%3D`), with or without a URL
  around it.

Each is hidden on screen (in text from git, the CLIs,
servers and the model; a local folder's name is your own and is shown
as it is, sanitized), refused as a command argument, in an AI plan
step, as a search and as a destination folder; a new finding
becomes a fixture in `credentialShapes` (forge) and `secretShapes`
(TUI), which every guard test runs.

### Protected

- tokens in URL-shaped input typed or pasted into the
  clone box or suggested by the AI assistant;
- credentials in clone URLs a
  server sends (stripped, then parsed like typed ones);
- credentials git or
  a CLI echoes in its output or errors (hidden on screen, in the details
  pane and in `-doctor`);
- ref names shaped like a credential (a valid
  branch name may hold `#access_token=…`, `;private_token=…` or
  `oauth2:<token>@host`): the branch, its upstream and origin's default
  branch are shown redacted in the list, the details pane and `-list`,
  and the real name is never a git argument (push goes by `HEAD`; an AI
  step that names such a branch is refused as a plan step, and the
  comparison with such a default branch is skipped, both by
  `forge.CheckArgs`);
- a risky config key shaped like one
  (`credential.https://oauth2:<token>@host.helper`), shown redacted in the
  details, the confirm line, the publish review and the plan, and
  compared in that form (so the confirmation still covers it);
- the
  cache's permissions, and its files against a symlink planted at their
  path or at the directory's (a cache file is read and written only as
  the regular file that was checked, relative to the cache directory
  that was checked and opened; a cache directory that is a symlink is not
  used, read from, chmod'ed or written to). What remains there is the
  directory itself: looking at it and opening it are two steps, so a
  directory swapped in between is noticed (`SameFile`) and not used,
  never followed.

### Not protected

- secrets in free text that has none of the shapes
  above (a commit message, a search phrase, a branch name without one of
  the shapes; display redaction is a best-effort heuristic that
  over-redacts rather than misses known shapes, but it is not a
  boundary);
- secrets in a diff, and the real branch, upstream and
  default-branch names, which the AI assistant sends to the Ollama server
  you configured;
- secrets already in a repository's history or its own
  `.git/config`;
- anything glab, gh, ssh or git itself logs or stores.
