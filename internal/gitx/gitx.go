// Package gitx reads repository status and runs git actions by shelling
// out to the git binary.
package gitx

import (
	"context"
	"errors"
	"fmt"
	"gl-repos/internal/forge"
	"gl-repos/internal/safe"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Status is a snapshot of one repository.
type Status struct {
	Path          string
	Branch        string // empty when detached
	Detached      bool
	Upstream      string // e.g. "origin/main"; empty when not tracking
	Ahead, Behind int
	Staged        int
	Modified      int
	Untracked     int
	Conflicts     int
	Origin        string // URL of the "origin" remote; empty if none
	DefaultBranch string // origin's default branch, e.g. "main"
	// BaseAhead/BaseBehind compare HEAD with origin/<DefaultBranch>; only
	// set (HasBase) when HEAD is not on the default branch.
	HasBase               bool
	BaseAhead, BaseBehind int
	LastCommit            time.Time
	// Risky lists keys of the repository's own config that run programs
	// (filters, gpg, ssh, hooks, …), sanitized; see trust.go.
	Risky []string
	Err   error

	filters []string // the repository's filter drivers, off for reads
}

// Dirty reports whether the working tree or index has changes.
func (s Status) Dirty() bool {
	return s.Staged+s.Modified+s.Untracked+s.Conflicts > 0
}

// TracksOriginDefault reports whether the current branch tracks the
// default branch of origin (e.g. main -> origin/main).
func (s Status) TracksOriginDefault() bool {
	return s.DefaultBranch != "" && s.Upstream == "origin/"+s.DefaultBranch
}

// NeedsAttention reports whether the repository is anything other than
// clean, in sync, tracking an upstream and marked with no risk (Risky).
func (s Status) NeedsAttention() bool {
	return s.Err != nil || s.Dirty() || s.Ahead > 0 || s.Behind > 0 ||
		s.Upstream == "" || s.Detached || len(s.Risky) > 0
}

// SyncText summarises ahead/behind counts: "≡", "↑2 ↓1" or "–".
func (s Status) SyncText() string {
	switch {
	case s.Upstream == "":
		return "–"
	case s.Ahead == 0 && s.Behind == 0:
		return "≡"
	}
	var parts []string
	if s.Ahead > 0 {
		parts = append(parts, fmt.Sprintf("↑%d", s.Ahead))
	}
	if s.Behind > 0 {
		parts = append(parts, fmt.Sprintf("↓%d", s.Behind))
	}
	return strings.Join(parts, " ")
}

// StateText summarises the working tree: "clean" or "+staged ~modified
// ?untracked !conflicts".
func (s Status) StateText() string {
	if s.Err != nil {
		return "error"
	}
	if !s.Dirty() {
		return "clean"
	}
	var parts []string
	for _, p := range []struct {
		sym string
		n   int
	}{{"+", s.Staged}, {"~", s.Modified}, {"?", s.Untracked}, {"!", s.Conflicts}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%s%d", p.sym, p.n))
		}
	}
	return strings.Join(parts, " ")
}

// BaseText summarises HEAD relative to origin's default branch, e.g.
// "↑3 ↓12", "≡", or "" when on the default branch or unknown.
func (s Status) BaseText() string {
	if !s.HasBase {
		return ""
	}
	if s.BaseAhead == 0 && s.BaseBehind == 0 {
		return "≡"
	}
	var parts []string
	if s.BaseAhead > 0 {
		parts = append(parts, fmt.Sprintf("↑%d", s.BaseAhead))
	}
	if s.BaseBehind > 0 {
		parts = append(parts, fmt.Sprintf("↓%d", s.BaseBehind))
	}
	return strings.Join(parts, " ")
}

// BranchText returns the branch name, or "(detached)".
func (s Status) BranchText() string {
	if s.Detached {
		return "(detached)"
	}
	return s.Branch
}

// UpstreamText describes the tracking setup.
func (s Status) UpstreamText() string {
	switch {
	case s.Upstream != "":
		return s.Upstream
	case s.Origin == "":
		return "no origin"
	default:
		return "no upstream"
	}
}

// ReadStatus collects the status of the repository at dir. Failures are
// reported in Status.Err rather than returned.
func ReadStatus(ctx context.Context, dir string) Status {
	s := Status{Path: dir}
	rc, err := readRepoConfig(ctx, dir)
	if err != nil {
		// not known to run nothing, like ConfigRisks says
		s.Err, s.Risky = err, []string{uncheckedConfig}
		return s
	}
	s.Risky, s.filters = rc.risky, rc.filters
	// Submodules are compared by commit only: looking inside them would
	// run their own config's filters.
	out, err := readOnly(ctx, dir, s.filters, "status", "--porcelain=v2", "--branch", "--ignore-submodules=dirty")
	if err != nil {
		s.Err = err
		return s
	}
	parsePorcelain(&s, out)
	s.Branch, s.Upstream = safe.Line(s.Branch), safe.Line(s.Upstream)

	if url, err := output(ctx, dir, "remote", "get-url", "origin"); err == nil {
		s.Origin = safe.Line(strings.TrimSpace(url))
	}
	if s.Origin != "" {
		s.DefaultBranch = defaultBranch(ctx, dir)
	}
	if s.DefaultBranch != "" && s.Branch != s.DefaultBranch {
		out, err := output(ctx, dir, "rev-list", "--left-right", "--count", "HEAD...origin/"+s.DefaultBranch)
		if err == nil {
			if _, err := fmt.Sscanf(out, "%d %d", &s.BaseAhead, &s.BaseBehind); err == nil {
				s.HasBase = true
			}
		}
	}
	if out, err := readOnly(ctx, dir, s.filters, "log", "-1", "--format=%ct"); err == nil {
		if sec, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
			s.LastCommit = time.Unix(sec, 0)
		}
	}
	return s
}

// defaultBranch returns origin's default branch from refs/remotes/origin/HEAD,
// falling back to origin/main or origin/master when that ref isn't set
// (common for repos created with "git remote add").
func defaultBranch(ctx context.Context, dir string) string {
	ref, err := output(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		return strings.TrimPrefix(strings.TrimSpace(ref), "origin/")
	}
	for _, b := range []string{"main", "master"} {
		if _, err := output(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b); err == nil {
			return b
		}
	}
	return ""
}

// Graph returns a colored commit graph of all refs, newest first.
func Graph(ctx context.Context, dir string, limit int) (string, error) {
	out, err := output(ctx, dir, "log", "--graph", "--all", "--color=always", "--decorate",
		"--format=%C(auto)%h%d %s %C(dim)(%ar, %an)%C(reset)", "-n", strconv.Itoa(limit))
	// Commit subjects and author names come from anyone who can push:
	// keep git's colors, drop every other escape.
	return safe.Colored(out), err
}

func parsePorcelain(s *Status, out string) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			head := strings.TrimPrefix(line, "# branch.head ")
			if head == "(detached)" {
				s.Detached = true
			} else {
				s.Branch = head
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			s.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			_, _ = fmt.Sscanf(strings.TrimPrefix(line, "# branch.ab "), "+%d -%d", &s.Ahead, &s.Behind)
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			if len(line) < 4 {
				continue
			}
			if line[2] != '.' {
				s.Staged++
			}
			if line[3] != '.' {
				s.Modified++
			}
		case strings.HasPrefix(line, "u "):
			s.Conflicts++
		case strings.HasPrefix(line, "? "):
			s.Untracked++
		}
	}
}

// Action is a git operation that can be applied to a repository.
type Action int

// The actions, in the order the keys f, p and h run them.
const (
	Fetch Action = iota // git fetch --all --prune
	Pull                // git pull --ff-only
	Push                // git push (-u origin HEAD without an upstream)
)

// String names the action as shown in the list ("fetch", "pull", "push").
func (a Action) String() string {
	switch a {
	case Fetch:
		return "fetch"
	case Pull:
		return "pull"
	case Push:
		return "push"
	}
	return "unknown"
}

// Args returns the git arguments for action a on the repository s.
func (a Action) Args(s Status) []string {
	switch a {
	case Fetch:
		return []string{"fetch", "--all", "--prune"}
	case Pull:
		return []string{"pull", "--ff-only"}
	case Push:
		if s.Upstream == "" && s.Origin != "" && s.Branch != "" {
			return []string{"push", "-u", "origin", "HEAD"}
		}
		return []string{"push"}
	}
	return nil
}

// Run performs action a on the repository described by s and returns the
// combined git output.
func Run(ctx context.Context, s Status, a Action) (string, error) {
	args := a.Args(s)
	if args == nil {
		return "", fmt.Errorf("unknown action %d", a)
	}
	return combined(ctx, s.Path, true, args...)
}

// combined runs git and returns trimmed stdout+stderr. network marks
// the confirmed commands that may contact a remote (over ssh too).
func combined(ctx context.Context, dir string, network bool, args ...string) (string, error) {
	cmd, err := command(ctx, dir, network, args...)
	if err != nil {
		return "", err
	}
	if network {
		sshEnv(ctx, dir, cmd)
	}
	// A remote's messages arrive here, so they are capped like glab's.
	out, err := forge.CombinedOutput(cmd)
	return strings.TrimSpace(string(out)), err
}

// sshEnv makes ssh fail fast instead of waiting: the TUI owns the
// terminal, so ssh must not prompt for a passphrase, and an unreachable
// host (VPN down) should not hang for minutes. A user's GIT_SSH_COMMAND
// or a repo's core.sshCommand is respected.
func sshEnv(ctx context.Context, dir string, cmd *exec.Cmd) {
	if os.Getenv("GIT_SSH_COMMAND") != "" || os.Getenv("GIT_SSH") != "" {
		return
	}
	if v, _ := output(ctx, dir, "config", "--get", "core.sshCommand"); strings.TrimSpace(v) == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=15")
	}
}

// repoEnv are variables that point git at one particular repository;
// inherited (e.g. when started from a git hook) they would make every
// command act on that repository instead of the one in dir.
var repoEnv = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_PREFIX"}

// Env is the environment for a program run in a repository: the process
// environment without repoEnv, so the git it runs acts on that
// repository too.
func Env() []string { return gitEnv() }

func gitEnv() []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(repoEnv, name) {
			out = append(out, kv)
		}
	}
	return out
}

// command builds a non-interactive git command running in dir. It
// refuses arguments with URL credentials (forge.CheckArgs): every git
// call passes here, so none reaches git's argv or .git/config. network
// marks the confirmed fetch, pull, push and clone, the only calls that
// may contact a remote; every other call gets noLazyFetch.
func command(ctx context.Context, dir string, network bool, args ...string) (*exec.Cmd, error) {
	if err := forge.CheckArgs(append([]string{dir}, args...)); err != nil {
		return nil, err
	}
	// A repository's own config should not run code just because gl-repos
	// lists it: core.fsmonitor names a program, log.showSignature runs
	// gpg.program on signed commits, and protocol.ext.allow lets a remote
	// URL "ext::<command>" run that command on fetch, pull and push. Fetch
	// and pull never recurse into submodules (git's default is on demand,
	// when a fetched commit moves a gitlink), nor does push: a
	// submodule's own config is checked only below .git/modules, and one
	// whose git directory sits in the work tree (lib/.git from an unpacked
	// archive) is not; it stays untouched instead. These -c settings beat
	// the repository's. A fetch runs no hook (reference-transaction,
	// pre-auto-gc): it may run unattended at start, or without y in a
	// repository that is not flagged. (Filters: see readOnly.)
	global := []string{
		"-c", "core.fsmonitor=false", "-c", "log.showSignature=false",
		"-c", "protocol.ext.allow=never",
		"-c", "fetch.recurseSubmodules=false", "-c", "submodule.recurse=false"}
	if len(args) > 0 && args[0] == "fetch" {
		global = append(global, "-c", "core.hooksPath="+os.DevNull)
	}
	cmd := exec.CommandContext(ctx, "git", append(append(global, "-C", dir), args...)...)
	cmd.Env = append(gitEnv(),
		"GIT_TERMINAL_PROMPT=0", // never prompt for credentials
		"GIT_OPTIONAL_LOCKS=0",  // don't contend with the user's own git use
	)
	if !network {
		cmd.Env = append(cmd.Env, noLazyFetch...)
	}
	// On cancel, let git remove its lock files instead of killing it.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd, nil
}

// noLazyFetch keeps a git call that is no confirmed fetch, pull, push or
// clone off the network. A partial clone (git clone --filter) has a
// promisor remote (extensions.partialClone, remote.<name>.promisor) and
// fetches an object it lacks the moment a command reads one: a lazy
// fetch, which connects with the repository's own config
// (remote.<name>.url, uploadpack, proxy, ...) while the repository is
// only listed, before any y. GIT_NO_LAZY_FETCH (git 2.45+) makes git not
// try; GIT_ALLOW_PROTOCOL (every git, inherited by the fetch git would
// run, and ahead of the repository's own protocol.allow) makes that
// fetch refuse every transport before it connects or runs a program. A
// read that needs a missing object fails instead (status shows the
// error); a normal partial clone has every object its reads need.
var noLazyFetch = []string{"GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=none"}

// output runs git and returns stdout; on failure the error carries stderr.
func output(ctx context.Context, dir string, args ...string) (string, error) {
	return outputEnv(ctx, dir, nil, args...)
}

// outputEnv is output with the command's environment adjusted by env
// (nil keeps it). Both streams are capped like glab's (forge.Output):
// a repository's own text ends up in them.
func outputEnv(ctx context.Context, dir string, env func([]string) []string, args ...string) (string, error) {
	cmd, err := command(ctx, dir, false, args...)
	if err != nil {
		return "", err
	}
	if env != nil {
		cmd.Env = env(cmd.Env)
	}
	out, stderr, err := forge.Output(cmd)
	if err != nil {
		return string(out), gitError(args, stderr, err)
	}
	return string(out), nil
}

// outputLimited is outputEnv reading at most limit bytes of stdout
// (limit <= 0: all of it). Once they are read the pipe is closed, which
// stops git (EPIPE on its next write) instead of holding its whole
// output in memory; stderr is kept (capped) for the error.
func outputLimited(ctx context.Context, dir string, env func([]string) []string, limit int, args ...string) (string, error) {
	if limit <= 0 {
		return outputEnv(ctx, dir, env, args...)
	}
	cmd, err := command(ctx, dir, false, args...)
	if err != nil {
		return "", err
	}
	if env != nil {
		cmd.Env = env(cmd.Env)
	}
	stderr := forge.NewCappedWriter(cmd)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	// one byte past the limit tells a cut output (git stopped by us) from
	// one of exactly limit bytes whose git then failed on its own
	out, rerr := io.ReadAll(io.LimitReader(stdout, int64(limit)+1))
	stdout.Close()
	cut := len(out) > limit
	out = out[:min(len(out), limit)]
	werr := stderr.Err(cmd.Wait()) // ErrTooLarge once stderr hit the cap
	switch {
	case rerr != nil:
		return string(out), gitError(args, stderr.Bytes(), rerr)
	case werr != nil && (!cut || errors.Is(werr, forge.ErrTooLarge)): // not stopped by us: git failed
		return string(out), gitError(args, stderr.Bytes(), werr)
	}
	return string(out), nil
}

// gitError is the error of a failed git command: its stderr if it said
// anything (and was not what overflowed), else err.
func gitError(args []string, stderr []byte, err error) error {
	msg := strings.TrimSpace(string(stderr))
	if msg == "" || errors.Is(err, forge.ErrTooLarge) {
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return fmt.Errorf("git %s: %s", args[0], msg)
}
