package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandbox points HOME and the XDG directories at a temp dir, so no test
// here can see or move the real settings, and clears the config and
// cache variables.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache")) // Linux; macOS uses ~/Library/Caches
	for _, v := range []string{"GL_REPOS_CONFIG", "GITGAZE_CONFIG", "GL_REPOS_CACHE_DIR", "GITGAZE_CACHE_DIR"} {
		t.Setenv(v, "")
	}
	return home
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateMovesConfigAndCache(t *testing.T) {
	home := sandbox(t)
	oldDir, newDir := filepath.Join(home, ".config", "gitgaze"), filepath.Join(home, ".config", "gl-repos")
	writeFile(t, filepath.Join(oldDir, "config.toml"), "depth = 2\n")
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cache, home) {
		t.Fatalf("cache dir %s is outside the sandbox", cache)
	}
	writeFile(t, filepath.Join(cache, "gitgaze", "model-sizes.json"), "{}")

	var w bytes.Buffer
	migrate(&w, false)
	want := "gl-repos: moved your settings from " + oldDir + " to " + newDir + " (gitgaze was renamed)\n"
	if w.String() != want {
		t.Errorf("stderr %q, want %q", w.String(), want)
	}
	if got, _ := os.ReadFile(filepath.Join(newDir, "config.toml")); string(got) != "depth = 2\n" {
		t.Errorf("config not moved: %q", got)
	}
	if _, err := os.Lstat(oldDir); err == nil {
		t.Error("old config dir still there")
	}
	if _, err := os.Stat(filepath.Join(cache, "gl-repos", "model-sizes.json")); err != nil {
		t.Errorf("cache not moved: %v", err)
	}

	w.Reset()
	migrate(&w, false) // second start: nothing left to do
	if w.Len() != 0 {
		t.Errorf("second start printed %q", w.String())
	}
}

func TestMigrateHonorsXDG(t *testing.T) {
	home := sandbox(t)
	xdg := filepath.Join(home, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeFile(t, filepath.Join(xdg, "gitgaze", "config.toml"), "")
	writeFile(t, filepath.Join(home, ".config", "gitgaze", "config.toml"), "") // not the default: untouched
	var w bytes.Buffer
	migrate(&w, false)
	if _, err := os.Stat(filepath.Join(xdg, "gl-repos", "config.toml")); err != nil || !strings.Contains(w.String(), filepath.Join(xdg, "gitgaze")) {
		t.Errorf("XDG config not moved (%v): %q", err, w.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "gitgaze")); err != nil {
		t.Error("~/.config/gitgaze moved although XDG_CONFIG_HOME is set")
	}
}

func TestMigrateBothExist(t *testing.T) {
	home := sandbox(t)
	oldDir, newDir := filepath.Join(home, ".config", "gitgaze"), filepath.Join(home, ".config", "gl-repos")
	writeFile(t, filepath.Join(oldDir, "config.toml"), "old")
	writeFile(t, filepath.Join(newDir, "config.toml"), "new")
	var w bytes.Buffer
	migrate(&w, false)
	if !strings.HasPrefix(w.String(), "gl-repos: ignoring "+oldDir+" (gitgaze was renamed): using "+newDir) || strings.Count(w.String(), "\n") != 1 {
		t.Errorf("stderr %q", w.String())
	}
	for dir, want := range map[string]string{oldDir: "old", newDir: "new"} {
		if got, _ := os.ReadFile(filepath.Join(dir, "config.toml")); string(got) != want {
			t.Errorf("%s changed: %q", dir, got)
		}
	}
}

func TestMigrateSymlinkNotMoved(t *testing.T) {
	home := sandbox(t)
	target := filepath.Join(home, "dotfiles", "gitgaze")
	writeFile(t, filepath.Join(target, "config.toml"), "")
	oldDir := filepath.Join(home, ".config", "gitgaze")
	os.MkdirAll(filepath.Dir(oldDir), 0o700)
	if err := os.Symlink(target, oldDir); err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	migrate(&w, false)
	if !strings.HasPrefix(w.String(), "gl-repos: "+oldDir+" is a symlink, so it was not moved") {
		t.Errorf("stderr %q", w.String())
	}
	if fi, err := os.Lstat(oldDir); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was changed")
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "gl-repos")); err == nil {
		t.Error("new config dir created")
	}
}

// The directories named in the migration lines come from HOME or
// XDG_CONFIG_HOME: a credential shape or an escape in them is redacted
// like in every other printed line.
func TestMigrateLinesRedacted(t *testing.T) {
	home := filepath.Join(sandbox(t), "u\x1b[31m-oauth2:glpat-secret@h")
	t.Setenv("HOME", home)
	oldDir := filepath.Join(home, ".config", "gitgaze")
	writeFile(t, filepath.Join(oldDir, "config.toml"), "")
	var w bytes.Buffer
	migrate(&w, false)
	if out := w.String(); !strings.Contains(out, "moved your settings from") || !strings.Contains(out, "u-oauth2:***@h/.config/gitgaze") ||
		strings.Contains(out, "glpat-secret") || strings.Contains(out, "\x1b") {
		t.Errorf("stderr %q", out)
	}
}

func TestMigrateExplicitConfigUntouched(t *testing.T) {
	home := sandbox(t)
	oldDir := filepath.Join(home, ".config", "gitgaze")
	writeFile(t, filepath.Join(oldDir, "config.toml"), "")
	var w bytes.Buffer
	migrate(&w, true)
	if w.Len() != 0 {
		t.Errorf("stderr %q", w.String())
	}
	if _, err := os.Stat(oldDir); err != nil {
		t.Error("config moved although -config or a config variable was given")
	}
	if dir, moves := legacyConfig("-config"); dir != oldDir || moves {
		t.Errorf("doctor with -config: %q, %v", dir, moves)
	}
	if dir, moves := legacyConfig(""); dir != oldDir || !moves {
		t.Errorf("doctor at the default location: %q, %v", dir, moves)
	}
	writeFile(t, filepath.Join(home, ".config", "gl-repos", "config.toml"), "")
	if dir, moves := legacyConfig(""); dir != oldDir || moves {
		t.Errorf("doctor with both dirs: %q, %v", dir, moves)
	}
	os.RemoveAll(oldDir)
	if dir, _ := legacyConfig(""); dir != "" {
		t.Errorf("doctor without an old dir: %q", dir)
	}
}

func TestMigrateCacheVariableUntouched(t *testing.T) {
	home := sandbox(t)
	cache, _ := os.UserCacheDir()
	writeFile(t, filepath.Join(cache, "gitgaze", "x.json"), "")
	t.Setenv("GITGAZE_CACHE_DIR", filepath.Join(home, "elsewhere"))
	migrate(&bytes.Buffer{}, false)
	if _, err := os.Stat(filepath.Join(cache, "gitgaze", "x.json")); err != nil {
		t.Error("default cache moved although a cache variable is set")
	}
}

func TestWarnLegacyEnv(t *testing.T) {
	sandbox(t)
	var w bytes.Buffer
	warnLegacyEnv(&w)
	if w.Len() != 0 {
		t.Errorf("no variables set: %q", w.String())
	}
	t.Setenv("GITGAZE_CONFIG", "/a.toml")
	t.Setenv("GITGAZE_CACHE_DIR", "/c")
	t.Setenv("GL_REPOS_CACHE_DIR", "/n")
	warnLegacyEnv(&w)
	want := "gl-repos: GITGAZE_CONFIG is deprecated: use GL_REPOS_CONFIG\n" +
		"gl-repos: GITGAZE_CACHE_DIR is deprecated: use GL_REPOS_CACHE_DIR (ignored: GL_REPOS_CACHE_DIR is set)\n"
	if w.String() != want {
		t.Errorf("stderr %q, want %q", w.String(), want)
	}
}
