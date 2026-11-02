package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gl-repos/internal/ai"
	"gl-repos/internal/config"
	"gl-repos/internal/doctor"
	"gl-repos/internal/forge"
	"gl-repos/internal/tui"
)

// configExample is the commented config written by -init.
//
//go:embed config.example.toml
var configExample []byte

// writeConfig writes data to path for -init: the directory is created
// private (0700; the default config directory is made private too when
// it exists already), the file is 0600, and an existing file is never
// overwritten.
func writeConfig(path string, data []byte) error {
	if path == "" {
		return errors.New("init: no config path (set -config or GL_REPOS_CONFIG)")
	}
	dir := filepath.Dir(path)
	_, err := os.Lstat(dir)
	created := errors.Is(err, fs.ErrNotExist)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone, and so does
	// -init unless it is gl-repos's own (config.Dir()): -config may name
	// a file in any folder of yours (the current one, your home), whose
	// mode is not gl-repos's to change. A symlink is left alone too: the
	// mode changed would be its target's.
	own := created || (config.Dir() != "" && filepath.Clean(dir) == filepath.Clean(config.Dir()))
	if fi, err := os.Lstat(dir); own && err == nil && fi.IsDir() && fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("init: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("init: %s already exists; not overwriting it (edit it, or move it away first)", path)
	}
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	// A half-written file is removed: left behind, the next -init would
	// refuse to overwrite it.
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("init: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("init: %w", err)
	}
	return nil
}

// initDone is what -init prints after writing the config: the Quick
// start steps that follow it (README.md, docs/getting-started.md), in
// the same order. The path is the
// user's (-config, GL_REPOS_CONFIG) and printed like every path in
// messages: sanitized, URL credentials hidden.
func initDone(path string) string {
	return "wrote " + forge.RedactLine(path) + " (everything in it is commented out: the defaults)\n\nNext:\n" +
		"  1. Log in, if you have not yet: glab auth login --hostname gitlab.example.com\n" +
		"     (a token with the api scope to create projects), and/or gh auth login;\n" +
		"     then once: ssh -T git@gitlab.example.com (or git@github.com).\n" +
		"  2. Edit it: uncomment a [[profile]] and set its host and default_group\n" +
		"     (e.g. host = \"gitlab.example.com\", default_group = \"acme/tools\").\n" +
		"  3. Check the setup: gl-repos -doctor\n"
}

// runDoctor checks the setup and returns the exit status: 1 if any
// check failed. A broken config is reported, not fatal: the remaining
// checks run with the defaults.
func runDoctor(path, source string, applyFlags func(*config.Config)) int {
	_, statErr := os.Stat(path)
	exists := statErr == nil
	cfg, err := config.Load(path)
	if err != nil {
		cfg = config.Default()
	}
	applyFlags(&cfg)
	if err == nil {
		if verr := cfg.Validate(); verr != nil { // the flags may have broken it
			err, cfg = verr, config.Default()
		}
	}
	s := doctor.Setup{
		ConfigPath: path, ConfigSource: source, ConfigExists: exists, ConfigErr: err,
		AI: ai.Client{URL: cfg.AI.URL, Model: cfg.AI.Model},
	}
	s.LegacyConfigDir, s.LegacyConfigMoves = legacyConfig(source)
	if words, err := tui.SplitCommand(cfg.Tool); err == nil && len(words) > 0 {
		s.Tool = words[0]
	} else {
		s.Tool = cfg.Tool
	}
	for _, a := range cfg.Agents {
		prog := a.Command
		if words, err := tui.SplitCommand(a.Command); err == nil && len(words) > 0 {
			prog = words[0]
		}
		s.Agents = append(s.Agents, doctor.Agent{Name: a.Name, Program: prog})
	}
	for _, p := range cfg.EffectiveProfiles() {
		f, err := profileForge(cfg, p)
		if err != nil {
			continue // reported by the config check
		}
		s.Profiles = append(s.Profiles, doctor.Profile{Name: p.Name, Forge: f, DefaultGroup: p.DefaultGroup,
			Implicit: cfg.Implicit(), HostKey: cfg.Key(p, "host")})
	}
	if doctor.Run(context.Background(), s, doctor.DefaultEnv(), os.Stdout) > 0 {
		return 1
	}
	return 0
}

// legacyConfig returns gitgaze's old config directory if it is still
// there, and whether the next start moves it (only from the default
// location, to a new directory that does not exist yet).
func legacyConfig(source string) (string, bool) {
	oldDir := config.LegacyDir()
	if _, err := os.Lstat(oldDir); oldDir == "" || err != nil {
		return "", false
	}
	return oldDir, source == "" && legacyDir(oldDir, config.Dir()) == legacyMove
}
