# Troubleshooting

What to do when something fails, with the messages as gl-repos prints them. Back to the [README](../README.md).

## Run `gl-repos -doctor` first

It changes nothing and prints one line
per check as soon as it is done, `✓` fine, `!` works with limits, `✗`
broken, with a fix below each `!` and `✗`; it exits with status 1 if
anything is `✗`. It checks:

- the config file in use (loads and is valid;
  a leftover `gitgaze` config directory is a `!`, see [Upgrading from gitgaze](#upgrading-from-gitgaze));
- git (2.31 or newer);
- the `tool` and agents;
- for each profile its CLI (glab/gh), host, login and user name;
- on GitLab the token scopes (`personal_access_tokens/self`: only read scopes means
  `n`/`u` will fail) and whether `default_group` exists;
- on GitHub `gh auth status` and its scopes;
- where glab/gh keep the token (the OS keyring or
  a token from an environment variable such as `GITLAB_TOKEN`/`GH_TOKEN`,
  or a `!` for plain text in their config file);
- `ssh -T git@<host>` for each host (timeouts point
  to a VPN, firewall or DNS, `Permission denied` to your key);
- and whether Ollama answers at `[ai] url` with `[ai] model` installed (only a warning:
  the AI is optional).

Every check has a timeout, and text from servers is
sanitized. A `default_group` that does not exist names the levels that
are missing (`default_group acme/tools/new not found on <host>:
acme/tools exists, missing acme/tools/new`).

Most errors show on the line above the key help. When a create, publish
or git command fails on a row, select the row and press `enter`: the
details pane shows the command's full output.

## glab or gh not installed or not logged in

`c` says at once `glab
not found: install it, or paste a clone URL` or `no GitLab host found:
glab auth login --hostname <host>, or set [clone] host` (`host in
[[profile]] "work"` with profiles), and `n`/`u` say `could not read your
user name: glab not found: install it`, `no GitLab login found: glab auth
login --hostname <host>`, or `could not read your user name: glab (…):
401 Unauthorized (not logged in to <host>: glab auth login --hostname
<host>)`. `-doctor` says `glab is not logged in to <host>` (or `gh is
not logged in to github.com`, fix `gh auth login`). Install the CLI,
run `glab auth login --hostname <host>` (or `gh auth login`) and check
`glab auth status`. If glab is logged in to several hosts, set `host` in
the profile (or `[clone] host`). A pasted clone URL works without any
login.

## Server not reachable (VPN off, DNS, firewall)

Errors end with
`(cannot reach <host>: check the VPN, DNS or firewall)`, and `-doctor`
says `cannot reach <host>: …` instead of "not logged in": glab or gh got
no answer at all (`no such host`, `dial tcp`, `connection refused`, `i/o
timeout`). Connect the VPN or check DNS and the firewall; your login is
probably fine.

## Token lacks the `api` scope (`insufficient_scope`)

Browsing and
cloning over ssh work (cloning over https with glab as git's credential
helper also needs `read_repository`), but `n`/`u` end with `publish failed: the glab token needs
the api scope (a legacy personal access token): glab auth login
--hostname <host> · enter: details` (for `n`: `created <repo> locally,
but publishing failed: …`), and the details pane shows the failed
command and its `403 … insufficient_scope` output. Log in again with a
GitLab personal access token that has the `api` scope (see
[Logging in](profiles.md#logging-in-glab-and-gh)). The local repository is kept:
press `u` on it to publish it once the login is fixed.

## 403 Forbidden without `insufficient_scope`

`403 Forbidden: your
role in the group may not allow this (creating subgroups needs
Maintainer/Owner), or the token lacks the api scope: glab auth login
--hostname <host>`. Check your role in the parent group first (Developer
may create projects but not subgroups, depending on the group's
settings), then the token's scopes with `gl-repos -doctor`. On GitHub a
403 says `the gh login may not do this: gh auth refresh --scopes repo`
(with `--hostname <host>` for GitHub Enterprise).

## Group does not exist

`X is not a group you can see on <host> (user
namespaces can't have subgroups), and gl-repos only creates subgroups of
existing groups`: gl-repos never creates a top-level group. Create it on
the server, or pick an existing group (`tab` completes your groups). A
group you cannot see counts as missing, and a user namespace (yours or
someone else's) holds projects but no subgroups. An owner in a group the
profile excludes is refused: `<group> is in a group this profile excludes
(exclude in the config): pick another owner`.

## ssh clone, fetch or push fails or times out

gl-repos runs git with
ssh `BatchMode=yes` and a 15 s connect timeout, so ssh never asks for a
passphrase, a password or a new host key. Typical messages and fixes:

- `Connection timed out`, `Connection refused`, or a clone stuck at
  "connecting": the ssh port of the server is not reachable from your
  network. Some servers are only reachable through a VPN or block ssh
  from outside; connect the VPN, or use the server's https URLs (or its
  ssh-over-443 endpoint, if it has one, via `~/.ssh/config`).
- `Permission denied (publickey)`: your key is not loaded or not
  registered on the server. Run `ssh-add` and add the public key to your
  account.
- `Host key verification failed`: connect once with `ssh -T git@<host>`
  and accept the host key.

Test outside gl-repos with `ssh -T git@<host>` and `git push` in the
repository. A custom `GIT_SSH_COMMAND` or `core.sshCommand` replaces
the ssh options gl-repos adds.

## Ollama not running

`i` shows `ollama unreachable at
http://localhost:11434 (is it running?)`: start Ollama (the app, or
`ollama serve`) or set `[ai] url`. `model gemma4:e2b is not installed
(ollama pull gemma4:e2b)`: pull it, or choose an installed model with
`-model` or `[ai] model`. The AI is optional: without Ollama only `i`
is unavailable. If the memory guard refuses the model, close
other programs, pick a smaller model, or press `f` to load it anyway.

## lazygit missing

`o` shows `lazygit not found in PATH`: install
lazygit, or set `tool` (or `-tool`) to another program such as `tig`.
Agents for `t` that are not installed are marked in the picker.

## Upgrading from gitgaze

gl-repos used to be called gitgaze. On its
first start it moves the old default config directory
(`~/.config/gitgaze`, or `$XDG_CONFIG_HOME/gitgaze`) to the new one and
says `gl-repos: moved your settings from <old> to <new> (gitgaze was
renamed)`; the project cache moves silently. If both directories exist,
it uses the new one and says `gl-repos: ignoring <old> (gitgaze was
renamed): using <new>; …`: copy what you need, then remove the old one.
An old directory that is a symlink is not moved (`… is a symlink, so it
was not moved …`): point the link, or its target, at the new name
yourself. Nothing moves when `-config`, `GL_REPOS_CONFIG` or
`GITGAZE_CONFIG` names the file, and `-doctor` only reports a leftover
old directory. The old variables `GITGAZE_CONFIG` and
`GITGAZE_CACHE_DIR` still work for one release when the new ones are
unset, with a warning. Rename the binary in your scripts and aliases:
the old `gitgaze` binary is not replaced by `make install`; delete it
from `$(go env GOPATH)/bin`.

## Config errors

gl-repos refuses to start on an unknown key or an
invalid value and names the file and key, e.g. `unknown setting(s):
defualt_profile` or `profile "work": host = "https://gitlab.example.com":
use the bare host name, e.g. gitlab.example.com`. `unknown setting(s):
ai.default_profile (default_profile is a top-level key: top-level keys
must come before the first [section])`: move the line to the top of the
file. `[profile]` with single brackets fails with `… (use [[profile]]:
each profile is its own [[profile]] entry)`. `[clone] host is
ignored when [[profile]] entries exist: move it into the profile`: with
`[[profile]]` entries, `[clone]` does nothing, so set `host`, `exclude`
and `default_group` in the profile instead. A file named with `-config`
or `GL_REPOS_CONFIG` that does not exist is an error (`config: -config
names <path>, which does not exist`, or `GL_REPOS_CONFIG names …`), so a
typo is not silently ignored; only the default location may be missing.

## `response too large (over 128 MB)`

glab or gh wrote more than
gl-repos reads from one call (far beyond any real project list); the
call is stopped instead of buffering it. Check the host, or narrow the
profile's `exclude`.

## No repositories listed

The list says `no repositories here yet ·
press c to clone one · searched to depth 1 (try -d 2) in <dir>`.
gl-repos searches only `-d` levels below the folder (1 by default) and
skips hidden folders, `node_modules`,
`vendor`, `venv`/`.venv`, `target` and `skip`. Start with a larger `-d`,
or set `depth` in the config.
