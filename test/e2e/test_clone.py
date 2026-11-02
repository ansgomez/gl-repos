import sys, os, subprocess, time, shutil, tempfile, json; sys.path.insert(0, os.path.dirname(__file__))
from tdrive import *

here = os.path.dirname(os.path.abspath(__file__))
fx = os.path.abspath(f"{W}/..")
fakebin = os.path.join(fx, "fakebin"); os.makedirs(fakebin, exist_ok=True)
shutil.copy(os.path.join(here, "fake_glab.sh"), os.path.join(fakebin, "glab"))
shutil.copy(os.path.join(here, "fake_gh.sh"), os.path.join(fakebin, "gh"))  # never the real gh
cfg = os.path.join(fx, "clone.toml")
open(cfg, "w").write('[clone]\nhost = "git.example.test"\nexclude = ["legal", "finance"]\n')
cache = os.path.join(fx, "cache")
env = {"PATH": fakebin + ":" + os.environ["PATH"], "FAKE_ORIGIN": os.path.join(fx, "origin.git"),
       "GL_REPOS_CACHE_DIR": cache}
git = lambda d, *a: subprocess.run(["git", "-C", d, *a], capture_output=True, text=True).stdout.strip()
def clear(s): s.keys("\x15", 0.15)

s = Session([G, "-config", cfg], cwd=W, env=env)
s.wait("noorigin"); s.wait_gone("loading", 10)

# --- autocomplete from the (paginated) project list
s.keys("c", 0.3)
check("c opens clone with host", "clone · git.example.test" in s.text() and "clone ▸" in s.text(), s.text())
check("project list loads, excluded groups dropped", s.wait("4 projects ·", 5), s.text())
check("legal/finance never suggested", "secret-calc" not in s.text() and "payroll-calc" not in s.text(), s.text())
check("empty input lists recent projects", all(p in s.text() for p in ["team/calc", "calc-docs", "billing-v3", "broken"]), s.text())
s.keys("calc", 0.3)
check("typing filters suggestions", "calc-docs" in s.text() and "billing-v3" not in s.text(), s.text())
check("best match highlighted", "▸ team/calc" in s.text(), s.text())
y = next(i for i, l in enumerate(s.screen.display) if "▸ team/calc" in l)
check("suggestion row has background", s.screen.buffer[y][s.screen.columns - 1].bg != "default")
s.keys("\x1b[B", 0.2)
check("down moves highlight", "▸ team/docs/calc-docs" in s.text(), s.text())
s.keys("\t", 0.2)
check("tab completes full path", "clone ▸ team/docs/calc-docs" in s.text(), s.text())
dump(s, "autocomplete")
s.keys("\r", 0.3)
check("enter takes suggestion to destination", "into ▸ calc-docs" in s.text() and "project: team/docs/calc-docs" in s.text(), s.text())
check("exact command shown", "git clone -- " + env["FAKE_ORIGIN"] + " calc-docs" in s.text(), s.text())
clear(s); s.keys("clean", 0.3)
check("existing dest rejected", "clean already exists" in s.text(), s.text())
clear(s); s.keys("../outside", 0.3)
check("dest outside root rejected", "must be inside" in s.text(), s.text())
s.keys("\r", 0.3); check("enter does nothing while invalid", "into ▸" in s.text())
clear(s); s.keys("libs/calc-docs", 0.3)
s.keys("\r")
check("clone finishes, row added", s.wait("cloned team/docs/calc-docs into libs/calc-docs", 15), s.text())
s.wait_gone("loading", 5)
r = row(s, "libs/calc-docs")
check("new row loaded with status", r is not None and "origin/main" in r and "▸" in r, r)
check("repo really cloned", git(f"{W}/libs/calc-docs", "log", "-1", "--format=%s") == "Bump README")
cf = os.path.join(cache, "projects-gitlab@git.example.test.json")
check("cache file written", os.path.exists(cf))
check("excluded groups never cached", os.path.exists(cf) and not any(x in open(cf).read() for x in ["legal", "finance", "secret-calc", "payroll-calc"]))

# excluded groups stay out of every path
s.keys("c", 0.2); s.keys("calc", 0.3)
check("typing does not reveal excluded", "secret-calc" not in s.text() and "payroll-calc" not in s.text(), s.text())
clear(s); s.keys("hidden"); s.keys("\r")
check("server search drops excluded", s.wait("no matching project", 5) and "hidden-calc" not in s.text(), s.text())
s.keys("\x1b", 0.2); clear(s)
s.keys("team/legal/secret-calc"); s.keys("\r")
check("direct path lookup of excluded refused", s.wait("no matching project", 5), s.text())
s.keys("\x1b", 0.2); clear(s)
s.keys("git@git.example.test:team/finance/payroll-calc.git", 0.2); s.keys("\r", 0.3)
check("excluded URL refused", "team/finance/payroll-calc is in an excluded group" in s.text(), s.text())
s.keys("\x1b", 0.2)

# fuzzy (subsequence) match
s.keys("c", 0.3); s.keys("bilv3", 0.3)
check("subsequence finds billing-v3", "▸ team/svc/payments/billing-v3" in s.text(), s.text())
s.keys("\x1b", 0.2)

# typed path matches its project
s.keys("c", 0.2); s.keys("team/calc", 0.3); s.keys("\r", 0.3)
check("typed path selects that project", "project: team/calc" in s.text() and "into ▸ calc" in s.text(), s.text())
s.keys("\x1b", 0.3); check("esc returns to query", "clone ▸ team/calc" in s.text(), s.text())
s.keys("\x1b", 0.3); check("esc closes clone", row(s, "noorigin") is not None)

# not in the list -> enter searches GitLab
s.keys("c", 0.2); s.keys("remoteonly", 0.3)
check("no local match hint", "no match in your projects" in s.text(), s.text())
s.keys("\r")
check("falls back to server search", s.wait("project: team/new/remoteonly", 5), s.text())
s.keys("\x1b", 0.2); clear(s)
s.keys("multi", 0.2); s.keys("\r")
check("server results picker", s.wait("2 matches", 5) and "team/multi-b" in s.text(), s.text())
s.keys("\x1b[B", 0.2); s.keys("\r", 0.3)
check("pick from server results", "project: team/multi-b" in s.text(), s.text())
s.keys("\x1b", 0.2); s.keys("\x1b", 0.2); s.keys("\x1b", 0.2)

# clone URL directly
s.keys("c", 0.2); s.keys(env["FAKE_ORIGIN"]); s.keys("\r", 0.3)
check("URL skips search and suggestions", "into ▸ origin" in s.text(), s.text())
clear(s); s.keys("via-url"); s.keys("\r")
check("URL clone works", s.wait("cloned", 15) and s.wait("via-url", 3), s.text())

# nothing anywhere
s.keys("c", 0.2); s.keys("zzz", 0.2); s.keys("\r")
check("no match reported", s.wait("no matching project", 5), s.text())
s.keys("\x1b", 0.2); s.keys("\x1b", 0.2)

# server search can be cancelled
s.keys("c", 0.2); s.keys("slow"); s.keys("\r", 0.5)
check("searching shown", "searching your projects" in s.text(), s.text())
t0 = time.time(); s.keys("\x1b", 0.3)
check("esc cancels search instantly", "search cancelled" in s.text() and time.time() - t0 < 1, s.text())

# failing clone is reported and stays listed as failed
s.keys("c", 0.2); s.keys("broken", 0.3); s.keys("\r", 0.3)
s.wait("into ▸ broken", 5); s.keys("\r")
check("failed clone reported", s.wait("clone of team/broken failed", 15), s.text())
check("failed clone row marked", "✗ clone failed" in (row(s, "broken") or ""), row(s, "broken"))
check("no leftover dir", not os.path.exists(f"{W}/broken"))
s.keys("q"); s.exited()

# cached list is used immediately next time (no refetch within 10 min)
os.utime(os.path.join(cache, "projects-gitlab@git.example.test.json"))
c2 = Session([G, "-config", cfg], cwd=W, env=dict(env, PATH=git_only(fx)))  # no glab at all
c2.wait("noorigin"); c2.wait_gone("loading", 10)
c2.keys("c", 0.3)
check("cached list shown without glab", "4 projects ·" in c2.text() and "loading your project list" not in c2.text(), c2.text())
c2.keys("calc", 0.2)
check("suggestions from cache", "▸ team/calc" in c2.text(), c2.text())
c2.keys("\x1b", 0.2); c2.keys("q"); c2.exited()

# start in an empty folder without host config: host from glab login, then list
nohost = os.path.join(fx, "nohost.toml"); open(nohost, "w").write("")
empty = tempfile.mkdtemp(dir=fx)
e = Session([G, "-config", nohost], cwd=empty, env=env)
check("empty folder opens with hint", e.wait("press c to clone", 5), e.text())
e.keys("c", 0.2)
check("host resolved from glab login", e.wait("clone · git.example.test", 5), e.text())
check("list available after host resolved", e.wait("4 projects", 5), e.text())
e.keys("team/calc", 0.2); e.keys("\r", 0.3); e.keys("\r")
check("first repo cloned into empty folder", e.wait("cloned", 15) and e.wait_gone("loading", 5) and row(e, "calc") is not None, e.text())
check("hint gone", "press c to clone" not in e.text())
e.keys("q"); e.exited()

# no glab login and no host: c says so at once, with the fix
nl = Session([G, "-config", nohost], cwd=tempfile.mkdtemp(dir=fx), env=dict(env, FAKE_GLAB_AUTH="none"))
check("empty folder names dir and depth", nl.wait("searched to depth 1 (try -d 2) in ", 5), nl.text())
nl.keys("c", 0.2)
check("no login explained in c", nl.wait("no GitLab host found: glab auth login --hostname <host>, or set [clone] host", 5)
      and "from your glab login" not in nl.text() and "no project list yet" not in nl.text(), nl.text())
nl.keys("\x1b", 0.2); nl.keys("q"); nl.exited()

# AI handoff (real Ollama; only with --ai)
if os.environ.get("E2E_AI"):
    a = Session([G, "-config", cfg, "-model", AI_MODEL], cwd=W, env=env)
    a.wait("noorigin"); a.wait_gone("loading", 10)
    a.keys("i", 0.2); a.keys("please clone the multi project from gitlab"); a.keys("\r")
    check("AI hands clone requests to clone flow", a.wait("2 matches", 60), a.text())
    dump(a, "AI clone handoff")
    a.keys("\x1b", 0.2); a.keys("\x1b", 0.2); a.keys("q"); a.exited()

# without [clone] exclude every project is listed
dcfg = os.path.join(fx, "clone-default.toml")
open(dcfg, "w").write('[clone]\nhost = "git.example.test"\n')
d = Session([G, "-config", dcfg], cwd=W, env={**env, "GL_REPOS_CACHE_DIR": os.path.join(fx, "cache-default")})
d.wait("noorigin"); d.wait_gone("loading", 10); d.keys("c", 0.3)
check("default excludes nothing", d.wait("6 projects ·", 5) and "secret-calc" in d.text() and "payroll-calc" in d.text(), d.text())
d.keys("\x1b", 0.2); d.keys("q"); d.exited()

# a fresh project list without a group list (saved by an older gl-repos)
# still fetches the groups right away
fresh = os.path.join(fx, "cache-nogroups"); os.makedirs(fresh, exist_ok=True)
pf = json.load(open(os.path.join(cache, "projects-gitlab@git.example.test.json")))
pf["fetched"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
json.dump(pf, open(os.path.join(fresh, "projects-gitlab@git.example.test.json"), "w"))
f2 = Session([G, "-config", cfg], cwd=W, env={**env, "GL_REPOS_CACHE_DIR": fresh})
f2.wait("noorigin"); f2.wait_gone("loading", 10); f2.keys("c", 0.3); f2.keys("tools", 0.2)
check("groups fetched even when project list is fresh", f2.wait("▸ team/tools/", 5), f2.text())
check("group list cached", os.path.exists(os.path.join(fresh, "groups-gitlab@git.example.test.json")))
f2.keys("\x1b", 0.2); f2.keys("q"); f2.exited()

# --- clone a whole group into nested folders
g = Session([G, "-config", cfg], cwd=W, env=env)
g.wait("noorigin"); g.wait_gone("loading", 10)
g.keys("c", 0.5); g.wait("2 groups", 5)
g.keys("tools", 0.3)
check("groups suggested", "▸ team/tools/" in g.text() and "group: clone all its projects" in g.text(), g.text())
g.keys("\r", 0.3)
check("group preview lists subgroup projects", g.wait("2 projects · 1 archived hidden", 5) and "cli/deploy" in g.text() and "lib" in g.text(), g.text())
check("shared-in project left out", "shared" not in g.text(), g.text())
g.keys("\t", 0.2); check("tab includes archived", "3 projects · archived included" in g.text() and "old  archived" in g.text(), g.text())
g.keys("\t", 0.2)
check("destination is the group name", "into ▸ tools" in g.text() and "will run 2 × git clone" in g.text(), g.text())
g.keys("\r", 0.3); check("asks to confirm", "Clone 2 projects into tools/? [y/N]" in g.text(), g.text())
g.keys("y")
check("group cloned", g.wait("cloned 2 of 2 projects of team/tools", 15), g.text())
check("depth hint", "next time start with -d 3" in g.text(), g.text())
check("nested folders", os.path.isdir(f"{W}/tools/cli/deploy/.git") and os.path.isdir(f"{W}/tools/lib/.git"))
check("rows listed", row(g, "tools/cli/deploy") is not None and row(g, "tools/lib") is not None, g.text())
# again via the group's web URL: everything is already here
g.keys("c", 0.4); g.keys("https://git.example.test/team/tools"); g.keys("\r", 0.5)
check("group URL opens preview", g.wait("clone group · team/tools", 5) and g.wait("2 already here", 5), g.text())
g.keys("\r", 0.3); check("nothing to clone again", "nothing to clone" in g.text() and "[y/N]" not in g.text(), g.text())
g.keys("\x1b", 0.3); check("esc back to query", "clone ▸" in g.text(), g.text())
g.keys("\x15", 0.1); g.keys("tools group"); g.keys("\r", 0.5)
check("'tools group' search opens the group", g.wait("clone group · team/tools", 5), g.text())
g.keys("\x1b", 0.2); g.keys("\x1b", 0.2); g.keys("q"); g.exited()

# --- clone rows: progress, automatic retry, failure, cancel, quit
# A fake ssh: host "flaky" fails the first connection, then serves the
# local origin; host "hang" never answers.
fakessh = os.path.join(fx, "fakessh")
open(fakessh, "w").write(f"""#!/bin/sh
for a; do last="$a"; done
case "$*" in
  *-G*) exit 0 ;;   # git probing the ssh variant
  *flaky*) n=$(cat {fx}/flaky.count 2>/dev/null || echo 0); echo $((n+1)) > {fx}/flaky.count
           [ "$n" -lt 1 ] && {{ echo "ssh: connect to host flaky port 22: Operation timed out" >&2; exit 255; }}
           exec sh -c "$last" ;;
  *hang*) sleep 60 ;;
esac
""")
os.chmod(fakessh, 0o755)
origin = os.path.join(fx, "origin.git")
j = Session([G, "-config", cfg], cwd=W, env={**env, "GIT_SSH_COMMAND": fakessh})
j.wait("noorigin"); j.wait_gone("loading", 10)
def clone_url(s, url, dest):
    s.keys("c", 0.4); s.keys(url, 0.1); s.keys("\r", 0.4); s.keys("\x15", 0.1); s.keys(dest, 0.1); s.keys("\r", 0.3)
clone_url(j, f"ssh://flaky{origin}", "flaky1")
check("row listed at once", row(j, "flaky1") is not None, j.text())
check("failed attempt retried automatically", j.wait("retrying (attempt 1/3 failed", 5), row(j, "flaky1"))
check("retry succeeds", j.wait("cloned ssh://flaky", 15) and "retrying" not in (row(j, "flaky1") or ""), j.text())
check("cloned repo gets status", j.wait_row("flaky1", "origin/main", 10), row(j, "flaky1"))

clone_url(j, "/nonexistent/repo.git", "broken1")
check("permanent error fails at once", j.wait("✗ clone failed", 5) and "r retry · x remove" in row(j, "broken1") and "attempt(s)" in j.text(), j.text())
check("not retried", "failed after 1 attempt(s)" in j.text(), j.text())
j.keys("o", 0.3); check("tools refused on failed clone", "failed to clone: r retries, x removes" in j.text(), j.text())
j.keys("x", 0.3); check("x removes failed row", row(j, "broken1") is None and not os.path.exists(f"{W}/broken1"), j.text())

clone_url(j, "ssh://hang/x.git", "hang1")
check("progress row while connecting", j.wait_row("hang1", "connecting", 5) and "░" in (row(j, "hang1") or ""), row(j, "hang1"))
check("title counts clones", "cloning 1" in j.screen.display[0], j.screen.display[0])
j.keys("q", 0.3); check("quit asks while cloning", "1 operation(s) still running (clone, fetch, pull, push, AI steps…): quit and stop them? [y/N]" in j.text(), j.text())
j.keys("n", 0.3); check("n keeps running", "clones keep running" in j.text() and row(j, "hang1") is not None, j.text())
j.keys("x", 0.5)
check("x cancels running clone", j.wait("clone of ssh://hang/x.git cancelled", 8) and row(j, "hang1") is None, j.text())
check("no leftovers", not os.path.exists(f"{W}/hang1"))
j.keys("q"); check("quits when idle", j.exited())

print("FAILURES:", FAILS)
sys.exit(1 if FAILS else 0)
