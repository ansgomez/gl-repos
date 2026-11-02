// Package scan locates git repositories below a root directory.
package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skipDirs are never descended into: they are large and almost never
// contain repositories the user cares about.
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	".venv":        true,
	"venv":         true,
	"target":       true,
}

// Find returns the absolute paths of all git repositories found at most
// maxDepth directory levels below root. Depth 0 checks only root itself.
// The search does not descend into a repository once one is found, and
// hidden directories are skipped, as are the built-in skip list and any
// names in extraSkip.
func Find(root string, maxDepth int, extraSkip ...string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}

	skip := make(map[string]bool, len(skipDirs)+len(extraSkip))
	for name := range skipDirs {
		skip[name] = true
	}
	for _, name := range extraSkip {
		skip[name] = true
	}

	var repos []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if isRepo(dir) {
			repos = append(repos, dir)
			return
		}
		if depth >= maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || skip[name] {
				continue
			}
			walk(filepath.Join(dir, name), depth+1)
		}
	}
	walk(root, 0)
	sort.Strings(repos)
	return repos, nil
}

// isRepo reports whether dir is a repository root. ".git" may be a
// directory or, for worktrees and submodules, a file.
func isRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
