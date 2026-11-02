// Package config loads gl-repos settings from a TOML file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"gl-repos/internal/ai"
	"gl-repos/internal/forge"
)

// Config holds user settings. Command-line flags override these values.
type Config struct {
	Depth        int      `toml:"depth"`
	Jobs         int      `toml:"jobs"`
	FetchOnStart bool     `toml:"fetch_on_start"`
	Sort         string   `toml:"sort"` // name | attention | recent
	Skip         []string `toml:"skip"` // extra directory names never searched
	Tool         string   `toml:"tool"` // git UI opened with "o", run in the repo dir
	AI           AI       `toml:"ai"`
	Clone        Clone    `toml:"clone"`
	Agents       []Agent  `toml:"agent"` // opened with "t"; the first is the default

	// Profiles are the accounts used to clone, create and publish
	// repositories; one is active at a time ("w" switches).
	Profiles       []Profile `toml:"profile"`
	DefaultProfile string    `toml:"default_profile"` // name; default: the first
}

// Profile is one forge account, reached through its CLI login (glab or
// gh).
type Profile struct {
	Name    string   `toml:"name"`
	Forge   string   `toml:"forge"`   // gitlab | github
	Host    string   `toml:"host"`    // default: glab login / github.com
	Exclude []string `toml:"exclude"` // groups never listed, cached or cloned
	Color   string   `toml:"color"`   // badge color: ANSI number 0-255, #rgb or #rrggbb
	// DefaultGroup is the owner prefilled for new projects ("n", "u"),
	// e.g. "acme/tools". Empty means the logged-in user's namespace.
	DefaultGroup string `toml:"default_group"`
}

// EffectiveProfiles returns the configured profiles, or by default a
// GitLab profile (from [clone]) and a GitHub profile. A profile without a
// name is named after its forge.
func (c Config) EffectiveProfiles() []Profile {
	if len(c.Profiles) > 0 {
		out := slices.Clone(c.Profiles)
		for i := range out {
			if out[i].Name == "" {
				out[i].Name = out[i].Forge
			}
		}
		return out
	}
	return []Profile{
		{Name: "gitlab", Forge: "gitlab", Host: c.Clone.Host, Exclude: c.Clone.Exclude, DefaultGroup: c.Clone.DefaultGroup},
		{Name: "github", Forge: "github"},
	}
}

// Implicit reports whether the profiles are the built-in ones (no
// [[profile]] in the config).
func (c Config) Implicit() bool { return len(c.Profiles) == 0 }

// Key names the setting key of profile p in messages: "[clone] host"
// for the built-in GitLab profile, else `host in [[profile]] "work"`.
func (c Config) Key(p Profile, key string) string {
	if c.Implicit() {
		if p.Forge == "gitlab" {
			return "[clone] " + key
		}
		return key + " in a [[profile]] for " + p.Forge
	}
	return fmt.Sprintf("%s in [[profile]] %q", key, p.Name)
}

// Agent is a coding assistant (or any command) started in a repository.
type Agent struct {
	Name    string `toml:"name"`
	Command string `toml:"command"` // program and arguments, run in the repo dir
}

// Clone configures the default GitLab profile when no [[profile]] is set.
type Clone struct {
	// Host is the GitLab server searched via glab, e.g. "gitlab.com".
	// Empty means the most common origin host of the listed repositories.
	Host string `toml:"host"`
	// Exclude optionally names groups that are never listed, cached or
	// cloned: a group path ("org/legal") or a group name at any level
	// ("legal"). Empty by default.
	Exclude []string `toml:"exclude"`
	// DefaultGroup is the owner prefilled for new projects, as in
	// [[profile]].
	DefaultGroup string `toml:"default_group"`
}

// AI configures the Ollama-backed assistant.
type AI struct {
	URL          string `toml:"url"`
	Model        string `toml:"model"`
	MaxDiffBytes int    `toml:"max_diff_bytes"` // diff context sent to the model
	MaxCtx       int    `toml:"max_ctx"`        // upper bound for Ollama num_ctx

	// Recovery controls: give up quickly instead of hanging the machine.
	TimeoutSeconds      int    `toml:"timeout_seconds"`       // whole request
	LoadTimeoutSeconds  int    `toml:"load_timeout_seconds"`  // until first output
	StallTimeoutSeconds int    `toml:"stall_timeout_seconds"` // max silence mid-answer
	KeepAlive           string `toml:"keep_alive"`            // unload a model we loaded after this idle time
	MemoryGuard         bool   `toml:"memory_guard"`          // refuse models that don't fit in free RAM
}

// Default returns the built-in settings.
func Default() Config {
	return Config{
		Depth: 1,
		Sort:  "name",
		Tool:  "lazygit",
		Agents: []Agent{
			{Name: "claude", Command: "claude"},
			{Name: "codex", Command: "codex"},
			{Name: "kimi", Command: "kimi"},
		},
		AI: AI{
			URL:          "http://localhost:11434",
			Model:        "gemma4:e2b",
			MaxDiffBytes: 24000,
			MaxCtx:       32768,

			TimeoutSeconds:      120,
			LoadTimeoutSeconds:  90,
			StallTimeoutSeconds: 20,
			KeepAlive:           "2m",
			MemoryGuard:         true,
		},
	}
}

// Name is the program's name and the name of its config and cache
// directories. LegacyName is its old name: gitgaze was renamed to
// gl-repos, and for one release the old directories are moved and the
// old environment variables still work (see main's migrate.go).
const (
	Name       = "gl-repos"
	LegacyName = "gitgaze"

	// EnvConfig names the config file; LegacyEnvConfig is its deprecated
	// old name, used only when EnvConfig is unset.
	EnvConfig       = "GL_REPOS_CONFIG"
	LegacyEnvConfig = "GITGAZE_CONFIG"
)

// Path returns the config file location in use without -config:
// $GL_REPOS_CONFIG if set, else the deprecated $GITGAZE_CONFIG, else
// config.toml in Dir().
func Path() string {
	if p, _ := EnvPath(); p != "" {
		return p
	}
	d := Dir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "config.toml")
}

// EnvPath returns the config file named by the environment and the
// variable that named it: GL_REPOS_CONFIG, else GITGAZE_CONFIG; "" if
// neither is set.
func EnvPath() (path, source string) {
	for _, v := range []string{EnvConfig, LegacyEnvConfig} {
		if p := os.Getenv(v); p != "" {
			return p, v
		}
	}
	return "", ""
}

// Dir is the default config directory: $XDG_CONFIG_HOME/gl-repos, else
// ~/.config/gl-repos; "" if neither can be found.
func Dir() string { return configDir(Name) }

// LegacyDir is where gitgaze kept its config, by the same rules as Dir.
func LegacyDir() string { return configDir(LegacyName) }

func configDir(name string) string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, name)
}

// Load reads the config at path on top of the defaults. A missing file is
// not an error; a malformed one, an unknown key or an invalid value is,
// with the path and the key named.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	// [[agent]] entries replace the built-in ones: decoded into them, an
	// entry without a command would inherit the default's.
	cfg.Agents = nil
	md, err := toml.DecodeFile(path, &cfg)
	if cfg.Agents == nil {
		cfg.Agents = Default().Agents
	}
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("%s: %w%s", path, err, tableHint(err))
	}
	for i := range cfg.Profiles {
		cfg.Profiles[i].Forge = strings.ToLower(strings.TrimSpace(cfg.Profiles[i].Forge)) // "GitLab" is fine
	}
	// A typo must not be silently ignored.
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return cfg, fmt.Errorf("%s: unknown setting(s): %s%s", path, strings.Join(keys, ", "), misplacedHint(keys))
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// topLevel lists the keys that belong before the first [section].
var topLevel = []string{"depth", "jobs", "fetch_on_start", "sort", "skip", "tool", "default_profile"}

// misplacedHint explains unknown keys like "ai.default_profile": a
// top-level key written after a [section] belongs to that section.
func misplacedHint(keys []string) string {
	for _, k := range keys {
		if i := strings.LastIndex(k, "."); i > 0 && slices.Contains(topLevel, k[i+1:]) {
			return fmt.Sprintf(" (%s is a top-level key: top-level keys must come before the first [section])", k[i+1:])
		}
	}
	return ""
}

// tableHint explains a TOML type error for the array tables: [profile]
// or [agent] written with single brackets (or [[clone]] with double).
func tableHint(err error) string {
	msg := err.Error()
	for _, t := range []string{"profile", "agent"} {
		if strings.Contains(msg, `"`+t+`"`) && strings.Contains(msg, "slice") {
			return fmt.Sprintf(" (use [[%s]]: each %s is its own [[%s]] entry)", t, t, t)
		}
	}
	for _, t := range []string{"clone", "ai"} {
		if strings.Contains(msg, `"`+t+`"`) && strings.Contains(msg, "expected table") {
			return fmt.Sprintf(" (use [%s], with single brackets)", t)
		}
	}
	return ""
}

// Validate checks values the type system cannot: enumerations, numbers
// that must not be negative, the Ollama URL and keep_alive, and that
// agents and profiles are complete and unambiguous.
func (c Config) Validate() error {
	switch c.Sort {
	case "name", "attention", "recent":
	default:
		return fmt.Errorf("sort = %q: use name, attention or recent", c.Sort)
	}
	if c.Depth < 0 {
		return fmt.Errorf("depth = %d: must be 0 or more", c.Depth)
	}
	if c.Jobs < 0 {
		return fmt.Errorf("jobs = %d: must be 0 (number of CPUs) or more", c.Jobs)
	}
	if strings.TrimSpace(c.Tool) == "" {
		return fmt.Errorf("tool: must name a program (e.g. \"lazygit\")")
	}
	for _, a := range c.Agents {
		if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Command) == "" {
			return fmt.Errorf("[[agent]] entries need a name and a command")
		}
	}
	if err := c.AI.validate(); err != nil {
		return err
	}
	if len(c.Profiles) > 0 {
		// [clone] only configures the default profile: with [[profile]]
		// entries it would be silently ignored.
		for _, k := range []struct {
			key string
			set bool
		}{
			{"host", c.Clone.Host != ""}, {"exclude", len(c.Clone.Exclude) > 0}, {"default_group", c.Clone.DefaultGroup != ""},
		} {
			if k.set {
				return fmt.Errorf("[clone] %s is ignored when [[profile]] entries exist: move it into the profile", k.key)
			}
		}
	}
	for _, p := range c.Profiles {
		if p.Forge != "gitlab" && p.Forge != "github" {
			return fmt.Errorf("profile %q: forge = %q: use gitlab or github", p.Name, p.Forge)
		}
	}
	var names []string
	for _, p := range c.EffectiveProfiles() {
		// The default GitLab profile takes its settings from [clone].
		where := fmt.Sprintf("profile %q: ", p.Name)
		if len(c.Profiles) == 0 {
			where = "[clone] "
		}
		if p.Host != "" {
			if err := forge.ValidHost(p.Host); err != nil {
				return fmt.Errorf("%shost = %q: %w", where, p.Host, err)
			}
		}
		if err := validColor(p.Color); err != nil {
			return fmt.Errorf("%scolor = %q: %w", where, p.Color, err)
		}
		if g := p.DefaultGroup; g != "" {
			if err := forge.CheckGroup(g); err != nil {
				return fmt.Errorf("%sdefault_group = %q: %w", where, g, err)
			}
			if p.Forge == "github" && strings.Contains(g, "/") {
				return fmt.Errorf("%sdefault_group = %q: a GitHub owner is one name (a user or organization)", where, g)
			}
		}
		if slices.Contains(names, p.Name) {
			return fmt.Errorf("profile %q is defined twice: give each [[profile]] its own name", p.Name)
		}
		// caches are named after the profile; on a case-insensitive file
		// system "Work" and "work" would share them
		if i := slices.IndexFunc(names, func(n string) bool { return strings.EqualFold(n, p.Name) }); i >= 0 {
			return fmt.Errorf("profiles %q and %q differ only in case: give each [[profile]] its own name", names[i], p.Name)
		}
		names = append(names, p.Name)
	}
	if c.DefaultProfile != "" && !slices.Contains(names, c.DefaultProfile) {
		return fmt.Errorf("no profile named %q (have: %s)", c.DefaultProfile, strings.Join(names, ", "))
	}
	return nil
}

// validColor accepts "" (a color by position), an ANSI color number
// 0-255, or a hex color #rgb / #rrggbb.
func validColor(c string) error {
	if c == "" {
		return nil
	}
	if n, err := strconv.Atoi(c); err == nil && n >= 0 && n <= 255 && c[0] != '+' && c[0] != '-' {
		return nil
	}
	if hex, ok := strings.CutPrefix(c, "#"); ok && (len(hex) == 3 || len(hex) == 6) {
		if _, err := strconv.ParseUint(hex, 16, 32); err == nil {
			return nil
		}
	}
	return errors.New("use an ANSI color number 0-255 or #rrggbb (or #rgb)")
}

func (a AI) validate() error {
	u, err := url.Parse(a.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// the URL itself is not echoed: it may hold credentials
		return fmt.Errorf("ai.url: not an http(s) URL with a host; use http://host:port")
	}
	if u.User != nil { // doctor and errors print the URL: keep credentials out of it
		return fmt.Errorf("ai.url: must not contain a user or password; put Ollama behind a proxy without them in the URL")
	}
	if u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(a.URL, "?#") { // nor a token in a query
		return fmt.Errorf("ai.url: must not have a query or fragment; use http://host:port")
	}
	// the URL is the prefix of every request and is printed by doctor:
	// no credential of any shape gl-repos knows (";private_token=…" in
	// the path, percent-encoded forms), and nothing that could hide one
	if strings.ContainsAny(a.URL, ";%") || forge.HasCredentials(a.URL) {
		return fmt.Errorf("ai.url: must not contain credentials, \";\" or \"%%\"; use http://host:port")
	}
	if strings.TrimSpace(a.Model) == "" {
		return fmt.Errorf("ai.model: must name an Ollama model")
	}
	if err := ai.CheckModel(a.Model); err != nil { // sent in every request, shown in messages
		return fmt.Errorf("ai.model: %w", err)
	}
	for _, n := range []struct {
		key  string
		val  int
		zero string // what 0 means
	}{
		{"ai.max_diff_bytes", a.MaxDiffBytes, "no limit"}, {"ai.max_ctx", a.MaxCtx, "the built-in default"},
		{"ai.timeout_seconds", a.TimeoutSeconds, "the built-in default"},
		{"ai.load_timeout_seconds", a.LoadTimeoutSeconds, "the built-in default"},
		{"ai.stall_timeout_seconds", a.StallTimeoutSeconds, "the built-in default"},
	} {
		if n.val < 0 {
			return fmt.Errorf("%s = %d: must be 0 (%s) or more", n.key, n.val, n.zero)
		}
	}
	// Ollama takes a Go duration ("2m"), seconds as a number, or -1 (keep
	// forever).
	if k := a.KeepAlive; k != "" {
		if _, err := time.ParseDuration(k); err != nil {
			if _, err := strconv.Atoi(k); err != nil {
				return fmt.Errorf("ai.keep_alive = %q: use a duration like \"2m\" or \"30s\"", k)
			}
		}
	}
	return nil
}
