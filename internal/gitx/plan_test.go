package gitx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoWithOrigin makes a clone of a bare origin with one commit on main.
func repoWithOrigin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare, work := filepath.Join(dir, "origin.git"), filepath.Join(dir, "work")
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main", bare},
		{"clone", "-q", bare, work},
		{"-C", work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", work, "push", "-q", "-u", "origin", "main"},
		{"-C", work, "config", "user.email", "t@t"},
		{"-C", work, "config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return work
}

func TestPlanArgsSimulatesBranches(t *testing.T) {
	work := repoWithOrigin(t)
	ctx := context.Background()
	st := ReadStatus(ctx, work)
	steps := []Step{{Op: "create_branch", Branch: "feat"}, {Op: "push"}, {Op: "switch", Branch: "main"}, {Op: "push"}}
	args, errs := PlanArgs(ctx, work, st, steps)
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := []string{CommandLine(args[1]), CommandLine(args[3])}
	if got[0] != "git push -u origin HEAD" || got[1] != "git push" {
		t.Fatalf("pushes shown as %q", got)
	}
	out, err := RunSteps(ctx, work, steps, args)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if u, _ := output(ctx, work, "rev-parse", "--abbrev-ref", "feat@{upstream}"); strings.TrimSpace(u) != "origin/feat" {
		t.Errorf("feat upstream = %q", u)
	}
}

func TestRunStepsRefusesUnconfirmedArgs(t *testing.T) {
	work := repoWithOrigin(t)
	ctx := context.Background()
	steps := []Step{{Op: "create_branch", Branch: "feat"}, {Op: "push"}}
	// as an older display would have shown it: plain "git push"
	confirmed := [][]string{{"switch", "-c", "feat"}, {"push"}}
	_, err := RunSteps(ctx, work, steps, confirmed)
	if err == nil || !strings.Contains(err.Error(), "instead of the confirmed git push") {
		t.Fatalf("err = %v", err)
	}
	if out, _ := output(ctx, work, "ls-remote", "--heads", "origin", "feat"); strings.TrimSpace(out) != "" {
		t.Errorf("unconfirmed push ran: %s", out)
	}
}

func TestInheritedRepoEnvIgnored(t *testing.T) {
	a, b := repoWithOrigin(t), repoWithOrigin(t)
	if _, err := output(context.Background(), b, "switch", "-q", "-c", "other"); err != nil {
		t.Fatal(err)
	}
	// as if gl-repos were started from a git hook in repository a
	t.Setenv("GIT_DIR", filepath.Join(a, ".git"))
	t.Setenv("GIT_WORK_TREE", a)
	if st := ReadStatus(context.Background(), b); st.Branch != "other" {
		t.Errorf("status of b read from a: branch %q", st.Branch)
	}
}

// The model gets sync in words and the commits a push would send: small
// models do not read "↑2" as work to push.
func TestDescribeSaysWhatPushWouldSend(t *testing.T) {
	work := repoWithOrigin(t)
	ctx := context.Background()
	if got := Describe(ctx, ReadStatus(ctx, work), 0); !strings.Contains(got, "Sync: up to date with origin/main") ||
		strings.Contains(got, "not pushed yet") {
		t.Errorf("in sync:\n%s", got)
	}
	for _, msg := range []string{"Add parser", "Fix parser"} {
		if out, err := exec.Command("git", "-C", work, "commit", "-q", "--allow-empty", "-m", msg).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	got := Describe(ctx, ReadStatus(ctx, work), 0)
	for _, want := range []string{"Sync: 2 commits made here, not on origin/main yet",
		"## Local commits not pushed yet\nFix parser\nAdd parser\n", "push, pull and fetch do not depend on it"} {
		if !strings.Contains(got, want) {
			t.Errorf("ahead: missing %q in\n%s", want, got)
		}
	}
	if out, err := exec.Command("git", "-C", work, "switch", "-q", "-c", "feat").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if got := Describe(ctx, ReadStatus(ctx, work), 0); !strings.Contains(got, "Sync: the branch has no upstream yet") {
		t.Errorf("no upstream:\n%s", got)
	}
}

// A diff larger than max_diff_bytes is cut while it is read, not after:
// git is stopped at the limit, so the whole diff is never in memory.
func TestDescribeReadsDiffBounded(t *testing.T) {
	dir := newRepo(t)
	var big strings.Builder
	for i := range 1 << 20 { // ~13 MB
		fmt.Fprintf(&big, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(big.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st := ReadStatus(ctx, dir)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got := Describe(ctx, st, 2000)
	runtime.ReadMemStats(&after)
	if !strings.Contains(got, "[diff truncated]") || len(got) > 2500 || !strings.Contains(got, "## Unstaged diff") {
		t.Errorf("Describe (%d bytes): %.300q", len(got), got)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 4<<20 {
		t.Errorf("Describe allocated %d bytes for a 2000-byte diff: the whole diff was read", grew)
	}
}
