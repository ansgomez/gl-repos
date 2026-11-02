package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

// Without a glab login the clone box says so at once, with the fix.
func TestCloneWithoutHostExplains(t *testing.T) {
	defer func(f func(context.Context) []string) { forge.LoggedInHosts = f }(forge.LoggedInHosts)
	forge.LoggedInHosts = func(context.Context) []string { return nil }
	defer func(f func(string) (string, error)) { lookPath = f }(lookPath)
	lookPath = func(p string) (string, error) { return "/bin/" + p, nil }

	m := Model{width: 120, height: 30,
		profiles: []profileState{{Profile: Profile{Name: "gitlab", Forge: forge.GitLab{HostKey: "[clone] host"}}}}}
	cmd := m.openClone("")
	if out := ansi.Strip(m.viewClone()); !strings.Contains(out, "looking for your glab login") {
		t.Errorf("while detecting:\n%s", out)
	}
	next, _ := m.Update(cmd())
	m = next.(Model)
	out := ansi.Strip(m.viewClone())
	if !strings.Contains(out, "no GitLab host found: glab auth login --hostname <host>, or set [clone] host") ||
		strings.Contains(out, "from your glab login") || strings.Contains(out, "no project list yet") {
		t.Errorf("no host:\n%s", out)
	}

	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	m = Model{width: 120, height: 30, profiles: []profileState{{Profile: Profile{Name: "gitlab", Forge: forge.GitLab{}}}}}
	cmd = m.openClone("")
	next, _ = m.Update(cmd())
	if out := ansi.Strip(next.(Model).viewClone()); !strings.Contains(out, "glab not found: install it, or paste a clone URL") {
		t.Errorf("no glab:\n%s", out)
	}
}

// The hosts "glab auth status" reports are taken only as plain host
// names (forge.ValidHost): a tampered status cannot put anything else
// into the profile (badge, clone box), a cache file name or a glab
// --hostname argument.
func TestCloneTakesOnlyPlainHostsFromGlab(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("GL_REPOS_CACHE_DIR", cache)
	defer func(f func(context.Context) []byte) { forge.GLAuthStatusAll = f }(forge.GLAuthStatusAll)
	defer func(f func(string) (string, error)) { lookPath = f }(lookPath)
	lookPath = func(p string) (string, error) { return "/bin/" + p, nil }
	bad := "x\n  ✓ Logged in to h\x1b[2J;private_token=SECRET as me (/x)\n" +
		"  ✓ Logged in to -hostname=evil.example.com as me (/x)\n  ✓ Logged in to oauth2:SECRET@h as me (/x)\n"
	forge.GLAuthStatusAll = func(context.Context) []byte { return []byte(bad) }
	open := func() Model {
		m := Model{width: 120, height: 30,
			profiles: []profileState{{Profile: Profile{Name: "gitlab", Forge: forge.GitLab{HostKey: "[clone] host"}}}}}
		cmd := m.openClone("")
		next, _ := m.Update(cmd())
		return next.(Model)
	}
	m := open()
	shown := ansi.Strip(m.viewClone()) + "\n" + ansi.Strip(m.profileInfo(0)) + "\n" + ansi.Strip(m.View())
	if strings.Contains(shown, "SECRET") || strings.Contains(shown, "evil") || strings.Contains(shown, "\x1b[2J") ||
		!strings.Contains(shown, "no GitLab host found") || m.forge().Hostname() != "" {
		t.Errorf("host %q, shown:\n%s", m.forge().Hostname(), shown)
	}
	if left, _ := os.ReadDir(cache); len(left) != 0 {
		t.Errorf("cache files named after a bad host: %v", left)
	}
	// a plain host beside them is the one taken
	forge.GLAuthStatusAll = func(context.Context) []byte { return []byte(bad + "  ✓ Logged in to good.example.com as me (/x)\n") }
	m = open()
	shown = ansi.Strip(m.viewClone()) + "\n" + ansi.Strip(m.profileInfo(0))
	if m.forge().Hostname() != "good.example.com" || strings.Contains(shown, "SECRET") || strings.Contains(shown, "evil") ||
		!strings.Contains(shown, "good.example.com") {
		t.Errorf("host %q, shown:\n%s", m.forge().Hostname(), shown)
	}
}

// The cached project list is read in a command, never in Update, and a
// fresh one is not fetched again.
func TestCloneCacheReadInCommand(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	if err := forge.SaveCache("work", "h", []forge.Project{{Path: "acme/calc", Name: "calc"}}); err != nil {
		t.Fatal(err)
	}
	forge.SaveGroupCache("work", "h", []forge.Project{{Path: "acme", Name: "acme"}})
	m := Model{width: 120, height: 30, profiles: []profileState{{Profile: Profile{Name: "work", Forge: forge.GitLab{Host: "h"}}}}}
	cmd := m.openClone("")
	if len(m.clone.all) != 0 || cmd == nil {
		t.Fatalf("cache read in Update: %+v", m.clone.all)
	}
	if out := ansi.Strip(m.viewClone()); !strings.Contains(out, "loading your project list") {
		t.Errorf("while reading:\n%s", out)
	}
	next, fetch := m.Update(cmd())
	m = next.(Model)
	if len(m.clone.all) != 1 || len(m.clone.groups) != 1 || fetch != nil {
		t.Errorf("after the read: %+v %+v, fetch %v", m.clone.all, m.clone.groups, fetch != nil)
	}
	// another profile on the same host has its own (empty) cache
	m = Model{width: 120, height: 30, profiles: []profileState{{Profile: Profile{Name: "audit", Forge: forge.GitLab{Host: "h", API: func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(`[]`), nil
	}}}}}}
	cmd = m.openClone("")
	next, fetch = m.Update(cmd())
	if len(next.(Model).clone.all) != 0 || fetch == nil {
		t.Errorf("profile audit used work's cache: %+v", next.(Model).clone.all)
	}
}

// Publishing a detached HEAD is refused before anything is created.
func TestPublishDetachedRefused(t *testing.T) {
	m := publishModel()
	m.create.step, m.create.loading = stepPath, true
	m.handlePublishInfo(publishInfoMsg{idx: 0, hasCommits: true, detached: true})
	if m.create != nil || m.mode != modeList || !strings.Contains(m.flash, "tool: HEAD is detached (no branch): switch to a branch first") {
		t.Errorf("detached: create %v, flash %q", m.create != nil, m.flash)
	}
}

// New subgroups are never more visible than their parent: GitLab would
// refuse them (400) and then the project.
func TestSubgroupVisibilityClamped(t *testing.T) {
	review := func(parentVis string, vis int) Model {
		m := groupStepModel("")
		m.profiles[0].Forge = forge.GitLab{Host: "h", API: func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
			if ep == "groups/acme%2Ftools" {
				return []byte(`{"id":9,"full_path":"acme/tools","path":"tools","visibility":"` + parentVis + `"}`), nil
			}
			return nil, errors.New(`{"message":"404 Group Not Found"}`)
		}}
		m.create.vis = vis
		m.input.SetValue("acme/tools/new/tool")
		next, cmd := m.handleCreateKey(keyEnter)
		next, _ = next.(Model).Update(cmd())
		return next.(Model)
	}
	m := review("private", 2) // a public project below a private group
	out := strings.Join(strings.Fields(ansi.Strip(m.viewCreate())), " ")
	for _, want := range []string{
		"it will be created as a new private subgroup of",
		"(acme/tools is private, and a subgroup can't be more visible than its parent)",
		"-f visibility=private",
		"a public project can't be in a private group: choose private or narrower with ctrl+v",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("review lacks %q:\n%s", want, out)
		}
	}
	if m = press(m, keyY); m.create.running {
		t.Error("y ran a plan GitLab refuses")
	}

	m = review("internal", 1) // internal below internal: fine
	out = strings.Join(strings.Fields(ansi.Strip(m.viewCreate())), " ")
	if !strings.Contains(out, "new internal subgroup of acme/tools") || strings.Contains(out, "can't be in") {
		t.Errorf("internal:\n%s", out)
	}
	m = review("public", 0) // private below public: private
	if s := m.createSteps(); !strings.Contains(s[0].show, "visibility=private") {
		t.Errorf("private project: %s", s[0].show)
	}
}

// A failed git init leaves nothing behind, not even what it wrote into
// the folder, so the same name works again.
func TestNewRepoInitFailureRemovesFolder(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root, bin := t.TempDir(), t.TempDir()
	// a git that writes into the new folder (its last argument) and fails
	fake := "#!/bin/sh\nfor a; do d=$a; done\n: > \"$d/HEAD\"\necho fatal: boom\nexit 128\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	m := pathStepModel(forge.GitHub{})
	m.opts.Root = root
	m.create.remote = false
	m.create.folder, m.create.dest = "grp/x", filepath.Join(root, "grp/x")
	realPath := os.Getenv("PATH")
	t.Setenv("PATH", bin)
	out, err := m.createSteps()[0].run(context.Background())
	if err == nil || !strings.Contains(out, "fatal: boom") {
		t.Fatalf("fake git: %q, %v", out, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "grp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the failed init's folder stays: %v", err)
	}
	t.Setenv("PATH", realPath)
	if out, err := m.createSteps()[0].run(context.Background()); err != nil {
		t.Fatalf("again: %q, %v", out, err)
	}
	if !gitx.OnBranch(context.Background(), m.create.dest) {
		t.Error("grp/x is not a new repository")
	}
}

// "n" creates its folder like a clone: never through a symlink.
func TestNewRepoChecksParents(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	os.MkdirAll(root, 0o755)
	os.MkdirAll(outside, 0o755)
	os.Symlink(outside, filepath.Join(root, "link"))
	m := pathStepModel(forge.GitHub{})
	m.opts.Root = root
	m.create.remote = false
	for _, c := range []struct{ folder, bad string }{{"link/x", "symlink"}, {"grp/ok", ""}} {
		m.create.folder, m.create.dest = c.folder, filepath.Join(root, c.folder)
		_, err := m.createSteps()[0].run(context.Background())
		switch {
		case c.bad == "" && err != nil:
			t.Errorf("%s: %v", c.folder, err)
		case c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad)):
			t.Errorf("%s: %v, want %s", c.folder, err, c.bad)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("created outside the root: %v", entries)
	}
	if !gitx.OnBranch(context.Background(), filepath.Join(root, "grp/ok")) {
		t.Error("grp/ok is not a new repository")
	}
}
