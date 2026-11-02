package tui

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

// pathStepModel is the "n" box with a remote, at the owner/name step.
func pathStepModel(f forge.Forge) Model {
	m := Model{width: 100, mode: modeCreate,
		profiles: []profileState{{Profile: Profile{Name: "work", Forge: f}}},
		create:   &createState{remote: true, step: stepPath, remoteName: "origin", path: "me/x"},
	}
	m.input.SetValue("me/x")
	return m
}

func press(m Model, keys ...tea.KeyMsg) Model {
	for _, k := range keys {
		next, _ := m.handleCreateKey(k)
		m = next.(Model)
	}
	return m
}

var (
	keyTab   = tea.KeyMsg{Type: tea.KeyTab}
	keyCtrlV = tea.KeyMsg{Type: tea.KeyCtrlV}
	keyUp    = tea.KeyMsg{Type: tea.KeyUp}
	keyDown  = tea.KeyMsg{Type: tea.KeyDown}
	keyEnter = tea.KeyMsg{Type: tea.KeyEnter}
	keyEsc   = tea.KeyMsg{Type: tea.KeyEsc}
	keyJ     = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	keyK     = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
)

func TestVisibilityDefaultsToPrivate(t *testing.T) {
	for _, f := range []forge.Forge{forge.GitLab{}, forge.GitHub{}} {
		m := pathStepModel(f)
		if v := m.visibility(); v != "private" {
			t.Errorf("%s: default visibility %q, want private", f.Name(), v)
		}
		if m.publicAlert() {
			t.Errorf("%s: alert shown for private", f.Name())
		}
	}
}

func TestVisibilityMenuEscCancels(t *testing.T) {
	m := press(pathStepModel(forge.GitLab{}), keyCtrlV)
	if !m.create.visMenu || m.create.visSel != 0 {
		t.Fatalf("tab: menu %v, cursor %d; want open on the current entry", m.create.visMenu, m.create.visSel)
	}
	m = press(m, keyDown, keyJ)
	if m.create.visSel != 2 || m.create.vis != 0 {
		t.Fatalf("moving changed the visibility: sel %d vis %d", m.create.visSel, m.create.vis)
	}
	m = press(m, keyEsc)
	if m.create.visMenu || m.visibility() != "private" {
		t.Errorf("esc: menu %v, visibility %s; want closed, private", m.create.visMenu, m.visibility())
	}
	if m.create.step != stepPath || m.mode != modeCreate {
		t.Errorf("esc in the menu left the step (%d, mode %d)", m.create.step, m.mode)
	}
}

func TestVisibilityMenuEnterConfirms(t *testing.T) {
	m := press(pathStepModel(forge.GitLab{}), keyCtrlV, keyJ, keyEnter)
	if m.create.visMenu || m.visibility() != "internal" {
		t.Fatalf("enter: menu %v, visibility %s; want closed, internal", m.create.visMenu, m.visibility())
	}
	if m.create.step != stepPath {
		t.Errorf("enter in the menu must not move on to review")
	}
	// reopening starts at the current entry; k wraps around
	m = press(m, keyCtrlV)
	if m.create.visSel != 1 {
		t.Errorf("menu reopened at %d, want 1", m.create.visSel)
	}
	m = press(m, keyK, keyK, keyEnter)
	if m.visibility() != "public" {
		t.Errorf("visibility %s, want public", m.visibility())
	}
}

func TestVisibilityMenuLists(t *testing.T) {
	m := press(pathStepModel(forge.GitLab{}), keyCtrlV)
	out := ansi.Strip(m.viewCreate())
	for _, want := range []string{"visibility ▸", "▸ private   only you and members (current)",
		"internal  any signed-in user on this instance", "public    anyone on the internet", "enter confirm · esc cancel"} {
		if !strings.Contains(out, want) {
			t.Errorf("menu lacks %q:\n%s", want, out)
		}
	}
}

func TestPublicAlert(t *testing.T) {
	m := pathStepModel(forge.GitHub{})
	plain := ansi.Strip(m.viewCreate())
	if strings.Contains(plain, "╭") || strings.Contains(plain, "PUBLIC") || strings.Contains(plain, "anyone on the internet") {
		t.Fatalf("private shows the alert:\n%s", plain)
	}

	m = press(m, keyCtrlV, keyDown, keyEnter)
	if !m.publicAlert() {
		t.Fatal("public selected but no alert")
	}
	for name, step := range map[string]int{"form": stepPath, "review": stepReview} {
		m.create.step = step
		if step == stepReview {
			m.enterReview()
		}
		out := ansi.Strip(m.viewCreate())
		lines := strings.Split(out, "\n")
		if !strings.HasPrefix(lines[0], "╭") || !strings.HasPrefix(lines[len(lines)-1], "╰") {
			t.Errorf("%s: no frame around the window:\n%s", name, out)
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > m.width {
				t.Errorf("%s: line %d columns wide, window %d: %q", name, w, m.width, l)
			}
		}
		if !strings.Contains(out, "· PUBLIC ·") || !strings.Contains(out, publicWarning) {
			t.Errorf("%s: no PUBLIC label or warning:\n%s", name, out)
		}
	}
	m.enterReview()
	if !strings.Contains(ansi.Strip(m.viewCreate()), "gh repo create me/x --public") {
		t.Error("review does not run the public create")
	}

	// the alert is ANSI red (works on light and dark terminals)
	red := lipgloss.Color("1")
	if styleAlert.GetForeground() != red || !styleAlert.GetBold() || styleAlertFrame.GetBorderTopForeground() != red {
		t.Error("alert styles are not bold ANSI red")
	}

	// a local-only repository has no visibility: no alert
	m.create.remote = false
	if m.publicAlert() {
		t.Error("alert for a local-only repository")
	}
}

// Every step of "n" and "u" ends with its key help on the window's last
// line, like the list view, framed or not.
func TestCreateHelpEveryStep(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(c *createState)
		want string
	}{
		{"folder", func(c *createState) { c.step, c.remote = stepFolder, false }, "enter next · tab toggle remote · ctrl+u clear · esc cancel"},
		{"folder with error", func(c *createState) { c.step, c.err = stepFolder, errors.New("x already exists") }, "enter next · tab toggle remote"},
		{"path (n)", func(c *createState) {}, "enter review · ctrl+v visibility · ctrl+u clear · esc back"},
		{"path (u)", func(c *createState) { c.publish = true }, "enter review · ctrl+v visibility · ctrl+u clear · esc cancel"},
		{"menu", func(c *createState) { c.visMenu = true }, "↑↓/jk choose · enter confirm · esc cancel"},
		{"review", func(c *createState) { c.step = stepReview }, "y run · esc back"},
		{"review public", func(c *createState) { c.step, c.vis = stepReview, 1 }, "y run · esc back"},
		{"running", func(c *createState) { c.step, c.running = stepReview, true }, "running… · ctrl+c stop and quit"},
	} {
		m := pathStepModel(forge.GitHub{})
		m.height = 30
		m.rows = []*row{{rel: "repo"}}
		tc.set(m.create)
		lines := strings.Split(ansi.Strip(m.viewCreate()), "\n")
		if len(lines) != m.height {
			t.Errorf("%s: %d lines, want the window's %d", tc.name, len(lines), m.height)
		}
		last := lines[len(lines)-1]
		if m.publicAlert() {
			last = lines[len(lines)-2] // above the frame's bottom border
		}
		if !strings.Contains(last, tc.want) {
			t.Errorf("%s: last line %q, want help %q", tc.name, last, tc.want)
		}
	}
}

// groupStepModel is the "u" box of a repository named "tool" on a GitLab
// profile whose groups are listed.
func groupStepModel(defaultGroup string) Model {
	m := Model{width: 100, height: 30, mode: modeCreate, rows: []*row{{rel: "tool", st: gitx.Status{Path: "/w/tool"}}},
		profiles: []profileState{{Profile: Profile{Name: "work", Forge: forge.GitLab{Host: "h"}, DefaultGroup: defaultGroup}, me: "me"}},
		create: &createState{publish: true, step: stepPath, remoteName: "origin",
			groups: []forge.Project{{Path: "acme", Group: true}, {Path: "acme/tools", Group: true}, {Path: "acme/tools/cli", Group: true}, {Path: "lab/web", Group: true}}},
	}
	m.prefillPath()
	return m
}

func typeText(m Model, s string) Model {
	for _, r := range s {
		m = press(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestDefaultGroupPrefilled(t *testing.T) {
	m := groupStepModel("acme/tools")
	if v := m.input.Value(); v != "acme/tools/tool" {
		t.Errorf("with default_group: %q, want acme/tools/tool", v)
	}
	if len(m.create.gsugg) != 0 { // a listed group as owner: nothing to complete
		t.Errorf("suggestions for a complete owner: %+v", m.create.gsugg)
	}
	m = groupStepModel("")
	if v := m.input.Value(); v != "me/tool" {
		t.Errorf("without default_group: %q, want me/tool", v)
	}
}

func TestGroupCompletion(t *testing.T) {
	m := groupStepModel("")
	m.input.SetValue("")
	m = typeText(m, "tools cl")
	if len(m.create.gsugg) == 0 || m.create.gsugg[0].Path != "acme/tools/cli" {
		t.Fatalf("suggestions for 'tools cl': %+v", m.create.gsugg)
	}
	m = press(m, keyTab)
	if v := m.input.Value(); v != "acme/tools/cli/tool" {
		t.Fatalf("tab completed to %q, want acme/tools/cli/tool", v)
	}
	if m.create.visMenu || len(m.create.gsugg) != 0 {
		t.Errorf("after completion: menu %v, suggestions %+v", m.create.visMenu, m.create.gsugg)
	}
	// slashes split words too; arrows move the highlight
	m.input.SetValue("")
	m = typeText(m, "acme/t")
	n := len(m.create.gsugg)
	m = press(m, keyDown)
	if n < 2 || m.create.gsel != 1 {
		t.Fatalf("down: sel %d of %d", m.create.gsel, n)
	}
	want := m.create.gsugg[1].Path + "/tool"
	m = press(m, keyUp, keyDown, keyTab)
	if v := m.input.Value(); v != want {
		t.Errorf("tab after moving: %q, want %q", v, want)
	}
	out := ansi.Strip(groupStepModel("").viewCreate())
	if !strings.Contains(out, "tab complete group") {
		t.Errorf("help does not mention completion:\n%s", out)
	}
}

func TestNoGroupSuggestionsOnGitHub(t *testing.T) {
	m := pathStepModel(forge.GitHub{})
	m = press(m, keyTab)
	if m.input.Value() != "me/x" || m.create.visMenu {
		t.Errorf("tab on GitHub changed something: %q, menu %v", m.input.Value(), m.create.visMenu)
	}
}

func TestMissingSubgroupsWarnedAndCreatedFirst(t *testing.T) {
	m := groupStepModel("")
	m.profiles[0].Forge = forge.GitLab{Host: "h", API: func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
		if ep == "groups/acme%2Ftools" {
			return []byte(`{"id":9,"full_path":"acme/tools","path":"tools"}`), nil
		}
		return nil, errors.New("404 Group Not Found")
	}}
	m.input.SetValue("acme/tools/new/deeper/tool")
	next, cmd := m.handleCreateKey(keyEnter)
	m = next.(Model)
	if cmd == nil || !m.create.checking || m.create.step != stepPath {
		t.Fatalf("enter with an unknown owner must check it first (checking %v, step %d)", m.create.checking, m.create.step)
	}
	if out := ansi.Strip(m.viewCreate()); !strings.Contains(out, "checking group acme/tools/new/deeper") {
		t.Errorf("no progress while checking:\n%s", out)
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.create.step != stepReview || strings.Join(m.create.sub.Missing, ",") != "acme/tools/new,acme/tools/new/deeper" {
		t.Fatalf("after the check: step %d, new groups %v, err %v", m.create.step, m.create.sub.Missing, m.create.err)
	}
	out := strings.Join(strings.Fields(ansi.Strip(m.viewCreate())), " ") // long lines wrap
	for _, want := range []string{
		"⚠ groups acme/tools/new, acme/tools/new/deeper do not exist: they will be created as new private subgroups below acme/tools",
		"glab api --hostname h groups -X POST -f name=new -f path=new -f parent_id=9 -f visibility=private",
		"-f name=deeper -f path=deeper -f 'parent_id=<id of acme/tools/new>' -f visibility=private",
		"glab repo create tool --group acme/tools/new/deeper --private --skipGitInit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("review lacks %q:\n%s", want, out)
		}
	}
	steps := m.createSteps()
	if len(steps) < 3 || !strings.Contains(steps[0].show, "name=new") || !strings.Contains(steps[1].show, "name=deeper") ||
		!strings.Contains(steps[2].show, "repo create") {
		t.Errorf("groups are not created before the project: %+v", steps)
	}

	// a listed group needs no check; esc while checking drops the answer
	m = groupStepModel("")
	m.input.SetValue("acme/tools/tool")
	if next, cmd := m.handleCreateKey(keyEnter); cmd != nil || next.(Model).create.step != stepReview {
		t.Error("a listed group was looked up again")
	}
	m.input.SetValue("acme/zzz/tool")
	next, cmd = m.handleCreateKey(keyEnter)
	m = press(next.(Model), keyEsc)
	if m.create.checking || m.create.step != stepPath || m.mode != modeCreate {
		t.Errorf("esc while checking: checking %v step %d", m.create.checking, m.create.step)
	}
	m.handleGroupCheck(groupCheckMsg{seq: m.checkSeq, path: "acme/zzz/tool", sub: forge.Subgroups{Missing: []string{"acme/zzz"}}})
	if m.create.step != stepPath {
		t.Error("a cancelled check moved on to the review")
	}
	_ = cmd
}

// An owner check answered after its dialog was closed is dropped, also
// when a new dialog is checking an owner of its own.
func TestGroupCheckAfterCloseDropped(t *testing.T) {
	m := groupStepModel("")
	m.input.SetValue("acme/zzz/tool")
	next, _ := m.handleCreateKey(keyEnter)
	m = next.(Model)
	late := groupCheckMsg{seq: m.checkSeq, path: "acme/zzz/tool", sub: forge.Subgroups{Missing: []string{"acme/zzz"}}}
	m.closeCreate()
	m.mode, m.create = modeCreate, groupStepModel("").create
	m.input.SetValue("acme/yyy/tool")
	next, _ = m.handleCreateKey(keyEnter)
	m = next.(Model)
	m.handleGroupCheck(late)
	if !m.create.checking || m.create.step != stepPath || m.create.path != "" {
		t.Errorf("late answer landed in the new dialog: step %d, path %q", m.create.step, m.create.path)
	}
}

// The repository read for u that arrives after its dialog was closed
// does not land in the next one.
func TestPublishInfoAfterCloseDropped(t *testing.T) {
	m := publishModel()
	m.view = []int{0}
	m.openCreate(true)
	late := publishInfoMsg{seq: m.publishSeq, idx: 0, remotes: []string{"origin"}, hasCommits: true}
	m.closeCreate()
	m.openCreate(true)
	m.handlePublishInfo(late)
	if c := m.create; c == nil || !c.loading || c.hasCommits {
		t.Errorf("late answer landed in the new dialog: %+v", c)
	}
}

// A new repository's folder typed with credentials is refused before
// the review: no git init, no folder, nothing shown.
func TestCreateFolderWithCredentialsRefused(t *testing.T) {
	root := t.TempDir()
	m := New(Options{Root: root, Profiles: []Profile{{Name: "work", Forge: forge.GitLab{Host: "h"}}}})
	defer m.life.cancel()
	m.width, m.height = 200, 40
	m.mode = modeCreate
	for _, bad := range secretShapes {
		m.create = &createState{step: stepFolder, remoteName: "origin"}
		m.input.SetValue(bad)
		m.input.CursorEnd()
		next, cmd := m.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(Model)
		if cmd != nil || m.create.step != stepFolder || !errors.Is(m.create.err, gitx.ErrDestCredentials) {
			t.Errorf("%q: step %d err %v", bad, m.create.step, m.create.err)
		}
		if out := ansi.Strip(m.viewCreate()); strings.Contains(out, "SECRET") {
			t.Errorf("%q:\n%s", bad, out)
		}
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("created %v", left)
	}
}

// A new repository's folder with .git on its way is refused before the
// review, like a clone's.
func TestCreateFolderGitDirRefused(t *testing.T) {
	root := t.TempDir()
	m := New(Options{Root: root, Profiles: []Profile{{Name: "work", Forge: forge.GitLab{Host: "h"}}}})
	defer m.life.cancel()
	m.width, m.height = 200, 40
	m.mode = modeCreate
	for _, bad := range []string{".git", "grp/.Git/tool"} {
		m.create = &createState{step: stepFolder, remoteName: "origin"}
		m.input.SetValue(bad)
		m.input.CursorEnd()
		next, cmd := m.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(Model)
		if cmd != nil || m.create.step != stepFolder || !errors.Is(m.create.err, gitx.ErrDestGitDir) {
			t.Errorf("%q: step %d err %v", bad, m.create.step, m.create.err)
		}
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("created %v", left)
	}
}

// In a window too narrow for anything after the command prefix, the
// commands are cut from the review: y is refused like a review cut off
// at the bottom, however tall the window is.
func TestReviewTooNarrowRefusesY(t *testing.T) {
	m := publishModel()
	m.life, m.sem = newLifetime(), make(chan struct{}, 1)
	m.width, m.height = reviewIndent, 200
	// the command's text, read down the lines after its prefix
	shown := func() string {
		out := ansi.Strip(m.viewCreate())
		_, cmds, _ := strings.Cut(out, "will run:")
		return strings.ReplaceAll(strings.ReplaceAll(cmds, " ", ""), "\n", "")
	}
	if strings.Contains(shown(), "repocreate") {
		t.Fatalf("a %d-column window shows the command:\n%s", m.width, ansi.Strip(m.viewCreate()))
	}
	if m.reviewFits() || !strings.Contains(m.createHelp(), "enlarge the window") {
		t.Errorf("a %d-column window: fits %v, help %q", m.width, m.reviewFits(), m.createHelp())
	}
	next, cmd := m.handleCreateKey(keyY)
	if cmd != nil || next.(Model).create.running {
		t.Error("y ran a review without its commands")
	}
	m.width++
	if !m.reviewFits() || !strings.Contains(shown(), "repocreatetool") {
		t.Errorf("a %d-column window: fits %v:\n%s", m.width, m.reviewFits(), ansi.Strip(m.viewCreate()))
	}
}

// Inside the red public frame a long line wraps onto more lines (the
// public warning too, in a window under 20 columns): the fit is judged
// on the lines as shown, not on the lines in the text, so y is refused
// until the whole review, help included, is on screen.
func TestReviewWrappedInFrameRefusesY(t *testing.T) {
	m := publishModel()
	m.life, m.sem = newLifetime(), make(chan struct{}, 1)
	m.create.vis = 2 // public
	m.rows[0].rel = strings.Repeat("deeply/nested/", 6) + "tool"
	m.width = 20 // a frame 16 wide: the commands are shown, the rest wraps
	w, inner := m.createFrame()
	body := m.createBody(w)
	// just tall enough counted by "\n", not by lines on screen
	m.height = strings.Count(body, "\n") + len(wrapHelp("y run · esc back", w)) + 1 + (m.height - inner)
	if m.reviewFits() {
		t.Fatal("a review wrapped by the frame counted as fitting")
	}
	next, cmd := m.handleCreateKey(keyY)
	if cmd != nil || next.(Model).create.running {
		t.Error("y ran a review that was cut off")
	}
	for !m.reviewFits() {
		m.height++
	}
	if got := lipgloss.Height(m.viewCreate()); got > m.height {
		t.Errorf("fits at height %d, but the view has %d lines:\n%s", m.height, got, ansi.Strip(m.viewCreate()))
	}
	if m.createHelp() != "y run · esc back" {
		t.Errorf("help %q", m.createHelp())
	}
}

// A review taller than the window would hide commands y runs: y does
// nothing and the help says so, until the window is tall enough.
func TestReviewTooTallRefusesY(t *testing.T) {
	m := publishModel()
	m.life, m.sem = newLifetime(), make(chan struct{}, 1)
	if !m.reviewFits() || m.createHelp() != "y run · esc back" {
		t.Fatalf("a 30-line window: fits %v, help %q", m.reviewFits(), m.createHelp())
	}
	m.height = 6
	lines := strings.Split(ansi.Strip(m.viewCreate()), "\n")
	if m.reviewFits() || !strings.Contains(lines[len(lines)-1], "enlarge the window to see every command before y") {
		t.Errorf("a 6-line window: fits %v, last line %q", m.reviewFits(), lines[len(lines)-1])
	}
	next, cmd := m.handleCreateKey(keyY)
	if cmd != nil || next.(Model).create.running || m.rows[0].busy != "" {
		t.Error("y ran a review that was cut off")
	}
	m.height = 30
	if next, cmd = m.handleCreateKey(keyY); cmd == nil || !next.(Model).create.running {
		t.Error("y did not run once the review fit")
	}
}

// The review runs exactly what it showed. What the plan depends on can
// change while the user reads it: the user name arrives (glab repo
// create drops --group for the user's own namespace), more rows are
// read and decide between ssh and https for the new remote. The plan is
// built when the review is entered; a change after that neither
// rewrites the screen behind the user nor what y runs.
func TestReviewRunsWhatItShowed(t *testing.T) {
	m := pathStepModel(forge.GitLab{Host: "h"})
	m.height = 30
	m.profiles[0].whoami = true // the user name is still being read
	m.create.groups = []forge.Project{{Path: "me", Group: true}}
	m = press(m, keyEnter)
	if m.create.step != stepReview {
		t.Fatalf("step %d, err %v", m.create.step, m.create.err)
	}
	shown := func() []string {
		var s []string
		for _, st := range m.create.steps {
			s = append(s, st.show)
		}
		return s
	}
	before, view := shown(), ansi.Strip(m.viewCreate())
	if len(before) != 3 || before[1] != "GITLAB_HOST=h glab repo create x --group me --private --skipGitInit" ||
		before[2] != "git remote add origin git@h:me/x.git" {
		t.Fatalf("review shows %q", before)
	}

	// meanwhile: the user name arrives, and rows with https origins on h
	m.handleWhoami(whoamiMsg{prof: 0, host: "h", me: "me"})
	m.rows = []*row{{st: gitx.Status{Origin: "https://h/a/b.git"}}, {st: gitx.Status{Origin: "https://h/a/c.git"}}}
	if fresh := m.createSteps(); fresh[1].show == before[1] || fresh[2].show == before[2] {
		t.Fatalf("setup: the updates should change a rebuilt plan, got %q", []string{fresh[1].show, fresh[2].show})
	}
	if got := shown(); !slices.Equal(got, before) || ansi.Strip(m.viewCreate()) != view {
		t.Errorf("the review changed behind the user: %q", got)
	}

	// y runs the plan as shown
	m.life, m.sem = newLifetime(), make(chan struct{}, 1)
	for i := range m.create.steps {
		s := &m.create.steps[i]
		s.run = func(context.Context) (string, error) { return "ran " + s.show, nil }
	}
	next, cmd := m.handleCreateKey(keyY)
	m = next.(Model)
	if cmd == nil || !m.create.running {
		t.Fatal("y did not run")
	}
	done, ok := cmd().(createDoneMsg)
	if !ok || done.err != nil || done.out != "ran "+before[0]+"\nran "+before[1]+"\nran "+before[2] {
		t.Errorf("ran %+v, want the reviewed commands %q", done, before)
	}

	// back and in again: the review is rebuilt from the current state
	m.create.running = false
	m = press(m, keyEsc, keyEnter)
	if got := shown(); m.create.step != stepReview || len(got) != 3 || got[1] != "GITLAB_HOST=h glab repo create x --private --skipGitInit" ||
		got[2] != "git remote add origin https://h/me/x.git" {
		t.Errorf("re-entered review shows %q", got)
	}
}
