package main

// gitgaze was renamed to gl-repos. For one release, gl-repos moves the
// old default config and cache directories to the new names and still
// reads the old environment variables (with a warning). Remove this
// file, its test and the Legacy* names in config and forge afterwards.

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"gl-repos/internal/config"
	"gl-repos/internal/forge"
)

// warnLegacyEnv prints one line to w for each deprecated environment
// variable that is set: in use if its new name is unset, else ignored.
func warnLegacyEnv(w io.Writer) {
	for _, v := range [][2]string{
		{config.LegacyEnvConfig, config.EnvConfig},
		{forge.LegacyEnvCacheDir, forge.EnvCacheDir},
	} {
		if os.Getenv(v[0]) == "" {
			continue
		}
		msg := fmt.Sprintf("%s is deprecated: use %s", v[0], v[1])
		if os.Getenv(v[1]) != "" {
			msg += fmt.Sprintf(" (ignored: %s is set)", v[1])
		}
		fmt.Fprintf(w, "%s: %s\n", config.Name, msg)
	}
}

// legacyState is what is found at the old default config directory.
type legacyState int

const (
	legacyNone    legacyState = iota // no old directory
	legacyMove                       // a real directory, and no new one: move it
	legacyIgnored                    // the new directory exists too: the old one is ignored
	legacyLink                       // a symlink or not a directory, and no new one: not moved
)

// legacyDir reports the state of oldDir next to newDir.
func legacyDir(oldDir, newDir string) legacyState {
	if oldDir == "" || newDir == "" {
		return legacyNone
	}
	fi, err := os.Lstat(oldDir)
	if err != nil {
		return legacyNone
	}
	if _, err := os.Lstat(newDir); err == nil {
		return legacyIgnored
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		return legacyLink
	}
	return legacyMove
}

// migrateConfigDir moves gitgaze's default config directory oldDir to
// newDir, the one of gl-repos, and tells w in one line. Call it only when the config
// path is the default one (no -config, GL_REPOS_CONFIG or
// GITGAZE_CONFIG): a config the user names is never touched.
func migrateConfigDir(w io.Writer, oldDir, newDir string) {
	// the paths and a rename error reach the terminal: redacted like
	// every other printed line (HOME or XDG_CONFIG_HOME is not ours)
	say := func(format string, a ...any) {
		fmt.Fprintln(w, forge.RedactLine(fmt.Sprintf(format, a...)))
	}
	switch legacyDir(oldDir, newDir) {
	case legacyMove:
		if err := os.Rename(oldDir, newDir); err != nil {
			say("%s: could not move your settings from %s to %s (%s was renamed): %v",
				config.Name, oldDir, newDir, config.LegacyName, err)
			return
		}
		say("%s: moved your settings from %s to %s (%s was renamed)", config.Name, oldDir, newDir, config.LegacyName)
	case legacyIgnored:
		say("%s: ignoring %s (%s was renamed): using %s; remove the old one when you no longer need it",
			config.Name, oldDir, config.LegacyName, newDir)
	case legacyLink:
		what := "not a directory"
		if fi, err := os.Lstat(oldDir); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			what = "a symlink"
		}
		say("%s: %s is %s, so it was not moved (%s was renamed): settings are read from %s",
			config.Name, oldDir, what, config.LegacyName, newDir)
	}
}

// migrateCacheDir moves gitgaze's cache directory oldDir to newDir,
// silently, if newDir does not exist yet; anything else leaves both
// alone (a cache is rebuilt on demand: a stale one costs only a refetch).
func migrateCacheDir(oldDir, newDir string) {
	if legacyDir(oldDir, newDir) == legacyMove {
		_ = os.Rename(oldDir, newDir)
	}
}

// defaultCacheDirs returns the old and new default cache directories,
// or "" if a cache directory is set in the environment.
func defaultCacheDirs() (oldDir, newDir string) {
	if os.Getenv(forge.EnvCacheDir) != "" || os.Getenv(forge.LegacyEnvCacheDir) != "" {
		return "", ""
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", ""
	}
	return filepath.Join(d, config.LegacyName), filepath.Join(d, config.Name)
}

// migrate moves the old default directories at startup. explicit is
// true when -config or a config environment variable names the file.
func migrate(w io.Writer, explicit bool) {
	if !explicit {
		migrateConfigDir(w, config.LegacyDir(), config.Dir())
	}
	migrateCacheDir(defaultCacheDirs())
}
