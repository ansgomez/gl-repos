# Usage

Command-line flags, the columns of the list and every key. Back to the [README](../README.md).

## Command line

```sh
gl-repos                 # repos directly under the current dir (depth 1)
gl-repos -d 3 ~/git      # search up to 3 levels below ~/git
gl-repos -fetch          # fetch everything on startup so ahead/behind is fresh
gl-repos -list -d 2      # print a status table and exit (scriptable)
gl-repos -init           # write a commented config file, then edit it
gl-repos -doctor         # check the setup without changing anything
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-d`, `-depth` | 1 | Directory levels below `dir` to search. 0 = `dir` only. |
| `-j`, `-jobs` | #CPUs | Max concurrent git operations (status, fetch, pull, push, AI); clones have the same number of slots of their own. |
| `-fetch` | off | `git fetch --all --prune` every repo on startup; a repo marked `!` is only read (press `f` there). Ignored with `-list`. |
| `-sort` | name | Initial sort: `name`, `attention`, `recent`. |
| `-tool` | lazygit | Program `o` opens in the repo. |
| `-model` | gemma4:e2b | Ollama model for `i`. |
| `-profile` | first profile | Profile active at start (see [Profiles](profiles.md)). |
| `-config` | see [Config](configuration.md#config-file) | Settings file; must exist when given. `gl-repos -h` prints the path in use. |
| `-list` | off | Non-interactive table output: read every repository, print the table and exit. It never fetches: `fetch_on_start` is ignored, and `-fetch` given with it is ignored with a note on stderr (ahead/behind are as of the last fetch). |
| `-init` | | Write the commented [`config.example.toml`](../config.example.toml) to the config path in use (`-config`, else `GL_REPOS_CONFIG`, else the default), directory 0700 (the default config directory is made 0700 too when it exists already; a directory named with `-config` or `GL_REPOS_CONFIG`, like the current folder, keeps its mode, and a symlinked one is left as it is) and file 0600, print the next steps and exit. Never overwrites an existing file. |
| `-doctor` | | Check the setup and exit (status 1 if any check fails); see [Troubleshooting](troubleshooting.md). |
| `-version` | | Print the version and exit. |

Hidden directories, `node_modules`, `vendor`, `venv`/`.venv` and `target`
are skipped (`skip` in the config adds more); symbolic links to
directories are not followed. The search stops at a repository and does
not look for nested repos inside it.

## Columns

- A red **`!`** before the name: the repository's own git config names
  programs that git would run (see [Security](security.md#a-repositorys-own-config)); the details
  pane lists the keys.
- **BRANCH** — yellow when not origin's default branch, red when detached.
- **SYNC** — `≡` in sync, `↑n` ahead, `↓n` behind, `–` no upstream.
  Reflects the last fetch; press `a` then `f` for fresh numbers.
- **VS BASE** — on a non-default branch: commits ahead/behind
  `origin/<default>`. Empty on the default branch.
- **STATE** — `clean`, or `+staged ~modified ?untracked !conflicts`.
- **UPSTREAM** — green when tracking `origin/<default branch>`.

## Keys

| Key | Action |
| --- | --- |
| `↑`/`↓`, `j`/`k` | Move |
| `g`/Home, End | First / last repository |
| PgUp/PgDn, `ctrl+u`/`ctrl+d` | Page up / down |
| `space` | Toggle selection |
| `a` | Select / deselect all visible |
| `f` | Fetch selected (or current); `a` then `f` fetches all |
| `p` | Pull (`--ff-only`) selected or current |
| `h` | Push selected or current (asks to confirm; sets upstream to `origin` if missing) |
| `r` | Refresh status of selected (or current) |
| `d` | Show only repos needing attention: error, dirty, ahead or behind, no upstream, detached, or marked `!` |
| `s` | Cycle sort: name → attention → recent commit |
| `l` | Commit graph of all refs (scroll, `esc` back) |
| `o` | Open the configured tool (lazygit) in the repo; refreshes on exit |
| `!` | Open `$SHELL` in the repo; refreshes on exit |
| `t` | Start a coding agent (claude, codex, kimi, …) in the repo (see [Agents](agents-and-ai.md#agents)) |
| `c` | Clone a project from the active profile (see [Cloning](repositories.md#cloning)) |
| `n` | New repository, optionally with a project on the active profile; then offers an agent |
| `u` | Publish the selected repository to the active profile (create project, add remote, push) |
| `w` | Switch profile (asks `y/N`) |
| `i` | Ask the AI assistant (see [AI assistant](agents-and-ai.md#ai-assistant)) |
| `x` | Cancel a running clone, or remove a failed one from the list |
| `enter` / `tab` | Toggle details pane (incl. last command output) |
| `q` / `esc` | Quit (asks first while clones, fetch/pull/push or AI steps are running) |
| `ctrl+c` | Quit at once from any screen, stopping everything that runs |

Shortcuts ignore case: `H` does the same as `h`.
