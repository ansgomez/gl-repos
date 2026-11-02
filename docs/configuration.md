# Configuration

The config file, every setting with its default, and the environment variables. Back to the [README](../README.md).

## Config file

`~/.config/gl-repos/config.toml`, or `$XDG_CONFIG_HOME/gl-repos/config.toml`
when that variable is set, or the file named by `GL_REPOS_CONFIG` or
`-config` (`gl-repos -h` prints the path in use). All keys are optional;
copy [`config.example.toml`](../config.example.toml) there to start. Flags
override the file. A missing file at the default location means defaults, but a file
named with `-config` or `GL_REPOS_CONFIG` must exist; an unknown key or an
invalid value is reported at startup with the file and key named.
[`config.example.toml`](../config.example.toml) (what `-init` writes) lists
every key commented out with its default, so it never pins a default.
Top-level keys come before the first `[section]` or `[[profile]]`: a
`default_profile` below `[ai]` is read as `ai.default_profile` and
reported as unknown. An example with some values changed (the
[reference](#reference) lists the defaults):

```toml
depth = 2
jobs = 8
fetch_on_start = false
sort = "attention"           # name | attention | recent
skip = ["archive", "tmp"]    # extra directory names never searched
tool = "lazygit"             # or "tig", "gitui", ...

[clone]
host = "gitlab.example.com"  # GitLab searched by "c" (default: glab login / origins)
exclude = []                 # groups never listed, cached or cloned (default: none)
default_group = "acme/tools" # owner prefilled by n/u (default: your user)

[[agent]]                    # agents started with "t" (default: claude, codex, kimi)
name = "claude"
command = "claude"

[ai]
url = "http://localhost:11434"
model = "gemma4:e2b"
max_ctx = 32768              # upper bound for num_ctx (minimum 8192)
max_diff_bytes = 24000       # diff sent to the model
timeout_seconds = 120        # whole request
load_timeout_seconds = 90    # until first output (model load + prompt)
stall_timeout_seconds = 20   # max silence while answering
keep_alive = "2m"            # unload models gl-repos loaded after idling
memory_guard = true          # refuse models that don't fit in free RAM
```

### Reference

| Key | Default | Flag | Meaning |
| --- | --- | --- | --- |
| `depth` | 1 | `-d` | Directory levels below the root to search; 0 = root only. |
| `jobs` | number of CPUs | `-j` | Concurrent git processes; clones get as many slots of their own. |
| `fetch_on_start` | false | `-fetch` | Fetch every repository at startup, except those marked `!`. |
| `sort` | `name` | `-sort` | Initial sort: `name`, `attention`, `recent`. |
| `skip` | `[]` | | Extra directory names never searched. |
| `tool` | `lazygit` | `-tool` | Program `o` runs in the repository: a command name (looked up in PATH) or an absolute path; a relative path such as `bin/tig` is refused, as for agents. |
| `default_profile` | first profile | `-profile` | Profile active at start; a top-level key (before any `[section]`). Without `[[profile]]` the default is the first built-in profile whose CLI is installed. |
| `[[agent]] name`, `command` | claude, codex, kimi | | Agents for `t`; any entry replaces all defaults. |
| `[[profile]] name` | the forge | | Shown in the badge; must be unique. |
| `[[profile]] forge` | | | `gitlab` or `github` (any case). |
| `[[profile]] host` | glab login / github.com | | Server of that account: a bare host name with an optional `:port`, e.g. `gitlab.example.com` (also a bracketed IPv6 address, `[::1]:8443`). |
| `[[profile]] exclude` | `[]` | | Groups never listed, cached or cloned by that profile. |
| `[[profile]] color` | by position | | Badge color: ANSI number 0-255, `#rgb` or `#rrggbb`. |
| `[[profile]] default_group` | your user | | Owner prefilled for new projects (`n`, `u`), e.g. `acme/tools`; one name on GitHub. |
| `[clone] host` | glab login / origins | | GitLab of the default `gitlab` profile. Only without `[[profile]]`: with profiles, setting any `[clone]` key is an error. |
| `[clone] exclude` | `[]` | | Same for the default profile. |
| `[clone] default_group` | your user | | Same for the default profile. |
| `[ai] url` | `http://localhost:11434` | | Ollama server. |
| `[ai] model` | `gemma4:e2b` | `-model` | Model for `i`. |
| `[ai] max_ctx` | 32768 | | Upper bound for `num_ctx` (sized per request from 8k; the minimum is 8192, smaller values count as that). |
| `[ai] max_diff_bytes` | 24000 | | Diff bytes sent to the model; 0 = no limit. |
| `[ai] timeout_seconds` | 120 | | Whole request. |
| `[ai] load_timeout_seconds` | 90 | | Until the first output (model load + prompt). |
| `[ai] stall_timeout_seconds` | 20 | | Longest silence while answering. |
| `[ai] keep_alive` | `2m` | | How long Ollama keeps a model gl-repos loaded (`30s`, `2m`, `-1` = forever). |
| `[ai] memory_guard` | true | | Refuse models that do not fit in free RAM (`f` forces). |

### Environment

gl-repos reads the variables below and passes its environment on to
git, the forge CLIs, the tool and agents. To the git and forge CLI calls
it makes itself it adds only: for git `GIT_TERMINAL_PROMPT=0`,
`GIT_OPTIONAL_LOCKS=0`, `GIT_SSH_COMMAND` (see below), for every call
that is not a confirmed fetch, pull, push or clone `GIT_NO_LAZY_FETCH=1`
and `GIT_ALLOW_PROTOCOL=none` (a partial clone fetches nothing while it
is listed; see [Security](security.md#listing-runs-nothing-and-connects-nowhere)) and, where it
switches off a repository's filter drivers, `GIT_CONFIG_COUNT`,
`GIT_CONFIG_KEY_<n>` and `GIT_CONFIG_VALUE_<n>` (every git call also
gets `-c core.fsmonitor=false -c log.showSignature=false -c
protocol.ext.allow=never -c fetch.recurseSubmodules=false -c
submodule.recurse=false`, which beat the repository's own settings:
the `ext::` transport runs a command named in the remote URL, and
fetch, pull and push never recurse into submodules); for glab and gh
`GLAB_NO_PROMPT=1`, `GH_PROMPT_DISABLED=1`, `GH_NO_UPDATE_NOTIFIER=1` and
`GITLAB_HOST` or `GH_HOST` (the deprecated `NO_PROMPT` is dropped).

| Variable | Effect |
| --- | --- |
| `GL_REPOS_CONFIG` | Config file, when `-config` is not given (then `$XDG_CONFIG_HOME/gl-repos/config.toml`, then `~/.config/gl-repos/config.toml`). |
| `GL_REPOS_CACHE_DIR` | Cache directory for project lists and model sizes (default: `~/Library/Caches/gl-repos` on macOS, `$XDG_CACHE_HOME/gl-repos` or `~/.cache/gl-repos` on Linux). |
| `GITGAZE_CONFIG`, `GITGAZE_CACHE_DIR` | Deprecated old names (gl-repos was called gitgaze), still read for one release when the new name is unset, with a warning such as `GITGAZE_CONFIG is deprecated: use GL_REPOS_CONFIG`. |
| `SHELL` | What `!` opens (`/bin/sh` if unset). |
| `GIT_SSH_COMMAND`, `GIT_SSH` | When set, gl-repos does not add its own `BatchMode=yes -o ConnectTimeout=15` ssh options. |
| `GITLAB_HOST`, `GH_HOST` | Overridden: glab and gh are always told the profile's host (`--hostname`, and these variables for `repo create`). |
| `NO_COLOR`, `TERM` | Honored by the terminal library for colors. |

git runs non-interactively (`GIT_TERMINAL_PROMPT=0`, ssh `BatchMode=yes`
and `ConnectTimeout=15` unless `GIT_SSH_COMMAND`, `GIT_SSH` or
`core.sshCommand` is set), so credentials must come from an ssh agent or
credential helper. `GIT_DIR`, `GIT_WORK_TREE` and the other variables
that pin git to one repository are dropped, for the git calls gl-repos
makes and for the tool, agents and shell it opens, so gl-repos also
works when started from a hook.
