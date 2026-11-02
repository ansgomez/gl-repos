import sys, os, subprocess, shutil; sys.path.insert(0, os.path.dirname(__file__))
from tdrive import *

# Profiles, new repositories and publishing, against fake glab/gh. Created
# projects are local bare repos; a throwaway global git config rewrites the
# forge ssh URLs to them so pushes work offline.
here = os.path.dirname(os.path.abspath(__file__))
fx = os.path.abspath(f"{W}/..")
fakebin = os.path.join(fx, "fakebin"); os.makedirs(fakebin, exist_ok=True)
shutil.copy(os.path.join(here, "fake_glab.sh"), os.path.join(fakebin, "glab"))
shutil.copy(os.path.join(here, "fake_gh.sh"), os.path.join(fakebin, "gh"))
bares, log = os.path.join(fx, "bares"), os.path.join(fx, "forge.log")
os.makedirs(bares, exist_ok=True)
gitcfg = os.path.join(fx, "gitconfig")
open(gitcfg, "w").write(f'[url "file://{bares}/"]\n\tinsteadOf = git@github.com:\n\tinsteadOf = git@git.example.test:\n')
agent = os.path.join(fx, "fakeagent")
open(agent, "w").write(f'#!/bin/sh\necho "agent $(pwd)" >> "{log}"\n'); os.chmod(agent, 0o755)
cfg = os.path.join(fx, "create.toml")
open(cfg, "w").write(f'''
[[profile]]
name = "work"
forge = "gitlab"
host = "git.example.test"
exclude = ["legal"]
[[profile]]
name = "home"
forge = "github"
[[agent]]
name = "fake"
command = "{agent}"
[[agent]]
name = "other"
command = "{agent}"
''')
env = {"PATH": fakebin + ":" + os.environ["PATH"], "GIT_CONFIG_GLOBAL": gitcfg, "FAKE_BARES": bares,
       "FAKE_LOG": log, "SHELL": "/bin/sh", "GL_REPOS_CACHE_DIR": os.path.join(fx, "cache")}
git = lambda d, *a: subprocess.run(["git", "-C", d, *a], capture_output=True, text=True).stdout.strip()
R = os.path.realpath(W)
def logged(): return open(log).read() if os.path.exists(log) else ""
def goto(s, name):
    s.keys("g", 0.2)
    for _ in range(14):
        if row(s, name) and "▸" in row(s, name): return True
        s.keys("j", 0.1)
    return False

def red_at(s, needle, line):
    """Is needle drawn in red on the screen line that reads line?"""
    for y, l in enumerate(s.screen.display):
        if line is not None and l.rstrip() == line and needle in l:
            return s.screen.buffer[y][l.index(needle)].fg == "red"
    return False
def footer(s):
    """The window's last line (inside the frame when there is one)."""
    return s.screen.display[-2 if framed(s) else -1]
def framed(s):
    """Is the window inside the red public-alert frame?"""
    top = s.screen.display[0]
    return top.startswith("╭") and s.screen.buffer[0][0].fg == "red"

s = Session([G, "-config", cfg], cwd=W, env=env)
s.wait("noorigin"); s.wait_gone("loading", 10)
check("active profile badge in title", " work · GitLab " in s.screen.display[0], s.screen.display[0])
check("help lists new keys", "n new" in s.text() and "u publish" in s.text() and "w profile" in s.text(), s.text())

# --- profile switch needs y
s.keys("w", 0.3)
check("w asks to switch", "switch profile" in s.text() and " home · GitHub " in s.text() and "y/N" in s.text(), s.text())
check("switch shows target account", "GitHub · github.com via gh" in s.text(), s.text())
s.keys("n", 0.3)
check("anything but y keeps profile", "profile unchanged: work" in s.text() and " work · GitLab " in s.screen.display[0], s.text())

# --- n: local-only repository, then the agent picker
s.keys("n", 0.3)
check("n opens new repo box", "new repository" in s.text() and "new repo ▸" in s.text() and "none, local only" in s.text(), s.text())
check("folder step: help on last line", "enter next · tab toggle remote · ctrl+u clear · esc cancel" in footer(s), footer(s))
s.keys("ahead\r", 0.3)
check("existing folder refused", "ahead already exists" in s.text(), s.text())
s.keys("\x15", 0.1); s.keys("fresh\r", 0.3)
check("review shows git init", "git init -- fresh" in s.text() and "y run" in s.text(), s.text())
check("review: help on last line", "y run · esc back" in footer(s), footer(s))
s.keys("y")
check("created and agent picker offered", s.wait("created fresh", 5) and "agent ▸" in s.text(), s.text())
s.keys("\x1b", 0.3)
check("esc stays in gl-repos on new repo", row(s, "fresh") and "▸" in row(s, "fresh") and "agent ▸" not in s.text(), s.text())
check("local repo exists", os.path.isdir(f"{W}/fresh/.git"))
check("no forge call for local-only", logged() == "", logged())

# --- switch to GitHub, new repo with remote, start agent in it
s.keys("w", 0.3); s.keys("y", 0.3)
check("switched to home", " home · GitHub " in s.screen.display[0] and "profile: home" in s.text(), s.text())
s.keys("n", 0.3); s.keys("ghnew"); s.keys("\t", 0.2)
check("tab enables remote on active forge", "create project on GitHub (github.com)" in s.text(), s.text())
s.keys("\r", 0.5)
check("owner from gh login", s.wait("as ▸ octo/ghnew", 5) and "on GitHub · github.com · private · remote origin" in s.text(), s.text())
check("path step (n): help on last line", "enter review · ctrl+v visibility · ctrl+u clear · esc back" in footer(s), footer(s))
# --- ctrl+v opens the visibility menu; only enter changes it
s.keys("\x16", 0.3)
check("ctrl+v opens visibility menu", "visibility ▸" in s.text() and "only you and members (current)" in s.text()
      and "anyone on the internet" in s.text() and "enter confirm · esc cancel" in s.text(), s.text())
check("menu: help on last line", "↑↓/jk choose · enter confirm · esc cancel" in footer(s), footer(s))
check("menu cursor on current (private)", "▸ private" in (s.line("only you and members") or ""), s.text())
check("public red in menu", red_at(s, "public", s.line("anyone on the internet")), s.line("anyone on the internet"))
s.keys("\x1b[B", 0.2); s.keys("\x1b", 0.4)
check("esc cancels menu, still private", "visibility ▸" not in s.text() and "· private ·" in s.text() and not framed(s), s.text())
s.keys("\x16", 0.3); s.keys("j", 0.2); s.keys("\r", 0.3)
check("enter confirms public", "· PUBLIC ·" in s.text() and "visibility ▸" not in s.text(), s.text())
check("framed path step: help on last line", "enter review · ctrl+v visibility · ctrl+u clear · esc back" in footer(s), footer(s))
check("public: red frame and warning", framed(s) and "anyone on the internet can see this repository" in s.text()
      and red_at(s, "PUBLIC", s.line("· PUBLIC ·")), s.text())
s.keys("\r", 0.3)
check("public review: red frame, --public", framed(s) and "y run · esc back" in footer(s) and "gh repo create octo/ghnew --public" in s.text()
      and "anyone on the internet can see this repository" in s.text() and "y run" in s.text(), s.text())
s.keys("\x1b", 0.4); s.keys("\x16", 0.3); s.keys("k", 0.2); s.keys("\r", 0.3)
check("back to private: no frame", "· private ·" in s.text() and not framed(s) and "PUBLIC" not in s.text(), s.text())
s.keys("\r", 0.3)
check("review shows exact commands", "gh repo create octo/ghnew --private" in s.text()
      and "git remote add origin git@github.com:octo/ghnew.git" in s.text() and "git init -- ghnew" in s.text(), s.text())
s.keys("\x1b", 0.2); check("esc goes back a step", "as ▸ octo/ghnew" in s.text(), s.text())
s.keys("\r", 0.2); s.keys("y")
check("created with remote", s.wait("created ghnew", 5), s.text())
s.keys("1", 1.0); s.wait("noorigin", 5)
check("agent started in new repo", f"agent {R}/ghnew" in logged(), logged())
check("gh ran outside any repo", "gh repo create octo/ghnew --private (cwd is repo: no)" in logged(), logged())
check("remote project exists", os.path.isdir(f"{bares}/octo/ghnew.git"))
check("origin set", git(f"{W}/ghnew", "remote", "get-url", "origin") == "git@github.com:octo/ghnew.git")

# --- u: publish a repo without remotes (pushes, sets upstream)
check("cursor to noorigin", goto(s, "noorigin"), s.text())
s.keys("u", 0.5)
check("publish box", "publish · noorigin" in s.text() and s.wait("as ▸ octo/noorigin", 3) and "remote origin" in s.text(), s.text())
check("publish path step: esc cancels in help", "enter review · ctrl+v visibility · ctrl+u clear · esc cancel" in footer(s), footer(s))
s.keys("\r", 0.3)
check("publish review pushes", "gh repo create octo/noorigin --private" in s.text() and "git push -u origin HEAD" in s.text(), s.text())
s.keys("y")
check("published", s.wait("published noorigin as octo/noorigin on github.com", 8), s.text())
check("pushed to remote", git(f"{bares}/octo/noorigin.git", "rev-parse", "main") == git(f"{W}/noorigin", "rev-parse", "HEAD") != "")
check("upstream tracked", s.wait("origin/main", 5) and "origin/main" in row(s, "noorigin"), row(s, "noorigin"))

# --- u on a repo that has origin, as GitLab group project: remote named after forge
s.keys("w", 0.3); s.keys("y", 0.3)
check("back on work", " work · GitLab " in s.screen.display[0], s.screen.display[0])
check("cursor to ahead", goto(s, "ahead"), s.text())
s.keys("u", 0.5)
check("second remote named gitlab", s.wait("as ▸ tester/ahead", 3) and "remote gitlab" in s.text() and "internal" not in s.text(), s.text())
check("gitlab path step: tab completes groups", "enter review · tab complete group · ctrl+v visibility · ctrl+u clear · esc cancel" in footer(s), footer(s))
s.keys("\x15", 0.1); s.keys("tools", 0.3)
check("groups suggested while typing", s.line("team/tools/") is not None and "▸ team/tools/" in s.line("team/tools/"), s.text())
s.keys("\t", 0.3)
check("tab completes group + repo name", "as ▸ team/tools/ahead" in s.text() and not any(l.startswith("▸ team/") for l in s.screen.display)
      and "in group team/tools" in s.text(), s.text())
s.keys("-mirror\r", 0.3)
check("group project uses --group", "GITLAB_HOST=git.example.test glab repo create ahead-mirror --group team/tools --private --skipGitInit" in s.text(), s.text())
check("existing upstream kept (no -u)", "git push gitlab HEAD" in s.text() and "push -u" not in s.text(), s.text())
s.keys("y")
check("published to gitlab", s.wait("published ahead as team/tools/ahead-mirror", 8), s.text())
check("origin untouched, gitlab added", git(f"{W}/ahead", "remote", "get-url", "gitlab") == "git@git.example.test:team/tools/ahead-mirror.git"
      and "origin.git" in git(f"{W}/ahead", "remote", "get-url", "origin"))
check("still tracks origin", git(f"{W}/ahead", "rev-parse", "--abbrev-ref", "@{u}") == "origin/main")
s.keys("u", 0.4)
check("no third remote", "ahead already has a remote named gitlab" in s.text(), s.text())

# --- missing subgroups: warned about in the review, created before the project
check("cursor to behind", goto(s, "behind"), s.text())
s.keys("u", 0.5); s.wait("as ▸ tester/behind", 3)
s.keys("\x15", 0.1); s.keys("team/tools/newsub/deeper/behind\r", 0.3)
flat = lambda: " ".join(s.text().split())
check("missing groups warned", s.wait("do not exist", 5) and "⚠ groups team/tools/newsub, team/tools/newsub/deeper do not exist: "
      "they will be created as new private subgroups below team/tools" in flat(), s.text())
check("subgroup commands shown in full, before the project",
      "glab api --hostname git.example.test groups -X POST -f name=newsub -f path=newsub -f parent_id=2 -f visibility=private" in flat()
      and "-f name=deeper -f path=deeper -f 'parent_id=<id of team/tools/newsub>' -f visibility=private" in flat()
      and flat().index("name=deeper") < flat().index("glab repo create behind --group team/tools/newsub/deeper"), s.text())
s.keys("y")
check("published into new subgroups", s.wait("published behind as team/tools/newsub/deeper/behind", 8), s.text())
check("groups created in order", logged().count("groups -X POST") == 2
      and logged().index("name=newsub") < logged().index("name=deeper") < logged().index("repo create behind"), logged())
check("project in new subgroup", os.path.isdir(f"{bares}/team/tools/newsub/deeper/behind.git"))
check("cursor to clean", goto(s, "clean"), s.text())
s.keys("u", 0.5); s.wait("as ▸ tester/clean", 3); s.keys("\x15", 0.1); s.keys("nogroup/x/clean\r", 0.3)
check("missing top-level group refused", s.wait("nogroup is not a group you can see on git.example.test", 5) and "as ▸" in s.text(), s.text())
s.keys("\x15", 0.1); s.keys("team/legal/clean\r", 0.3)
check("excluded owner refused at enter", "team/legal is in a group this profile excludes (exclude in the config)" in flat()
      and "checking group" not in s.text(), s.text())
# an argument wider than the review column wraps across lines, never cut
long = "a-very-long-project-name-that-is-much-wider-than-the-review-column-allows-even-in-a-terminal-of-130-columns"
s.keys("\x15", 0.1); s.keys(f"team/tools/{long}\r", 0.3)
check("review open", s.wait("will run:", 5), s.text())
check("long remote URL fully visible", f"git@git.example.test:team/tools/{long}.git" in "".join(l[15:].strip() for l in s.screen.display)
      and not any("…" in l for l in s.screen.display), s.text())
s.keys("\x1b", 0.3); s.keys("\x1b", 0.3)

# a forge error names its cause first; the details pane has the output
s.keys("u", 0.5); s.wait("as ▸ tester/clean", 3); s.keys("\x15", 0.1); s.keys("tester/noscope\r", 0.3); s.keys("y")
check("publish failure: reason first", s.wait("publish failed: the glab token needs the api scope (a legacy personal access token)", 8)
      and "enter: details" in s.text(), "\n".join(s.screen.display[-4:]))
s.keys("\r", 0.3)
check("details show the forge output", "insufficient_scope" in s.text() and "$ GITLAB_HOST=git.example.test glab repo create noscope" in s.text(), s.text())
s.keys("\r", 0.3)

# --- failures
s.keys("n", 0.3); s.keys("dup"); s.keys("\t"); s.keys("\r", 0.3); s.wait("as ▸ tester/dup", 3)
s.keys("\x15", 0.1); s.keys("bad path\r", 0.3)
check("bad project path refused", "use owner/name" in s.text(), s.text())
s.keys("\x15", 0.1); s.keys("tester/taken\r", 0.3); s.keys("y")
ok = s.wait("created dup locally, but", 8)
check("forge error reported, local repo kept", ok and "publishing failed: the name is already taken on git.example.test" in s.text()
      and row(s, "dup") is not None, "\n".join(s.screen.display[-4:]))

s.keys("q"); check("quit", s.exited())

# a detached HEAD is not published: refused before anything is created
dt = Session([G, "-config", cfg], cwd=W, env=env); dt.wait("noorigin"); dt.wait_gone("loading", 10)
before = logged()
check("cursor to detached", goto(dt, "detached"), dt.text())
dt.keys("u", 0.5)
check("detached HEAD refused", dt.wait("detached: HEAD is detached (no branch): switch to a branch first", 5)
      and logged() == before, dt.text())
dt.keys("q"); dt.exited()

# single profile: w explains
one = os.path.join(fx, "one.toml")
open(one, "w").write('[[profile]]\nname = "solo"\nforge = "github"\n')
o = Session([G, "-config", one], cwd=W, env=env); o.wait("noorigin"); o.wait_gone("loading", 10)
o.keys("w", 0.3); check("one profile: no switch", "only one profile configured" in o.text() and " solo · GitHub " in o.text(), o.text())
o.keys("q"); o.exited()

# without glab and without a host: say that glab is missing
nogl = os.path.join(fx, "nogl.toml")
open(nogl, "w").write('[[profile]]\nname = "lab"\nforge = "gitlab"\n')
n = Session([G, "-config", nogl], cwd=W, env=dict(env, PATH=git_only(fx)))  # git, but surely no glab
n.wait("noorigin"); n.wait_gone("loading", 10)
n.keys("n", 0.3); n.keys("x"); n.keys("\t"); n.keys("\r", 0.5)
check("glab missing named", n.wait("glab not found: install it", 5) and "no GitLab login found" not in n.text(), n.text())
n.keys("\x1b", 0.2); n.keys("\x1b", 0.2); n.keys("q"); n.exited()

# default_group prefills the owner of new projects
grp = os.path.join(fx, "group.toml")
open(grp, "w").write('[[profile]]\nname = "lab"\nforge = "gitlab"\nhost = "git.example.test"\ndefault_group = "team"\n')
d = Session([G, "-config", grp], cwd=W, env=env); d.wait("noorigin"); d.wait_gone("loading", 10)
check("cursor to clean", goto(d, "clean"), d.text())
d.keys("u", 0.5)
check("default_group prefilled", d.wait("as ▸ team/clean", 3) and "in group team" in d.text(), d.text())
d.keys("\x1b", 0.3); d.keys("q"); d.exited()

# -profile picks the start profile; unknown names fail clearly
p = Session([G, "-config", cfg, "-profile", "home"], cwd=W, env=env); p.wait("noorigin")
check("-profile selects start profile", " home · GitHub " in p.screen.display[0], p.screen.display[0])
p.keys("q"); p.exited()
r = subprocess.run([G, "-config", cfg, "-profile", "nope", "-list"], cwd=W, capture_output=True, text=True)
check("unknown profile rejected", r.returncode != 0 and 'no profile named "nope" (have: work, home)' in r.stderr, r.stderr)

print("FAILURES:", FAILS)
sys.exit(1 if FAILS else 0)
