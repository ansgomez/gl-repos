package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

var keyY = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}

// publishModel is the "u" review of row "tool" on a GitLab profile.
func publishModel() Model {
	m := groupStepModel("")
	m.create.path = "acme/tools/tool"
	m.enterReview()
	return m
}

func TestPublishFailureShowsReasonFirst(t *testing.T) {
	steps := []createStep{
		{show: "glab api groups -X POST …", forge: true, quiet: true,
			run: func(context.Context) (string, error) { return `{"id":12,"full_path":"acme/tools/new"}`, nil }},
		{show: "GITLAB_HOST=h glab repo create tool --group acme/tools/new --private --skipGitInit", forge: true,
			run: func(context.Context) (string, error) {
				return "POST https://h/api/v4/projects\n403 Forbidden: insufficient_scope\nerror: request failed", errors.New("exit status 1")
			}},
		{show: "git remote add", run: func(context.Context) (string, error) { t.Error("ran after a failure"); return "", nil }},
	}
	msg := runCreateSteps(context.Background(), nil, steps, createDoneMsg{publish: true})
	if strings.Contains(msg.out, "full_path") {
		t.Errorf("output of a step that succeeded kept: %q", msg.out)
	}
	if !msg.byForge || !strings.Contains(msg.failed, "repo create") {
		t.Errorf("failed step not recorded: %+v", msg)
	}

	m := publishModel()
	m.width = 80
	m.rows[0].busy = "publish"
	m.handleCreateDone(msg)
	want := "publish failed: the glab token needs the api scope (a legacy personal access token): glab auth login --hostname h · enter: details"
	if m.flash != want {
		t.Errorf("flash = %q\nwant    %q", m.flash, want)
	}
	r := m.rows[0]
	if !strings.HasPrefix(r.lastOut, "$ GITLAB_HOST=h glab repo create tool") || !strings.Contains(r.lastOut, "insufficient_scope") {
		t.Errorf("details = %q", r.lastOut)
	}
	if r.busy != "status" {
		t.Errorf("busy after publish = %q, want the status reload", r.busy)
	}

	// unknown causes: the last line, still first
	m = publishModel()
	m.handleCreateDone(createDoneMsg{publish: true, failed: "git push -u origin HEAD", out: "To h:x.git\n! [rejected] main (fetch first)", err: errors.New("exit status 1")})
	if m.flash != "publish failed: ! [rejected] main (fetch first) · enter: details" {
		t.Errorf("flash = %q", m.flash)
	}
	// a git step is never explained with a forge hint
	m = publishModel()
	m.handleCreateDone(createDoneMsg{publish: true, failed: "git push", out: "ERROR: Repository not found.", err: errors.New("exit status 128")})
	if !strings.Contains(m.flash, "publish failed: ERROR: Repository not found.") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestCreateInitFailureStaysVisible(t *testing.T) {
	m := pathStepModel(forge.GitHub{})
	m.height = 30
	m.create.remote, m.create.step, m.create.running = false, stepReview, true
	m.create.folder, m.create.dest = "x", "/nonexistent/x"
	m.handleCreateDone(createDoneMsg{failed: "git init -- x", out: "fatal: cannot mkdir /nonexistent/x: Permission denied", err: errors.New("exit status 128")})
	if m.create == nil || m.mode != modeCreate || m.create.running {
		t.Fatal("the box closed: the reason would only be a cut flash")
	}
	out := strings.Join(strings.Fields(ansi.Strip(m.viewCreate())), " ")
	if !strings.Contains(out, "git init failed: fatal: cannot mkdir /nonexistent/x: Permission denied") {
		t.Errorf("reason not shown:\n%s", out)
	}
}

func TestCreatePartialFailureFlash(t *testing.T) {
	m := pathStepModel(forge.GitLab{Host: "h"})
	m.opts.Root = "/w"
	m.handleCreateDone(createDoneMsg{dest: "/w/dup", initDone: true, failed: "glab repo create taken", byForge: true,
		out: "glab: name has already been taken (HTTP 400)", err: errors.New("exit status 1")})
	if m.flash != "created dup locally, but publishing failed: the name is already taken on h: choose another · enter: details" {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestReviewWrapsLongArguments(t *testing.T) {
	m := publishModel()
	m.width, m.height = 80, 40
	name := "a-very-long-project-name-that-is-much-wider-than-the-review-column-allows-in-eighty-columns"
	m.create.path = "acme/tools/" + name
	m.rows[0].st.Path = "/w/" + name
	m.enterReview()
	out := ansi.Strip(m.viewCreate())
	var joined strings.Builder
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) > m.width {
			t.Errorf("line wider than %d: %q", m.width, l)
		}
		if strings.Contains(l, "…") && !strings.Contains(l, "running…") {
			t.Errorf("cut line: %q", l)
		}
		if len(l) > 15 {
			joined.WriteString(strings.TrimSpace(l[15:]))
		}
	}
	url := "git@h:acme/tools/" + name + ".git"
	if !strings.Contains(joined.String(), url) {
		t.Errorf("remote URL not fully visible:\n%s", out)
	}
	for _, w := range []int{1, 5, 20} {
		lines := wrapWords("git remote add origin "+url, w)
		if strings.Join(lines, "") != strings.ReplaceAll("git remote add origin "+url, " ", "") {
			// words that fit keep their spaces inside one line
			flat := strings.ReplaceAll(strings.Join(lines, ""), " ", "")
			if flat != strings.ReplaceAll("git remote add origin "+url, " ", "") {
				t.Errorf("w=%d: text lost: %q", w, lines)
			}
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > w {
				t.Errorf("w=%d: line %q too wide", w, l)
			}
		}
	}
	if l := wrapWords("ü日本語", 2); strings.Join(l, "|") != "ü|日|本|語" {
		t.Errorf("wide runes: %q", l)
	}
}

func TestExcludedOwnerRefused(t *testing.T) {
	m := groupStepModel("")
	m.profiles[0].Forge = forge.GitLab{Host: "h", Exclude: []string{"legal"}, API: func(context.Context, string, string, ...string) ([]byte, error) {
		t.Error("an excluded owner was looked up")
		return nil, errors.New("404")
	}}
	m.input.SetValue("acme/legal/new/tool")
	next, cmd := m.handleCreateKey(keyEnter)
	m = next.(Model)
	if cmd != nil || m.create.step != stepPath || m.create.err == nil ||
		m.create.err.Error() != "acme/legal/new is in a group this profile excludes (exclude in the config): pick another owner" {
		t.Errorf("excluded owner: cmd %v, step %d, err %v", cmd != nil, m.create.step, m.create.err)
	}
}

func TestOwnerWaitsForUserName(t *testing.T) {
	m := groupStepModel("")
	m.profiles[0].me, m.profiles[0].whoami = "", true
	m.profiles[0].Forge = forge.GitLab{Host: "h", API: func(context.Context, string, string, ...string) ([]byte, error) {
		t.Error("the user's namespace was looked up as a group")
		return nil, errors.New("404")
	}}
	m.input.SetValue("Someone/tool")
	next, cmd := m.handleCreateKey(keyEnter)
	m = next.(Model)
	if cmd != nil || !m.create.retryEnter || m.create.err == nil || !strings.Contains(m.create.err.Error(), "still reading your user name") {
		t.Fatalf("enter while whoami runs: cmd %v retry %v err %v", cmd != nil, m.create.retryEnter, m.create.err)
	}
	cmd = m.handleWhoami(whoamiMsg{prof: 0, host: "h", me: "someone"})
	if m.create.step != stepReview || m.create.err != nil || m.create.checking {
		t.Errorf("after the name arrived: step %d err %v checking %v", m.create.step, m.create.err, m.create.checking)
	}
	if c := m.createSteps(); !strings.Contains(c[0].show, "glab repo create tool --private") || strings.Contains(c[0].show, "--group") {
		t.Errorf("own namespace (other case) passed as a group: %s", c[0].show)
	}
	_ = cmd
}

func TestPublishBusyRow(t *testing.T) {
	m := groupStepModel("")
	m.mode, m.create = modeList, nil
	m.view = []int{0}
	m.rows[0].busy = "fetch"
	if cmd := m.openCreate(true); cmd != nil || m.create != nil || !strings.Contains(m.flash, "repository is busy") {
		t.Errorf("u on a busy row: create %v flash %q", m.create != nil, m.flash)
	}
	// y on a row that became busy while the form was open
	m = publishModel()
	m.rows[0].busy = "pull"
	next, cmd := m.handleCreateKey(keyY)
	m = next.(Model)
	if cmd != nil || m.create.running || m.create.err == nil || m.create.err.Error() != "repository is busy, try again" {
		t.Errorf("y on a busy row: running %v err %v", m.create.running, m.create.err)
	}
	m.rows[0].busy = ""
	m.life, m.sem = newLifetime(), make(chan struct{}, 1)
	next, cmd = m.handleCreateKey(keyY)
	m = next.(Model)
	if cmd == nil || m.rows[0].busy != "publish" || m.runningWork() != 1 {
		t.Errorf("y: busy %q, running work %d", m.rows[0].busy, m.runningWork())
	}
	if m.startAction(0, gitx.Fetch, nil) != nil {
		t.Error("a fetch started on a row being published")
	}
	next, _ = m.Update(statusMsg{idx: 0, st: gitx.Status{Path: "/w/tool"}})
	if next.(Model).rows[0].busy != "publish" {
		t.Error("a status reload cleared the publish")
	}
	m.life.cancel()
}

func TestPublishReadsRepoInBackground(t *testing.T) {
	m := groupStepModel("")
	m.mode, m.create = modeList, nil
	m.view = []int{0}
	cmd := m.openCreate(true)
	if cmd == nil || m.create == nil || !m.create.loading || m.mode != modeCreate {
		t.Fatal("u must open at once and read the repository in a command")
	}
	if out := ansi.Strip(m.viewCreate()); !strings.Contains(out, "reading tool…") || !strings.Contains(out, "esc cancel") {
		t.Errorf("no loading state:\n%s", out)
	}
	next, _ := m.handleCreateKey(keyEnter)
	if next.(Model).create.step != stepPath {
		t.Error("enter worked before the repository was read")
	}
	m.handlePublishInfo(publishInfoMsg{seq: m.publishSeq, idx: 0, remotes: []string{"origin", "gitlab"}})
	if m.create != nil || m.flash != "tool already has a remote named gitlab" {
		t.Errorf("conflicting remote: create %v flash %q", m.create != nil, m.flash)
	}
	m.openCreate(true)
	m.handlePublishInfo(publishInfoMsg{seq: m.publishSeq, idx: 0, remotes: []string{"origin"}, hasCommits: true})
	if c := m.create; c == nil || c.loading || c.remoteName != "gitlab" || !c.hasCommits {
		t.Errorf("after reading: %+v", c)
	}
}

func TestNoCLIForLogin(t *testing.T) {
	defer func(f func(string) (string, error)) { lookPath = f }(lookPath)
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	m := pathStepModel(forge.GitLab{})
	msg := m.lookupUser()().(whoamiMsg)
	if msg.err == nil || msg.err.Error() != "glab not found: install it" {
		t.Errorf("whoami without glab: %v", msg.err)
	}
}

func TestLongFlashWrapsInList(t *testing.T) {
	m := New(Options{Root: "/w", Repos: []string{"/w/a", "/w/b"}})
	m.width, m.height = 80, 20
	m.flash = "publish failed: the glab token needs the api scope (a legacy personal access token): glab auth login --hostname gitlab.example.com · enter: details"
	lines := strings.Split(ansi.Strip(m.viewList()), "\n")
	if len(lines) != m.height {
		t.Errorf("%d lines, want %d", len(lines), m.height)
	}
	text := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	if !strings.Contains(text, m.flash) {
		t.Errorf("flash not fully shown:\n%s", strings.Join(lines, "\n"))
	}
}

// The review shows the command as it runs: a folder name with two
// spaces in a row keeps them (wrapping never collapses whitespace).
func TestReviewKeepsRepeatedSpaces(t *testing.T) {
	m := pathStepModel(forge.GitHub{})
	m.height = 30
	m.opts.Root = "/w"
	m.create.remote = false
	m.create.folder, m.create.dest = "a  b", "/w/a  b"
	m.enterReview()
	out := ansi.Strip(m.viewCreate())
	if want := gitx.CommandLine(gitx.InitArgs("a  b")); !strings.Contains(want, "'a  b'") || !strings.Contains(out, want) {
		t.Errorf("review does not show %q:\n%s", want, out)
	}
}
