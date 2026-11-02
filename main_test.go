package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gl-repos/internal/config"
	"gl-repos/internal/forge"
)

func TestCheckExplicitConfig(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.toml")
	if err := checkExplicitConfig(missing, ""); err != nil {
		t.Errorf("the default location may be missing: %v", err)
	}
	for _, src := range []string{"-config", "GL_REPOS_CONFIG"} {
		err := checkExplicitConfig(missing, src)
		if err == nil || err.Error() != "config: "+src+" names "+missing+", which does not exist" {
			t.Errorf("%s: %v", src, err)
		}
	}
	here := filepath.Join(dir, "c.toml")
	if err := os.WriteFile(here, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkExplicitConfig(here, "-config"); err != nil {
		t.Errorf("existing file: %v", err)
	}
	if err := checkExplicitConfig(filepath.Join(here, "x"), "-config"); err == nil || !strings.Contains(err.Error(), "-config names") {
		t.Errorf("unreadable path: %v", err)
	}
}

func TestWriteConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "gl-repos", "config.toml")
	if err := writeConfig(path, configExample); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", di.Mode().Perm())
	}
	if got, _ := os.ReadFile(path); string(got) != string(configExample) || !strings.Contains(string(got), "[[profile]]") {
		t.Error("the written config is not the commented example")
	}
	os.WriteFile(path, []byte("depth = 3\n"), 0o600)
	err = writeConfig(path, configExample)
	if err == nil || !strings.Contains(err.Error(), path+" already exists; not overwriting it") {
		t.Errorf("overwrite: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "depth = 3\n" {
		t.Error("an existing config was changed")
	}
	if s := initDone(path); !strings.Contains(s, "gl-repos -doctor") || !strings.Contains(s, "glab auth login") || !strings.Contains(s, "default_group") {
		t.Errorf("next steps: %s", s)
	}
}

func TestImplicitProfileStart(t *testing.T) {
	only := func(have ...string) func(string) (string, error) {
		return func(p string) (string, error) {
			for _, h := range have {
				if p == h {
					return "/bin/" + p, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	for _, c := range []struct {
		have []string
		want string
	}{{[]string{"gh"}, "github"}, {[]string{"glab", "gh"}, "gitlab"}, {nil, "gitlab"}} {
		ps, start, err := buildProfiles(config.Default(), only(c.have...))
		if err != nil || ps[start].Name != c.want {
			t.Errorf("with %v: start %d, %v", c.have, start, err)
		}
	}
	// a chosen default_profile wins, and so does any [[profile]] order
	cfg := config.Default()
	cfg.DefaultProfile = "gitlab"
	if ps, start, _ := buildProfiles(cfg, only("gh")); ps[start].Name != "gitlab" {
		t.Errorf("default_profile ignored: %s", ps[start].Name)
	}
	cfg = config.Default()
	cfg.Profiles = []config.Profile{{Name: "work", Forge: "gitlab"}, {Name: "home", Forge: "github"}}
	ps, start, _ := buildProfiles(cfg, only("gh"))
	if ps[start].Name != "work" || ps[1].ExcludeKey != `exclude in [[profile]] "home"` {
		t.Errorf("configured profiles: start %s, %+v", ps[start].Name, ps[1])
	}
	if g := ps[0].Forge.(forge.GitLab); g.HostKey != `host in [[profile]] "work"` {
		t.Errorf("HostKey = %q", g.HostKey)
	}
}

// -list prints directory names, which anyone can choose: no escape
// sequence may reach the terminal.
func TestPrintListSanitizesNames(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	repo := filepath.Join(root, "x\x1b]52;c;ZWNobyBwd25lZA==\x07\x1b[2Jy")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	for _, kv := range [][2]string{{"core.pager", "less"}, {"credential.https://oauth2:SECRET@h.helper", "store"}} {
		if out, err := exec.Command("git", "-C", repo, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	var out, errOut strings.Builder
	printList(&out, &errOut, root, []string{repo}, 1, false)
	for _, s := range []string{out.String(), errOut.String()} {
		if strings.ContainsAny(s, "\x1b\x07") || strings.Contains(s, "SECRET") {
			t.Errorf("escape sequence or credential printed: %q", s)
		}
	}
	if !strings.Contains(out.String(), "\nxy ") || !strings.Contains(errOut.String(), "core.pager") {
		t.Errorf("out %q, errOut %q", out.String(), errOut.String())
	}
}

// -list warns about a repository whose config cannot be read: it is not
// known to run nothing.
func TestPrintListWarnsUnreadableConfig(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	repo := filepath.Join(root, "x")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[core\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	printList(&out, &errOut, root, []string{repo}, 1, false)
	if !strings.Contains(errOut.String(), "x: its own git config runs programs (config not readable, not checked)") {
		t.Errorf("out %q, errOut %q", out.String(), errOut.String())
	}
}

// -list prints branch and upstream names, which anyone who pushed can
// choose, and a valid ref name can hold a credential shape: it is
// printed redacted, like the TUI shows it.
func TestPrintListRedactsRefNames(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	repo := filepath.Join(root, "r")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.com",
			"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "x#access_token=SECRET")
	git("commit", "-q", "--allow-empty", "-m", "init")
	git("remote", "add", "origin", "/nonexistent/origin.git")
	git("update-ref", "refs/remotes/origin/y;private_token=SECRET", "HEAD")
	git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/y;private_token=SECRET")
	git("branch", "-u", "origin/y;private_token=SECRET")
	var out, errOut strings.Builder
	printList(&out, &errOut, root, []string{repo}, 1, false)
	if s := out.String() + errOut.String(); strings.Contains(s, "SECRET") {
		t.Errorf("credential printed: %q", s)
	}
	if s := out.String(); !strings.Contains(s, "\nr ") || !strings.Contains(s, " x#access_token=*** ") || !strings.Contains(s, " origin/y;private_token=***\n") {
		t.Errorf("out %q", s)
	}
}

// -list reads jobs repositories at a time and prints them in the order
// given, however the reads finish.
func TestPrintListKeepsOrder(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	var repos []string
	for _, name := range []string{"c", "a", "b", "d", "e"} {
		repo := filepath.Join(root, name)
		if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
		repos = append(repos, repo)
	}
	for _, jobs := range []int{0, 2, 9} {
		var out, errOut strings.Builder
		printList(&out, &errOut, root, repos, jobs, false)
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		var got []string
		for _, l := range lines[1:] {
			got = append(got, strings.Fields(l)[0])
		}
		if want := []string{"c", "a", "b", "d", "e"}; !slices.Equal(got, want) || errOut.Len() != 0 {
			t.Errorf("jobs %d: rows %q, errOut %q", jobs, got, errOut.String())
		}
	}
}

// -init makes the default config directory private too when it exists
// already; a directory the user named with -config (the current folder,
// their home) keeps its mode, and one that is a symlink is left as it is
// (the mode changed would be its target's).
func TestWriteConfigTightensOwnDirOnly(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	own := config.Dir()
	if own != filepath.Join(base, "gl-repos") {
		t.Fatalf("config.Dir() = %q", own)
	}
	if err := os.Mkdir(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeConfig(filepath.Join(own, "config.toml"), configExample); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(own); fi.Mode().Perm() != 0o700 {
		t.Errorf("existing default dir mode %v, want 0700", fi.Mode().Perm())
	}

	dir := filepath.Join(base, "cfg")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeConfig(filepath.Join(dir, "config.toml"), configExample); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Errorf("user's existing dir mode %v, want 0755 (left alone)", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(dir, "config.toml")); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}

	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(target)
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if err := writeConfig(filepath.Join(link, "config.toml"), configExample); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(target); after.Mode().Perm() != before.Mode().Perm() {
		t.Errorf("symlink target mode changed %v -> %v", before.Mode().Perm(), after.Mode().Perm())
	}
}

// -h and -init print the config path, which -config or GL_REPOS_CONFIG
// may have given a credential shape or escape sequences: sanitized like
// every message, the flag defaults (which name it too) included.
func TestUsageAndInitDoneRedactPath(t *testing.T) {
	path := "/tmp/x\x1b[2J/https://oauth2:SECRET@h/config.toml"
	var out strings.Builder
	usage(&out, path)
	if s := out.String(); strings.Contains(s, "SECRET") || strings.Contains(s, "\x1b") ||
		!strings.Contains(s, "Settings are read from /tmp/x") || !strings.Contains(s, "-init") {
		t.Errorf("usage: %q", s)
	}
	if s := initDone(path); strings.Contains(s, "SECRET") || strings.Contains(s, "\x1b") || !strings.Contains(s, "wrote /tmp/x") {
		t.Errorf("initDone: %q", s)
	}
}

// -list never fetches: -fetch given with it is noted once on stderr.
func TestPrintListNotesIgnoredFetch(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	repo := filepath.Join(root, "x")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	for _, fetch := range []bool{false, true} {
		var out, errOut strings.Builder
		printList(&out, &errOut, root, []string{repo}, 1, fetch)
		if got := strings.Contains(errOut.String(), "-list never fetches; -fetch ignored"); got != fetch {
			t.Errorf("fetch %v: errOut %q", fetch, errOut.String())
		}
		if !strings.Contains(out.String(), "\nx ") {
			t.Errorf("fetch %v: out %q", fetch, out.String())
		}
	}
}
