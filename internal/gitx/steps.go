package gitx

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"gl-repos/internal/forge"
	"gl-repos/internal/safe"
)

// Step is one allow-listed operation in a plan, typically proposed by the
// AI assistant. Steps never map to destructive commands (reset, clean,
// checkout -- <path>, force push).
type Step struct {
	Op               string   `json:"op"`
	Message          string   `json:"message,omitempty"`
	Paths            []string `json:"paths,omitempty"`
	Branch           string   `json:"branch,omitempty"`
	IncludeUntracked bool     `json:"include_untracked,omitempty"`
}

// StepOps lists the supported operations with a short description, in the
// order they are presented to the model.
var StepOps = []struct{ Op, Doc string }{
	{"stage_all", "stage every change, including untracked files (git add -A)"},
	{"stage", "stage specific paths"},
	{"unstage", "unstage specific paths, keeping the working-tree changes"},
	{"commit", "commit what is staged, with message"},
	{"stash", "stash changes, optional message, include_untracked to also stash untracked files"},
	{"stash_pop", "apply and drop the latest stash"},
	{"create_branch", "create and switch to a new branch named branch"},
	{"switch", "switch to existing branch named branch"},
	{"fetch", "fetch all remotes"},
	{"pull", "fast-forward-only pull"},
	{"push", "push the current branch"},
}

// Args validates the step and returns the git arguments it runs.
func (st Step) Args(s Status) ([]string, error) {
	needPaths := func() error {
		if len(st.Paths) == 0 {
			return fmt.Errorf("%s: no paths given", st.Op)
		}
		return nil
	}
	needBranch := func() error {
		b := strings.TrimSpace(st.Branch)
		// A valid ref name can hold "#access_token=…" or
		// ";private_token=…": command would refuse it anyway, and it is
		// not quoted (the screen would show it redacted).
		if forge.HasCredentials(b) {
			return fmt.Errorf("%s: branch name: %w", st.Op, forge.ErrURLCredentials)
		}
		if !validBranch(b) {
			return fmt.Errorf("%s: invalid branch name %q", st.Op, st.Branch)
		}
		return nil
	}
	// Model output must not smuggle control characters into what is
	// shown or committed.
	for _, p := range st.Paths {
		if safe.Line(p) != p || p == "" {
			return nil, fmt.Errorf("%s: invalid path %q", st.Op, p)
		}
	}
	if safe.Text(st.Message) != st.Message {
		return nil, fmt.Errorf("%s: message contains control characters", st.Op)
	}

	switch st.Op {
	case "stage_all":
		return []string{"add", "-A"}, nil
	case "stage":
		if err := needPaths(); err != nil {
			return nil, err
		}
		return append([]string{"add", "--"}, st.Paths...), nil
	case "unstage":
		if err := needPaths(); err != nil {
			return nil, err
		}
		return append([]string{"restore", "--staged", "--"}, st.Paths...), nil
	case "commit":
		if strings.TrimSpace(st.Message) == "" {
			return nil, fmt.Errorf("commit: empty message")
		}
		return []string{"commit", "-m", strings.TrimSpace(st.Message)}, nil
	case "stash":
		args := []string{"stash", "push"}
		if st.IncludeUntracked {
			args = append(args, "--include-untracked")
		}
		if m := strings.TrimSpace(st.Message); m != "" {
			args = append(args, "-m", m)
		}
		return args, nil
	case "stash_pop":
		return []string{"stash", "pop"}, nil
	case "create_branch":
		if err := needBranch(); err != nil {
			return nil, err
		}
		return []string{"switch", "-c", strings.TrimSpace(st.Branch)}, nil
	case "switch":
		if err := needBranch(); err != nil {
			return nil, err
		}
		return []string{"switch", strings.TrimSpace(st.Branch)}, nil
	case "fetch":
		return Fetch.Args(s), nil
	case "pull":
		return Pull.Args(s), nil
	case "push":
		return Push.Args(s), nil
	}
	return nil, fmt.Errorf("unsupported operation %q", st.Op)
}

func (st Step) network() bool {
	return st.Op == "fetch" || st.Op == "pull" || st.Op == "push"
}

// validBranch follows git check-ref-format's rules for branch names.
func validBranch(b string) bool {
	if b == "" || b == "@" || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") ||
		strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") ||
		strings.Contains(b, "..") || strings.Contains(b, "@{") || strings.Contains(b, "//") ||
		strings.ContainsAny(b, " ~^:?*[\\") || safe.Line(b) != b {
		return false
	}
	for _, seg := range strings.Split(b, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".lock") {
			return false
		}
	}
	return true
}

// CommandLine renders args as a copy-pasteable git command for display.
func CommandLine(args []string) string {
	parts := []string{"git"}
	for _, a := range args {
		parts = append(parts, safe.Quote(a))
	}
	return strings.Join(parts, " ")
}

// PlanArgs returns the exact git arguments of each step as they will run,
// simulating how earlier steps change the repository (a new branch has
// no upstream; switching takes that branch's upstream), so what the user
// confirms is what RunSteps executes.
func PlanArgs(ctx context.Context, dir string, s Status, steps []Step) ([][]string, []error) {
	args := make([][]string, len(steps))
	errs := make([]error, len(steps))
	for i, st := range steps {
		args[i], errs[i] = st.Args(s)
		if errs[i] != nil {
			continue
		}
		switch st.Op {
		case "create_branch":
			s.Branch, s.Upstream = strings.TrimSpace(st.Branch), ""
		case "switch":
			b := strings.TrimSpace(st.Branch)
			s.Branch, s.Upstream = b, ""
			if u, err := output(ctx, dir, "rev-parse", "--abbrev-ref", b+"@{upstream}"); err == nil {
				s.Upstream = strings.TrimSpace(u)
			} else if _, err := output(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err != nil {
				// git switch creates a tracking branch from origin/<b>
				if _, err := output(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b); err == nil {
					s.Upstream = "origin/" + b
				}
			}
		case "push":
			if s.Upstream == "" && s.Origin != "" {
				s.Upstream = "origin/" + s.Branch
			}
		}
	}
	return args, errs
}

// RunSteps executes steps in order, stopping at the first failure. Each
// step's arguments are recomputed from the live status and must equal
// the confirmed ones; otherwise nothing more runs.
func RunSteps(ctx context.Context, dir string, steps []Step, confirmed [][]string) (string, error) {
	var log strings.Builder
	for i, st := range steps {
		s := ReadStatus(ctx, dir)
		if s.Err != nil {
			return log.String(), s.Err
		}
		args, err := st.Args(s)
		if err != nil {
			return log.String(), err
		}
		if i >= len(confirmed) || !slices.Equal(args, confirmed[i]) {
			want := ""
			if i < len(confirmed) {
				want = CommandLine(confirmed[i])
			}
			return strings.TrimSpace(log.String()), fmt.Errorf("step %d would run %s instead of the confirmed %s: stopped",
				i+1, CommandLine(args), want)
		}
		fmt.Fprintf(&log, "$ %s\n", CommandLine(args))
		out, err := combined(ctx, dir, st.network(), args...)
		if out != "" {
			log.WriteString(out + "\n")
		}
		if err != nil {
			return strings.TrimSpace(log.String()), fmt.Errorf("%s failed: %w", st.Op, err)
		}
	}
	return strings.TrimSpace(log.String()), nil
}

// syncWords says what push and pull would do, for the model.
func syncWords(s Status) string {
	switch {
	case s.Detached:
		return "detached HEAD: not on a branch, so push and pull do not apply"
	case s.Origin == "":
		return "no origin remote: push, pull and fetch are not possible"
	case s.Upstream == "":
		return "the branch has no upstream yet: push would publish it to origin and set the upstream"
	}
	commits := func(n int) string {
		if n == 1 {
			return "1 commit"
		}
		return fmt.Sprintf("%d commits", n)
	}
	var parts []string
	if s.Ahead > 0 {
		parts = append(parts, commits(s.Ahead)+" made here, not on "+s.Upstream+" yet")
	}
	if s.Behind > 0 {
		parts = append(parts, s.Upstream+" has "+commits(s.Behind)+" not pulled yet")
	}
	if len(parts) == 0 {
		return "up to date with " + s.Upstream + " (as of the last fetch)"
	}
	return strings.Join(parts, "; ") + " (as of the last fetch)"
}

// Describe summarises the repository for the AI assistant: branch and
// tracking (in words), short status, unpushed commits, stashes, recent
// commit subjects, and staged and unstaged diffs (truncated to maxDiff
// bytes in total).
func Describe(ctx context.Context, s Status, maxDiff int) string {
	var b strings.Builder
	// Sync in words: small models do not read "↑2" as commits to push.
	fmt.Fprintf(&b, "Branch: %s\nUpstream: %s\nSync: %s\n", s.BranchText(), s.UpstreamText(), syncWords(s))
	if s.DefaultBranch != "" {
		fmt.Fprintf(&b, "Origin default branch: %s\n", s.DefaultBranch)
	}

	// limit bounds what is read of a command's output (0: all)
	section := func(limit int, title string, args ...string) string {
		out, err := readOnlyLimited(ctx, s.Path, s.filters, limit, args...)
		out = strings.TrimRight(out, "\n")
		if err != nil || out == "" {
			return ""
		}
		return "\n## " + title + "\n" + out + "\n"
	}
	if st := section(0, "Status (git status --short)", "status", "--short", "--ignore-submodules=dirty"); st != "" {
		b.WriteString(st)
	} else {
		b.WriteString("\n## Status\nWorking tree clean: nothing to commit or stash " +
			"(push, pull and fetch do not depend on it).\n")
	}
	if s.Upstream != "" && s.Ahead > 0 {
		b.WriteString(section(0, "Local commits not pushed yet", "log", "--format=%s", "-n", "10", "@{upstream}..HEAD"))
	}
	b.WriteString(section(0, "Stashes", "stash", "list"))
	b.WriteString(section(0, "Recent commits (match this message style)", "log", "--format=%s", "-n", "10"))

	// The diffs are read bounded: git is stopped once maxDiff bytes are
	// in (one more, so the cut below still happens), so a huge diff is
	// never held in memory to be cut afterwards.
	limit := 0
	if maxDiff > 0 {
		limit = maxDiff + 1
	}
	diff := section(limit, "Staged diff", "diff", "--cached", "--no-color", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty")
	if limit > 0 {
		limit = max(limit-len(diff), 1)
	}
	diff += section(limit, "Unstaged diff", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty")
	if maxDiff > 0 && len(diff) > maxDiff {
		cut := maxDiff
		for cut > 0 && !utf8.RuneStart(diff[cut]) { // don't split a character
			cut--
		}
		diff = diff[:cut] + "\n[diff truncated]\n"
	}
	b.WriteString(diff)
	return b.String()
}
