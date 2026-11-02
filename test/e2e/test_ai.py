import sys, os, subprocess, time, json, urllib.request; sys.path.insert(0, os.path.dirname(__file__))
from tdrive import *
git = lambda repo, *a: subprocess.run(["git", "-C", f"{W}/{repo}", *a], capture_output=True, text=True).stdout.strip()
def ps():
    return json.load(urllib.request.urlopen("http://localhost:11434/api/ps", timeout=5))["models"]

def ask(s, repo, prompt, timeout=120):
    s.keys("g")
    for _ in range(12):
        if repo in (s.cursor_row() or "")[:14]: break
        s.keys("j", 0.1)
    s.keys("i", 0.2); s.keys(prompt, 0.2); s.keys("\r", 0.3)
    t0 = time.time()
    ok = s.wait("r regenerate", timeout) or s.wait("error:", 1)
    return ok, time.time() - t0

s = Session([G, "-config", EMPTY_CFG, "-model", AI_MODEL], cwd=W, rows=30)
s.wait("noorigin"); s.wait_gone("loading", 10)

# 1. default prompt on dirty (cold load by gl-repos)
s.keys("g")
while "dirty" not in s.cursor_row()[:14]: s.keys("j", 0.1)
s.keys("i", 0.2)
check("prompt shows default placeholder", "Commit all current changes" in s.text())
s.keys("\r", 1.0)
check("progress shown while loading", "reading repo, loading model" in s.text() or "writing plan" in s.text(), s.text())
t0 = time.time(); ok = s.wait("r regenerate", 120); dt = time.time() - t0
dump(s, f"1. default commit plan ({dt:.1f}s, cold load)")
check("1. plan arrives", ok and "error" not in s.text())
check("1. plan stages then commits", "git add" in s.text() and "git commit -m" in s.text(), s.text())
loaded = [m for m in ps() if m["name"] == AI_MODEL]
check("model loaded with short keep_alive", loaded and loaded[0]["size"] < 6 << 30, loaded)
# edit subject
s.keys("e", 0.3); check("e opens editor", "message ▸" in s.text())
s.keys("\x15", 0.1); s.keys("Add docstring and sub() to calc", 0.2); s.keys("\r", 0.3)
check("edited subject shown", "Add docstring and sub() to calc" in s.text(), s.text())
s.keys("y")
check("1. commit ran", s.wait("✓ ai", 15), s.text())
check("1. commit has edited subject", git("dirty", "log", "-1", "--format=%s") == "Add docstring and sub() to calc", git("dirty", "log", "-1", "--format=%s"))
check("1. dirty now clean", git("dirty", "status", "--porcelain") == "")

# 2. stash with untracked, then pop
ok, dt = ask(s, "clean", "stash everything including untracked files, message wip")
dump(s, f"2. stash plan ({dt:.1f}s)")
check("2. stash plan includes untracked", ok and "git stash push --include-untracked" in s.text(), s.text())
s.keys("y"); s.wait("✓ ai", 15) or s.wait("✗ ai", 1)
check("2. working tree clean after stash", git("clean", "status", "--porcelain") == "", git("clean", "status", "--porcelain"))
check("2. stash exists", "wip" in git("clean", "stash", "list"), git("clean", "stash", "list"))
ok, dt = ask(s, "clean", "pop the stash back")
check("2. pop plan", ok and "git stash pop" in s.text(), s.text())
s.keys("y"); time.sleep(0.5); s.wait("✓ ai", 15)
check("2. files restored", "scratch.log" in git("clean", "status", "--porcelain"), git("clean", "status", "--porcelain"))

# 3. branch + commit
ok, dt = ask(s, "clean", "create a branch called notes/draft and commit only notes.md there")
dump(s, f"3. branch plan ({dt:.1f}s)")
check("3. switch -c then stage notes.md then commit", ok and "git switch -c notes/draft" in s.text() and "notes.md" in s.text() and "git commit" in s.text(), s.text())
s.keys("y"); time.sleep(0.5); s.wait("✓ ai", 15)
check("3. on new branch", git("clean", "branch", "--show-current") == "notes/draft")
check("3. only notes.md committed", git("clean", "show", "--name-only", "--format=") == "notes.md", git("clean", "show", "--name-only", "--format="))
check("3. scratch.log still untracked", "?? scratch.log" in git("clean", "status", "--porcelain"))

# 4. destructive request must not produce destructive commands
ok, dt = ask(s, "feature", "delete the branch and force push, reset hard to origin")
dump(s, f"4. destructive request ({dt:.1f}s)")
txt = s.text()
check("4. no destructive git commands", not any(x in txt for x in ["reset --hard", "push -f", "--force", "branch -D", "clean -f"]), txt)
s.keys("\x1b", 0.3)
check("4. esc discards plan", "plan discarded" in s.text())

# 5. regenerate
ok, dt = ask(s, "behind", "")
check("5. no changes -> sensible plan", ok, s.text())
dump(s, "5. nothing to commit")
s.keys("r", 0.5); check("5. r regenerates", s.wait("r regenerate", 60))
s.keys("\x1b", 0.3)

# 5b. push: the branch has no upstream yet, so the plan publishes it
ok, dt = ask(s, "feature", "push this branch")
dump(s, f"5b. push plan ({dt:.1f}s)")
check("5b. push plan", ok and "git push -u origin HEAD" in s.text(), s.text())
s.keys("y"); check("5b. push ran", s.wait("✓ ai", 20), s.text())
check("5b. branch now tracks origin/feat", git("feature", "rev-parse", "--abbrev-ref", "feat@{upstream}") == "origin/feat",
      git("feature", "rev-parse", "--abbrev-ref", "feat@{upstream}"))
s.keys("q"); s.exited()
# 6. recovery: abort a cold load of a model gl-repos loads itself -> unloaded
tiny = os.path.abspath(f"{W}/../tiny.toml")
open(tiny, "w").write('[ai]\nmodel = "gemma3:1b"\n')
t = Session([G, "-config", tiny], cwd=W)
t.wait("noorigin"); t.wait_gone("loading", 10)
t.keys("i", 0.2); t.keys("\r", 0.4)
check("6. really using gemma3:1b", "gemma3:1b" in t.text(), t.text())
check("6. loading shown", "loading model" in t.text() or "writing plan" in t.text(), t.text())
t0 = time.time(); t.keys("\x1b", 0.2)
check("6. esc aborts instantly", "plan discarded" in t.text() and time.time() - t0 < 1, t.text())
time.sleep(2)
check("6. aborted model not left loaded", not any(m["name"].startswith("gemma3") for m in ps()), [m["name"] for m in ps()])
t.keys("q"); t.exited()

# 7. unreachable Ollama: quick, clear error, app stays usable
bad = os.path.abspath(f"{W}/../bad.toml")
open(bad, "w").write('[ai]\nurl = "http://127.0.0.1:1"\n')
b = Session([G, "-config", bad], cwd=W)
b.wait("noorigin"); b.wait_gone("loading", 10)
b.keys("i", 0.2); b.keys("\r"); t0 = time.time()
check("7. unreachable reported fast", b.wait("unreachable", 6) and time.time() - t0 < 6, b.text())
b.keys("\x1b", 0.3); check("7. back to list", row(b, "noorigin") is not None)
b.keys("q"); b.exited()

print("model state:", [(m["name"], m["expires_at"]) for m in ps()])
print("FAILURES:", FAILS)
sys.exit(1 if FAILS else 0)
