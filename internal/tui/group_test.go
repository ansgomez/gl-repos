package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

func TestGroupPlanInCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tools", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Root: root})
	m.mode = modeCloneGroup
	m.clone = &cloneState{group: &groupState{group: forge.Project{Path: "acme/tools", Name: "tools", Group: true}, loading: true}}
	m.input.SetValue("tools")
	m.input.CursorEnd()
	m.cloneSeq = 1
	cmd := m.handleGroupProjects(groupProjectsMsg{seq: 1, projects: []forge.Project{
		{Path: "acme/tools/cli", SSHURL: "git@h:acme/tools/cli.git"}, {Path: "acme/tools/lib", SSHURL: "git@h:acme/tools/lib.git"},
		{Path: "acme/tools/old", SSHURL: "git@h:acme/tools/old.git", Archived: true}}})
	g := m.clone.group
	if cmd == nil || !g.planning || g.items != nil {
		t.Fatal("the plan (a stat per project) must be computed in a command")
	}
	first := cmd().(groupPlanMsg)

	// typing replans; the older answer is dropped
	next, cmd2 := m.handleGroupKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = next.(Model)
	m.handleGroupPlan(first)
	if g.items != nil || !g.planning {
		t.Fatal("a stale plan was used")
	}
	m.handleGroupPlan(cmd2().(groupPlanMsg))
	if g.planning || len(g.items) != 2 || todo(g.items) != 2 || g.hidden != 1 || g.planDest != "tools2" {
		t.Errorf("plan for tools2: %+v hidden %d dest %q", g.items, g.hidden, g.planDest)
	}

	// enter asks y/N only once a fresh plan is in
	next, _ = m.handleGroupKey(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(Model)
	next, cmd3 := m.handleGroupKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if cmd3 == nil || g.confirm {
		t.Fatal("enter asked before the plan was fresh")
	}
	m.handleGroupPlan(cmd3().(groupPlanMsg))
	if !g.confirm || todo(g.items) != 1 || !strings.Contains(m.viewGroup(), "Clone 1 projects into tools/? [y/N]") {
		t.Errorf("confirm %v, todo %d:\n%s", g.confirm, todo(g.items), m.viewGroup())
	}
	m.life.cancel()
}

// A group web URL is checked whole: a credential after "/-/" makes it
// no group URL, so it is refused as a clone URL, never looked up.
func TestGroupURLWithCredentialsRefused(t *testing.T) {
	m := Model{width: 200, height: 30, opts: Options{Root: t.TempDir()},
		profiles: []profileState{{Profile: Profile{Name: "gitlab", Forge: forge.GitLab{Host: "h",
			API: func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
				t.Errorf("forge asked for %s", ep)
				return nil, errors.New("no")
			}}}}}}
	if got := m.groupURLPath("https://h/acme/tools/-/tree/main"); got != "acme/tools" {
		t.Errorf("plain group URL: %q", got)
	}
	for _, q := range []string{
		"https://h/acme/tools/-/tree/main?private_token=SECRET",
		"https://h/acme/tools/-/tree/main#access_token=SECRET",
		"https://h/acme/tools/-/tree/main;private_token=SECRET",
		"https://h/acme/tools/-/tree/%70rivate_token=SECRET",
		"https://h/acme/tools/-/tree/oauth2:SECRET@h",
		"https://h/acme/tools/-/tree/main\x1b]0;x\x07",
	} {
		if got := m.groupURLPath(q); got != "" {
			t.Errorf("groupURLPath(%q) = %q", q, got)
		}
		m.clone = &cloneState{query: q}
		cmd, done := m.useURLQuery()
		if !done || cmd != nil || m.clone.searching || !errors.Is(m.clone.err, forge.ErrBadCloneURL) || strings.Contains(m.clone.err.Error(), "SECRET") {
			t.Errorf("%q: done %v cmd %v err %v", q, done, cmd != nil, m.clone.err)
		}
	}
}

// A group clone destination shaped like credentials is refused.
func TestGroupDestWithCredentialsRefused(t *testing.T) {
	for _, bad := range []string{"tools?private_token=SECRET", "oauth2:SECRET@h/p", "x#access_token=SECRET"} {
		if _, err := groupDest(t.TempDir(), bad); !errors.Is(err, gitx.ErrDestCredentials) || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("groupDest(%q): %v", bad, err)
		}
	}
	for _, bad := range []string{".git", "acme/.GIT/x"} {
		if _, err := groupDest(t.TempDir(), bad); !errors.Is(err, gitx.ErrDestGitDir) {
			t.Errorf("groupDest(%q): %v", bad, err)
		}
	}
}

// A failed group list does not lose the project list fetched with it: the
// projects are installed, the groups known so far stay, and the error is
// shown only when there are no groups to show.
func TestGroupListFailureKeepsProjects(t *testing.T) {
	m := New(Options{Root: t.TempDir()})
	m.openClone("")
	p := forge.Project{Path: "acme/p", Name: "p", SSHURL: "git@h:acme/p.git"}
	g := forge.Project{Path: "acme", Name: "acme", Group: true}
	bad := errors.New("glab (h): 500")
	m.handleProjectsMsg(projectsMsg{prof: 0, host: "h", projects: []forge.Project{p}, fetched: true, groupErr: bad})
	c := m.clone
	if len(c.all) != 1 || c.err == nil || !errors.Is(c.err, bad) || !strings.HasPrefix(c.err.Error(), "group list: ") {
		t.Errorf("projects %d, err %v", len(c.all), c.err)
	}
	m = New(Options{Root: t.TempDir()}) // with groups already known (e.g. from the cache)
	m.openClone("")
	m.handleProjectsMsg(projectsMsg{prof: 0, host: "h", projects: []forge.Project{p}, groups: []forge.Project{g}, fetched: true})
	m.handleProjectsMsg(projectsMsg{prof: 0, host: "h", projects: []forge.Project{p, p}, fetched: true, groupErr: bad})
	if c = m.clone; len(c.all) != 2 || len(c.groups) != 1 || c.err != nil {
		t.Errorf("after a second failure: projects %d, groups %d, err %v", len(c.all), len(c.groups), c.err)
	}
}

// The group's path may differ in case from the server's canonical
// project paths ("Acme/Tools" lists acme/tools/…, like forge's inGroup):
// the projects still nest right below the destination.
func TestGroupPlanIgnoresPathCase(t *testing.T) {
	root := t.TempDir()
	items, _, err := groupPlan(root, "tools", forge.Project{Path: "Acme/Tools", Name: "Tools", Group: true}, []forge.Project{
		{Path: "acme/tools/cli", SSHURL: "git@h:acme/tools/cli.git"}, {Path: "acme/tools/sub/lib", SSHURL: "git@h:acme/tools/sub/lib.git"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].rel != "cli" || items[1].rel != "sub/lib" || items[0].dest != filepath.Join(root, "tools", "cli") {
		t.Errorf("items = %+v", items)
	}
}
