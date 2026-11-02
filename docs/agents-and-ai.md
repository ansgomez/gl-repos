# Agents and AI assistant

Coding agents (`t`), the local AI assistant (`i`) and its recovery controls. Back to the [README](../README.md).

## Agents

`t` starts a coding agent in the repository under the cursor, in the same
terminal: gl-repos steps aside while it runs and refreshes the repo when you
quit it. A picker lists the configured agents; `enter` starts the first
(default), `←`/`→` choose another, `1`-`9` start that agent at once,
`esc` cancels. With a single
agent configured, `t` starts it directly. Agents that aren't installed are
marked in the picker.

Defaults are `claude`, `codex` and `kimi`. `[[agent]]` entries in the
config replace them, in order; `command` is the program and its arguments
(split into words like a shell would, `"…"` and `'…'` quoting allowed,
but nothing is expanded or run through a shell), with the repo as working
directory:

```toml
[[agent]]
name = "claude"
command = "claude"

[[agent]]
name = "codex"
command = "codex"

[[agent]]
name = "shell"
command = "zsh"
```

gl-repos adds no flags of its own, so each agent keeps its normal
permission prompts unless you put such flags in `command`.

## AI assistant

Press `i` on a repo and type a request, e.g. "commit only the docs
changes", "stash everything including untracked as wip", "make a branch
fix/login and commit there", "push my commits", "commit everything and
push it". An empty request means *commit all current changes with a good
message*: it never pushes, pulls or fetches (gl-repos drops such steps if
the model adds them, and says so in the plan).

gl-repos sends the branch info, how it stands against its upstream (in
words, with the commits not pushed yet), `git status`, staged + unstaged
diff (truncated), stashes and recent commit subjects to Ollama on
localhost. The model returns a plan built only from allow-listed steps:
`stage_all`, `stage`, `unstage`, `commit`, `stash`, `stash_pop`,
`create_branch`, `switch`, `fetch`, `pull` (ff-only), `push`. Nothing
destructive (reset, clean, force push) exists. You see the exact git
commands, wrapped but never cut, and press `y` to run them (only `y`),
`e` to edit the commit subject, `r` to regenerate, `esc`, `n` or `q` to
discard. A plan taller than the window scrolls (`↑`/`↓`, `j`/`k`,
PgUp/PgDn); `y` runs it only once you have scrolled through every line.
Right before each
step runs, gl-repos recomputes its command; if it differs from what you
confirmed (the repository changed meanwhile), nothing more runs.

The context window is sized per request (8k up to `max_ctx`) rather than a
fixed large window, which keeps memory use low.

### Recovery controls

A local model can bog down a machine. gl-repos guards every request:

- **Memory guard** — before loading a model that isn't already resident,
  gl-repos compares its size (+ KV-cache allowance) with free RAM and
  refuses if it won't fit. It remembers the real resident size from
  `ollama ps` (`model-sizes.json` in the cache directory), since file
  sizes overstate multimodal models. Press `f` to load anyway.
- **Watchdog** — responses are streamed; the request is aborted if
  nothing arrives within `load_timeout_seconds` (model load + prompt), if
  output stops for `stall_timeout_seconds`, or after `timeout_seconds`.
  Aborting closes the connection, which makes Ollama stop.
- **`esc` aborts** a running request instantly.
- **Unload on failure** — if gl-repos loaded the model and the request
  fails or is aborted, it asks Ollama to unload it immediately. Models
  another tool already had loaded are left alone (and keep their
  `keep_alive`).
- **Short keep-alive** — models gl-repos loads are unloaded after
  `keep_alive` idle time (default 2m).
