package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gl-repos/internal/forge"
)

// ErrDestCredentials refuses a destination shaped like credentials: the
// folder name would hold a secret (on disk, in the review, in git's
// arguments). It never quotes the input.
var ErrDestCredentials = errors.New("the destination looks like a URL with credentials (user:secret@, ?token=); type a folder name")

// ErrDestGitDir refuses a destination with a folder named .git on its
// way: created, it would mark its parent as a repository.
var ErrDestGitDir = errors.New("a folder named .git would be taken for a repository; type another name")

// CheckDest refuses a destination (a folder name typed for a clone or
// a new repository) that is empty, holds credentials or has a .git
// component. The check every destination passes, before anything is
// shown or created.
func CheckDest(dest string) error {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return errors.New("destination is empty")
	}
	if forge.HasCredentials(dest) {
		return ErrDestCredentials
	}
	for _, part := range strings.Split(filepath.Clean(dest), string(filepath.Separator)) {
		if strings.EqualFold(part, ".git") {
			return ErrDestGitDir
		}
	}
	return nil
}

// CloneDest validates a destination typed relative to root and returns its
// absolute path. It must stay inside root and not exist yet.
func CloneDest(root, dest string) (string, error) {
	dest = strings.TrimSpace(dest)
	if err := CheckDest(dest); err != nil {
		return "", err
	}
	if filepath.IsAbs(dest) {
		return "", fmt.Errorf("use a path relative to %s", root)
	}
	clean := filepath.Clean(dest)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("destination must be inside %s", root)
	}
	abs := filepath.Join(root, clean)
	if _, err := os.Stat(abs); err == nil {
		return "", fmt.Errorf("%s already exists", clean)
	}
	return abs, nil
}

// CloneArgs returns the git arguments to clone url into dest.
func CloneArgs(url forge.CloneURL, dest string) []string {
	return []string{"clone", "--", url.String(), dest}
}

// Clone clones url into dest (absolute, below root), running git from
// root; git creates missing parent directories and removes a partial
// clone if it fails.
func Clone(ctx context.Context, root string, url forge.CloneURL, dest string) (string, error) {
	if url.IsZero() {
		return "", forge.ErrBadCloneURL
	}
	return combined(ctx, root, true, CloneArgs(url, dest)...)
}
