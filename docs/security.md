# Security design

How gl-repos keeps tokens out of commands, confirms what it runs, and
treats a repository's own git config: the trust model with its
allowlist, the keys that mark a repository, hooks and the unattended
fetch. [SECURITY.md](../SECURITY.md) is the security policy: how to
report a problem, and the table of guarantees with the code that
enforces each. Back to the [README](../README.md).

## Credentials

- gl-repos never handles forge tokens: GitLab and GitHub access goes
  through `glab` and `gh` with their own logins, and git authenticates
  with your ssh key or credential helper. It never passes credentials to
  git, glab or gh.
- A clone only takes a URL that parses as one of git's
  own forms and is rebuilt from the checked parts; one with credentials
  (an http(s) or `git://` URL with a user or password, an ssh URL with
  a password, `oauth2:<token>@host/…`), or with `?`, `#`, `;` or `%` (where tokens
  hide), pasted or suggested by the AI assistant, is refused before
  anything runs and never searched, so no token ends up in git's
  arguments, a clone's `.git/config` or a folder name. Clone URLs a
  server sends lose their credentials and are parsed the same way.
- As a
  last check, every git, glab and gh command refuses arguments with URL
  credentials, search text that is a clone source (a URL, `host:path`,
  `user:secret@host`, a local path) or has a credential is not sent,
  and a destination folder shaped like one is
  refused.
- Where a credential could be shown anyway (an existing
  remote, a branch or upstream name, a config key, git's or a CLI's
  output, a server's description, error messages, `-doctor`, `-list`),
  it is hidden (`https://***@…`, `git://***@…`,
  `ssh://git:***@…`, `?private_token=***`, `*` in the field), and the
  shapes the screen hides are exactly the shapes that are refused, so
  nothing runs with text shown redacted (a confirmation is compared in
  the shown form, so it still covers what you saw).

See [SECURITY.md](../SECURITY.md#credentials-threat-model) for what is and is not protected.

## Confirmation

Everything that changes a remote (push, publish, create, clone) or
runs AI-proposed steps needs a key press to confirm. Publish, create
and AI steps are shown as the exact command first, a clone as its
command with the destination relative to the root folder (see
[Cloning](repositories.md#cloning)); a push names how many repositories it pushes. AI plans are limited to an
allow-list of non-destructive git operations, and each step is checked
again right before it runs: if its command would differ from the one you
confirmed, nothing more runs.

## Text and paths from outside

- Text from outside (commit messages, project descriptions, git and CLI
  output, model answers, and folder names, which an unpacked archive
  can choose) is stripped of terminal escape sequences before it is
  shown, so it cannot rewrite the clipboard, the title or the screen.
- Project paths from a server are only used if every segment is
  a plain name, so a clone can never land outside the folder you chose,
  and a clone only ever removes a folder it created itself, after
  checking that it is still that folder, not one swapped in since (a
  process of your own user could still swap one in right between that
  check and the removal; a clone into a folder inside, or around, one
  still being cloned is refused until that one is done; `r` retries it).

## A repository's own config

### What it can do

A repository's own config (`.git/config`, files it includes, its
`config.worktree`, the configs of its submodules below `.git/modules`)
can name programs that git
runs: clean/smudge filters, `gpg.program`, `core.sshCommand`,
`core.pager`, `core.hooksPath`, credential helpers, remote transport
helpers (`remote.<name>.vcs`, a remote URL `<transport>::…` or with a
scheme git does not handle itself, which run `git-remote-<transport>`
from your PATH) and more. It can also redirect where a fetch or push
connects (`http.proxy`, `remote.<name>.proxy`, TLS switched off or
pinned to a planted certificate, `http.extraHeader`, cookies, a
`remote.<name>.pushurl` or `url.<base>.insteadOf` rewrite to another
host; git's credential helper, keyed by the real host, would then hand
your token to whoever answers), and change what a bare `git push` or
`git fetch --all --prune` sends, writes or deletes (`remote.<name>.mirror`,
`remote.<name>.push`, `push.default`, `push.followTags`,
`remote.pushDefault`, `branch.<name>.pushRemote`, a `remote.<name>.fetch`
refspec outside `refs/remotes/<name>/`, `pruneTags`, `fetch.bundleURI`).
Anyone who hands you a `.git` directory (an unpacked archive, a copied
disk) controls them.

### Listing runs nothing and connects nowhere

Listing a repository runs none of them: gl-repos
reads status with `core.fsmonitor`, signature checks
(`log.showSignature`), external diff/textconv and the repository's own
filter drivers switched off, and does not look inside submodules (a
submodule counts as changed only when its commit differs). A file
handled by such a filter (e.g. `git lfs install --local`) may show as
modified until git refreshes the index.

Nor does
it connect: a partial clone (`git clone --filter`) fetches an object
it lacks the moment a command reads one, with its own config
(`remote.<name>.url`, `uploadpack`, proxy), so every git call that is
not a confirmed fetch, pull, push or clone runs with
`GIT_NO_LAZY_FETCH=1` (git 2.45+) and `GIT_ALLOW_PROTOCOL=none` (any
git; it also beats the repository's own `protocol.allow`); a normal
partial clone lists cleanly, one missing an object a read needs shows
an error instead of fetching.

The `ext::`
transport, whose remote URL is a command (`protocol.ext.allow`), is
switched off for every git call, fetch, pull and push included. Fetch,
pull and push never recurse into submodules (git's default fetches
inside a submodule as soon as a fetched commit moves it): a submodule
whose git directory sits in the work tree (`lib/.git`, as an unpacked
archive has it) is not checked, and is left alone.

### The allowlist

Which keys are fine is an allowlist, not a list of dangerous ones: a
key in the repository's own config is fine only if it is one a normal
clone or user writes there and cannot run a program, redirect a
connection or change what push and fetch send, write or delete; every
other key marks the repository.

Allowed with any value:

- the `core.*`
  keys `git init` and `git clone` write (`repositoryformatversion`,
  `filemode`, `bare`, `logallrefupdates`, `ignorecase`,
  `precomposeunicode`, `symlinks`, `autocrlf`, `eol`, `safecrlf`,
  `quotepath`, `sparsecheckout`, `sparsecheckoutcone`, `longpaths`,
  `checkstat`, `trustctime`, `untrackedcache`, `worktree`),
- `extensions.*`, `include.path` (the included keys are checked like the
  rest),
- `remote.<name>.{tagopt,prune,promisor,partialclonefilter,gh-resolved}`
  (the last written by `gh repo clone` of a fork and `gh repo set-default`),
- `branch.<name>.{remote,merge,rebase,description}`, `submodule.active`,
  `submodule.<name>.{active,branch}`, `user.{name,email,signingkey}`,
- `commit.{gpgsign,template}`, `tag.gpgsign`, `pull.{rebase,ff}`, `fetch.prune`,
  `init.defaultbranch`, `rebase.autostash`, `merge.ff`, `diff.renames`,
  `rerere.enabled`, `lfs.repositoryformatversion`
- and the `color.*`,
  `status.*`, `log.*`, `advice.*`, `gc.*` (except
  `gc.recentObjectsHook`, a command), `maintenance.*`, `index.*` and
  `feature.*` sections.

Allowed by value:

- `remote.<name>.url` and
  `submodule.<name>.url` unless they name a transport helper,
- and a
  remote's `url` unless it is a local path into the
  repository's own folder or git directory (a planted bare repository
  at `.git/x` or `vendor/x.git`, whose hooks and config a confirmed push
  would run: `push: remote.<name>.url`; a local bare repository
  elsewhere is fine),
- `remote.<name>.fetch` when the refspec writes only below
  `refs/remotes/<name>/` with the same number of `*` on both sides (what
  a clone writes, `--single-branch` included, and a forge's
  `+refs/merge-requests/*/head:refs/remotes/origin/mr/*`),
- `core.fsmonitor` and `pager.<cmd>` as a boolean,
- and a
  `submodule.<name>.update` mode (a command, `!…`, counts).

Every other key marks the repository: a program, `http.proxy`,
`http.sslVerify=false`, `url.<base>.insteadOf`, `remote.<name>.pushurl`,
`remote.pushDefault`, `branch.<name>.pushRemote`, `push.*`, the other
`fetch.*` keys, `remote.<name>.mirror`, a mirror's fetch refspec, an
unknown key. A legacy remote file (`.git/remotes/*`, `.git/branches/*`)
marks it too.

### How a repository is marked

Such a repository is marked with a red `!` in the list, the details
pane names the keys (`config: ! its own git config runs programs
(filter.x.clean)`), and `-list` prints a warning for it.

- A key that
  names a program is listed by name;
- a network setting as
  `network: http.proxy` (a boolean at its harmless default,
  `http.sslVerify = true` or `http.saveCookies = false`, and an empty
  proxy are not flagged);
- a setting of what a bare push or fetch does as
  `push: remote.origin.pushurl`, `push: push.followTags` or
  `fetch: remote.origin.fetch` (`remote.<name>.mirror`, `pruneTags` and
  `push.followTags` set to `false` are not flagged);
- and any other key
  off the allowlist as `config: core.excludesfile`.

At most 100 submodule
configs are read; a repository with more is marked as well
(`submodules: more than 100, not all checked`), and so is one whose
config, or a submodule's, cannot be read (`config not readable, not
checked`), one with legacy remote files (`push: .git/remotes` or
`push: .git/branches`: git reads a remote's URL and refspecs there
when the config names none), and one whose work tree is not the folder itself (`work
tree: not this folder`: `core.worktree`, in its own config or in the
one a planted `.git` file points to, can name any folder, and pull
would write files there while the folder looks clean). A linked
worktree (`git worktree add`) or a submodule keeps only its git
directory elsewhere and is not flagged.

### Actions on a marked repository

Fetch, pull, push, AI plans, publish, and opening the tool, an agent
or a shell there ask for `y` first and name the keys; right before
running, gl-repos reads the config again and stops if it gained keys
you did not confirm. The unattended fetch at start (`-fetch`,
`fetch_on_start`) leaves such a repository alone. Settings in your
global or system config are yours and never flagged.

### Hooks

Hooks are not
checked and not flagged: the scripts in `.git/hooks` (or wherever
`core.hooksPath` points, which key is flagged) run whenever git itself
would run them: a pull
(post-merge, post-checkout, reference-transaction) or push
(pre-push, reference-transaction) from gl-repos, and whatever you do
in the tool, an agent or a shell opened there, runs the repository's
own hooks. A fetch runs none (gl-repos points `core.hooksPath` at
`/dev/null` for it), the unattended one at start included. Listing
runs none (status, log and rev-list have no hooks). Many normal repositories use hooks, so they
are not a mark: don't run actions in repositories you would not run
git in.

## AI assistant

The AI assistant sends the selected repository's status and diff to
the Ollama server in `[ai] url` (local by default). A diff can contain
secrets; only point it at a server you trust. The URL may hold no
user, query, `;`, `%` or credential of any shape, and `[ai] model`
must be a plain model name: both are in every request and in
messages. The model sizes remembered from `ollama ps` are kept only
under plain model names.

## Caches

Caches (`~/Library/Caches/gl-repos` on macOS, `~/.cache/gl-repos` on
Linux; `GL_REPOS_CACHE_DIR` overrides) hold project names and parsed
clone URLs and are readable only by you: the files are 0600 and the
default directory 0700 (made so again if an older version left it
0755; a directory chosen with `GL_REPOS_CACHE_DIR` keeps its own
mode). A cache file that cannot be made private is deleted, and none
is written into a default directory that cannot.

Nothing there is
followed: every cache file is read and
written relative to the cache directory after it was checked and
opened, so a path that would leave it through a symlink is refused, a
symlink planted at a cache file is removed and counts as no cache, and
a cache directory that is itself a symlink is not used, read or
written (`cache directory … is a symlink or no directory: not used`;
point `GL_REPOS_CACHE_DIR` at a real directory).
