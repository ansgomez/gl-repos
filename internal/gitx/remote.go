package gitx

import (
	"context"
	"strings"
)

// InitArgs returns the git arguments to create an empty repository at
// dest (relative to the directory git runs in).
func InitArgs(dest string) []string { return []string{"init", "--", dest} }

// Init creates an empty repository at dest (absolute, below root).
func Init(ctx context.Context, root, dest string) (string, error) {
	return combined(ctx, root, false, InitArgs(dest)...)
}

// Remotes lists the names of dir's remotes.
func Remotes(ctx context.Context, dir string) ([]string, error) {
	out, err := output(ctx, dir, "remote")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// AddRemoteArgs returns the git arguments to add remote name at url.
func AddRemoteArgs(name, url string) []string { return []string{"remote", "add", name, url} }

// AddRemote adds remote name at url to the repository in dir.
func AddRemote(ctx context.Context, dir, name, url string) (string, error) {
	return combined(ctx, dir, false, AddRemoteArgs(name, url)...)
}

// PushToArgs pushes the current branch to remote, tracking it there only
// if track is set (a branch's existing upstream is never changed).
func PushToArgs(remote string, track bool) []string {
	if track {
		return []string{"push", "-u", remote, "HEAD"}
	}
	return []string{"push", remote, "HEAD"}
}

// PushTo pushes dir's current branch to remote (see PushToArgs).
func PushTo(ctx context.Context, dir, remote string, track bool) (string, error) {
	return combined(ctx, dir, true, PushToArgs(remote, track)...)
}

// HasUpstream reports whether dir's current branch tracks a remote branch.
func HasUpstream(ctx context.Context, dir string) bool {
	_, err := output(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return err == nil
}

// HasCommits reports whether dir's HEAD points at a commit.
func HasCommits(ctx context.Context, dir string) bool {
	_, err := output(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

// OnBranch reports whether dir's HEAD is a branch (possibly without
// commits yet), not a detached commit.
func OnBranch(ctx context.Context, dir string) bool {
	_, err := output(ctx, dir, "symbolic-ref", "-q", "HEAD")
	return err == nil
}
