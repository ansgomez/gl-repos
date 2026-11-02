"""Drive gl-repos in a pty with a real terminal emulator (pyte) for e2e checks."""
import os, pty, time, select, struct, fcntl, termios, signal, shutil, pyte

G = os.environ["GL_REPOS_BIN"]
W = os.environ["E2E_WORK"]  # fixture work dir
EMPTY_CFG = os.environ["E2E_EMPTY_CFG"]  # config with only defaults
AI_MODEL = os.environ.get("E2E_AI_MODEL", "gemma4:e2b")  # model for the --ai flows
FAILS = []

# Sessions get their own HOME and XDG dirs: the real glab/gh logins, tokens
# and gl-repos config must be out of reach of every test.
SANDBOX = os.path.join(os.path.dirname(os.path.abspath(W)), "home")
SANDBOX_ENV = {"HOME": SANDBOX, "XDG_CONFIG_HOME": os.path.join(SANDBOX, ".config"),
               "XDG_STATE_HOME": os.path.join(SANDBOX, ".local", "state"),
               "XDG_CACHE_HOME": os.path.join(SANDBOX, ".cache"),
               "XDG_DATA_HOME": os.path.join(SANDBOX, ".local", "share")}
for d in SANDBOX_ENV.values(): os.makedirs(d, exist_ok=True)
# Only these variables reach gl-repos, git and the fakes; everything else
# (forge tokens and hosts, GH_CONFIG_DIR, GLAB_CONFIG_DIR, GL_REPOS_CONFIG,
# SSH_AUTH_SOCK, proxies, …) is dropped. os.environ itself is replaced, so
# subprocess.run in the tests inherits the same environment.
KEEP_ENV = {"PATH", "TERM", "LANG", "TZ", "TMPDIR", "USER", "LOGNAME", "SHELL",
            "GL_REPOS_BIN", "E2E_WORK", "E2E_EMPTY_CFG", "E2E_AI", "E2E_AI_MODEL",
            "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GL_REPOS_CACHE_DIR"}
_kept = {k: v for k, v in os.environ.items() if k in KEEP_ENV or k.startswith("LC_")}
os.environ.clear()
os.environ.update(_kept, GLAB_SEND_TELEMETRY="false", GH_NO_UPDATE_NOTIFIER="1", **SANDBOX_ENV)

def tooldir(base, name, *fakes):
    """A directory to use alone as PATH: just what gl-repos runs (git, sh for
    the shell agent) and the named fakes (glab, gh, ssh), so a real glab or
    gh installed in /usr/bin is never found."""
    d = os.path.join(base, name); os.makedirs(d, exist_ok=True)
    for t in ("git", "sh"):
        if not os.path.exists(os.path.join(d, t)): os.symlink(shutil.which(t), os.path.join(d, t))
    for t in fakes:
        shutil.copy(os.path.join(os.path.dirname(os.path.abspath(__file__)), f"fake_{t}.sh"), os.path.join(d, t))
    return d

def git_only(base):
    """A PATH directory with only git and sh: surely no glab or gh."""
    return tooldir(base, "gitonly")

class LenientScreen(pyte.Screen):
    # lazygit emits private SGR sequences (CSI > ... m) that pyte rejects
    def select_graphic_rendition(self, *attrs, private=False, **kw):
        if not private:
            super().select_graphic_rendition(*attrs)
    # and newer versions a private device status request (CSI ? … n)
    def report_device_status(self, *args, private=False, **kw):
        if not private:
            super().report_device_status(*args)

class Session:
    def __init__(self, args, cols=130, rows=30, cwd=None, env=None):
        self.screen = LenientScreen(cols, rows)
        self.stream = pyte.ByteStream(self.screen)
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            if cwd: os.chdir(cwd)
            # a real terminal (os.environ is already reduced to KEEP_ENV)
            e = dict(os.environ, TERM="xterm-256color", **SANDBOX_ENV)
            e.update(env or {})
            os.execve(args[0], args, e)
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def pump(self, t):
        end = time.time() + t
        while time.time() < end:
            r, _, _ = select.select([self.fd], [], [], 0.03)
            if not r: continue
            try: data = os.read(self.fd, 65536)
            except OSError: return False
            if not data: return False
            # answer terminal queries a real terminal would answer
            if b"\x1b]11;?" in data: os.write(self.fd, b"\x1b]11;rgb:0000/0000/0000\x1b\\")
            if b"\x1b[6n" in data: os.write(self.fd, b"\x1b[1;1R")
            self.stream.feed(data)
        return True

    def text(self):
        return "\n".join(l.rstrip() for l in self.screen.display)

    def wait(self, needle, timeout=10):
        end = time.time() + timeout
        while time.time() < end:
            if needle in self.text(): return True
            if not self.pump(0.1): break
        return needle in self.text()

    def wait_row(self, name, needle, timeout=10):
        """Wait until the table row for name contains needle."""
        end = time.time() + timeout
        while time.time() < end:
            if needle in (row(self, name) or ""): return True
            if not self.pump(0.1): break
        return needle in (row(self, name) or "")

    def wait_gone(self, needle, timeout=10):
        end = time.time() + timeout
        while time.time() < end:
            if needle not in self.text(): return True
            self.pump(0.1)
        return needle not in self.text()

    def keys(self, k, settle=0.25):
        os.write(self.fd, k.encode() if isinstance(k, str) else k)
        self.pump(settle)

    def line(self, needle):
        for l in self.screen.display:
            if needle in l: return l.rstrip()
        return None

    def cursor_row(self):
        return self.line("▸")

    def close(self):
        try: os.kill(self.pid, signal.SIGKILL)
        except ProcessLookupError: pass
        try: os.waitpid(self.pid, 0)
        except ChildProcessError: pass

    def exited(self, timeout=3):
        end = time.time() + timeout
        while time.time() < end:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid: return True
            self.pump(0.1)
        return False

def check(name, cond, detail=""):
    print(("PASS " if cond else "FAIL ") + name + ("" if cond else f"\n     {detail}"))
    if not cond: FAILS.append(name)

def dump(s, label):
    print(f"----- {label} -----\n{s.text()}\n-----")

def row(s, name):
    """Table row whose REPO column is exactly name."""
    for l in s.screen.display:
        if l[3:].split(" ")[0] == name: return l.rstrip()
    return None
