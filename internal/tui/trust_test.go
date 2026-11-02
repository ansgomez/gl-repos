package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/ai"
	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

// riskyRepo is a repository whose own config sets core.sshCommand.
func riskyRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "core.sshCommand", "ssh -o ProxyCommand=x"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func riskyModel(t *testing.T) Model {
	dir := riskyRepo(t)
	m := New(Options{Root: filepath.Dir(dir), Repos: []string{dir}, Tool: "true"})
	t.Cleanup(m.life.cancel)
	m.width, m.height = 120, 30
	m.Update(statusMsg{idx: 0, st: gitx.ReadStatus(context.Background(), dir)})
	if len(m.rows[0].st.Risky) == 0 {
		t.Fatalf("status did not flag core.sshCommand: %+v", m.rows[0].st)
	}
	return m
}

func TestRiskyRepoIsMarked(t *testing.T) {
	m := riskyModel(t)
	m.detail = true
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "▸ !") {
		t.Errorf("no marker on the row:\n%s", out)
	}
	if !strings.Contains(out, "config:   ! its own git config runs programs (core.sshcommand)") {
		t.Errorf("no reason in the details:\n%s", out)
	}
}

// A repository whose config cannot be read is not known to run nothing:
// it is marked like one that does, with the reason in the details.
func TestUnreadableConfigIsMarked(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Root: filepath.Dir(dir), Repos: []string{dir}})
	t.Cleanup(m.life.cancel)
	m.width, m.height = 120, 30
	m.Update(statusMsg{idx: 0, st: gitx.ReadStatus(context.Background(), dir)})
	m.detail = true
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "▸ !") {
		t.Errorf("no marker on the row:\n%s", out)
	}
	if !strings.Contains(out, "config:   ! its own git config runs programs (config not readable, not checked)") {
		t.Errorf("no reason in the details:\n%s", out)
	}
}

func TestRiskyRepoActionsAskFirst(t *testing.T) {
	keyF := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}
	keyO := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}}
	keyN := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}

	m := riskyModel(t)
	next, cmd := m.handleListKey(keyF)
	m = next.(Model)
	if cmd != nil || m.confirm == nil {
		t.Fatal("f fetched a risky repository without asking")
	}
	if out := ansi.Strip(m.View()); !strings.Contains(out, "Fetch 1 repo(s)? [y/N] !") || !strings.Contains(out, "core.sshcommand") {
		t.Errorf("confirm line:\n%s", out)
	}
	next, cmd = m.handleListKey(keyN)
	m = next.(Model)
	if cmd != nil || m.confirm != nil {
		t.Fatal("n did not cancel")
	}
	next, _ = m.handleListKey(keyF)
	m = next.(Model)
	if _, cmd = m.handleListKey(keyY); cmd == nil {
		t.Fatal("y did not fetch")
	}

	m = riskyModel(t)
	next, cmd = m.handleListKey(keyO)
	m = next.(Model)
	if cmd != nil || m.confirm == nil || m.confirm.exec == nil {
		t.Fatal("o opened the tool in a risky repository without asking")
	}
	if out := ansi.Strip(m.View()); !strings.Contains(out, "Open true in") {
		t.Errorf("confirm line:\n%s", out)
	}
	if _, cmd = m.handleListKey(keyY); cmd == nil {
		t.Fatal("y did not open the tool")
	}
}

// An action that was not confirmed (fetch on start, a row whose status
// had not loaded yet) re-reads the config and leaves a risky repository
// alone; a confirmed one runs.
func TestUnconfirmedActionRefusesRiskyRepo(t *testing.T) {
	dir := riskyRepo(t)
	m := New(Options{Root: "/w", Repos: []string{dir}})
	t.Cleanup(m.life.cancel)
	msg := m.startAction(0, gitx.Fetch, nil)().(actionMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "not run: its own git config runs programs (core.sshcommand)") {
		t.Errorf("unconfirmed fetch: %v", msg.err)
	}

	m.rows[0].busy = ""
	m.rows[0].st = gitx.ReadStatus(context.Background(), dir)
	msg = m.startAction(0, gitx.Fetch, m.rows[0].st.Risky)().(actionMsg)
	if msg.err != nil && strings.Contains(msg.err.Error(), "not run") {
		t.Errorf("confirmed fetch refused: %v", msg.err)
	}

	// a key added after the confirmation is not covered by it
	if out, err := exec.Command("git", "-C", dir, "config", "core.pager", "x").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	m.rows[0].busy = ""
	msg = m.startAction(0, gitx.Fetch, m.rows[0].st.Risky)().(actionMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "core.pager") {
		t.Errorf("fetch after the config changed: %v", msg.err)
	}
}

// A remote URL "<transport>::<address>" (or remote.<name>.vcs) makes git
// run git-remote-<transport> from PATH on fetch, pull and push: such a
// repository is risky, and an unconfirmed fetch leaves it alone.
func TestUnconfirmedFetchRunsNoTransportHelper(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "ran")
	helper := "#!/bin/sh\necho ran >> '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "git-remote-evil"), []byte(helper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "evil::x"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	_ = exec.Command("git", "-C", dir, "fetch", "origin").Run() // fails: the helper is no remote
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("setup: plain git fetch should run the helper")
	}
	os.Remove(marker)

	m := New(Options{Root: "/w", Repos: []string{dir}})
	t.Cleanup(m.life.cancel)
	if st := gitx.ReadStatus(context.Background(), dir); !slices.Contains(st.Risky, "remote.origin.url") {
		t.Errorf("risky keys %q, want remote.origin.url", st.Risky)
	}
	msg := m.startAction(0, gitx.Fetch, nil)().(actionMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "not run: its own git config runs programs (remote.origin.url)") {
		t.Errorf("unconfirmed fetch: %v", msg.err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the unconfirmed fetch ran the helper")
	}
}

// A risky key may hold a credential ("credential.<url>.helper"): it is
// shown redacted, and the confirmation given on that form covers it
// when the fresh keys are compared, so a confirmed fetch, AI plan and
// publish run (through Update, as the keys do), and a key added after
// the confirmation is still caught. The secret is never on screen.
func TestConfirmationCoversRedactedKey(t *testing.T) {
	const secretKey = "credential.https://oauth2:SECRET@h.helper"
	keyF := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}
	dir := riskyRepo(t)
	if out, err := exec.Command("git", "-C", dir, "config", secretKey, "x").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	m := New(Options{Root: filepath.Dir(dir), Repos: []string{dir}, Tool: "true"})
	t.Cleanup(m.life.cancel)
	m.width, m.height = 120, 60
	send := func(msg tea.Msg) {
		t.Helper()
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	send(statusMsg{idx: 0, st: gitx.ReadStatus(context.Background(), dir)})
	if got := m.rows[0].st.Risky; !slices.Contains(got, "credential.https://***@h.helper") || !slices.Contains(got, "core.sshcommand") {
		t.Fatalf("risky keys %q", got)
	}
	m.detail = true
	if out := ansi.Strip(m.View()); strings.Contains(out, "SECRET") || !strings.Contains(out, "credential.https://***@h.helper") {
		t.Errorf("details:\n%s", out)
	}

	// fetch: f asks, y runs
	send(keyF)
	if m.confirm == nil {
		t.Fatal("f did not ask")
	}
	if out := ansi.Strip(m.View()); strings.Contains(out, "SECRET") {
		t.Errorf("confirm line:\n%s", out)
	}
	next, cmd := m.Update(keyY)
	m = next.(Model)
	if cmd == nil {
		t.Fatal("y did not fetch")
	}
	for _, msg := range runCmds(cmd) {
		if a, ok := msg.(actionMsg); ok && a.err != nil && strings.Contains(a.err.Error(), "not run") {
			t.Errorf("confirmed fetch refused: %v", a.err)
		}
	}

	// an AI plan: the keys come with the validation, as checked
	m.rows[0].busy = ""
	m.mode, m.planSeq = modePlan, 1
	steps := []gitx.Step{{Op: "fetch"}}
	m.plan = &planState{idx: 0, request: "fetch", plan: ai.Plan{Summary: "Fetch", Steps: steps}, valSeq: 1}
	send(planValidMsg{planSeq: 1, valSeq: 1, steps: steps, args: [][]string{{"fetch", "--all", "--prune"}}, errs: []error{nil},
		risky: gitx.ConfigRisks(context.Background(), dir)})
	if out := ansi.Strip(m.View()); strings.Contains(out, "SECRET") || !strings.Contains(out, "credential.https://***@h.helper") {
		t.Errorf("plan:\n%s", out)
	}
	next, cmd = m.Update(keyY)
	m = next.(Model)
	if cmd == nil {
		t.Fatal("y did not run the plan")
	}
	for _, msg := range runCmds(cmd) {
		if a, ok := msg.(actionMsg); ok && a.err != nil && strings.Contains(a.err.Error(), "not run") {
			t.Errorf("confirmed plan refused: %v", a.err)
		}
	}

	// publish: the keys come with the repository's info; the push step
	// checks them (it then fails: there is no remote yet)
	m.rows[0].busy = ""
	m.mode = modeCreate
	m.profiles = []profileState{{Profile: Profile{Name: "work", Forge: forge.GitLab{Host: "h"}}, me: "me"}}
	m.create = &createState{publish: true, step: stepReview, remoteName: "origin", loading: true, idx: 0, path: "acme/tool"}
	send(publishInfoMsg{idx: 0, hasCommits: true, risky: gitx.ConfigRisks(context.Background(), dir)})
	if out := ansi.Strip(m.View()); strings.Contains(out, "SECRET") || !strings.Contains(out, "credential.https://***@h.helper") {
		t.Errorf("review:\n%s", out)
	}
	cs := m.createSteps()
	push := cs[len(cs)-1]
	if !strings.HasPrefix(push.show, "git push") {
		t.Fatalf("last step %q", push.show)
	}
	if _, err := push.run(context.Background()); err != nil && strings.Contains(err.Error(), "not run") {
		t.Errorf("confirmed publish refused: %v", err)
	}

	// a key added after the confirmation is not covered by it
	if out, err := exec.Command("git", "-C", dir, "config", "credential.https://oauth2:OTHER@h2.helper", "x").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if _, err := push.run(context.Background()); err == nil || !strings.Contains(err.Error(), "not run") ||
		strings.Contains(err.Error(), "OTHER") {
		t.Errorf("publish after the config changed: %v", err)
	}
}

// -fetch leaves a repository whose own config runs programs alone: no
// failed fetch on the row and no message, only its status is read, and
// it is marked; a plain repository is fetched.
func TestFetchOnStartSkipsRiskyRepo(t *testing.T) {
	dir := riskyRepo(t)
	plain := t.TempDir()
	if out, err := exec.Command("git", "-C", plain, "init", "-q").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	m := New(Options{Root: filepath.Dir(dir), Repos: []string{dir, plain}, FetchOnStart: true})
	t.Cleanup(m.life.cancel)
	m.width, m.height = 120, 30
	send := func(msg tea.Msg) tea.Cmd {
		next, cmd := m.Update(msg)
		m = next.(Model)
		return cmd
	}
	var results []actionMsg
	for _, msg := range runCmds(m.Init()) {
		if a, ok := msg.(actionMsg); ok {
			results = append(results, a)
		}
	}
	if len(results) != 2 {
		t.Fatalf("startup jobs: %+v", results)
	}
	for _, a := range results {
		if a.idx == 1 && (a.name != "fetch" || a.err != nil) {
			t.Errorf("plain repository not fetched: %+v", a)
		}
		cmd := send(a)
		r := m.rows[a.idx]
		if a.idx == 0 && (!errors.Is(a.err, errRiskyAtStart) || r.lastAct != "" || r.lastErr != nil || m.flash != "" || r.busy != "status") {
			t.Fatalf("risky repository at start: %+v, row %+v, flash %q", a, *r, m.flash)
		}
		if cmd == nil {
			t.Fatalf("no status read after the fetch of row %d", a.idx)
		}
		send(cmd()) // the status read that follows every action
		if a.idx == 0 && (!r.loaded || len(r.st.Risky) == 0 || r.busy != "") {
			t.Fatalf("status after the skipped fetch: %+v", *r)
		}
	}
	m.cursor, m.detail = 0, true
	out := ansi.Strip(m.View())
	if strings.Contains(out, "✗ fetch") || strings.Contains(out, "fetch failed") || !strings.Contains(out, "▸ !") {
		t.Errorf("a skipped startup fetch is shown as a failure:\n%s", out)
	}
	if !strings.Contains(out, "✓ fetch") {
		t.Errorf("the plain repository's fetch is not shown:\n%s", out)
	}
}

// A repository whose own config redirects the network (an http proxy,
// TLS off) is treated like one that runs programs: the unattended
// startup fetch leaves it alone and the actions ask first.
func TestNetworkConfigSkipsFetchOnStart(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "http.proxy", "http://attacker.example:8080"},
		{"config", "http.sslVerify", "false"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	m := New(Options{Root: filepath.Dir(dir), Repos: []string{dir}, FetchOnStart: true})
	t.Cleanup(m.life.cancel)
	m.width, m.height = 120, 30
	m.Update(statusMsg{idx: 0, st: gitx.ReadStatus(context.Background(), dir)})
	want := []string{"network: http.proxy", "network: http.sslverify"}
	if !slices.Equal(m.rows[0].st.Risky, want) {
		t.Fatalf("status: risky %q, want %q", m.rows[0].st.Risky, want)
	}
	if a, ok := m.startFetchOnStart(0)().(actionMsg); !ok || !errors.Is(a.err, errRiskyAtStart) {
		t.Errorf("startup fetch through the repository's proxy: %+v", a)
	}
	if err := checkRisks(context.Background(), dir, nil); err == nil || !strings.Contains(err.Error(), "network: http.proxy") {
		t.Errorf("unconfirmed action: %v", err)
	}
}

// The tool, an agent or a shell runs without the variables that pin git
// to one repository (GIT_DIR, GIT_WORK_TREE, ...), like git itself: the
// git it runs acts on the repository it was opened in.
func TestExecDropsRepoEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_WORK_TREE", "/elsewhere")
	t.Setenv("GL_REPOS_TEST_KEEP", "1")
	c := repoCommand("/repo", []string{"sh", "-c", "x"})
	if c.Dir != "/repo" || len(c.Args) != 3 || c.Args[2] != "x" {
		t.Errorf("command: dir %q, args %q", c.Dir, c.Args)
	}
	if c.Env == nil {
		t.Fatal("env not set: the process environment would be inherited as it is")
	}
	for _, kv := range c.Env {
		if name, _, _ := strings.Cut(kv, "="); name == "GIT_DIR" || name == "GIT_WORK_TREE" {
			t.Errorf("env keeps %s", kv)
		}
	}
	if !slices.Contains(c.Env, "GL_REPOS_TEST_KEEP=1") {
		t.Error("env drops the other variables")
	}
}

// Opening the tool, an agent or a shell reads the config again right
// before the program starts, like a git action: unconfirmed (the status
// had not loaded yet) a risky repository is refused, confirmed it runs,
// and a key added after the confirmation stops it.
func TestExecRechecksRisksBeforeRunning(t *testing.T) {
	keyO := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}}
	m := riskyModel(t)
	dir := m.rows[0].st.Path
	msg, refused := m.runExec(0, []string{"true"}, nil)().(execDoneMsg)
	if !refused || msg.err == nil || !strings.Contains(msg.err.Error(), "not run: its own git config runs programs (core.sshcommand)") {
		t.Errorf("unconfirmed: %+v", msg)
	}
	next, _ := m.handleListKey(keyO)
	_, cmd := next.(Model).handleListKey(keyY)
	if cmd == nil {
		t.Fatal("y did not open the tool")
	}
	if msg, refused := cmd().(execDoneMsg); refused { // Bubble Tea's exec request otherwise
		t.Errorf("confirmed exec refused: %v", msg.err)
	}

	// a key added after the confirmation is not covered by it
	if out, err := exec.Command("git", "-C", dir, "config", "core.pager", "x").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	next, _ = m.handleListKey(keyO)
	_, cmd = next.(Model).handleListKey(keyY)
	msg, refused = cmd().(execDoneMsg)
	if !refused || msg.err == nil || !strings.Contains(msg.err.Error(), "core.pager") {
		t.Errorf("exec after the config changed: %+v", msg)
	}
}

// y covers the keys the question showed: a key that a status read lists
// while the question is on screen was not shown, so the action and the
// tool are refused.
func TestConfirmationCoversOnlyShownKeys(t *testing.T) {
	for _, key := range []rune{'f', 'o'} {
		m := riskyModel(t)
		dir := m.rows[0].st.Path
		next, _ := m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		m = next.(Model)
		if m.confirm == nil {
			t.Fatalf("%c did not ask", key)
		}
		if out, err := exec.Command("git", "-C", dir, "config", "core.pager", "x").CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
		m.Update(statusMsg{idx: 0, st: gitx.ReadStatus(context.Background(), dir)}) // a status read finishes meanwhile
		_, cmd := m.handleListKey(keyY)
		var err error
		for _, msg := range runCmds(cmd) {
			switch msg := msg.(type) {
			case actionMsg:
				err = msg.err
			case execDoneMsg:
				err = msg.err
			}
		}
		if err == nil || !strings.Contains(err.Error(), "not run") || !strings.Contains(err.Error(), "core.pager") {
			t.Errorf("%c then y with a key added meanwhile: %v", key, err)
		}
	}
}

// The "nothing to push" shortcut re-reads the status first: a commit made
// in a shell since the list was read is pushed.
func TestPushAfterExternalCommit(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	bare, dir := filepath.Join(root, "bare.git"), filepath.Join(root, "work")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.com",
			"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "--bare", "-b", "main", bare)
	git("init", "-q", "-b", "main", dir)
	git("-C", dir, "commit", "-q", "--allow-empty", "-m", "init")
	git("-C", dir, "remote", "add", "origin", bare)
	git("-C", dir, "push", "-q", "-u", "origin", "main")

	m := New(Options{Root: root, Repos: []string{dir}})
	t.Cleanup(m.life.cancel)
	m.rows[0].st = gitx.ReadStatus(context.Background(), dir)
	if st := m.rows[0].st; st.Err != nil || st.Upstream == "" || st.Ahead != 0 {
		t.Fatalf("status %+v", st)
	}
	git("-C", dir, "commit", "-q", "--allow-empty", "-m", "outside") // the listed status is now stale
	msg := m.startAction(0, gitx.Push, nil)().(actionMsg)
	if msg.err != nil || msg.out == "nothing to push" {
		t.Fatalf("push: %q, %v", msg.out, msg.err)
	}
	if st := gitx.ReadStatus(context.Background(), dir); st.Ahead != 0 {
		t.Errorf("not pushed: ahead %d", st.Ahead)
	}
	m.rows[0].busy = ""
	if msg := m.startAction(0, gitx.Push, nil)().(actionMsg); msg.err != nil || msg.out != "nothing to push" {
		t.Errorf("push with nothing to push: %q, %v", msg.out, msg.err)
	}
}
