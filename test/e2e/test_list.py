import sys, os, subprocess; sys.path.insert(0, os.path.dirname(__file__))
from tdrive import *
git = lambda repo, *a: subprocess.run(["git", "-C", f"{W}/{repo}", *a], capture_output=True, text=True).stdout.strip()

# an explicitly named config must exist; the default location may be missing
r = subprocess.run([G, "-config", f"{W}/../nonexistent.toml", "-list"], cwd=W, capture_output=True, text=True)
check("-config to a missing file fails", r.returncode == 1 and "config: -config names " in r.stderr and "nonexistent.toml, which does not exist" in r.stderr, r.stderr)
r = subprocess.run([G, "-list"], cwd=W, capture_output=True, text=True, env={**os.environ, "GL_REPOS_CONFIG": f"{W}/../nonexistent.toml"})
check("GL_REPOS_CONFIG to a missing file fails", r.returncode == 1 and "config: GL_REPOS_CONFIG names " in r.stderr
      and "nonexistent.toml, which does not exist" in r.stderr, r.stderr)
bad = f"{W}/../badcolor.toml"
open(bad, "w").write('[[profile]]\nforge = "gitlab"\ncolor = "blue"\n')
r = subprocess.run([G, "-config", bad, "-list"], cwd=W, capture_output=True, text=True)
check("bad profile color rejected", r.returncode == 1 and 'color = "blue": use an ANSI color number 0-255' in r.stderr, r.stderr)
r = subprocess.run([G, "-list"], cwd=W, capture_output=True, text=True, env={**os.environ, "XDG_CONFIG_HOME": f"{W}/../no-xdg"})
check("missing default config is fine", r.returncode == 0 and "noorigin" in r.stdout, r.stderr)

s = Session([G, "-config", EMPTY_CFG], cwd=W)
check("initial load", s.wait("noorigin") and s.wait_gone("loading", 10), s.text())
dump(s, "initial")
# the root path is shortened from the left only when it doesn't fit
# (long macOS temp dirs); a short one (Linux /tmp) is shown in full
check("title summary visible despite long path", "8 repos · 1 dirty · 1 ahead · 0 behind" in s.text()
      and ("…" in s.line("gl-repos") or W in s.line("gl-repos")) and "depth 1" in s.line("gl-repos")
      and len(s.line("gl-repos").rstrip()) <= 130, s.line("gl-repos"))
check("legacy fallback: VS BASE vs origin/master", "↑1" in (row(s, "legacy") or "") and "topic" in row(s, "legacy"), row(s, "legacy"))
check("feature VS BASE ↑1", row(s, "feature").count("↑1") == 1, row(s, "feature"))
check("detached shown", "(detached)" in row(s, "detached"))
check("noorigin", "no origin" in row(s, "noorigin"))

# navigation
s.keys("j"); check("j moves", "behind" in s.cursor_row(), s.cursor_row())
s.keys("\x1b[F"); check("End key", "noorigin" in s.cursor_row(), s.cursor_row())
s.keys("g"); check("g home", "ahead" in s.cursor_row(), s.cursor_row())
s.keys("k"); check("k clamps at top", "ahead" in s.cursor_row())

# the cursor row has a faint background across the full width; others don't
def row_bg(s, needle):
    for y, l in enumerate(s.screen.display):
        if needle in l: return {s.screen.buffer[y][x].bg for x in range(s.screen.columns)}
bg = row_bg(s, "▸")
check("cursor row highlighted full width", bg is not None and len(bg) == 1 and "default" not in bg, bg)
check("other rows not highlighted", row_bg(s, "noorigin") == {"default"}, row_bg(s, "noorigin"))

# selection
s.keys(" "); check("space selects + advances", "●" in row(s, "ahead") and "behind" in s.cursor_row(), row(s, "ahead"))
s.keys("a"); check("a selects all", all("●" in (row(s, r) or "") for r in ["ahead","behind","clean","noorigin"]))
s.keys("a"); check("a again clears", not any("●" in (row(s, r) or "") for r in ["ahead","behind","noorigin"]))

# details pane
s.keys("g"); s.keys("\r", 0.4)
check("details pane", "path:" in s.text() and "origin:" in s.text() and "default branch main" in s.text(), s.text())
s.keys("\r", 0.3); check("details toggles off", "path:" not in s.text())

# attention filter: clean + behind(not yet fetched) are 'clean' -> hidden
s.keys("d", 0.4)
check("d hides healthy repos", row(s, "clean") is None and row(s, "behind") is None and row(s, "dirty") is not None and "attention only" in s.text(), s.text())
s.keys("d", 0.4); check("d restores", row(s, "clean") is not None)

# sort
s.keys("s", 0.3); check("sort attention", "sort: attention" in s.text())
rows = [l for l in s.screen.display[2:12] if l.strip()]
first_healthy = next(i for i,l in enumerate(rows) if " clean " in l and "origin/main" in l and "≡" in l)
first_attn = next(i for i,l in enumerate(rows) if "dirty" in l)
check("attention rows first", first_attn < first_healthy, "\n".join(rows))
s.keys("s", 0.3); check("sort recent", "sort: recent" in s.text())
s.keys("s", 0.3); check("sort back to name", "sort:" not in s.line("gl-repos"))

# fetch all -> behind becomes ↓1
s.keys("a"); s.keys("f"); check("a f fetches all", s.wait("↓1", 15), s.text()); s.keys("a")
check("behind shows ↓1", "↓1" in row(s, "behind"), row(s, "behind"))
check("fetch marks ✓", s.wait("✓ fetch"), s.text())

# pull on behind
s.keys("g"); s.keys("j"); s.keys("p")
check("p pulls behind", s.wait("✓ pull", 15) and "≡" in row(s, "behind"), row(s, "behind"))
check("pull fast-forwarded", git("behind", "log", "-1", "--format=%s") == "Bump README")

# push with confirm: n cancels
s.keys("g"); s.keys("h", 0.3)
check("push asks confirmation", "Push 1 repo(s)? [y/N]" in s.text(), s.text())
s.keys("n", 0.3); check("n cancels", "cancelled" in s.text() and "ahead" in git("ahead", "status", "-sb"))
s.keys("H", 0.3); check("keys ignore case (H = h)", "Push 1 repo(s)? [y/N]" in s.text(), s.text()); s.keys("n", 0.3)
# ahead is also behind after fetch (seed pushed); push should be rejected non-ff -> error shown, not crash
s.keys("h", 0.3); s.keys("y")
check("rejected push reported", s.wait("push failed in ahead", 15), s.text())
s.keys("\r", 0.4)
check("details show git error", "rejected" in s.text() or "fetch first" in s.text() or "non-fast-forward" in s.text(), s.text())
s.keys("\r", 0.3)

# feature: push -u sets upstream
s.keys("\x1b[F"); s.keys("k"); s.keys("k")
check("cursor on feature", "feature" in s.cursor_row(), s.cursor_row())
s.keys("h", 0.3); s.keys("y")
check("push -u feature", s.wait("origin/feat", 15), row(s, "feature"))
check("feature tracking", git("feature", "rev-parse", "--abbrev-ref", "@{u}") == "origin/feat")

# refresh after external change
open(f"{W}/clean/new.txt", "w").write("x")
s.keys("g"); s.keys("j"); s.keys("j"); s.keys("r")
check("cursor on clean", "clean" in s.cursor_row()[:12], s.cursor_row())
check("r refreshes", s.wait("?1", 10) and "?1" in row(s, "clean"), row(s, "clean"))
open(f"{W}/behind/x.txt", "w").write("x")
s.keys("a"); s.keys("r"); s.keys("a"); check("a r refreshes all", s.wait("?1", 5) and "?1" in (row(s, "behind") or ""), row(s, "behind"))

# push on a repo with nothing to push (clean is behind only): skipped, no ✗
s.keys("h", 0.3); s.keys("y")
check("nothing to push is not an error", s.wait("✓ push", 10) and "✗" not in row(s, "clean"), row(s, "clean"))
s.keys("\r", 0.4); check("details say nothing to push", "nothing to push" in s.text(), s.text()); s.keys("\r", 0.3)
dump(s, "after actions")

s.keys("q"); check("q quits", s.exited())

# narrow terminal: no line may wrap past 80 columns, table must stay aligned
n = Session([G, "-config", EMPTY_CFG], cwd=W, cols=80, rows=20)
n.wait("noorigin"); n.wait_gone("loading", 10); n.keys("\r", 0.5)
dump(n, "80 columns with details")
check("80 cols: summary visible", "8 repos" in n.text(), n.line("gl-repos"))
check("80 cols: details visible", "path:" in n.text() and "default branch main" in n.text(), n.text())
check("80 cols: no line exceeds width", all(len(l.rstrip()) <= 80 for l in n.screen.display))
check("80 cols: 5 rows fit above details and 3 help lines", sum(1 for nm in ["ahead","behind","clean","detached","dirty","feature","legacy","noorigin"] if row(n, nm)) == 5 and "q quit" in n.text() and "i ask AI" in n.text(), n.text())
n.keys("q"); n.exited()

# a repository whose own config names a program: listing runs nothing,
# it is marked, and f asks first
rr = os.path.abspath(f"{W}/../risky-root"); evil = f"{rr}/evil"; ran = f"{rr}/ran"
os.makedirs(evil)
subprocess.run(["git", "init", "-q", evil], check=True)
open(f"{rr}/filter.sh", "w").write(f"#!/bin/sh\necho ran >> '{ran}'\ncat\n"); os.chmod(f"{rr}/filter.sh", 0o755)
open(f"{evil}/f.txt", "w").write("x\n")
for a in (["config", "filter.x.clean", f"{rr}/filter.sh"], ["add", "f.txt"],
          ["-c", "user.name=a", "-c", "user.email=a@example.com", "commit", "-qm", "init"]):
    subprocess.run(["git", "-C", evil, *a], check=True)
open(f"{evil}/.git/info/attributes", "w").write("* filter=x\n")
os.utime(f"{evil}/f.txt", (1, 1))  # stat-dirty: plain git status would run the filter
# a repository whose own config sends a plain push to another host than
# the one status shows: no program, but off the allowlist all the same
redirect = f"{rr}/redirect"
subprocess.run(["git", "clone", "-q", evil, redirect], check=True)
subprocess.run(["git", "-C", redirect, "config", "remote.origin.pushurl", "https://attacker.example/a/b.git"], check=True)
r = subprocess.run([G, "-config", EMPTY_CFG, "-list"], cwd=rr, capture_output=True, text=True)
check("-list warns about a risky repo", "evil: its own git config runs programs (filter.x.clean); gl-repos did not run them" in r.stderr, r.stderr)
check("-list warns about a pushurl", "redirect: its own git config runs programs (push: remote.origin.pushurl); gl-repos did not run them" in r.stderr, r.stderr)
e = Session([G, "-config", EMPTY_CFG], cwd=rr)
check("risky repo listed", e.wait("evil") and e.wait_gone("loading", 10), e.text())
check("risky repo marked with !", (row(e, "evil") or "  ")[2] == "!", row(e, "evil"))
check("pushurl repo marked with !", (row(e, "redirect") or "  ")[2] == "!", row(e, "redirect"))
e.keys("\r", 0.4)
check("details name the key", "its own git config runs programs (filter.x.clean)" in e.text(), e.text())
e.keys("\r", 0.3); e.keys("f", 0.4)
check("f asks first", "Fetch 1 repo(s)? [y/N] ! own git config runs programs (filter.x.clean)" in e.text(), e.text())
e.keys("n", 0.3); check("n cancels", "cancelled" in e.text(), e.text())
check("listing ran no filter", not os.path.exists(ran))
e.keys("q"); e.exited()
# -fetch leaves it alone: no failed fetch, still marked, still nothing run
e = Session([G, "-config", EMPTY_CFG, "-fetch"], cwd=rr)
check("-fetch lists the risky repo", e.wait("evil") and e.wait_gone("loading", 10), e.text())
check("-fetch does not fail on it", "✗ fetch" not in e.text() and "fetch failed" not in e.text(), e.text())
check("-fetch keeps the mark", (row(e, "evil") or "  ")[2] == "!", row(e, "evil"))
check("-fetch ran no filter", not os.path.exists(ran))
e.keys("q"); e.exited()
print("FAILURES:", FAILS)
sys.exit(1 if FAILS else 0)
