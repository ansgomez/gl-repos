package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gl-repos/internal/forge"
)

func TestParsePorcelain(t *testing.T) {
	out := `# branch.oid 1234567890abcdef
# branch.head main
# branch.upstream origin/main
# branch.ab +2 -1
1 M. N... 100644 100644 100644 abc abc staged.go
1 .M N... 100644 100644 100644 abc abc modified.go
1 MM N... 100644 100644 100644 abc abc both.go
2 R. N... 100644 100644 100644 abc abc R100 new.go	old.go
u UU N... 100644 100644 100644 100644 abc abc abc conflict.go
? untracked.txt
`
	var s Status
	parsePorcelain(&s, out)

	if s.Branch != "main" || s.Upstream != "origin/main" {
		t.Errorf("branch/upstream = %q/%q", s.Branch, s.Upstream)
	}
	if s.Ahead != 2 || s.Behind != 1 {
		t.Errorf("ahead/behind = %d/%d, want 2/1", s.Ahead, s.Behind)
	}
	if s.Staged != 3 || s.Modified != 2 || s.Untracked != 1 || s.Conflicts != 1 {
		t.Errorf("counts = +%d ~%d ?%d !%d, want +3 ~2 ?1 !1", s.Staged, s.Modified, s.Untracked, s.Conflicts)
	}
	if got := s.SyncText(); got != "↑2 ↓1" {
		t.Errorf("SyncText = %q", got)
	}
}

func TestParsePorcelainDetached(t *testing.T) {
	var s Status
	parsePorcelain(&s, "# branch.oid abc\n# branch.head (detached)\n")
	if !s.Detached || s.Branch != "" || s.Dirty() {
		t.Errorf("got %+v", s)
	}
	if s.SyncText() != "–" || s.StateText() != "clean" {
		t.Errorf("sync=%q state=%q", s.SyncText(), s.StateText())
	}
}

func TestStepArgs(t *testing.T) {
	s := Status{Branch: "feat", Origin: "git@x:y.git"}
	ok := []struct {
		st   Step
		want string
	}{
		{Step{Op: "stage_all"}, "git add -A"},
		{Step{Op: "stage", Paths: []string{"a.go", "-rf"}}, "git add -- a.go -rf"},
		{Step{Op: "commit", Message: "Fix bug"}, `git commit -m 'Fix bug'`},
		{Step{Op: "stash", Message: "wip", IncludeUntracked: true}, "git stash push --include-untracked -m wip"},
		{Step{Op: "create_branch", Branch: "fix/x"}, "git switch -c fix/x"},
		{Step{Op: "push"}, "git push -u origin HEAD"},
	}
	for _, c := range ok {
		args, err := c.st.Args(s)
		if err != nil {
			t.Errorf("%+v: %v", c.st, err)
			continue
		}
		if got := CommandLine(args); got != c.want {
			t.Errorf("%+v: got %s, want %s", c.st, got, c.want)
		}
	}
	for _, st := range []Step{
		{Op: "reset"},
		{Op: "commit"},
		{Op: "stage"},
		{Op: "switch", Branch: "--force"},
		{Op: "create_branch", Branch: "has space"},
	} {
		if _, err := st.Args(s); err == nil {
			t.Errorf("%+v: expected error", st)
		}
	}
	// a valid ref name shaped like a credential is refused, not quoted
	for _, st := range []Step{
		{Op: "switch", Branch: "main#access_token=SECRET"},
		{Op: "create_branch", Branch: "fix/x;private_token=SECRET"},
	} {
		if _, err := st.Args(s); !errors.Is(err, forge.ErrURLCredentials) || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%+v: %v", st, err)
		}
	}
}

// A status carries ref names as they are (what is shown is redacted at
// the sinks: the TUI's scrubStatus, -list), and a credential-shaped
// default branch is never a git argument: no comparison with it.
func TestReadStatusKeepsRefNames(t *testing.T) {
	dir := newRepo(t)
	runGit(t, dir, "branch", "-m", "x#access_token=SECRET")
	runGit(t, dir, "remote", "add", "origin", "/nonexistent/origin.git")
	runGit(t, dir, "update-ref", "refs/remotes/origin/y;private_token=SECRET", "HEAD")
	runGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/y;private_token=SECRET")
	runGit(t, dir, "branch", "-u", "origin/y;private_token=SECRET")
	st := ReadStatus(context.Background(), dir)
	if st.Err != nil || st.Branch != "x#access_token=SECRET" || st.Upstream != "origin/y;private_token=SECRET" ||
		st.DefaultBranch != "y;private_token=SECRET" || st.HasBase {
		t.Errorf("got %+v", st)
	}
	if args := Push.Args(st); !slices.Equal(args, []string{"push"}) {
		t.Errorf("push args %q", args)
	}
}

func TestCloneDest(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := CloneDest(root, " sub/new "); err != nil || got != filepath.Join(root, "sub/new") {
		t.Errorf("valid dest: %q, %v", got, err)
	}
	for _, bad := range []string{"", ".", "..", "../x", "/abs", "taken", "a/../../x"} {
		if _, err := CloneDest(root, bad); err == nil {
			t.Errorf("CloneDest(%q) should fail", bad)
		}
	}
	// a folder name shaped like credentials is refused, before anything
	// is shown or created, and the error never quotes it
	for _, bad := range []string{"tools?private_token=SECRET", "oauth2:SECRET@h/p", "x#access_token=SECRET",
		"x;private_token=SECRET", "x?%70rivate_token=SECRET", "x?a=1%26private_token=SECRET", "https://oauth2:SECRET@h/p.git"} {
		_, err := CloneDest(root, bad)
		if !errors.Is(err, ErrDestCredentials) || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("CloneDest(%q): %v", bad, err)
		}
	}
	// a .git component (in any case, anywhere on the way) would mark its
	// parent as a repository: refused before anything is created
	for _, bad := range []string{".git", ".GIT", "sub/.git", "sub/.Git/x", "./.git/", "sub/x/../.git"} {
		if _, err := CloneDest(root, bad); !errors.Is(err, ErrDestGitDir) {
			t.Errorf("CloneDest(%q): %v", bad, err)
		}
	}
	if _, err := CloneDest(root, "tool.git/.github"); err != nil {
		t.Errorf("tool.git/.github: %v", err)
	}
}

func TestCloneLocal(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", src},
		{"-C", src, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	dest := filepath.Join(root, "nested", "copy")
	if out, err := Clone(context.Background(), root, mustURL(t, src), dest); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	if s := ReadStatus(context.Background(), dest); s.Err != nil || s.Branch != "main" || s.Origin != src {
		t.Errorf("cloned status = %+v", s)
	}
}

func TestStepRejectsUnsafeModelOutput(t *testing.T) {
	for _, st := range []Step{
		{Op: "create_branch", Branch: "x\x1b]52;c;eA==\x07"},
		{Op: "create_branch", Branch: "--force"},
		{Op: "create_branch", Branch: "a..b"},
		{Op: "switch", Branch: "feat/.hidden"},
		{Op: "switch", Branch: "x.lock"},
		{Op: "switch", Branch: "x.lock/y"},
		{Op: "stage", Paths: []string{"ok.go", "bad\rname"}},
		{Op: "commit", Message: "Fix\x1b[2J it"},
	} {
		if _, err := st.Args(Status{}); err == nil {
			t.Errorf("%+v accepted", st)
		}
	}
	for _, b := range []string{"main", "fix/login-2", "feat/ü", "release-1.2"} {
		if _, err := (Step{Op: "create_branch", Branch: b}).Args(Status{}); err != nil {
			t.Errorf("branch %q rejected: %v", b, err)
		}
	}
	if _, err := (Step{Op: "commit", Message: "Subject\n\nBody\twith tab"}).Args(Status{}); err != nil {
		t.Errorf("multi-line message rejected: %v", err)
	}
}

func TestOnBranch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r")
	ctx := context.Background()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !OnBranch(ctx, dir) {
		t.Error("a new repository (unborn branch) is on a branch")
	}
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("checkout", "-q", "--detach")
	if OnBranch(ctx, dir) {
		t.Error("detached HEAD counted as a branch")
	}
}

// No git call gets an argument with credentials: command refuses it
// before git starts.
func TestGitArgsWithCredentialsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, a := range []string{"https://oauth2:SECRET@h/p.git", "https://h/p.git?%70rivate_token=SECRET",
		"https://h/p.git#access_token=SECRET", "https://h/p.git;private_token=SECRET"} {
		if out, err := combined(context.Background(), dir, false, "-c", "remote.x.url="+a, "init", "-q"); !errors.Is(err, forge.ErrURLCredentials) || out != "" {
			t.Errorf("combined(%q): %q, %v", a, out, err)
		}
		if _, err := output(context.Background(), dir, "ls-remote", a); !errors.Is(err, forge.ErrURLCredentials) {
			t.Errorf("output(%q): %v", a, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Error("git ran")
	}
}

// Every other git call is capped too, stdout and stderr, in full and in
// limited reads: a repository's own text (commit messages, config) ends
// up there.
func TestOutputCapped(t *testing.T) {
	old := forge.MaxOutput
	forge.MaxOutput = 4
	t.Cleanup(func() { forge.MaxOutput = old })
	ctx, dir := context.Background(), t.TempDir()
	if out, err := output(ctx, dir, "version"); !errors.Is(err, forge.ErrTooLarge) || len(out) > 4 {
		t.Errorf("stdout: %q, %v", out, err)
	}
	if _, err := output(ctx, dir, "no-such-command"); !errors.Is(err, forge.ErrTooLarge) {
		t.Errorf("stderr: %v", err)
	}
	if _, err := outputLimited(ctx, dir, nil, 2, "no-such-command"); !errors.Is(err, forge.ErrTooLarge) {
		t.Errorf("limited, stderr: %v", err)
	}
	if out, err := outputLimited(ctx, dir, nil, 2, "version"); err != nil || len(out) != 2 {
		t.Errorf("limited, stdout: %q, %v", out, err)
	}
}

// A limited read stops git by closing its stdout, and git's exit from
// that is not an error; a git that wrote exactly the limit and then
// failed on its own is one. Only a git that wrote past the limit was
// stopped by us.
func TestOutputLimitedExactFailure(t *testing.T) {
	bin := t.TempDir()
	git := filepath.Join(bin, "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf \"$GIT_OUT\"\necho failed >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, dir := context.Background(), t.TempDir()
	t.Setenv("GIT_OUT", "ab")
	if out, err := outputLimited(ctx, dir, nil, 2, "version"); out != "ab" || err == nil || !strings.Contains(err.Error(), "failed") {
		t.Errorf("exactly at the limit: %q, %v", out, err)
	}
	t.Setenv("GIT_OUT", "abc")
	if out, err := outputLimited(ctx, dir, nil, 2, "version"); out != "ab" || err != nil {
		t.Errorf("past the limit: %q, %v", out, err)
	}
}

// Fetch, pull and push output comes from a remote, so it is capped.
func TestCombinedCapped(t *testing.T) {
	old := forge.MaxOutput
	forge.MaxOutput = 4
	t.Cleanup(func() { forge.MaxOutput = old })
	if out, err := combined(context.Background(), t.TempDir(), false, "version"); !errors.Is(err, forge.ErrTooLarge) || len(out) > 4 {
		t.Errorf("combined: %q, %v", out, err)
	}
}

// A repository marked with a risk needs attention like a dirty one: the
// attention filter must not hide what the ! marks.
func TestNeedsAttention(t *testing.T) {
	clean := Status{Branch: "main", Upstream: "origin/main"}
	if clean.NeedsAttention() {
		t.Error("a clean, tracking repository needs attention")
	}
	for name, s := range map[string]Status{
		"risky":       {Branch: "main", Upstream: "origin/main", Risky: []string{"core.pager"}},
		"dirty":       {Branch: "main", Upstream: "origin/main", Modified: 1},
		"ahead":       {Branch: "main", Upstream: "origin/main", Ahead: 1},
		"no upstream": {Branch: "main"},
		"detached":    {Detached: true, Upstream: "origin/main"},
		"error":       {Err: errors.New("x"), Upstream: "origin/main"},
	} {
		if !s.NeedsAttention() {
			t.Errorf("%s: needs no attention", name)
		}
	}
}
