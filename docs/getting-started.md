# Getting started

From nothing to a running dashboard, step by step. Back to the [README](../README.md).

1. **Install** git 2.31+, the CLI of your forge, and, to build from
   source, Go 1.26+:

   - GitLab: [`glab`](https://gitlab.com/gitlab-org/cli)
     (`brew install glab`, or your distribution's package)
   - GitHub: [`gh`](https://cli.github.com) (`brew install gh`, or your
     distribution's package)

   On Linux, see [gl-repos on Linux](linux.md) for the package commands.
   Without either, gl-repos still lists, fetches, pulls and pushes your
   local repositories; searching your projects to clone, creating and
   publishing need one. Then:

   ```sh
   git clone https://github.com/ansgomez/gl-repos.git gl-repos && cd gl-repos
   make install                  # puts gl-repos in $(go env GOPATH)/bin
   export PATH="$(go env GOPATH)/bin:$PATH"   # if that is not on PATH yet
   gl-repos -version
   ```

   (Once a release is published, you can also download it, see
   [Install](install.md).)

2. **Log in to your forge once.** gl-repos never handles tokens; it reuses
   the login of `glab` (GitLab) or `gh` (GitHub). Do the part for the
   forge you use, or both.

   GitLab: first create a personal access token with the `api` scope at
   `https://<host>/-/user_settings/personal_access_tokens` (*Edit profile →
   Access tokens*), then:

   ```sh
   glab auth login --hostname gitlab.example.com   # choose "Token", paste the token
   glab auth status                        # should say "Logged in to gitlab.example.com as …"
   ssh -T git@gitlab.example.com           # once: accept the host key, check your key works
   ```

   GitHub:

   ```sh
   gh auth login                           # the defaults are fine
   ssh -T git@github.com                   # once: accept the host key, check your key works
   ```

   Browsing and cloning work with read scopes; **creating projects or
   subgroups (`n`, `u`) needs the `api` scope**. See
   [Logging in](profiles.md#logging-in-glab-and-gh) for the token and web choices.

3. **Write a config and check the setup.** The config is optional.
   Without it, gl-repos has a `gitlab` profile from your glab login and a
   `github` profile, and starts in the first whose CLI is installed;
   `default_profile = "github"` at the top of the config picks one.
   With it, you name your own profiles:

   ```sh
   gl-repos -init       # writes the commented example to ~/.config/gl-repos/config.toml
   $EDITOR ~/.config/gl-repos/config.toml   # uncomment a [[profile]]: host, default_group
   gl-repos -doctor     # checks config, git, tools, logins, token scopes, ssh, Ollama
   ```

   A minimal profile:

   ```toml
   [[profile]]
   name = "work"
   forge = "gitlab"
   host = "gitlab.example.com"
   default_group = "acme/tools"    # owner prefilled for new projects (n, u)
   ```

   [`config.example.toml`](../config.example.toml) lists every other key,
   commented out with its default. Top-level keys such as
   `default_profile` go before the first `[section]`.

   The AI assistant (`i`) is optional: it needs
   [Ollama](https://ollama.com) and `ollama pull gemma4:e2b`; everything
   else works without it, and `-doctor` only warns.

4. **Run it** in the folder that holds your repositories:

   ```sh
   gl-repos -d 2 ~/git      # repos up to 2 levels below ~/git
   ```

   gl-repos also starts in an empty folder, so you can clone the first
   repository there.

5. **Try the keys** shown on the bottom line: `c` clone (type to search
   your projects), `n` new repository (`tab` for a project on the active
   profile too; with the profile above, the owner is prefilled as
   `acme/tools/<name>`), `f`/`p`/`h`
   fetch/pull/push, `o` lazygit, `q` quit. If something fails, see
   [Troubleshooting](troubleshooting.md).
