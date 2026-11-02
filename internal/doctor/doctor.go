// Package doctor checks a gl-repos setup without changing anything: the
// config, git and the tools gl-repos starts, each profile's CLI, login,
// token scopes and default group, ssh to each host, and Ollama. Every
// check has a timeout, and everything that comes from a server or a CLI
// is sanitized before it is printed.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gl-repos/internal/ai"
	"gl-repos/internal/forge"
	"gl-repos/internal/safe"
)

// Status is the outcome of one check.
type Status int

// Outcomes: fine, works with limits, broken.
const (
	OK Status = iota
	Warn
	Fail
)

func (s Status) symbol() string {
	switch s {
	case Warn:
		return "!"
	case Fail:
		return "✗"
	}
	return "✓"
}

// Result is one printed check: a line, and for ! or ✗ a fix.
type Result struct {
	Status Status
	Text   string
	Fix    string
}

// Profile is a configured account to check.
type Profile struct {
	Name         string
	Forge        forge.Forge
	DefaultGroup string
	// Implicit marks the built-in profiles used without [[profile]]:
	// their problems are warnings, since nobody chose them.
	Implicit bool
	// HostKey names the config setting of the host, e.g. "[clone] host".
	HostKey string
}

// Setup is what to check.
type Setup struct {
	ConfigPath   string
	ConfigSource string // "-config", "GL_REPOS_CONFIG" or "" (default location)
	ConfigExists bool
	ConfigErr    error
	// LegacyConfigDir is gitgaze's old config directory (gl-repos was
	// called gitgaze), if it is still there; LegacyConfigMoves is true
	// when the next start moves it to the new place.
	LegacyConfigDir   string
	LegacyConfigMoves bool
	Tool              string
	Agents            []Agent
	Profiles          []Profile
	AI                ai.Client
}

// Agent is a configured agent: its name and command's program.
type Agent struct{ Name, Program string }

// Env holds what the checks run; tests replace it.
type Env struct {
	LookPath func(string) (string, error)
	// SSH runs "ssh -T git@host" non-interactively and returns its
	// combined output.
	SSH func(ctx context.Context, host string) (string, error)
	// GitVersion runs "git version".
	GitVersion func(ctx context.Context) (string, error)
	Timeout    time.Duration // per check
}

// DefaultEnv runs the real programs.
func DefaultEnv() Env {
	return Env{LookPath: exec.LookPath, SSH: runSSH, GitVersion: gitVersion, Timeout: 15 * time.Second}
}

func gitVersion(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "version").Output()
	return string(out), err
}

func runSSH(ctx context.Context, host string) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "git@"+host)
	out, err := forge.CombinedOutput(cmd) // capped: a server could send anything
	return string(out), err
}

// Run checks s and writes one line per check to w as soon as it is done
// (with a fix line below each ! or ✗). It returns the number of failed
// (✗) checks.
func Run(ctx context.Context, s Setup, env Env, w io.Writer) int {
	fails := 0
	check(ctx, s, env, func(r Result) {
		fmt.Fprintf(w, "%s %s\n", r.Status.symbol(), show(r.Text))
		if r.Status != OK && r.Fix != "" {
			fmt.Fprintf(w, "    %s\n", show(r.Fix))
		}
		if r.Status == Fail {
			fails++
		}
	})
	return fails
}

// show is a result line as printed: one line, sanitized, URL
// credentials hidden (a check may quote a CLI's or server's output).
func show(s string) string { return safe.Line(forge.RedactURLs(s)) }

// Check runs every check in order and returns the results.
func Check(ctx context.Context, s Setup, env Env) []Result {
	var rs []Result
	check(ctx, s, env, func(r Result) { rs = append(rs, r) })
	return rs
}

// check runs every check in order, handing each result to add when it
// is done.
func check(ctx context.Context, s Setup, env Env, add func(Result)) {
	add(checkConfig(s))
	if s.LegacyConfigDir != "" {
		add(checkLegacyConfig(s))
	}
	add(checkGit(ctx, env))
	if _, err := env.LookPath(s.Tool); err != nil {
		add(Result{Warn, "tool " + s.Tool + " not found: o won't work", "install it, or set tool in the config (e.g. \"tig\")"})
	} else {
		add(Result{OK, "tool " + s.Tool + " installed (o)", ""})
	}
	var have, missing []string
	for _, a := range s.Agents {
		if _, err := env.LookPath(a.Program); err != nil {
			missing = append(missing, a.Name)
		} else {
			have = append(have, a.Name)
		}
	}
	if len(have) > 0 {
		add(Result{OK, "agents installed (t): " + strings.Join(have, ", "), ""})
	}
	if len(missing) > 0 {
		add(Result{Warn, "agents not installed: " + strings.Join(missing, ", "), "install them, or list only yours as [[agent]] in the config"})
	}

	var hosts []string
	for _, p := range s.Profiles {
		// ssh runs on its own port: "gitlab.example.com:8443" is the web port
		host := forge.HostName(checkProfile(ctx, p, env, add))
		if host != "" && !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	for _, h := range hosts {
		add(checkSSH(ctx, h, env))
	}
	add(checkOllama(ctx, s.AI, env))
}

// minGit is the oldest git gl-repos supports: git switch (2.23),
// config --show-scope (2.26), GIT_CONFIG_COUNT and rev-parse
// --path-format (2.31), which keep a repository's own config from
// running programs while it is listed. (GIT_NO_LAZY_FETCH needs 2.45;
// older gits ignore it, and GIT_ALLOW_PROTOCOL covers them: gitx.noLazyFetch.)
var minGit = [2]int{2, 31}

var gitVersionRe = regexp.MustCompile(`git version (\d+)\.(\d+)(\.\d+)?`)

func checkGit(ctx context.Context, env Env) Result {
	if _, err := env.LookPath("git"); err != nil {
		return Result{Fail, "git not found", "install git: gl-repos runs it for everything"}
	}
	if env.GitVersion == nil {
		return Result{OK, "git installed", ""}
	}
	tctx, cancel := context.WithTimeout(ctx, env.Timeout)
	defer cancel()
	out, err := env.GitVersion(tctx)
	if err != nil {
		return Result{Fail, "git version failed: " + errText(tctx, err), "check that git runs: git version"}
	}
	m := gitVersionRe.FindStringSubmatch(out)
	if m == nil {
		return Result{OK, "git installed", ""} // the version is unknown, not wrong
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	v := m[1] + "." + m[2] + m[3]
	if major < minGit[0] || major == minGit[0] && minor < minGit[1] {
		return Result{Fail, fmt.Sprintf("git %s is too old: gl-repos needs %d.%d or newer", v, minGit[0], minGit[1]), "update git"}
	}
	return Result{OK, "git " + v + " installed", ""}
}

func checkConfig(s Setup) Result {
	switch {
	case s.ConfigErr != nil:
		return Result{Fail, "config: " + s.ConfigErr.Error(), "fix the named key (config.example.toml lists them all)"}
	case !s.ConfigExists && s.ConfigSource != "":
		return Result{Fail, fmt.Sprintf("config: %s names %s, which does not exist", s.ConfigSource, s.ConfigPath), "gl-repos -init writes one there"}
	case !s.ConfigExists:
		return Result{Warn, "no config file at " + s.ConfigPath + ": using the defaults", "gl-repos -init writes a commented one"}
	}
	return Result{OK, "config " + s.ConfigPath + " loads and is valid", ""}
}

// checkLegacyConfig reports gitgaze's leftover config directory.
func checkLegacyConfig(s Setup) Result {
	text := "old config directory " + s.LegacyConfigDir + " is still there (gitgaze was renamed to gl-repos)"
	if s.LegacyConfigMoves {
		return Result{Warn, text, "gl-repos moves it to " + filepath.Dir(s.ConfigPath) + " on its next start (-doctor changes nothing)"}
	}
	return Result{Warn, text + " and is not read", "copy what you need into " + s.ConfigPath + ", then remove " + s.LegacyConfigDir}
}

// checkProfile checks one profile, handing each result to add, and
// returns the host it resolved.
func checkProfile(ctx context.Context, p Profile, env Env, add func(Result)) string {
	f := p.Forge
	bad := Fail
	label := "profile " + p.Name + " (" + f.Name() + ")"
	if p.Implicit {
		bad = Warn
		label = "default profile " + p.Name + " (" + f.Name() + ")"
	}
	cli := f.CLI()
	if _, err := env.LookPath(cli); err != nil {
		add(Result{bad, label + ": " + cli + " not found: c/n/u won't work", "install " + cli + " (pasting a clone URL still works)"})
		return ""
	}
	add(Result{OK, label + ": " + cli + " installed", ""})

	tctx, cancel := context.WithTimeout(ctx, env.Timeout)
	host := f.DetectHost(tctx, nil)
	cancel()
	if host == "" {
		key := p.HostKey
		if key == "" {
			key = "host in the config"
		}
		add(Result{bad, label + ": no host: " + cli + " is not logged in anywhere",
			cli + " auth login --hostname <host>, or set " + key})
		return ""
	}
	f = f.WithHost(host)

	tctx, cancel = context.WithTimeout(ctx, env.Timeout)
	me, err := f.Whoami(tctx)
	cancel()
	if err != nil {
		text, hint := explain(tctx, cli, host, err)
		switch {
		case unreachable(tctx, err):
			add(Result{bad, label + ": cannot reach " + host + ": " + text, "check the VPN, DNS or firewall"})
		case hint != "" && strings.HasPrefix(hint, "not logged in"):
			add(Result{bad, label + ": " + cli + " is not logged in to " + host + ": " + text, forge.LoginCmd(cli, host)})
		default:
			if hint == "" {
				hint = forge.LoginCmd(cli, host) + ", then check: " + cli + " auth status"
			}
			add(Result{bad, label + ": not logged in to " + host + ": " + text, hint})
		}
		// where the token is still matters when the login check failed
		tctx, cancel := context.WithTimeout(ctx, env.Timeout)
		var store forge.TokenStore
		switch ff := f.(type) {
		case forge.GitLab:
			store = ff.TokenStore(tctx)
		case forge.GitHub:
			_, store, _ = ff.AuthStatus(tctx)
		}
		cancel()
		if r, ok := tokenStore(store, label, cli, host); ok {
			add(r)
		}
		return host
	}
	add(Result{OK, label + ": logged in to " + host + " as " + me, ""})

	switch ff := f.(type) {
	case forge.GitLab:
		add(gitlabScopes(ctx, ff, label, env))
		tctx, cancel := context.WithTimeout(ctx, env.Timeout)
		store := ff.TokenStore(tctx)
		cancel()
		if r, ok := tokenStore(store, label, "glab", host); ok {
			add(r)
		}
		if p.DefaultGroup != "" {
			add(gitlabGroup(ctx, ff, label, p.DefaultGroup, env))
		}
	case forge.GitHub:
		tctx, cancel := context.WithTimeout(ctx, env.Timeout)
		scopes, store, err := ff.AuthStatus(tctx)
		cancel()
		switch {
		case errors.Is(err, forge.ErrNotLoggedIn):
			add(Result{Warn, label + ": gh is not logged in to " + host, forge.LoginCmd("gh", host)})
		case err != nil && unreachable(tctx, err):
			add(Result{Warn, label + ": gh auth status: cannot reach " + host, "check the VPN, DNS or firewall"})
		case err != nil:
			add(Result{Warn, label + ": " + errText(tctx, err), forge.LoginCmd("gh", host)})
		case scopes != "":
			add(Result{OK, label + ": gh auth status ok, token scopes " + scopes, ""})
		default:
			add(Result{OK, label + ": gh auth status ok", ""})
		}
		if r, ok := tokenStore(store, label, "gh", host); ok {
			add(r)
		}
	}
	return host
}

// tokenStore reports where cli keeps its token for host (from its auth
// status; the token itself is never read or printed): a warning when it
// is plain text in the CLI's config file. ok is false when the CLI does
// not say (an older version).
func tokenStore(store forge.TokenStore, label, cli, host string) (r Result, ok bool) {
	switch store {
	case forge.StoreKeyring:
		return Result{OK, label + ": " + cli + " keeps the token in the OS keyring", ""}, true
	case forge.StoreEnv:
		return Result{OK, label + ": " + cli + " uses a token from an environment variable", ""}, true
	case forge.StorePlaintext:
		return Result{Warn, label + ": " + cli + " keeps the token in plain text in its config file",
			forge.LoginCmd(cli, host) + " stores it in the OS keyring instead (needs a working keyring)"}, true
	}
	return Result{}, false
}

// unreachable reports a check that failed before the server answered.
func unreachable(ctx context.Context, err error) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded) || forge.Unreachable(err.Error())
}

// explain is a forge error as one line without the hint glab/gh errors
// already end with, and that hint (see forge.AuthHint).
func explain(ctx context.Context, cli, host string, err error) (text, hint string) {
	hint = forge.AuthHint(cli, host, err.Error())
	text = errText(ctx, err)
	if hint != "" {
		text = strings.Replace(text, " ("+hint+")", "", 1)
	}
	return text, hint
}

func gitlabScopes(ctx context.Context, g forge.GitLab, label string, env Env) Result {
	tctx, cancel := context.WithTimeout(ctx, env.Timeout)
	defer cancel()
	scopes, err := g.TokenScopes(tctx)
	if err != nil {
		text, _ := explain(tctx, "glab", g.Host, err)
		switch {
		case unreachable(tctx, err):
			return Result{Warn, label + ": token scopes unknown: cannot reach " + g.Host, "check the VPN, DNS or firewall"}
		case forge.IsUnauthorized(err.Error()):
			// the login works (Whoami did): this token just can't
			// describe itself, as with a browser (OAuth) login
			return Result{Warn, label + ": token scopes can't be read (web/OAuth login?)",
				"browsing works (cloning over ssh needs no token scope); if n/u (create, publish, subgroups) fail with 403, log in with a personal access token " +
					"with the api scope: glab auth login --hostname " + g.Host + " --stdin < token.txt"}
		}
		return Result{Warn, label + ": token scopes unknown: " + text,
			"n/u (create, publish, subgroups) need a token with the api scope; browsing needs read_api (cloning over ssh needs no token scope)"}
	}
	text := label + ": token scopes " + strings.Join(scopes, ", ")
	if slices.Contains(scopes, "api") {
		return Result{OK, text, ""}
	}
	browse := "browsing works"
	if !slices.Contains(scopes, "read_api") {
		browse = "browsing and search need read_api or api"
	}
	return Result{Warn, text, browse + " (cloning over ssh needs no token scope); n/u (create, publish, subgroups) need the api scope: " +
		"create a legacy personal access token with api, then glab auth login --hostname " + g.Host}
}

func gitlabGroup(ctx context.Context, g forge.GitLab, label, group string, env Env) Result {
	tctx, cancel := context.WithTimeout(ctx, env.Timeout)
	defer cancel()
	sub, err := g.MissingGroups(tctx, group)
	switch {
	case err == nil && len(sub.Missing) == 0:
		return Result{OK, label + ": default_group " + group + " exists", ""}
	case err == nil:
		return Result{Warn, label + ": default_group " + group + " not found on " + g.Host + ": " + sub.Parent +
			" exists, missing " + strings.Join(sub.Missing, ", "),
			"n/u will offer to create the missing subgroups (after a warning); creating them needs Maintainer or Owner in " + sub.Parent}
	case unreachable(tctx, err):
		return Result{Warn, label + ": default_group " + group + ": cannot reach " + g.Host, "check the VPN, DNS or firewall"}
	}
	text, _ := explain(tctx, "glab", g.Host, err)
	return Result{Warn, label + ": default_group " + group + ": " + text,
		"check the group path: its top-level group must exist and be visible to you"}
}

func checkSSH(ctx context.Context, host string, env Env) Result {
	tctx, cancel := context.WithTimeout(ctx, env.Timeout)
	defer cancel()
	if _, err := env.LookPath("ssh"); err != nil {
		return Result{Warn, "ssh not found: ssh clone URLs won't work", "install OpenSSH, or use https remotes"}
	}
	out, err := env.SSH(tctx, host)
	// GitLab says "Welcome to GitLab, @user!", GitHub "Hi user! You've
	// successfully authenticated" (and exits 1): both mean the key works,
	// unless a refusal follows (a server banner may say anything, "Welcome"
	// included).
	denied := strings.Contains(out, "Permission denied") || strings.Contains(out, "Host key verification failed")
	if !denied && (err == nil || strings.Contains(out, "Welcome to GitLab") || strings.Contains(out, "You've successfully authenticated")) {
		return Result{OK, "ssh git@" + host + " works", ""}
	}
	last := lastLine(out)
	if last == "" {
		last = errText(tctx, err)
	}
	text := "ssh git@" + host + ": " + last
	switch {
	case tctx.Err() != nil || strings.Contains(out, "timed out") || strings.Contains(out, "Connection refused"):
		return Result{Warn, text, "the ssh port is not reachable: connect the VPN, check the firewall/DNS, or use https remotes"}
	case strings.Contains(out, "Could not resolve"):
		return Result{Warn, text, "the host name does not resolve: check DNS or the VPN"}
	case strings.Contains(out, "Permission denied"):
		return Result{Warn, text, "load your key (ssh-add) and add the public key to your account on " + host}
	case strings.Contains(out, "Host key verification failed"):
		return Result{Warn, text, "run ssh -T git@" + host + " once and accept the host key"}
	}
	return Result{Warn, text, "test with: ssh -T git@" + host}
}

func checkOllama(ctx context.Context, c ai.Client, env Env) Result {
	tctx, cancel := context.WithTimeout(ctx, env.Timeout)
	defer cancel()
	ok, err := c.Installed(tctx)
	switch {
	case err != nil:
		return Result{Warn, "ollama: " + errText(tctx, err) + ": i (AI, optional) won't work",
			"only for i: start ollama (the app or ollama serve) and ollama pull " + c.Model + ", or set [ai] url"}
	case !ok:
		return Result{Warn, "ollama at " + c.URL + " has no model " + c.Model + ": i (AI, optional) won't work",
			"only for i: ollama pull " + c.Model + ", or set [ai] model"}
	}
	return Result{OK, "ollama at " + c.URL + " has " + c.Model + " (i)", ""}
}

// errText is err as one line (its first: CLIs put the cause first and
// advice after it), naming a timeout as such.
func errText(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timed out"
	}
	return strings.TrimSpace(strings.SplitN(strings.TrimSpace(err.Error()), "\n", 2)[0])
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
