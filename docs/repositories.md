# Repositories

Creating, publishing and cloning repositories, and cloning a whole GitLab group. Back to the [README](../README.md).

## New repositories and publishing

`n` creates a repository below the root folder: type the folder, then `tab`
to also create an empty project on the active profile, or leave it local
only. With a project, the next field is `group/name`: the owner is the
profile's `default_group` if set, else your user name. On GitLab, your
groups are suggested as you type (words of the path, in any order; `/`
separates words too): `↑`/`↓` pick one and `tab` completes it to
`group/<repo name>`, which you can still edit. If the group does not
exist yet, gl-repos checks on `enter` and the review warns that it will
create the missing subgroups (below the deepest existing group, with the
project's visibility but never more visible than that group, which
GitLab refuses) before the project; nothing is created until `y`. A
project more visible than its new group is refused in the review (`a
public project can't be in a private group: choose private or narrower
with ctrl+v (esc back)`).
Top-level groups are never created. Visibility is private
unless you change it: `ctrl+v` opens a menu of the forge's visibilities
(private, internal on GitLab, public) with a one-line description each;
`↑`/`↓` (or `j`/`k`) move, `enter` confirms, `esc` closes it and keeps the
current one. While public is chosen, the window gets a red frame, the
visibility reads **PUBLIC** in red and a warning says anyone on the
internet can see the repository, up to and including the review of the
commands. The exact commands are shown before `y` runs them.
The new folder is checked like a clone's: it must not exist and is never
created through a symlink or inside another repository.
Afterwards the new repository is selected and the agent picker opens:
`enter` starts the default agent in it, `esc` stays in gl-repos.

`u` publishes the selected repository: it creates the project on the
active profile, adds it as a remote and pushes the current branch. The
remote is `origin` if the repository has none, otherwise it is named after
the forge (`gitlab` or `github`), so an existing `origin` is never changed.
A branch that already tracks a remote keeps doing so. A repository without
commits is published without a push. A detached HEAD is refused before
anything is created (`HEAD is detached (no branch): switch to a branch
first (git switch -c <name>), then publish`).

The forge CLIs run in an empty temporary directory so they never touch
your repositories themselves. Remote URLs use ssh, or https if most listed
repositories use https for that host; for a host with a port
(`gitlab.example.com:8443`) the port is the web port: https URLs keep it,
ssh URLs drop it (`git@gitlab.example.com:path.git`; set another ssh
port in `~/.ssh/config`).
The create and publish git steps share the `-jobs` slots with other git
work. If a step fails, gl-repos says why
first (`publish failed: the name is already taken on <host>: choose
another · enter: details`), with a fix for the usual causes: a token
without the `api` scope, no login, a taken name, a group that is not
found. `enter` on the row shows the failed command and its output. A new
local repository is kept; if `git init` itself fails, the window stays
open with the reason. An owner in a group the profile excludes is
refused. While `u` runs, the repository is busy: no other git job starts
on it, and `u` on a busy repository says so.

## Cloning

Press `c` and start typing: suggestions from all projects you are a member
of appear as you type (words in any order, `billing payments`; path
fragments, `handbook/doc`; typos tolerated, `bilv3`). `↑`/`↓` choose, `Tab` completes
the field, `Enter` takes the highlighted project. A clone URL (anything
with `://`, with a colon before the first slash as in git's
`host:path` form, so also `user:secret@host`, starting with `git@` or
ending in `.git`) is never searched (a GitLab group's web URL on the
profile's host is looked up as that group, see [Cloning a GitLab group](#cloning-a-gitlab-group)); a phrase with a
space (`fix: parser`) is a search. It must be one of git's own forms:
`https://host/group/project(.git)` (also `http`, `git`),
`ssh://[user@]host[:port]/…`, `[user@]host:group/project.git` (also an
absolute path on the server, `host:/srv/git/project.git`),
`file:///…` or a local path (absolute, or starting with `./` or `../`,
relative to the root folder; a bare name is a search). One with
credentials (`https://oauth2:<token>@…`, `https://user@…`, a password
in an ssh URL, the scheme-less `oauth2:<token>@host/…`) is refused:
`this URL contains credentials; use an ssh URL or a credential helper
(gl-repos never passes credentials to git)`. Anything else that does not
fit (a `?`, `#`, `;` or `%` anywhere, a project path that is not plain
names) is refused with `not a clone URL gl-repos accepts: …`, which
lists the forms. A query with no local match is searched on the forge
instead, unless it is shaped like credentials (`tools?private_token=…`,
`acme/x#access_token=…`): `not searched: this looks like a URL or
credentials (user:secret@host, ?token=); paste a clone URL without
credentials, or type a project name or path`. A
search word that is also a group's name lists that group first, followed
by the matching projects, so you pick one; a group path such as
`acme/tools` opens the group directly.

The project list comes from the active profile through its CLI (`glab` or
`gh`, reusing their logins; no tokens in gl-repos). Fetching it takes a few seconds for
hundreds of projects, so it is kept in your cache directory
(`projects-<profile>@<host>.json` in `~/Library/Caches/gl-repos` on macOS,
`~/.cache/gl-repos` on Linux), read in the background when `c` opens and
refreshed when older than 10 minutes.

gl-repos lists the projects you are a member of (GitLab: your projects
and those of your groups, `membership=true`; GitHub: the repositories
you own, collaborate on or reach through an organization), not every
project your login could read. To hide some groups,
list them in `[clone] exclude` or a profile's `exclude`. A group name
matches at any level, and a path like `org/legal` matches that group and
everything below it: their projects are then
never shown, suggested, cached or cloned, and pasted URLs into them are
refused. A pasted URL whose project path is not plain names separated by
single slashes (`acme//legal/x.git`, `acme/./legal`, `acme/tools/..`) is
refused too (`not a clone URL gl-repos accepts: …`): it could reach an
excluded project on the server.

After choosing a project, gl-repos asks where to put the repository,
prefilled with the project name and relative to the root folder. The
`git clone` command is shown before Enter runs it, with the destination
as you typed it (`will run in <root>: git clone -- <url> <dest>`); git
then runs from the root folder with that destination as an absolute path
below it, plus `--progress` for the progress bar. The new repository then
joins the list. A folder name shaped like credentials
(`tools?private_token=…`) is refused before the review: `the destination
looks like a URL with credentials (user:secret@, ?token=); type a folder
name`.

### Progress and retries

A clone joins the list right away with its own progress bar
(connecting → receiving objects → resolving deltas → checking out); the
title shows how many are running and, for a group, the overall progress.
`enter` on a cloning row shows the URL, attempt and phase.

Clones are retried automatically: up to 3 attempts, 3 s and 10 s apart,
after cleaning up the failed attempt. Errors that a retry cannot fix
(repository not found, permission denied) fail at once. An attempt is
aborted when git shows no progress for 90 s (and any attempt after 2
hours, without retrying), and ssh gives up connecting after 15 s, so a dead connection (e.g. VPN off) never hangs silently. A
clone that still fails stays in the list marked `✗` with the error: `r`
retries it, `x` removes it. `x` on a running clone cancels it and removes
what was cloned so far. Quitting while clones run asks first.

### Cloning a GitLab group

Groups are suggested too (shown with a trailing `/`), and a group path or
a group's web URL (`https://gitlab.example.com/acme/tools`) works as well.
Picking one lists every project of the group and all its subgroups:

```
acme/tools → into ▸ tools
  cli/deploykit        → tools/cli/deploykit
  lib                  → tools/lib
  old  archived        (hidden unless tab)
```

The destination folder is named after the group, and the subgroups become
nested folders inside it. Archived projects are hidden unless you press
`tab`; projects only shared into the group are left out. Projects that
already exist are skipped, so running it again later clones only new
ones. `enter` asks `Clone N projects into tools/? [y/N]`; all projects
then appear in the list at once, each with its progress bar, and are
cloned in parallel (`-jobs` at a time). If they lie deeper than the current `-d`, gl-repos tells you
which depth to start with next time. Only GitLab profiles have groups.

The GitLab host is the profile's `host` (or `[clone] host`), else the
GitLab glab is logged in to, preferring the most common origin host of the
listed repositories. gl-repos also starts in a folder
without repositories, so you can clone the first one there.

The AI assistant understands clone requests too ("clone the handbook
documentation repo") and hands them to the same flow.
