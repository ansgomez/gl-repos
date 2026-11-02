# Profiles and logins

Profiles, and how to log in with glab and gh. Back to the [README](../README.md).

## Profiles

A profile is one account: GitLab through
[`glab`](https://gitlab.com/gitlab-org/cli) or GitHub through
[`gh`](https://cli.github.com), each with its own login (`glab auth
login`, `gh auth login`). gl-repos never handles their tokens. One profile is active
at a time and is always shown as a colored badge in the title, e.g.
` work · GitLab `. `c`, `n` and `u` use only the active profile.
`w` switches, and only after you confirm with `y`. With more than two
profiles, `←`/`→` choose the target first.

Without `[[profile]]` in the config there are two: `gitlab` (host from
`[clone] host` or your glab login) and `github`. gl-repos starts in
`default_profile` / `-profile` if set; otherwise in the first configured
profile, or, without `[[profile]]`, in the first built-in one whose CLI
(`glab`, `gh`) is installed. The project and group lists are cached per
profile and host, each without that profile's excluded groups (a group
excluded later leaves the cached lists the next time they are read).

```toml
default_profile = "work"      # top-level: before the first [section] or [[profile]]

[[profile]]
name = "work"
forge = "gitlab"
host = "gitlab.example.com"   # default: the GitLab glab is logged in to
color = "4"                   # badge color: ANSI number 0-255 or "#rrggbb"

[[profile]]
name = "personal"
forge = "github"
color = "5"
```

### Logging in (glab and gh)

Each profile needs its CLI installed and logged in to the profile's host
before `c`, `n` or `u` can reach it:

```sh
glab auth login --hostname gitlab.example.com   # GitLab (self-hosted or gitlab.com)
gh auth login                                   # GitHub
glab auth status; gh auth status                # check
```

What the login may do depends on its token:

| Use | GitLab token scopes | GitHub (`gh`) |
| --- | --- | --- |
| Browse and search projects and groups (`c`, group autocomplete in `n`/`u`) | `read_api` | default `gh auth login` |
| Clone | none from glab: git clones over ssh (or https with your credential helper) | none from gh, as for GitLab |
| Create projects and missing subgroups (`n`, `u`) | **`api`** | default `gh auth login` (`repo` scope) |

`glab auth login` asks how to log in:

- **Token** (recommended): create a personal access token in GitLab
  (*Edit profile → Access tokens*) with the `api` scope, then choose
  *Token* and paste it, or run `glab auth login --hostname
  gitlab.example.com --stdin < token.txt` (keeps it out of your shell
  history). Everything works, and `gl-repos -doctor` can show the token's
  scopes. The token expires on the date you set.
- **Web** (browser, OAuth): no token to manage, but its scopes are those
  of glab's OAuth application, and on a self-managed GitLab it only
  works if an administrator set one up. `gl-repos -doctor` cannot read
  the scopes of such a login (`token scopes can't be read (web/OAuth
  login?)`); if `n`/`u` fail with 403, log in with a token instead.

`gh auth login` (web or token) gives the `repo` scope by default, which
is all gl-repos needs; for GitHub Enterprise add `--hostname <host>`.

glab 1.123 and newer, and recent gh versions, store the token in the OS
keyring by default (gl-repos never reads it); older versions, or a
system without a working keyring, keep it in plain text in the CLI's
config file. `gl-repos -doctor` reports which when the CLI's status
says so (older glab does not; upgrade it to see this). If it says `glab keeps the token in plain text
in its config file` (or `gh …`), run the login again (`glab auth login
--hostname <host>`, `gh auth login`) to move it into the keyring.

On GitLab, use a classic (legacy, scope-based) personal access token
with the `api` scope if you want to create projects or subgroups; a
token with only `read_api` is enough for browsing and searching, but
creating fails with `insufficient_scope` (see
[Troubleshooting](troubleshooting.md#token-lacks-the-api-scope-insufficient_scope)). Creating subgroups also needs a
role that may create subgroups in the parent group (Maintainer or Owner
by default). To change the token, run `glab auth login --hostname …`
again.

glab keeps one login per host, so gl-repos and its sibling gl-issues
share the same token. For both tools, tick only `api` in GitLab's
*Select scopes* list; it includes everything `read_api` and `read_user`
allow. The full list, scope by scope:

| Scope | gl-repos | gl-issues | Tick it? |
| --- | --- | --- | --- |
| `api` | create projects and subgroups (`n`, `u`) | comment, assign, due date, describe, close/reopen, new issues | **Yes** |
| `read_api` | browsing and search only | viewing only | Only for a read-only token instead of `api` |
| `read_user` | not used (`api`/`read_api` cover `glab auth status`) | not used | No |
| `read_repository`, `write_repository` | not used by gl-repos itself; git may use them over https if glab is its credential helper | not used | Only if git uses this token over https (glab as git's credential helper): `write_repository` to push |
| `self_rotate` | not used | not used | Optional: lets the token rotate itself |
| `read_registry`, `write_registry`, `read_virtual_registry`, `write_virtual_registry`, `ai_features`, `create_runner`, `manage_runner`, `k8s_proxy` | not used | not used | No |
| `sudo`, `admin_mode` (administrators only) | not used | not used | No |

gl-repos never hands the glab/gh token to git (clone, fetch, pull,
push): git uses your ssh key or your https credential helper (which,
if you set it to glab or gh, uses their token), and never prompts (see
[Environment](configuration.md#environment)). Load your key into `ssh-agent` and connect
once with `ssh -T git@<host>` so its host key is known.
