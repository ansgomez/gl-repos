package doctor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gl-repos/internal/ai"
	"gl-repos/internal/forge"
)

// TestMain replaces glab/gh auth status with fakes (token in the
// keyring), so no test ever runs the real CLIs.
func TestMain(m *testing.M) {
	forge.GLAuthStatus = func(_ context.Context, host string) string {
		return host + "\n  ✓ Logged in to " + host + " as tester (/c/glab-cli/config.yml)\n" +
			"  ✓ Token found in operating system keyring: **************************\n"
	}
	forge.GHAuthStatus = func(_ context.Context, host string) (string, bool) {
		return host + "\n  ✓ Logged in to " + host + " account octo (keyring)\n", true
	}
	forge.GLAuthStatusAll = func(context.Context) []byte { return nil } // no login anywhere: tests set the host
	os.Exit(m.Run())
}

// A host "glab auth status" reports is taken only as a plain host name
// (forge.ValidHost): a tampered status cannot put anything else into
// the report, a glab --hostname or an ssh host.
func TestDoctorTakesOnlyPlainHostsFromGlab(t *testing.T) {
	defer func(f func(context.Context) []byte) { forge.GLAuthStatusAll = f }(forge.GLAuthStatusAll)
	bad := "x\n  ✓ Logged in to h\x1b[2J;private_token=SECRET as me (/x)\n" +
		"  ✓ Logged in to -hostname=evil.example.com as me (/x)\n  ✓ Logged in to oauth2:SECRET@h as me (/x)\n"
	forge.GLAuthStatusAll = func(context.Context) []byte { return []byte(bad) }
	p := Profile{Name: "w", Forge: forge.GitLab{API: fakeGitLab("tester", `"api"`), HostKey: "[clone] host"}}
	out, _ := run(t, setup(t, p), env())
	if strings.Contains(out, "SECRET") || strings.Contains(out, "evil") || strings.Contains(out, "\x1b[2J") ||
		!strings.Contains(out, "no host: glab is not logged in anywhere") {
		t.Errorf("doctor:\n%s", out)
	}
	forge.GLAuthStatusAll = func(context.Context) []byte { return []byte(bad + "  ✓ Logged in to good.example.com as me (/x)\n") }
	out, _ = run(t, setup(t, p), env())
	if strings.Contains(out, "SECRET") || strings.Contains(out, "evil") || !strings.Contains(out, "good.example.com") {
		t.Errorf("doctor:\n%s", out)
	}
}

// fakeGitLab answers like glab api for user, token scopes and groups.
func fakeGitLab(user, scopes string) func(context.Context, string, string, ...string) ([]byte, error) {
	return func(_ context.Context, host, ep string, _ ...string) ([]byte, error) {
		switch ep {
		case "user":
			if user == "" {
				return nil, errors.New("glab (" + host + "): 401 Unauthorized (not logged in to " + host + ": glab auth login --hostname " + host + ")")
			}
			return []byte(`{"username":"` + user + `"}`), nil
		case "personal_access_tokens/self":
			if scopes == "" {
				return nil, errors.New("glab (" + host + "): 404 Not Found")
			}
			return []byte(`{"scopes":[` + scopes + `]}`), nil
		case "groups/acme%2Ftools":
			return []byte(`{"id":9,"full_path":"acme/tools","path":"tools"}`), nil
		case "groups/acme":
			return []byte(`{"id":7,"full_path":"acme","path":"acme"}`), nil
		}
		return nil, errors.New(`glab (` + host + `): {"message":"404 Group Not Found"}`)
	}
}

func ollama(t *testing.T, models string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("doctor called %s: only /api/tags is read-only", r.URL.Path)
		}
		w.Write([]byte(`{"models":[` + models + `]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func env(missing ...string) Env {
	return Env{
		LookPath: func(p string) (string, error) {
			for _, m := range missing {
				if p == m {
					return "", errors.New("not found")
				}
			}
			return "/bin/" + p, nil
		},
		SSH: func(_ context.Context, host string) (string, error) {
			switch host {
			case "slow.example.com":
				return "ssh: connect to host slow.example.com port 22: Operation timed out", errors.New("exit status 255")
			case "nokey.example.com":
				return "git@nokey.example.com: Permission denied (publickey).", errors.New("exit status 255")
			case "github.com":
				return "Hi octo! You've successfully authenticated, but GitHub does not provide shell access.", errors.New("exit status 1")
			}
			return "Welcome to GitLab, @tester!", nil
		},
		GitVersion: func(context.Context) (string, error) { return "git version 2.45.1 (Apple Git-1)\n", nil },
		Timeout:    5 * time.Second,
	}
}

func run(t *testing.T, s Setup, e Env) (string, int) {
	t.Helper()
	var b bytes.Buffer
	n := Run(context.Background(), s, e, &b)
	return b.String(), n
}

func setup(t *testing.T, profiles ...Profile) Setup {
	return Setup{ConfigPath: "/c/config.toml", ConfigExists: true, Tool: "lazygit",
		Agents:   []Agent{{"claude", "claude"}, {"kimi", "kimi"}},
		Profiles: profiles, AI: ai.Client{URL: ollama(t, `{"name":"gemma4:e2b"}`), Model: "gemma4:e2b"}}
}

func TestAllGood(t *testing.T) {
	defer func(f func(context.Context, string) (string, bool)) { forge.GHAuthStatus = f }(forge.GHAuthStatus)
	forge.GHAuthStatus = func(context.Context, string) (string, bool) {
		return "github.com\n  ✓ Logged in to github.com account octo (keyring)\n  - Token scopes: 'gist', 'read:org', 'repo'\n", true
	}
	out, fails := run(t, setup(t,
		Profile{Name: "work", Forge: forge.GitLab{Host: "gitlab.example.com", API: fakeGitLab("tester", `"api","read_api"`)}, DefaultGroup: "acme/tools"},
		Profile{Name: "home", Forge: forge.GitHub{API: func(context.Context, string, string, ...string) ([]byte, error) {
			return []byte(`{"login":"octo"}`), nil
		}}},
	), env())
	if fails != 0 || strings.Contains(out, "✗") || strings.Contains(out, "!") {
		t.Errorf("%d failures:\n%s", fails, out)
	}
	for _, want := range []string{
		"✓ config /c/config.toml loads and is valid", "✓ git 2.45.1 installed", "✓ tool lazygit installed (o)",
		"✓ agents installed (t): claude, kimi",
		"✓ profile work (GitLab): glab installed", "✓ profile work (GitLab): logged in to gitlab.example.com as tester",
		"✓ profile work (GitLab): token scopes api, read_api", "✓ profile work (GitLab): default_group acme/tools exists",
		"✓ profile home (GitHub): logged in to github.com as octo", "token scopes 'gist', 'read:org', 'repo'",
		"✓ ssh git@gitlab.example.com works", "✓ ssh git@github.com works", "has gemma4:e2b (i)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestProblems(t *testing.T) {
	s := setup(t,
		Profile{Name: "ro", Forge: forge.GitLab{Host: "slow.example.com", API: fakeGitLab("tester", `"read_api","read_repository"`)}, DefaultGroup: "acme/new"},
		Profile{Name: "out", Forge: forge.GitLab{Host: "nokey.example.com", API: fakeGitLab("", "")}},
	)
	s.AI = ai.Client{URL: ollama(t, `{"name":"llama3:8b"}`), Model: "gemma4:e2b"}
	out, fails := run(t, s, env("lazygit", "kimi"))
	if fails != 1 {
		t.Errorf("fails = %d, want 1 (not logged in):\n%s", fails, out)
	}
	for _, want := range []string{
		"! tool lazygit not found: o won't work",
		"! agents not installed: kimi",
		"! profile ro (GitLab): token scopes read_api, read_repository\n    browsing works (cloning over ssh needs no token scope); n/u (create, publish, subgroups) need the api scope: create a legacy personal access token with api, then glab auth login --hostname slow.example.com",
		"! profile ro (GitLab): default_group acme/new not found on slow.example.com: acme exists, missing acme/new\n    n/u will offer to create the missing subgroups",
		"✗ profile out (GitLab): glab is not logged in to nokey.example.com: glab (nokey.example.com): 401 Unauthorized\n    glab auth login --hostname nokey.example.com\n",
		"! ssh git@slow.example.com: ssh: connect to host slow.example.com port 22: Operation timed out\n    the ssh port is not reachable: connect the VPN",
		"! ssh git@nokey.example.com: git@nokey.example.com: Permission denied (publickey).\n    load your key (ssh-add)",
		"has no model gemma4:e2b: i (AI, optional) won't work\n    only for i: ollama pull gemma4:e2b",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "token scopes unknown") && strings.Contains(out, "profile out") && strings.Contains(out, "out (GitLab): token") {
		t.Errorf("checks went on after the login failed:\n%s", out)
	}
}

func TestMissingCLIAndConfig(t *testing.T) {
	s := setup(t, Profile{Name: "gitlab", Forge: forge.GitLab{}, Implicit: true})
	s.ConfigExists, s.ConfigSource = false, ""
	s.AI = ai.Client{URL: "http://127.0.0.1:1", Model: "gemma4:e2b"}
	out, fails := run(t, s, env("glab"))
	if fails != 0 {
		t.Errorf("an implicit profile and a missing default config are warnings:\n%s", out)
	}
	for _, want := range []string{
		"! no config file at /c/config.toml: using the defaults\n    gl-repos -init",
		"! default profile gitlab (GitLab): glab not found: c/n/u won't work\n    install glab",
		"! ollama: ollama unreachable at http://127.0.0.1:1",
		"i (AI, optional) won't work\n    only for i: start ollama (the app or ollama serve) and ollama pull gemma4:e2b",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ssh git@") {
		t.Errorf("ssh checked without a host:\n%s", out)
	}

	s = setup(t, Profile{Name: "work", Forge: forge.GitLab{Host: "h"}})
	s.ConfigErr = errors.New(`/c/config.toml: profile "work": color = "blue": use an ANSI color number 0-255 or #rrggbb (or #rgb)`)
	out, fails = run(t, s, env("glab", "git"))
	if fails != 3 || !strings.Contains(out, `✗ config: /c/config.toml: profile "work": color = "blue"`) ||
		!strings.Contains(out, "✗ git not found") || !strings.Contains(out, "✗ profile work (GitLab): glab not found") {
		t.Errorf("fails %d:\n%s", fails, out)
	}
	s.ConfigErr, s.ConfigExists, s.ConfigSource = nil, false, "GL_REPOS_CONFIG"
	if out, _ := run(t, s, env()); !strings.Contains(out, "✗ config: GL_REPOS_CONFIG names /c/config.toml, which does not exist") {
		t.Errorf("explicit missing config:\n%s", out)
	}
}

func TestServerTextSanitized(t *testing.T) {
	s := setup(t, Profile{Name: "x", Forge: forge.GitLab{Host: "h", API: fakeGitLab("evil\x1b[31mname", `"api"`)}})
	out, _ := run(t, s, env())
	if strings.Contains(out, "\x1b") {
		t.Errorf("escape sequence printed: %q", out)
	}
}

// The output sink hides URL credentials, whatever check quoted them.
func TestOutputRedacted(t *testing.T) {
	api := func(_ context.Context, host, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("glab (" + host + "): cannot reach https://oauth2:SECRET@h/api/v4?private_token=SECRET")
	}
	e := env()
	e.SSH = func(context.Context, string) (string, error) {
		return "fatal: 'oauth2:SECRET@h/acme/tools.git' https://h/p.git#access_token=SECRET", errors.New("exit status 255")
	}
	out, _ := run(t, setup(t, Profile{Name: "x", Forge: forge.GitLab{Host: "h", API: api}}), e)
	if strings.Contains(out, "SECRET") {
		t.Errorf("credential printed:\n%s", out)
	}
}

func TestTimeouts(t *testing.T) {
	e := env()
	e.Timeout = 50 * time.Millisecond
	e.SSH = func(ctx context.Context, _ string) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	slow := func(ctx context.Context, _, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s := setup(t, Profile{Name: "w", Forge: forge.GitLab{Host: "h", API: slow}})
	done := make(chan string)
	go func() { out, _ := run(t, s, e); done <- out }()
	select {
	case out := <-done:
		if !strings.Contains(out, "✗ profile w (GitLab): cannot reach h: timed out\n    check the VPN, DNS or firewall") {
			t.Errorf("no timeout reported:\n%s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a check without a timeout hung")
	}
}

// A server that cannot be reached is a network problem, never "not
// logged in"; glab's NO_PROMPT banner never shows.
func TestUnreachable(t *testing.T) {
	down := func(_ context.Context, host, _ string, _ ...string) ([]byte, error) {
		msg := `Get "https://` + host + `/api/v4/user": dial tcp: lookup ` + host + `: no such host`
		return nil, errors.New("glab (" + host + "): " + msg + " (" + forge.AuthHint("glab", host, msg) + ")")
	}
	out, fails := run(t, setup(t, Profile{Name: "w", Forge: forge.GitLab{Host: "h", API: down}}), env())
	want := "✗ profile w (GitLab): cannot reach h: glab (h): Get \"https://h/api/v4/user\": dial tcp: lookup h: no such host\n" +
		"    check the VPN, DNS or firewall\n"
	if fails != 1 || !strings.Contains(out, want) || strings.Contains(out, "logged in") {
		t.Errorf("fails %d:\n%s", fails, out)
	}
}

// After a working login, a 401 on the token endpoint means the token
// can't describe itself (OAuth): no "log in again".
func TestOAuthScopesUnknown(t *testing.T) {
	api := func(_ context.Context, host, ep string, _ ...string) ([]byte, error) {
		if ep == "personal_access_tokens/self" {
			return nil, errors.New("glab (" + host + "): 401 Unauthorized (not logged in to " + host + ": glab auth login --hostname " + host + ")")
		}
		return fakeGitLab("tester", `"api"`)(context.Background(), host, ep)
	}
	out, _ := run(t, setup(t, Profile{Name: "w", Forge: forge.GitLab{Host: "h", API: api}}), env())
	if !strings.Contains(out, "! profile w (GitLab): token scopes can't be read (web/OAuth login?)\n    browsing works (cloning over ssh needs no token scope);") ||
		strings.Contains(out, "not logged in") {
		t.Errorf("OAuth scopes:\n%s", out)
	}
}

func TestGHNotLoggedIn(t *testing.T) {
	defer func(f func(context.Context, string) (string, bool)) { forge.GHAuthStatus = f }(forge.GHAuthStatus)
	forge.GHAuthStatus = func(context.Context, string) (string, bool) {
		return "You are not logged into any GitHub hosts. To log in, run: gh auth login\n" +
			"Alternatively, populate the GH_TOKEN environment variable with a GitHub API authentication token.\n", false
	}
	gh := forge.GitHub{API: func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(`{"login":"octo"}`), nil
	}}
	out, _ := run(t, setup(t, Profile{Name: "home", Forge: gh}), env())
	if !strings.Contains(out, "! profile home (GitHub): gh is not logged in to github.com\n    gh auth login\n") || strings.Contains(out, "Alternatively") {
		t.Errorf("gh status:\n%s", out)
	}
	// gh api without a login: the first line, not gh's advice
	gh.API = func(context.Context, string, string, ...string) ([]byte, error) {
		return nil, errors.New("gh (github.com): To get started with GitHub CLI, please run:  gh auth login\n" +
			"Alternatively, populate the GH_TOKEN environment variable with a GitHub API authentication token.")
	}
	out, _ = run(t, setup(t, Profile{Name: "home", Forge: gh}), env())
	if !strings.Contains(out, "✗ profile home (GitHub): gh is not logged in to github.com") || strings.Contains(out, "Alternatively") {
		t.Errorf("gh api:\n%s", out)
	}
}

func TestGitTooOldAndHostKey(t *testing.T) {
	e := env("glab")
	e.GitVersion = func(context.Context) (string, error) { return "git version 2.20.1", nil }
	s := setup(t)
	out, fails := run(t, s, e)
	if fails != 1 || !strings.Contains(out, "✗ git 2.20.1 is too old: gl-repos needs 2.31 or newer") {
		t.Errorf("old git: %d\n%s", fails, out)
	}
	defer func(f func(context.Context) []string) { forge.LoggedInHosts = f }(forge.LoggedInHosts)
	forge.LoggedInHosts = func(context.Context) []string { return nil }
	s = setup(t, Profile{Name: "gitlab", Forge: forge.GitLab{}, Implicit: true, HostKey: "[clone] host"})
	out, _ = run(t, s, env())
	if !strings.Contains(out, "glab auth login --hostname <host>, or set [clone] host") {
		t.Errorf("host key:\n%s", out)
	}
}

// Results appear as each check finishes, not all at the end.
func TestStreams(t *testing.T) {
	e := env()
	release := make(chan struct{})
	e.SSH = func(context.Context, string) (string, error) { <-release; return "Welcome to GitLab", nil }
	s := setup(t, Profile{Name: "w", Forge: forge.GitLab{Host: "h", API: fakeGitLab("tester", `"api"`)}})
	w := &syncBuf{}
	done := make(chan struct{})
	go func() { Run(context.Background(), s, e, w); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(w.String(), "logged in to h as tester") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(w.String(), "logged in to h as tester") {
		t.Errorf("nothing printed while ssh runs:\n%s", w.String())
	}
	close(release)
	<-done
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// ssh runs on its own port: a host with the web port is checked without it.
func TestSSHHostWithoutWebPort(t *testing.T) {
	e := env()
	var asked []string
	e.SSH = func(_ context.Context, host string) (string, error) {
		asked = append(asked, host)
		return "Welcome to GitLab, @tester!", nil
	}
	out, _ := run(t, setup(t,
		Profile{Name: "a", Forge: forge.GitLab{Host: "gitlab.example.com:8443", API: fakeGitLab("tester", `"api"`)}},
		Profile{Name: "b", Forge: forge.GitLab{Host: "gitlab.example.com", API: fakeGitLab("tester", `"api"`)}}), e)
	if len(asked) != 1 || asked[0] != "gitlab.example.com" || !strings.Contains(out, "✓ ssh git@gitlab.example.com works") {
		t.Errorf("ssh asked for %q:\n%s", asked, out)
	}
}

// gh says "error connecting to <host>" when DNS or the network fails:
// a network problem, not a login one.
func TestGHUnreachable(t *testing.T) {
	defer func(f func(context.Context, string) (string, bool)) { forge.GHAuthStatus = f }(forge.GHAuthStatus)
	forge.GHAuthStatus = func(context.Context, string) (string, bool) { return "", true }
	gh := forge.GitHub{Host: "ghe.example.com", API: func(context.Context, string, string, ...string) ([]byte, error) {
		return nil, errors.New("gh (ghe.example.com): error connecting to ghe.example.com\ncheck your internet connection or https://githubstatus.com")
	}}
	out, _ := run(t, setup(t, Profile{Name: "gh", Forge: gh}), env())
	if !strings.Contains(out, "✗ profile gh (GitHub): cannot reach ghe.example.com") ||
		!strings.Contains(out, "check the VPN, DNS or firewall") || strings.Contains(out, "logged in") {
		t.Errorf("gh unreachable:\n%s", out)
	}
}

// Where glab/gh keep the token: plain text is a warning with the login
// command that moves it into the keyring; the token itself never shows,
// and the check has a timeout.
func TestTokenStore(t *testing.T) {
	defer func(f func(context.Context, string) string) { forge.GLAuthStatus = f }(forge.GLAuthStatus)
	defer func(f func(context.Context, string) (string, bool)) { forge.GHAuthStatus = f }(forge.GHAuthStatus)
	gl := Profile{Name: "w", Forge: forge.GitLab{Host: "h", API: fakeGitLab("tester", `"api"`)}}
	gh := Profile{Name: "home", Forge: forge.GitHub{API: func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(`{"login":"octo"}`), nil
	}}}

	out, fails := run(t, setup(t, gl, gh), env())
	if fails != 0 || !strings.Contains(out, "✓ profile w (GitLab): glab keeps the token in the OS keyring\n") ||
		!strings.Contains(out, "✓ profile home (GitHub): gh keeps the token in the OS keyring\n") {
		t.Errorf("keyring: %d\n%s", fails, out)
	}

	forge.GLAuthStatus = func(context.Context, string) string {
		return "h\n  ✓ Logged in to h as tester (/c/glab-cli/config.yml)\n" +
			"  ✓ Token found in configuration file (plaintext): glpat-xxx\x1b[31m\n"
	}
	forge.GHAuthStatus = func(context.Context, string) (string, bool) {
		return "github.com\n  ✓ Logged in to github.com account octo (/c/gh/hosts.yml)\n  - Token: gho_xxx\n", true
	}
	out, fails = run(t, setup(t, gl, gh), env())
	for _, want := range []string{
		"! profile w (GitLab): glab keeps the token in plain text in its config file\n" +
			"    glab auth login --hostname h stores it in the OS keyring instead (needs a working keyring)\n",
		"! profile home (GitHub): gh keeps the token in plain text in its config file\n" +
			"    gh auth login stores it in the OS keyring instead (needs a working keyring)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if fails != 0 || strings.Contains(out, "glpat-") || strings.Contains(out, "gho_") || strings.Contains(out, "\x1b") {
		t.Errorf("plaintext: %d, token or escape printed:\n%q", fails, out)
	}

	// a token from the environment is fine; an older glab that does not
	// say is not reported at all
	forge.GLAuthStatus = func(context.Context, string) string { return "h\n  ✓ Token: ****\n" }
	forge.GHAuthStatus = func(context.Context, string) (string, bool) {
		return "github.com\n  ✓ Logged in to github.com account octo (GH_TOKEN)\n", true
	}
	out, _ = run(t, setup(t, gl, gh), env())
	if strings.Contains(out, "glab keeps") || !strings.Contains(out, "✓ profile home (GitHub): gh uses a token from an environment variable\n") {
		t.Errorf("env/unknown:\n%s", out)
	}

	// a failed login check still reports a plain-text token
	forge.GLAuthStatus = func(context.Context, string) string {
		return "h\n  x h: API call failed: 401\n  ✓ Token found in configuration file (plaintext): glpat-xxx\n"
	}
	out, _ = run(t, setup(t, Profile{Name: "w", Forge: forge.GitLab{Host: "h", API: fakeGitLab("", "")}}), env())
	if !strings.Contains(out, "! profile w (GitLab): glab keeps the token in plain text in its config file\n") ||
		strings.Contains(out, "glpat-") {
		t.Errorf("failed login, plaintext:\n%s", out)
	}
	// so does gh, whose status fails for an expired token
	forge.GHAuthStatus = func(context.Context, string) (string, bool) {
		return "github.com\n  X Failed to log in to github.com account octo (/c/gh/hosts.yml)\n  - The token is invalid.\n", false
	}
	failGH := Profile{Name: "home", Forge: forge.GitHub{API: func(context.Context, string, string, ...string) ([]byte, error) {
		return nil, errors.New("gh: HTTP 401: Bad credentials")
	}}}
	out, _ = run(t, setup(t, failGH), env())
	if !strings.Contains(out, "! profile home (GitHub): gh keeps the token in plain text in its config file\n") {
		t.Errorf("failed gh login, plaintext:\n%s", out)
	}

	forge.GLAuthStatus = func(ctx context.Context, _ string) string { <-ctx.Done(); return "" }
	e := env()
	e.Timeout = 50 * time.Millisecond
	done := make(chan string)
	go func() { out, _ := run(t, setup(t, gl), e); done <- out }()
	select {
	case out := <-done:
		if strings.Contains(out, "glab keeps") {
			t.Errorf("hung status reported a store:\n%s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the token store check has no timeout")
	}
}

// A token without read_api is not told that browsing works.
func TestReadUserOnlyScope(t *testing.T) {
	p := Profile{Name: "w", Forge: forge.GitLab{Host: "h", API: fakeGitLab("tester", `"read_user"`)}}
	out, _ := run(t, setup(t, p), env())
	if !strings.Contains(out, "browsing and search need read_api or api") || strings.Contains(out, "browsing works") {
		t.Errorf("read_user only:\n%s", out)
	}
}

// An ssh server that sends without end is cut off instead of buffered.
func TestSSHOutputCapped(t *testing.T) {
	old := forge.MaxOutput
	forge.MaxOutput = 4096
	t.Cleanup(func() { forge.MaxOutput = old })
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/ssh", []byte("#!/bin/sh\nhead -c 100000 /dev/zero\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := runSSH(context.Background(), "h"); !errors.Is(err, forge.ErrTooLarge) || int64(len(out)) > forge.MaxOutput {
		t.Errorf("ssh output: %d bytes, %v", len(out), err)
	}
}

// A git that is found but does not run is a failure, not "git installed";
// a version that cannot be parsed is still only unknown.
func TestGitDoesNotRun(t *testing.T) {
	e := env()
	e.GitVersion = func(context.Context) (string, error) {
		return "", errors.New("fork/exec /bin/git: permission denied\nmore")
	}
	out, fails := run(t, setup(t), e)
	if fails != 1 || !strings.Contains(out, "✗ git version failed: fork/exec /bin/git: permission denied\n    check that git runs: git version") {
		t.Errorf("git not running: %d\n%s", fails, out)
	}
	e.GitVersion = func(context.Context) (string, error) { return "git version vendored", nil }
	if out, fails := run(t, setup(t), e); fails != 0 || !strings.Contains(out, "✓ git installed") {
		t.Errorf("unknown version: %d\n%s", fails, out)
	}
}

// A banner that says "Welcome" does not make a refused key work.
func TestSSHBannerThenDenied(t *testing.T) {
	e := env()
	e.SSH = func(context.Context, string) (string, error) {
		return "Welcome to the example.edu network\ngit@gitlab.example.com: Permission denied (publickey).", errors.New("exit status 255")
	}
	out, _ := run(t, setup(t, Profile{Name: "x", Forge: forge.GitLab{Host: "gitlab.example.com", API: fakeGitLab("tester", `"api"`)}}), e)
	if !strings.Contains(out, "! ssh git@gitlab.example.com: git@gitlab.example.com: Permission denied (publickey).\n    load your key (ssh-add)") {
		t.Errorf("banner then denied:\n%s", out)
	}
}

// A banner that says "Welcome" is no forge's greeting: with ssh failing
// (and no known refusal), the key is not known to work. The forges'
// greetings are (TestRun: "Welcome to GitLab, @tester!" on any host,
// "Hi octo! You've successfully authenticated").
func TestSSHBannerAlone(t *testing.T) {
	e := env()
	e.SSH = func(context.Context, string) (string, error) {
		return "Welcome to the example.edu network\nConnection closed by 192.0.2.1 port 22", errors.New("exit status 255")
	}
	out, _ := run(t, setup(t, Profile{Name: "x", Forge: forge.GitLab{Host: "gitlab.example.com", API: fakeGitLab("tester", `"api"`)}}), e)
	if !strings.Contains(out, "! ssh git@gitlab.example.com: Connection closed by 192.0.2.1 port 22\n    test with: ssh -T git@gitlab.example.com") {
		t.Errorf("banner alone:\n%s", out)
	}
}
