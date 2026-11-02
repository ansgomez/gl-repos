# gl-repos on Linux

gl-repos works the same on Linux as on macOS. This page covers the Linux
install and where files live. Back to the [README](../README.md).

## 1. Install git and your forge's CLI

gl-repos needs git 2.31 or newer. For cloning from, creating on and
publishing to your forge, it also needs `glab` (GitLab), `gh` (GitHub),
or both.

| Tool | Fedora | Debian / Ubuntu | Arch |
| --- | --- | --- | --- |
| git | `sudo dnf install git` | `sudo apt install git` | `sudo pacman -S git` |
| glab | `sudo dnf install glab` | the `.deb` from the [glab releases](https://gitlab.com/gitlab-org/cli/-/releases) page | `sudo pacman -S glab` |
| gh | `sudo dnf install gh` | `sudo apt install gh`, or GitHub's own apt repository ([cli.github.com](https://cli.github.com)) for the latest version | `sudo pacman -S github-cli` |

Check them: `git --version`, `glab --version`, `gh --version`.

Optional tools:

- [lazygit](https://github.com/jesseduffield/lazygit#installation) for
  `o`.
- [Ollama](https://ollama.com/download) for the AI assistant (`i`), then
  `ollama pull gemma4:e2b`.
- The coding agents you want to start with `t` (see
  [Agents](agents-and-ai.md#agents)).

## 2. Install gl-repos

**Option A: prebuilt binary (no Go needed).** Download the archive,
`.deb` or `.rpm` for your machine (`uname -m`: `x86_64` → `amd64`,
`aarch64` → `arm64`) from the project's Releases page and check it as
shown in [Verify a release](install.md#verify-a-release). The
`.deb`/`.rpm` install `/usr/bin/gl-repos` (and pull in git); from the
archive, put the binary in your `PATH`:

```sh
tar xzf gl-repos_*_linux_amd64.tar.gz gl-repos   # arm64: *_linux_arm64.tar.gz
mkdir -p ~/.local/bin
install -m 755 gl-repos ~/.local/bin/gl-repos
export PATH="$HOME/.local/bin:$PATH"          # if that is not on PATH yet
```

**Option B: build on the Linux machine.** This needs `git` and Go 1.26
or newer. Distribution Go packages are often older; check with
`go version`, and if needed install Go from https://go.dev/dl/.

```sh
git clone https://github.com/ansgomez/gl-repos.git gl-repos
cd gl-repos && make install    # into $(go env GOPATH)/bin, usually ~/go/bin
export PATH="$(go env GOPATH)/bin:$PATH"   # if that is not on PATH yet
```

Then log in and run it as in [Getting started](getting-started.md),
from step 2.

## 3. Where things live on Linux

| What | Location |
| --- | --- |
| gl-repos config (profiles; no token) | `~/.config/gl-repos/config.toml`, or `$XDG_CONFIG_HOME/gl-repos/config.toml` if that variable is set |
| Config template | built in (`gl-repos -init`); also `config.example.toml` in the archive, `/usr/share/doc/gl-repos/` for `.deb`/`.rpm` |
| glab and gh logins (the tokens) | kept by glab and gh: in the OS keyring (Secret Service) with glab 1.123+ and recent gh, in plain text with older versions (distribution packages can be older); see [Logging in](profiles.md#logging-in-glab-and-gh) |
| Project list and model size caches (no secrets) | `~/.cache/gl-repos/`, or `$XDG_CACHE_HOME/gl-repos/`; `GL_REPOS_CACHE_DIR` overrides |
| gl-repos binary | `/usr/bin` (`.deb`/`.rpm`), wherever you put it (archive, e.g. `~/.local/bin`) or `~/go/bin` (Option B) |
