import sys, os, subprocess; sys.path.insert(0, os.path.dirname(__file__))
from tdrive import *
# /bin/sh starts instantly; the user's interactive shell may take seconds
# and drop typed-ahead input, which would test their dotfiles, not gl-repos.
s = Session([G, "-config", EMPTY_CFG], cwd=W, rows=24, env={"SHELL": "/bin/sh"})
s.wait("noorigin"); s.wait_gone("loading", 10)

# graph on 'ahead' (cursor starts there)
s.keys("l"); check("graph opens", s.wait("commit graph · ahead") and s.wait("Merge branch 'side'"), s.text())
check("graph shows branch structure", any(c in s.text() for c in ["|\\", "|/", "* |"]), s.text())
check("graph shows decorations", "HEAD -> main" in s.text(), s.text())
top = s.screen.display[1]
s.keys("\x1b[6~", 0.4)  # pgdown
check("graph scrolls", s.screen.display[1] != top, s.screen.display[1])
s.keys("\x1b", 0.4); check("esc back to list", row(s, "ahead") is not None and "commit graph" not in s.text())
s.keys("l"); s.wait("commit graph"); s.keys("q", 0.4)
check("q in graph returns (does not quit)", row(s, "ahead") is not None)

# shell: '!' suspends, runs $SHELL in repo dir; create a file, exit -> refresh shows it
s.keys("j"); check("cursor behind", row(s, "behind") and "▸" in row(s, "behind"))
s.keys("!", 1.0)
s.keys("pwd > /dev/null; touch from_shell.txt; exit\r", 1.5)
check("shell returned to gl-repos", s.wait("noorigin", 5), s.text())
check("refresh after shell shows new file", s.wait("?1", 5) and "?1" in row(s, "behind"), row(s, "behind"))
check("file created in repo dir", __import__("os").path.exists(f"{W}/behind/from_shell.txt"))

# lazygit: 'o' opens it in the repo, q returns
s.keys("o", 0.5)
ok = s.wait("behind → main", 10)
dump(s, "lazygit")
check("lazygit opened", ok, s.text())
check("lazygit in right repo", "behind → main" in s.text(), s.text())
s.keys("q", 1.5)
check("back from lazygit", s.wait("noorigin", 6), s.text())

# agents: 't' with the default config lists claude/codex/kimi (not started here)
s.keys("t", 0.4)
check("t opens agent picker", "agent ▸" in s.text() and all(a in s.text() for a in ["1 claude", "2 codex", "3 kimi"]), s.text())
check("agent help names repo", "enter start in behind" in s.text(), s.text())
s.keys("\x1b", 0.3); check("esc closes picker", "agent ▸" not in s.text() and row(s, "behind") is not None, s.text())

# configured agents: a fake one records its args and cwd
fx = os.path.abspath(f"{W}/..")
agent = os.path.join(fx, "fakeagent")
open(agent, "w").write('#!/bin/sh\necho "$1 $(pwd)" >> "%s/agent.log"\n' % fx); os.chmod(agent, 0o755)
acfg = os.path.join(fx, "agents.toml")
open(acfg, "w").write(f'[[agent]]\nname = "first"\ncommand = "{agent} one"\n'
                      f'[[agent]]\nname = "ghost"\ncommand = "nosuchagent"\n'
                      f'[[agent]]\nname = "third"\ncommand = "{agent} three"\n')
log = os.path.join(fx, "agent.log")
if os.path.exists(log): os.remove(log)
s.keys("q"); s.exited()
s = Session([G, "-config", acfg], cwd=W, env={"SHELL": "/bin/sh"})
s.wait("noorigin"); s.wait_gone("loading", 10); s.keys("j", 0.2)
s.keys("t", 0.4)
check("custom agents listed", all(a in s.text() for a in ["1 first", "2 ghost", "3 third"]), s.text())
check("missing agent marked", "ghost (not installed)" in s.text(), s.text())
s.keys("\r", 1.0)
check("enter starts default agent and returns", s.wait("noorigin", 5) and "agent ▸" not in s.text(), s.text())
s.keys("t", 0.3); s.keys("\x1b[C", 0.1); s.keys("\x1b[C", 0.1); s.keys("\r", 1.0); s.wait("noorigin", 5)
s.keys("t", 0.3); s.keys("2", 0.4)
check("missing agent reported", "nosuchagent not found in PATH" in s.text(), s.text())
s.keys("k", 0.2); s.keys("t", 0.3); s.keys("3", 1.0); s.wait("noorigin", 5)
R = os.path.realpath(W)
lines = open(log).read().splitlines() if os.path.exists(log) else []
check("agents ran in the right repos", lines == [f"one {R}/behind", f"three {R}/behind", f"three {R}/ahead"], lines)

# missing tool -> flash error, no crash
s.keys("q"); s.exited()
m = Session([G, "-config", EMPTY_CFG, "-tool", "nosuchtool"], cwd=W)
m.wait("noorigin"); m.wait_gone("loading", 10); m.keys("o", 0.4)
check("missing tool reported", "nosuchtool not found in PATH" in m.text(), m.text())
m.keys("q"); check("quit", m.exited())
print("FAILURES:", FAILS)
sys.exit(1 if FAILS else 0)
