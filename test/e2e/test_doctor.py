import sys, os, subprocess, shutil, stat, threading, json; sys.path.insert(0, os.path.dirname(__file__))
from tdrive import *
from http.server import BaseHTTPRequestHandler, HTTPServer

# gl-repos -init and -doctor, against fake glab/gh/ssh and a local fake
# Ollama: nothing here reaches a real server.
here = os.path.dirname(os.path.abspath(__file__))
fx = os.path.abspath(f"{W}/..")

full = tooldir(fx, "bin-full", "glab", "gh", "ssh")  # used alone as PATH: never a real glab
noglab = tooldir(fx, "bin-noglab", "gh", "ssh")

class Tags(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"models": [{"name": "gemma4:e2b"}]}).encode() if self.path == "/api/tags" else b"{}"
        self.send_response(200 if self.path == "/api/tags" else 404); self.end_headers(); self.wfile.write(body)
    def log_message(self, *a): pass
srv = HTTPServer(("127.0.0.1", 0), Tags)
threading.Thread(target=srv.serve_forever, daemon=True).start()
ai_url = f"http://127.0.0.1:{srv.server_port}"

def config(name, default_group="team/tools", model="gemma4:e2b"):
    p = os.path.join(fx, name)
    open(p, "w").write(f'''tool = "git"
[[agent]]
name = "shell"
command = "sh"
[[profile]]
name = "work"
forge = "gitlab"
host = "git.example.test"
default_group = "{default_group}"
[[profile]]
name = "home"
forge = "github"
[ai]
url = "{ai_url}"
model = "{model}"
''')
    return p

def doctor(cfg, path=full, **extra):
    env = {"PATH": path, "HOME": fx, "GIT_CONFIG_GLOBAL": "/dev/null",
           "GL_REPOS_CACHE_DIR": os.path.join(fx, "cache"), **extra}
    return subprocess.run([G, "-config", cfg, "-doctor"], cwd=W, capture_output=True, text=True, env=env, timeout=60)

# --- -init writes the commented example, private, never over an existing file
target = os.path.join(fx, "home-init", ".config", "gl-repos", "config.toml")
env = {**os.environ, "GL_REPOS_CONFIG": target}
r = subprocess.run([G, "-init"], capture_output=True, text=True, env=env)
check("init writes config", r.returncode == 0 and os.path.isfile(target) and f"wrote {target}" in r.stdout, r.stdout + r.stderr)
check("init prints next steps", "default_group" in r.stdout and "glab auth login" in r.stdout and "gl-repos -doctor" in r.stdout, r.stdout)
check("init file is 0600, dir 0700", stat.S_IMODE(os.stat(target).st_mode) == 0o600
      and stat.S_IMODE(os.stat(os.path.dirname(target)).st_mode) == 0o700, oct(os.stat(target).st_mode))
check("init writes the example", open(target).read() == open(os.path.join(here, "..", "..", "config.example.toml")).read())
open(target, "a").write("# mine\n")
r = subprocess.run([G, "-init"], capture_output=True, text=True, env=env)
check("init refuses to overwrite", r.returncode != 0 and f"{target} already exists; not overwriting it" in r.stderr
      and open(target).read().endswith("# mine\n"), r.stderr)
r = subprocess.run([G, "-h"], capture_output=True, text=True)
check("-h mentions init and doctor", "-init" in r.stderr and "-doctor" in r.stderr and "gl-repos -init, edit the file, then gl-repos -doctor" in r.stderr, r.stderr)

# --- everything fine
ok = config("ok.toml")
r = doctor(ok)
out = r.stdout
check("doctor ok exits 0", r.returncode == 0 and "✗" not in out, out + r.stderr)
for want in [f"✓ config {ok} loads and is valid", "✓ git 2.", "✓ tool git installed (o)", "✓ agents installed (t): shell",
             "✓ profile work (GitLab): glab installed", "✓ profile work (GitLab): logged in to git.example.test as tester",
             "✓ profile work (GitLab): token scopes api, read_api", "✓ profile work (GitLab): default_group team/tools exists",
             "✓ profile home (GitHub): logged in to github.com as octo", "token scopes 'gist', 'read:org', 'repo'",
             "✓ profile work (GitLab): glab keeps the token in the OS keyring",
             "✓ profile home (GitHub): gh keeps the token in the OS keyring",
             "✓ ssh git@git.example.test works", "✓ ssh git@github.com works", f"✓ ollama at {ai_url} has gemma4:e2b (i)"]:
    check(f"doctor ok: {want[:50]}", want in out, out)

# --- not logged in to GitLab
r = doctor(ok, FAKE_GLAB_AUTH="none")
check("not logged in: ✗ and exit 1", r.returncode == 1 and "✗ profile work (GitLab): glab is not logged in to git.example.test" in r.stdout
      and "glab auth login --hostname git.example.test" in r.stdout, r.stdout)

# --- server unreachable (VPN off): a network problem, not a login one
r = doctor(ok, FAKE_GLAB_NET="down")
check("unreachable: cannot reach, VPN hint", r.returncode == 1 and "✗ profile work (GitLab): cannot reach git.example.test:" in r.stdout
      and "check the VPN, DNS or firewall" in r.stdout and "not logged in" not in r.stdout, r.stdout)
check("no glab deprecation banner", "DEPRECATION" not in r.stdout, r.stdout)

# --- a read_api-only token
r = doctor(ok, FAKE_GLAB_SCOPES="read_api,read_repository")
check("read_api only: warning with the fix", r.returncode == 0 and "! profile work (GitLab): token scopes read_api, read_repository" in r.stdout
      and "browsing works (cloning over ssh needs no token scope); n/u (create, publish, subgroups) need the api scope: create a legacy personal access token with api, "
          "then glab auth login --hostname git.example.test" in r.stdout, r.stdout)

# --- the token in plain text in glab's config: a warning with the fix, never the token
r = doctor(ok, FAKE_GLAB_STORE="plaintext")
check("plaintext token: warning with the fix", r.returncode == 0
      and "! profile work (GitLab): glab keeps the token in plain text in its config file\n"
          "    glab auth login --hostname git.example.test stores it in the OS keyring instead" in r.stdout, r.stdout)
check("plaintext token: never printed", "glpat-e2e-secret" not in r.stdout + r.stderr, r.stdout)

# --- glab not installed
r = doctor(ok, path=noglab)
check("glab missing: ✗ and exit 1", r.returncode == 1 and "✗ profile work (GitLab): glab not found: c/n/u won't work" in r.stdout
      and "install glab" in r.stdout, r.stdout)

# --- default_group missing
r = doctor(config("nogroup.toml", default_group="team/nope"))
check("default_group missing: warning", r.returncode == 0 and "! profile work (GitLab): default_group team/nope not found on git.example.test: team exists, missing team/nope" in r.stdout
      and "n/u will offer to create the missing subgroups" in r.stdout, r.stdout)

# --- ssh and Ollama problems are warnings with a hint
r = doctor(config("noai.toml", model="nosuch:1b"), FAKE_SSH="timeout")
check("ssh timeout: VPN hint", "! ssh git@git.example.test: ssh: connect to host git.example.test port 22: Operation timed out" in r.stdout
      and "connect the VPN" in r.stdout, r.stdout)
check("ollama model missing: pull hint", "has no model nosuch:1b: i (AI, optional) won't work" in r.stdout and "ollama pull nosuch:1b" in r.stdout, r.stdout)
r = doctor(ok, FAKE_SSH="denied")
check("ssh denied: key hint", "Permission denied (publickey)" in r.stdout and "ssh-add" in r.stdout, r.stdout)

# --- a broken config is reported, the other checks still run
bad = os.path.join(fx, "bad.toml"); open(bad, "w").write('[[profile]]\nforge = "gitlab"\ncolor = "blue"\n')
r = doctor(bad)
check("bad config: ✗, other checks run", r.returncode == 1 and '✗ config: ' in r.stdout and 'color = "blue"' in r.stdout
      and "✓ git 2." in r.stdout, r.stdout)

srv.shutdown()
print("FAILURES:", FAILS)
sys.exit(1 if FAILS else 0)
