package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/ai"
	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

// secretShapes are the credential shapes every external text field of
// the all-modes test carries.
var secretShapes = []string{
	"https://oauth2:SECRET@h/p.git",
	"https://SECRET@h/p.git",
	"git://SECRET@h/p.git",
	"ssh://git:SECRET@h/p.git",
	"https://h/p.git?private_token=SECRET",
	"https://h/p.git?%70rivate_token=SECRET",
	"https://h/p.git#access_token=SECRET",
	"https://h/p.git;private_token=SECRET",
	"https://h/p.git?x=1%26private_token=SECRET",
	"oauth2:SECRET@h/p.git",
	"oauth2:SECRET@h:acme/p.git",
	"?private_token=SECRET",
	"#access_token=SECRET",
	";private_token=SECRET",
	"?%70rivate_token=SECRET",
	"?x=1%26private_token=SECRET",
	"tools?private_token=SECRET",
	"acme/tools#access_token=SECRET",
	"https://h/acme/tools/-/tree/main?private_token=SECRET",
	// valid ref names (git check-ref-format allows # ; = : @)
	"main#access_token=SECRET",
	"fix/x;private_token=SECRET",
	"fix/oauth2:SECRET@h/p.git",
}

// leaked reports whether SECRET is on screen, also when wrapping split
// it across lines.
func leaked(view string) bool {
	flat := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, ansi.Strip(view))
	return strings.Contains(flat, "SECRET")
}

// rawEscape reports whether an escape sequence from data (not lipgloss's
// SGR colours) is in view: the OSC and the CSI 2J the fixture folder
// names of driveAllModes carry.
func rawEscape(view string) bool {
	return strings.Contains(view, "\x1b]") || strings.Contains(view, "\x1b[2J")
}

// namedView is one rendering of a mode, named after the state it shows.
type namedView struct{ what, view string }

// driveAllModes builds a model whose root and repository folders carry
// an escape sequence, brings every message type text with sec in it
// (git, CLI and model output, errors, server descriptions, ref names,
// typed input), and renders every mode after each one, the list with
// the details of every row.
func driveAllModes(t *testing.T, sec string) []namedView {
	t.Helper()
	ext := "see " + sec + " now"
	bad := errors.New("failed: " + sec)
	// a folder name is data too: an unpacked archive can carry one that
	// sets the title (OSC) or clears the screen (CSI 2J)
	root := filepath.Join(t.TempDir(), "r\x1b]0;evil\x07")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	api := func(context.Context, string, string, ...string) ([]byte, error) { return nil, bad }
	m := New(Options{Root: root, Repos: []string{filepath.Join(root, "a\x1b[2J"), filepath.Join(root, "b")},
		Profiles: []Profile{{Name: "work", Forge: forge.GitLab{Host: "h", API: api}}}})
	m.width, m.height = 160, 60
	t.Cleanup(m.life.cancel)

	// check renders every mode m has the state for, and the list with
	// details of every row
	var views []namedView
	check := func(what string) {
		modes := []mode{modeList, modePrompt, modeAgent, modeProfile, modeGraph}
		if m.plan != nil {
			modes = append(modes, modePlan, modeEditMsg)
		}
		if m.clone != nil {
			modes = append(modes, modeCloneQuery, modeClonePick, modeCloneDest)
			if m.clone.group != nil {
				modes = append(modes, modeCloneGroup)
			}
		}
		if m.create != nil {
			modes = append(modes, modeCreate)
		}
		views = append(views, namedView{what, m.View()})
		for _, md := range modes {
			v := m
			v.mode = md
			views = append(views, namedView{what, v.View()})
			for i := range m.view {
				v.cursor, v.detail = i, true
				views = append(views, namedView{what, v.View()})
			}
		}
	}
	send := func(msg tea.Msg) {
		t.Helper()
		next, _ := m.Update(msg)
		m = next.(Model)
	}

	// ref names are from anyone who pushed: branch, upstream and
	// default branch (list row, details pane)
	send(statusMsg{idx: 0, st: gitx.Status{Path: m.rows[0].st.Path, Branch: sec, Upstream: "origin/" + sec,
		DefaultBranch: sec, HasBase: true, BaseAhead: 1, Origin: sec, Risky: []string{ext}}})
	send(statusMsg{idx: 1, st: gitx.Status{Path: m.rows[1].st.Path, Branch: "fix/" + sec, DefaultBranch: "main", Err: bad}})
	check("status")
	send(actionMsg{idx: 0, name: "fetch", out: ext, err: bad})
	check("action")
	send(execDoneMsg{idx: 0, name: "lazygit", err: bad})
	check("exec")

	m.mode, m.graphIdx = modeGraph, 0
	send(graphMsg{idx: 0, out: "\x1b[33m" + ext + "\x1b[m"})
	check("graph")
	send(graphMsg{idx: 0, err: bad})
	check("graph error")

	m.mode, m.planSeq = modePlan, 1
	m.plan = &planState{idx: 0, request: "tidy up", loading: true}
	steps := []gitx.Step{{Op: "commit", Message: ext}}
	send(planMsg{seq: 1, plan: ai.Plan{Summary: ext, Steps: steps}})
	check("plan")
	send(planValidMsg{planSeq: 1, valSeq: m.plan.valSeq, steps: steps, args: [][]string{{"commit", "-m", ext}},
		errs: []error{bad}, risky: []string{ext}})
	check("plan validated")
	m.plan = &planState{idx: 0, request: "tidy up", loading: true}
	send(planMsg{seq: 1, err: bad})
	check("plan error")
	m.closePlan()

	m.openClone("")
	p := forge.Project{Path: "acme/p", Name: "p", SSHURL: "git@h:acme/p.git", Description: ext}
	g := forge.Project{Path: "acme", Name: "acme", Group: true, Description: ext}
	send(projectsMsg{prof: 0, host: "h", err: bad})
	check("projects error")
	send(projectsMsg{prof: 0, host: "h", projects: []forge.Project{p}, groups: []forge.Project{g}, fetched: true})
	check("projects")
	send(projectsMsg{prof: 0, host: "h", projects: []forge.Project{p}, fetched: true, groupErr: bad})
	check("group list error")
	m.mode, m.clone.searching = modeClonePick, true
	send(cloneSearchMsg{seq: m.cloneSeq, host: "h", results: []forge.Project{p, p}})
	check("search")
	send(cloneSearchMsg{seq: m.cloneSeq, host: "h", err: bad})
	check("search error")
	m.choose(p)
	check("destination")
	m.input.SetValue(sec) // typed as the folder: refused before the review
	m.validateCloneDest()
	check("destination typed")
	m.closeClone()
	for _, msg := range runCmds(m.openClone(sec)) { // typed or from the AI: refused or not searched
		send(msg)
	}
	check("query")
	m.clone.group = &groupState{group: g, loading: true}
	m.mode = modeCloneGroup
	send(groupProjectsMsg{seq: m.cloneSeq, err: bad})
	check("group")
	send(groupPlanMsg{seq: m.groupPlanSeq, err: bad})
	check("group plan")
	m.closeClone()

	u, _ := forge.ParseCloneURL("https://h/acme/p.git")
	idx, _ := m.enqueueClone(u, "acme/p", filepath.Join(root, "p"), "")
	send(cloneJobDoneMsg{job: m.rows[idx].job, out: ext, err: bad})
	check("clone job")

	m.mode = modeCreate
	m.create = &createState{step: stepFolder, remoteName: "origin"}
	m.input.SetValue(sec) // typed as the new folder: refused when it holds a credential
	send(tea.KeyMsg{Type: tea.KeyEnter})
	if forge.HasCredentials(sec) && (m.create == nil || m.create.step != stepFolder) {
		t.Fatalf("%q accepted as a folder", sec)
	}
	check("create folder")
	m.mode = modeCreate
	m.create = &createState{publish: true, step: stepPath, remoteName: "origin", loading: true, idx: 0}
	send(publishInfoMsg{idx: 0, err: bad})
	check("publish info")
	m.mode = modeCreate
	m.create = &createState{publish: true, step: stepPath, remoteName: "origin", gloading: true, checking: true}
	send(whoamiMsg{prof: 0, host: "h", err: bad})
	send(createGroupsMsg{prof: 0, err: bad})
	check("whoami, groups error")
	send(createGroupsMsg{prof: 0, groups: []forge.Project{g}})
	check("groups")
	send(groupCheckMsg{seq: m.checkSeq, err: bad})
	check("group check")
	send(createDoneMsg{publish: true, idx: 0, failed: "git push " + sec, out: ext, err: bad})
	check("create done")
	return views
}

// Every external text a message brings (git, CLI and model output,
// errors, server descriptions) is shown without its credentials, in
// every mode, whatever shape they have.
func TestNoSecretInAnyMode(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	for _, sec := range secretShapes {
		for _, v := range driveAllModes(t, sec) {
			if leaked(v.view) {
				t.Errorf("%s: %q shown:\n%s", v.what, sec, ansi.Strip(v.view))
				break
			}
		}
	}
}

// A folder name from the file system (the root, a repository, a clone
// destination) is shown sanitized like remote text: no escape sequence
// of its reaches the terminal, in any mode, the details pane included.
func TestNoEscapeFromDataInAnyMode(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	for _, v := range driveAllModes(t, "plain") {
		if rawEscape(v.view) {
			t.Errorf("%s: an escape sequence from data is shown:\n%q", v.what, v.view)
			break
		}
	}
}

// scrub keeps what errors.Is needs, and leaves executed values alone.
func TestScrubKeepsMeaning(t *testing.T) {
	err := scrubErr(errors.Join(errCancelled, errors.New("https://oauth2:SECRET@h/p.git")))
	if !errors.Is(err, errCancelled) || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("scrubErr: %v", err)
	}
	sec := "https://oauth2:SECRET@h/p.git"
	msg := scrub(planMsg{plan: ai.Plan{Clone: sec, Steps: []gitx.Step{{Op: "commit", Message: sec}}}}).(planMsg)
	if msg.plan.Clone != sec || msg.plan.Steps[0].Message != sec {
		t.Errorf("executed values changed: %+v", msg.plan)
	}
	v := scrub(planValidMsg{args: [][]string{{"commit", "-m", sec}}}).(planValidMsg)
	if v.args[0][2] != sec {
		t.Errorf("plan args changed: %v", v.args)
	}
}

// A line of command output is cut once, after redaction, when it
// arrives: a single line as long as the output cap is not redacted on
// every frame, and no part of a credential survives the cut. Ordinary
// output is stored as it is.
func TestScrubBoundsOutputLines(t *testing.T) {
	long := strings.Repeat("a", 4<<20)
	sec := "https://oauth2:" + strings.Repeat("SECRET", 1000) + "@h/p.git"
	msg := scrub(actionMsg{out: long + "\n" + sec + "\n" + sec + long}).(actionMsg)
	for i, l := range strings.Split(msg.out, "\n") {
		if len(l) > maxOutLine || strings.Contains(l, "SECRET") {
			t.Errorf("line %d: %d bytes, secret %t", i, len(l), strings.Contains(l, "SECRET"))
		}
	}
	if want := long[:maxOutLine] + "\nhttps://***@h/p.git\n" + ("https://***@h/p.git" + long)[:maxOutLine]; msg.out != want {
		t.Errorf("cut lines differ from the first %d bytes of each", maxOutLine)
	}
	// a multi-byte character on the cut is dropped whole
	wide := strings.Repeat("é", maxOutLine) // 2 bytes each: the cut falls inside one
	if got := scrub(actionMsg{out: wide}).(actionMsg).out; got != strings.Repeat("é", maxOutLine/2) {
		t.Errorf("cut in a rune: %d bytes, valid %t", len(got), utf8.ValidString(got))
	}
	plain := "Fetching origin\nFrom https://oauth2:SECRET@h/p.git\n   abc..def  main -> origin/main\n"
	if got := scrub(actionMsg{out: plain}).(actionMsg).out; got != forge.RedactURLs(plain) {
		t.Errorf("ordinary output changed:\n%s", got)
	}
}

// A row keeps of a finished action's output only the lines the details
// pane can show: not up to MaxOutput per repository for as long as it
// is listed. What the pane shows is unchanged.
func TestRowKeepsShownOutputOnly(t *testing.T) {
	root := t.TempDir()
	m := New(Options{Root: root, Repos: []string{filepath.Join(root, "a")}})
	t.Cleanup(m.life.cancel)
	line := strings.Repeat("x", maxOutLine-1) + "\n"
	huge := "\n \n" + strings.Repeat(line+"\n\x1b[2J\n", 1<<12) // blank and control-only lines are skipped, as shown
	next, _ := m.Update(actionMsg{idx: 0, name: "fetch", out: huge})
	m = next.(Model)
	r := m.rows[0]
	if want := strings.Repeat(line, detailHeight); r.lastOut != want[:len(want)-1] {
		t.Errorf("kept %d bytes, %d lines", len(r.lastOut), strings.Count(r.lastOut, "\n")+1)
	}
	if got, want := outputLines(r.lastOut, detailHeight), outputLines(huge, detailHeight); !slices.Equal(got, want) {
		t.Errorf("shown lines changed:\n%q\n%q", got, want)
	}
	for _, short := range []string{"", "ok", "a\n\nb\n", "$ git push\n" + line} {
		if got, want := outputLines(keepOut(short), detailHeight), outputLines(short, detailHeight); !slices.Equal(got, want) {
			t.Errorf("keepOut(%q) shows %q", short, got)
		}
	}
	if keepOut("ok") != "ok" {
		t.Errorf("short output changed: %q", keepOut("ok"))
	}
}
