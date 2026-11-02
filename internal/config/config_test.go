package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clone.Exclude) != 0 {
		t.Errorf("nothing should be excluded by default: %v", cfg.Clone.Exclude)
	}
	if len(cfg.Agents) != 3 || cfg.Agents[0].Name != "claude" {
		t.Errorf("agents = %+v", cfg.Agents)
	}
}

func TestAgentsReplaceDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("[[agent]]\nname = \"kimi\"\ncommand = \"kimi --continue\"\n"), 0o644)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0] != (Agent{"kimi", "kimi --continue"}) {
		t.Errorf("agents = %+v", cfg.Agents)
	}
}

// An [[agent]] without a command is refused, not filled in from the
// built-in agent at the same position.
func TestAgentWithoutCommandRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("[[agent]]\nname = \"mine\"\n"), 0o644)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "need a name and a command") {
		t.Errorf("err = %v", err)
	}
}

func TestProfiles(t *testing.T) {
	cfg := Default()
	cfg.Clone.Host = "git.example.edu"
	ps := cfg.EffectiveProfiles()
	if len(ps) != 2 || ps[0].Forge != "gitlab" || ps[0].Host != "git.example.edu" || ps[1].Forge != "github" {
		t.Errorf("default profiles = %+v", ps)
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("default_profile = \"home\"\n[[profile]]\nname = \"work\"\nforge = \"gitlab\"\n[[profile]]\nname = \"home\"\nforge = \"github\"\ncolor = \"5\"\n"), 0o644)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if ps := cfg.EffectiveProfiles(); len(ps) != 2 || ps[1].Name != "home" || ps[1].Color != "5" || cfg.DefaultProfile != "home" {
		t.Errorf("profiles = %+v, default %q", ps, cfg.DefaultProfile)
	}
	cfg.Clone.DefaultGroup = "acme/tools"
	if ps := Default().EffectiveProfiles(); ps[0].DefaultGroup != "" {
		t.Errorf("default group set by default: %+v", ps[0])
	}
	cfg.Profiles, cfg.DefaultProfile = nil, ""
	if ps := cfg.EffectiveProfiles(); ps[0].DefaultGroup != "acme/tools" || cfg.Validate() != nil {
		t.Errorf("[clone] default_group not used by the default profile: %+v", ps[0])
	}
	cfg.Profiles = []Profile{{Forge: "github"}}
	if ps := cfg.EffectiveProfiles(); ps[0].Name != "github" || cfg.Profiles[0].Name != "" {
		t.Errorf("unnamed profile = %+v, config changed to %+v", ps, cfg.Profiles)
	}
}

func TestTyposRejected(t *testing.T) {
	for body, want := range map[string]string{
		"dept = 3\n":            "unknown setting(s): dept",
		"[ai]\nnodel = \"x\"\n": "ai.nodel",
		"sort = \"newest\"\n":   "use name, attention or recent",
		"[[profile]]\nname = \"x\"\nforge = \"gitea\"\n":                                                       "use gitlab or github",
		"[[profile]]\nname = \"x\"\nforge = \"gitlab\"\n[[profile]]\nname = \"x\"\nforge = \"github\"\n":       "defined twice",
		"[[profile]]\nforge = \"gitlab\"\n[[profile]]\nforge = \"gitlab\"\n":                                   "\"gitlab\" is defined twice",
		"[[profile]]\nname = \"Work\"\nforge = \"gitlab\"\n[[profile]]\nname = \"work\"\nforge = \"github\"\n": "profiles \"Work\" and \"work\" differ only in case: give each [[profile]] its own name",
		"[[profile]]\nforge = \"gitlab\"\ndefault_group = \"acme//tools\"\n":                                   "default_group = \"acme//tools\"",
		"[clone]\ndefault_group = \"acme tools\"\n":                                                            "want a group path",
		"default_profile = \"nope\"\n":                                                                         "no profile named \"nope\" (have: gitlab, github)",
		"jobs = -1\n":                                                                                          "jobs = -1",
		"tool = \"\"\n":                                                                                        "tool: must name a program",
		"[ai]\nurl = \"localhost:11434\"\n":                                                                    "ai.url",
		"[ai]\nurl = \"http://u:pw@localhost:11434\"\n":                                                        "ai.url: must not contain a user or password",
		"[ai]\nmodel = \"\"\n":                                                                                 "ai.model",
		"[ai]\nurl = \"http://localhost:11434/;private_token=SECRET\"\n":                                       "ai.url: must not contain credentials",
		"[ai]\nurl = \"http://localhost:11434/%3Fprivate_token%3DSECRET\"\n":                                   "ai.url: must not contain credentials",
		"[ai]\nurl = \"http://localhost:11434/?private_token=SECRET\"\n":                                       "ai.url: must not have a query",
		"[ai]\nurl = \"http://oauth2:SECRET@localhost:11434\"\n":                                               "ai.url: must not contain a user or password",
		"[ai]\nmodel = \"oauth2:SECRET@h/p\"\n":                                                                "ai.model: use a plain Ollama model name",
		"[ai]\nmodel = \"m;private_token=SECRET\"\n":                                                           "ai.model: use a plain Ollama model name",
		"[ai]\nmodel = \"gemma4 e2b\"\n":                                                                       "ai.model: use a plain Ollama model name",
		"[ai]\nmodel = \"m\\u001b[2J\"\n":                                                                      "ai.model: use a plain Ollama model name",
		"[ai]\nmodel = \"-m\"\n":                                                                               "ai.model: use a plain Ollama model name",
		"[ai]\ntimeout_seconds = -5\n":                                                                         "ai.timeout_seconds = -5",
		"[ai]\nkeep_alive = \"soon\"\n":                                                                        "ai.keep_alive",
		"depth = [1]\n":                                                                                        "c.toml",
		"[clone]\ndefault_group = \"-acme\"\n":                                                                 "[clone] default_group = \"-acme\"",
		"[clone]\ndefault_group = \"acme/.git\"\n":                                                             "[clone] default_group",
		"[clone]\nhost = \"https://gitlab.example.com\"\n":                                                     "[clone] host = \"https://gitlab.example.com\": use the bare host name, e.g. gitlab.example.com",
		"[[profile]]\nforge = \"gitlab\"\nhost = \"gitlab.example.com/acme\"\n":                                "profile \"gitlab\": host",
		"[[profile]]\nforge = \"gitlab\"\nhost = \"me@gitlab.example.com\"\n":                                  "use the bare host name",
		"[[profile]]\nforge = \"gitlab\"\nhost = \"gitlab example\"\n":                                         "use the bare host name",
		"[[profile]]\nforge = \"gitlab\"\nhost = \"gitlab.example.com:http\"\n":                                "use the bare host name",
		"[[profile]]\nforge = \"github\"\ndefault_group = \"acme/tools\"\n":                                    "a GitHub owner is one name",
		"[[profile]]\nforge = \"gitlab\"\ncolor = \"blue\"\n":                                                  "profile \"gitlab\": color = \"blue\": use an ANSI color number",
		"[[profile]]\nforge = \"gitlab\"\ncolor = \"256\"\n":                                                   "color = \"256\"",
		"[[profile]]\nforge = \"gitlab\"\ncolor = \"#12345\"\n":                                                "color = \"#12345\"",
		"[clone]\nhost = \"gitlab.example.com\"\n[[profile]]\nforge = \"gitlab\"\n":                            "[clone] host is ignored when [[profile]] entries exist: move it into the profile",
		"[clone]\nexclude = [\"legal\"]\n[[profile]]\nforge = \"gitlab\"\n":                                    "[clone] exclude is ignored",
		"[clone]\ndefault_group = \"acme\"\n[[profile]]\nforge = \"gitlab\"\n":                                 "[clone] default_group is ignored",
		"[ai]\nmodel = \"x\"\ndefault_profile = \"work\"\n":                                                    "top-level keys must come before the first [section]",
		"[profile]\nname = \"x\"\nforge = \"gitlab\"\n":                                                        "use [[profile]]",
		"[agent]\nname = \"x\"\ncommand = \"x\"\n":                                                             "use [[agent]]",
		"[[clone]]\nhost = \"h\"\n":                                                                            "use [clone], with single brackets",
		"[ai]\nmax_diff_bytes = -1\n":                                                                          "must be 0 (no limit) or more",
	} {
		p := filepath.Join(t.TempDir(), "c.toml")
		os.WriteFile(p, []byte(body), 0o644)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		} else if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "\x1b") {
			t.Errorf("%q: the error quotes the value: %v", body, err)
		}
	}
	for _, body := range []string{"[ai]\nmodel = \"hf.co/user/repo:Q4_K_M\"\n", "[ai]\nmodel = \"llama3.1:8b-instruct-q4_K_M\"\n",
		"[ai]\nurl = \"http://[::1]:11434\"\n", "[ai]\nurl = \"https://ollama.example.com/v1/\"\n"} {
		p := filepath.Join(t.TempDir(), "c.toml")
		os.WriteFile(p, []byte(body), 0o644)
		if _, err := Load(p); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
}

func TestForgeCaseInsensitive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("[[profile]]\nforge = \"GitLab\"\n[[profile]]\nname = \"home\"\nforge = \"GITHUB\"\n"), 0o644)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if ps := cfg.EffectiveProfiles(); ps[0].Forge != "gitlab" || ps[0].Name != "gitlab" || ps[1].Forge != "github" {
		t.Errorf("profiles = %+v", ps)
	}
	if k := cfg.Key(cfg.EffectiveProfiles()[1], "exclude"); k != `exclude in [[profile]] "home"` {
		t.Errorf("Key = %s", k)
	}
	if k := Default().Key(Default().EffectiveProfiles()[0], "host"); k != "[clone] host" {
		t.Errorf("implicit Key = %s", k)
	}
}

func TestValidProfileValues(t *testing.T) {
	for _, p := range []Profile{
		{Forge: "gitlab", Host: "gitlab.example.com", Color: "4", DefaultGroup: "acme/tools"},
		{Forge: "gitlab", Host: "gitlab.example.com:8443", Color: "#0af", DefaultGroup: "acme_1/my.tools"},
		{Forge: "gitlab", Host: "localhost", Color: "#00aaff"},
		{Forge: "github", Color: "255", DefaultGroup: "acme"},
		{Forge: "github", Color: "0"},
	} {
		cfg := Default()
		cfg.Profiles = []Profile{p}
		if err := cfg.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	// empty [clone] values (as in config.example.toml) are fine with profiles
	cfg := Default()
	cfg.Profiles = []Profile{{Forge: "github"}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("empty [clone] with profiles: %v", err)
	}
}

func TestKeepAliveForms(t *testing.T) {
	for _, k := range []string{"2m", "30s", "0", "-1", "300", ""} {
		cfg := Default()
		cfg.AI.KeepAlive = k
		if err := cfg.Validate(); err != nil {
			t.Errorf("keep_alive %q: %v", k, err)
		}
	}
}

func TestPathEnv(t *testing.T) {
	t.Setenv("GL_REPOS_CONFIG", "/elsewhere/g.toml")
	if p := Path(); p != "/elsewhere/g.toml" {
		t.Errorf("Path() = %q with GL_REPOS_CONFIG set", p)
	}
	if p, src := EnvPath(); p != "/elsewhere/g.toml" || src != "GL_REPOS_CONFIG" {
		t.Errorf("EnvPath() = %q, %q with GL_REPOS_CONFIG set", p, src)
	}
	t.Setenv("GL_REPOS_CONFIG", "")
	if p, src := EnvPath(); p != "" || src != "" {
		t.Errorf("EnvPath() = %q, %q with GL_REPOS_CONFIG unset", p, src)
	}
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if p := Path(); p != filepath.Join("/xdg", "gl-repos", "config.toml") {
		t.Errorf("Path() = %q with XDG_CONFIG_HOME set", p)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/u")
	if d := Dir(); d != filepath.Join("/home/u", ".config", "gl-repos") {
		t.Errorf("Dir() = %q without XDG_CONFIG_HOME", d)
	}
}

// The shipped example must load to exactly the defaults: every value in
// it is commented out, so it never freezes a default. Uncommented, the
// values it shows for the top level, [clone] and [ai] are the defaults.
func TestExampleConfig(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.toml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("example differs from the defaults:\n%+v\n%+v", cfg, Default())
	}
	data, _ := os.ReadFile(path)
	keyLine := regexp.MustCompile(`^# ([a-z_]+ = .*?)(\s+#.*)?$`)
	section, uncommented := "", ""
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			t.Errorf("uncommented line in the example: %q", l)
		}
		switch h := strings.TrimPrefix(l, "# "); {
		case strings.HasPrefix(h, "["):
			section = strings.Trim(h, "[]")
			if section == "clone" || section == "ai" {
				uncommented += h + "\n"
			}
		case keyLine.MatchString(l) && (section == "" || section == "clone" || section == "ai") &&
			!strings.HasPrefix(h, "default_profile"):
			uncommented += keyLine.FindStringSubmatch(l)[1] + "\n"
		}
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte(uncommented), 0o644)
	// %v: an empty list ([]) and no list (nil) are the same setting
	if cfg, err := Load(p); err != nil || fmt.Sprintf("%+v", cfg) != fmt.Sprintf("%+v", Default()) {
		t.Errorf("the commented values are not the defaults (%v):\n%s", err, uncommented)
	}
	if !strings.Contains(uncommented, "memory_guard = true") || !strings.Contains(uncommented, "depth = 1") {
		t.Errorf("values not found:\n%s", uncommented)
	}
}

func TestAIURLErrorHidesURL(t *testing.T) {
	for _, u := range []string{"ftp://u:topsecret@host", "http://u:topsecret@host:11434", "::topsecret@",
		"http://host:11434/?access_token=topsecret", "http://host:11434/#topsecret"} {
		err := (AI{URL: u, Model: "m"}).validate()
		if err == nil || strings.Contains(err.Error(), "topsecret") {
			t.Errorf("%s: %v", u, err)
		}
	}
}
