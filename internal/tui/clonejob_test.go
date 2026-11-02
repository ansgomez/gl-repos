package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

func TestClaimDirStaysInRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	os.MkdirAll(filepath.Join(root, "grp", "repo", ".git"), 0o755)
	os.MkdirAll(outside, 0o755)
	os.Symlink(outside, filepath.Join(root, "grp", "link"))
	// the root itself reached through a symlink (like /tmp on macOS)
	alias := filepath.Join(base, "alias")
	os.Symlink(root, alias)
	// a root that is itself a repository
	repoRoot := filepath.Join(base, "reporoot")
	os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755)

	for _, c := range []struct {
		root, dest string
		bad        string
	}{
		{root, filepath.Join(root, "grp", "new", "x"), ""},
		{alias, filepath.Join(alias, "grp", "other"), ""},
		{root, filepath.Join(root, "grp", "link", "x"), "symlink"},
		{root, filepath.Join(root, "grp", "repo", "sub", "x"), "repository"},
		{root, filepath.Join(root, "grp", "repo"), "exists"},
		{repoRoot, filepath.Join(repoRoot, "x"), "repository"},
	} {
		cl, err := claimDir(c.root, c.dest)
		if cl != nil {
			cl.release()
		}
		switch {
		case c.bad == "" && err != nil:
			t.Errorf("claimDir(%s): %v", c.dest, err)
		case c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad)):
			t.Errorf("claimDir(%s) = %v, want error about %s", c.dest, err, c.bad)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("something was created outside the root: %v", entries)
	}
}

// The root "/" contains every folder below it: a prefix test with
// root+"/" would call them all outside.
func TestCheckParentsRootSlash(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := checkParents(string(filepath.Separator), filepath.Join(base, "x")); err != nil {
		t.Errorf("checkParents(/): %v", err)
	}
}

// Two clones whose folders nest: while acme/a is being cloned, acme/a/b
// cannot be claimed (removing a's failed attempt would delete it), nor
// can acme while acme/a is; once a is released, they can.
func TestClaimDirRefusesNestedActiveClaims(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "acme", "a")
	claimA, err := claimDir(root, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(a, "b"), filepath.Join(root, "acme")} {
		if c, err := claimDir(root, d); err == nil || !strings.Contains(err.Error(), "still being created") {
			if c != nil {
				c.release()
			}
			t.Errorf("claimDir(%s) while %s is claimed: %v", d, a, err)
		}
	}
	if _, err := os.Stat(filepath.Join(a, "b")); err == nil {
		t.Error("the refused claim created a folder inside a")
	}
	sibling := filepath.Join(root, "acme", "ab") // a prefix of the name, not of the path
	claimS, err := claimDir(root, sibling)
	if err != nil {
		t.Errorf("sibling %s: %v", sibling, err)
	} else {
		claimS.release()
	}

	// a's clone fails: its attempt is removed, then the claim ends
	if err := claimA.remove(); err != nil {
		t.Fatal(err)
	}
	claimA.release()
	claimB, err := claimDir(root, filepath.Join(a, "b"))
	if err != nil {
		t.Fatalf("after a was released: %v", err)
	}
	claimB.release()
	if c, err := claimDir(root, filepath.Join(a, "b")); err == nil || !strings.Contains(err.Error(), "already exists") {
		if c != nil {
			c.release()
		}
		t.Errorf("claiming an existing folder again: %v", err)
	}
}

// credJob lists a clone job for url into dest without enqueueClone.
func credJob(m *Model, url forge.CloneURL, dest string) *cloneJob {
	j := &cloneJob{url: url, label: showURL(url.String()), dest: dest}
	m.rows[m.addRow(dest, "clone", "")].job = j
	return j
}

func mustURL(t *testing.T, s string) forge.CloneURL {
	t.Helper()
	u, err := forge.ParseCloneURL(s)
	if err != nil {
		t.Fatalf("ParseCloneURL(%q): %v", s, err)
	}
	return u
}

// A clone needs a CloneURL: no URL with credentials can be one, and
// enqueueClone refuses the zero value before anything runs. ssh URLs
// with only a user are fine.
func TestCloneNeedsCloneURL(t *testing.T) {
	root := t.TempDir()
	m := New(Options{Root: root})
	defer m.life.cancel()
	m.width, m.height = 200, 40
	for _, u := range []string{"https://oauth2:glpat-xxx@host/p.git", "https://user@host/p.git", "ssh://git:pw@host/p.git"} {
		if _, err := forge.ParseCloneURL(u); !errors.Is(err, forge.ErrURLCredentials) {
			t.Errorf("%s parsed", u)
		}
	}
	if idx, cmd := m.enqueueClone(forge.CloneURL{}, "acme/p", filepath.Join(root, "p"), ""); idx >= 0 || cmd != nil || len(m.rows) != 0 {
		t.Error("clone of no URL started")
	}
	if _, err := os.Stat(filepath.Join(root, "p")); err == nil {
		t.Error("a folder was created for a refused clone")
	}
	for i, u := range []string{"git@host:acme/p.git", "ssh://git@host/acme/p.git", "https://host/acme/p.git"} {
		if idx, cmd := m.enqueueClone(mustURL(t, u), u, filepath.Join(root, fmt.Sprint("ok", i)), ""); idx < 0 || cmd == nil {
			t.Errorf("%s refused: %q", u, m.flash)
		}
	}
}

// git may echo the clone URL with its credentials when it fails: the
// row's stored output, its error line, the flash and the details never
// show them.
func TestCloneOutputRedacted(t *testing.T) {
	const url, secret = "https://oauth2:glpat-xxx@host/p.git", "glpat-xxx"
	fatal := "Cloning into 'p'...\nfatal: unable to access '" + url + "/': The requested URL returned error: 403"
	out, err := redactOutput(fatal, errors.New("git clone "+url+": exit status 128"))
	if strings.Contains(out, secret) || strings.Contains(err.Error(), secret) ||
		!strings.Contains(out, "'https://***@host/p.git/'") || !strings.Contains(err.Error(), "exit status 128") {
		t.Errorf("redactOutput: %q, %v", out, err)
	}

	root := t.TempDir()
	m := New(Options{Root: root})
	defer m.life.cancel()
	m.width, m.height, m.detail = 200, 40, true
	j := credJob(&m, mustURL(t, "https://host/p.git"), filepath.Join(root, "p"))
	idx := len(m.rows) - 1
	// even raw output (as before redactOutput) is not shown
	m.handleCloneJobDone(cloneJobDoneMsg{job: j, out: fatal, err: errors.New("exit status 128")})
	shown := m.flash + "\n" + j.view().lastErr + "\n" + ansi.Strip(m.renderDetail()) + "\n" + ansi.Strip(m.renderJob(j, 120))
	if strings.Contains(shown, secret) || !strings.Contains(m.flash, "https://***@host/p.git") {
		t.Errorf("token shown:\n%s", shown)
	}
	m.rows[idx].job = nil // the row as a plain repository: its last output in the details
	if out := ansi.Strip(m.renderDetail()); strings.Contains(out, secret) || !strings.Contains(out, "https://***@host") {
		t.Errorf("details of the last output:\n%s", out)
	}
}

// While a cancelled clone's git is still exiting, its row cannot be
// reused: the old run's late end must not land in a new one. Once it is
// done the folder can be cloned again.
func TestCloneRestartDuringCancel(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "p")
	m := New(Options{Root: root})
	defer m.life.cancel()
	idx, _ := m.enqueueClone(mustURL(t, "https://host/p.git"), "acme/p", dest, "")
	j := m.rows[idx].job
	m.cursor = 0
	m.cancelOrDismiss()
	if j.view().state != jobStopping {
		t.Fatalf("state %v after x, want stopping", j.view().state)
	}
	if i, cmd := m.enqueueClone(mustURL(t, "https://host/p.git"), "acme/p", dest, ""); i >= 0 || cmd != nil ||
		!strings.Contains(m.flash, "still being cancelled") {
		t.Fatalf("restarted while stopping: %d, flash %q", i, m.flash)
	}
	if m.rows[idx].job != j || len(m.rows) != 1 {
		t.Fatal("the stopping row was replaced")
	}
	m.handleCloneJobDone(cloneJobDoneMsg{job: j, err: errCancelled})
	i, cmd := m.enqueueClone(mustURL(t, "https://host/p.git"), "acme/p", dest, "")
	if i < 0 || cmd == nil || m.rows[i].job == j {
		t.Errorf("no fresh clone after the cancel finished: row %d", i)
	}
}

// A URL pasted into the clone box shows its token as "*" (also with
// the cursor inside it), and enter refuses it instead of cloning.
func TestCloneQueryMasksToken(t *testing.T) {
	const url, secret = "https://oauth2:glpat-xxx@host/acme/p.git", "glpat-xxx"
	m := Model{width: 200, height: 30, opts: Options{Root: t.TempDir()},
		profiles: []profileState{{Profile: Profile{Name: "gitlab", Forge: forge.GitLab{Host: "host"}}}}}
	m.clone = &cloneState{}
	m.mode = modeCloneQuery
	m.input.Prompt = "clone ▸ "
	m.input.SetValue(url)
	m.input.CursorEnd()
	view := func() string { return ansi.Strip(m.viewClone()) }
	if out := view(); strings.Contains(out, secret) || !strings.Contains(out, "https://"+strings.Repeat("*", len("oauth2:glpat-xxx"))+"@host/acme/p.git") {
		t.Errorf("query field:\n%s", out)
	}
	m.submitCloneQuery()
	if m.mode != modeCloneQuery || !m.clone.url.IsZero() || !errors.Is(m.clone.err, forge.ErrURLCredentials) {
		t.Fatalf("mode %v url %q err %v", m.mode, m.clone.url, m.clone.err)
	}
	if out := view(); strings.Contains(out, secret) || !strings.Contains(out, "use an ssh URL or a credential helper") {
		t.Errorf("refused:\n%s", out)
	}
	m.input.pos = 12 // cursor inside the token: still masked, same width
	if out := m.input.View(); strings.Contains(ansi.Strip(out), "glpat") || ansi.StringWidth(out) != ansi.StringWidth("clone ▸ "+url) {
		t.Errorf("cursor in token: %q", ansi.Strip(out))
	}

	// A pasted URL with an escape sequence inside its token is cleaned on
	// insert, so it is still recognised and masked.
	m.input.Reset()
	m.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("https://oauth2:glpat\x1b]8;; /x\x07-xxx@host/acme/p.git")})
	if v := m.input.Value(); strings.ContainsRune(v, '\x1b') || v != url {
		t.Errorf("pasted value %q", v)
	}
	if out := ansi.Strip(m.input.View()); strings.Contains(out, "glpat") {
		t.Errorf("pasted token shown: %q", out)
	}
}

func TestPlanStepsRedactURLs(t *testing.T) {
	m := planModel(t, 120, 40, gitx.Step{Op: "commit", Message: "Pin https://oauth2:glpat-one@host/a.git"},
		gitx.Step{Op: "commit", Message: "Two lines\n\nsee https://oauth2:glpat-two@host/b.git"})
	lines, _ := m.planLines()
	if out := ansi.Strip(strings.Join(lines, "\n")); strings.Contains(out, "glpat") || !strings.Contains(out, "https://***@host/a.git") {
		t.Errorf("plan steps:\n%s", out)
	}
	m.plan.stepErrs[0] = errors.New("cannot reach https://u:pw@host/x")
	lines, _ = m.planLines()
	if out := ansi.Strip(strings.Join(lines, "\n")); strings.Contains(out, ":pw@") {
		t.Errorf("step error:\n%s", out)
	}
}

func TestPlanScreenRedactsURLs(t *testing.T) {
	m := planModel(t, 120, 40)
	m.plan.request = "clone https://oauth2:glpat-xxx@host/acme/p.git"
	m.plan.plan.Summary = "Clone https://oauth2:glpat-xxx@host/acme/p.git"
	lines, _ := m.planLines()
	if out := ansi.Strip(strings.Join(lines, "\n")); strings.Contains(out, "glpat") || !strings.Contains(out, "https://***@host/acme/p.git") {
		t.Errorf("plan screen:\n%s", out)
	}
	m.plan.loading, m.plan.err = false, errors.New("bad ssh://git:pw@host/q")
	lines, _ = m.planLines()
	if out := ansi.Strip(strings.Join(lines, "\n")); strings.Contains(out, ":pw@") {
		t.Errorf("plan error:\n%s", out)
	}
}

// runCmds runs cmd (a batch or a single command) and returns its messages.
func runCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msgs := []tea.Msg{cmd()}
	if b, ok := msgs[0].(tea.BatchMsg); ok {
		msgs = nil
		for _, c := range b {
			if c != nil {
				msgs = append(msgs, c())
			}
		}
	}
	return msgs
}

// A clone URL from the AI assistant is taken like a pasted one: never
// searched on the forge (whose CLI arguments and request would carry a
// token), refused with credentials, else straight to the destination.
func TestAICloneURLNotSearched(t *testing.T) {
	const url, secret = "https://oauth2:glpat-xxx@host/p.git", "glpat-xxx"
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	newModel := func() Model {
		return Model{width: 200, height: 30, opts: Options{Root: t.TempDir()},
			profiles: []profileState{{Profile: Profile{Name: "gitlab", Forge: forge.GitLab{Host: "host",
				API: func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
					t.Errorf("forge asked for %s", ep)
					return nil, errors.New("no")
				}}}}}}
	}
	searched := func(cmd tea.Cmd) bool {
		for _, msg := range runCmds(cmd) {
			if _, ok := msg.(cloneSearchMsg); ok {
				return true
			}
		}
		return false
	}

	m := newModel()
	cmd := m.openClone(url)
	if m.mode != modeCloneQuery || !m.clone.url.IsZero() || m.clone.searching || !errors.Is(m.clone.err, forge.ErrURLCredentials) {
		t.Fatalf("mode %v url %q searching %v err %v", m.mode, m.clone.url, m.clone.searching, m.clone.err)
	}
	if searched(cmd) {
		t.Error("searched the forge for the URL")
	}
	if out := ansi.Strip(m.viewClone()); strings.Contains(out, secret) || !strings.Contains(out, "contains credentials") {
		t.Errorf("refused:\n%s", out)
	}

	m = newModel()
	cmd = m.openClone("https://host/acme/p.git")
	if m.mode != modeCloneDest || m.clone.url.String() != "https://host/acme/p.git" || m.clone.searching || m.input.Value() != "p" {
		t.Fatalf("clean URL: mode %v url %q searching %v dest %q", m.mode, m.clone.url, m.clone.searching, m.input.Value())
	}
	if searched(cmd) {
		t.Error("searched the forge for the clean URL")
	}
}

// The search view never shows a URL's credentials, nor in an error; a
// URL taken after a search drops its results, so esc cannot bring the
// URL back into the pick view.
func TestClonePickViewRedacted(t *testing.T) {
	const url, secret = "https://oauth2:glpat-xxx@host/p.git", "glpat-xxx"
	m := Model{width: 200, height: 30, opts: Options{Root: t.TempDir()},
		profiles: []profileState{{Profile: Profile{Name: "gitlab", Forge: forge.GitLab{Host: "host"}}}}}
	m.clone = &cloneState{query: url, err: errors.New("search for " + url + " failed")}
	m.mode = modeClonePick
	if out := ansi.Strip(m.viewClone()); strings.Contains(out, secret) || !strings.Contains(out, "search: https://***@host/p.git") {
		t.Errorf("pick view:\n%s", out)
	}

	for _, u := range []string{url, "https://host/acme/p.git"} {
		m.clone = &cloneState{query: "tools", results: []forge.Project{{Path: "acme/a"}, {Path: "acme/b"}}}
		m.mode = modeCloneQuery
		m.input.SetValue(u)
		m.submitCloneQuery()
		if m.clone.results != nil {
			t.Fatalf("%s: results kept %v", u, m.clone.results)
		}
		if m.mode == modeCloneDest {
			next, _ := m.handleCloneDestKey(keyEsc)
			m = next.(Model)
		}
		if m.mode != modeCloneQuery {
			t.Errorf("%s: mode %v, want the query field", u, m.mode)
		}
		if out := ansi.Strip(m.viewClone()); strings.Contains(out, secret) {
			t.Errorf("%s:\n%s", u, out)
		}
	}
}

func TestProjectWebURLWithCredentialsRefused(t *testing.T) {
	m := Model{width: 200, height: 30, opts: Options{Root: t.TempDir()},
		profiles: []profileState{{Profile: Profile{Name: "gitlab", Forge: forge.GitLab{Host: "host"}}}}}
	m.clone = &cloneState{query: "https://oauth2:glpat-xxx@host/acme/p"} // a web URL, no .git
	cmd, done := m.useURLQuery()
	if !done || cmd != nil || !errors.Is(m.clone.err, forge.ErrURLCredentials) || m.clone.searching {
		t.Errorf("done %v cmd %v err %v searching %v", done, cmd != nil, m.clone.err, m.clone.searching)
	}
}

// URL-looking input that does not parse is refused in the box: never
// searched, never a destination folder, and the error never quotes it.
func TestCloneQueryRefusedShapes(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	for _, q := range []string{
		"oauth2:glpat-SECRET@gitlab.example.com/acme/tools.git",
		"oauth2:glpat-SECRET@gitlab.example.com/acme/tools",
		"https://h/p.git#access_token=SECRET",
		"https://gitlab.example.com/acme/tools.git?%70rivate_token=SECRET",
		"https://gitlab.example.com/acme/tools.git;private_token=SECRET",
		"https://gitlab.example.com/acme/tools?private_token=SECRET", // no group URL either
	} {
		m := Model{width: 200, height: 30, opts: Options{Root: t.TempDir()},
			profiles: []profileState{{Profile: Profile{Name: "gitlab", Forge: forge.GitLab{Host: "gitlab.example.com",
				API: func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
					t.Errorf("%s: forge asked for %s", q, ep)
					return nil, errors.New("no")
				}}}}}}
		cmd := m.openClone(q)
		for _, msg := range runCmds(cmd) {
			if _, ok := msg.(cloneSearchMsg); ok {
				t.Errorf("%s: searched", q)
			}
		}
		if m.mode != modeCloneQuery || !m.clone.url.IsZero() || m.clone.err == nil {
			t.Errorf("%s: mode %v url %q err %v", q, m.mode, m.clone.url, m.clone.err)
		}
		if strings.Contains(m.clone.err.Error(), "SECRET") {
			t.Errorf("%s: error quotes it: %v", q, m.clone.err)
		}
	}
}

// A destination typed with credentials is refused in the box: no
// review, no row, no folder, and nothing on screen shows the secret.
func TestCloneDestWithCredentialsRefused(t *testing.T) {
	root := t.TempDir()
	m := New(Options{Root: root})
	defer m.life.cancel()
	m.width, m.height = 200, 30
	m.clone = &cloneState{}
	m.chooseClone(mustURL(t, "https://host/acme/p.git"), "acme/p", "p")
	for _, bad := range secretShapes {
		m.input.SetValue(bad)
		m.input.CursorEnd()
		m.validateCloneDest()
		if !errors.Is(m.clone.destErr, gitx.ErrDestCredentials) {
			t.Errorf("%q: destErr %v", bad, m.clone.destErr)
		}
		if out := ansi.Strip(m.viewClone()); strings.Contains(out, "SECRET") || strings.Contains(out, "git clone") {
			t.Errorf("%q:\n%s", bad, out)
		}
		next, cmd := m.handleCloneDestKey(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(Model)
		if cmd != nil || m.mode != modeCloneDest || len(m.rows) != 0 {
			t.Errorf("%q: enter ran a clone (mode %v, rows %d)", bad, m.mode, len(m.rows))
		}
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("created %v", left)
	}
}

// A destination with a .git folder on its way is refused in the box:
// created, it would mark the root (or a folder in it) as a repository.
func TestCloneDestGitDirRefused(t *testing.T) {
	root := t.TempDir()
	m := New(Options{Root: root})
	defer m.life.cancel()
	m.width, m.height = 200, 30
	m.clone = &cloneState{}
	m.chooseClone(mustURL(t, "https://host/acme/p.git"), "acme/p", "p")
	for _, bad := range []string{".git", "acme/.GIT/p"} {
		m.input.SetValue(bad)
		m.input.CursorEnd()
		m.validateCloneDest()
		if !errors.Is(m.clone.destErr, gitx.ErrDestGitDir) {
			t.Errorf("%q: destErr %v", bad, m.clone.destErr)
		}
		next, cmd := m.handleCloneDestKey(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(Model)
		if cmd != nil || m.mode != modeCloneDest || len(m.rows) != 0 {
			t.Errorf("%q: enter ran a clone (mode %v, rows %d)", bad, m.mode, len(m.rows))
		}
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("created %v", left)
	}
}

// A failed clone's folder is removed only while it is still the one
// claimDir created. Removing by path would follow a parent swapped for
// a symlink (and delete the folder it points to) or empty a folder put
// in the claimed one's place.
func TestClaimRemoveLeavesSwappedFolders(t *testing.T) {
	for _, c := range []struct {
		name string
		// swap changes the tree after the claim and returns a file that
		// must survive the removal ("" when the folder is simply removed)
		swap func(root, dest string) string
	}{
		{"unchanged", func(root, dest string) string { return "" }},
		{"parent swapped for a symlink", func(root, dest string) string {
			grp, other := filepath.Dir(dest), filepath.Join(root, "other")
			os.Rename(grp, other) // the claimed folder is now other/x
			os.Symlink(other, grp)
			return filepath.Join(other, filepath.Base(dest), "data")
		}},
		{"folder replaced", func(root, dest string) string {
			os.Rename(dest, dest+".bak")
			os.Mkdir(dest, 0o755)
			os.WriteFile(filepath.Join(dest, "theirs"), []byte("x"), 0o644)
			return filepath.Join(dest, "theirs")
		}},
		{"folder swapped for a symlink", func(root, dest string) string {
			os.Rename(dest, dest+".bak")
			os.Symlink(dest+".bak", dest)
			return filepath.Join(dest+".bak", "data")
		}},
	} {
		root := t.TempDir()
		dest := filepath.Join(root, "grp", "x")
		cl, err := claimDir(root, dest)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		os.WriteFile(filepath.Join(dest, "data"), []byte("partial clone"), 0o644)
		keep := c.swap(root, dest)
		err = cl.remove()
		cl.release()
		if keep == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			if _, err := os.Lstat(dest); err == nil {
				t.Errorf("%s: the failed clone's folder is still there", c.name)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "not removed") {
			t.Errorf("%s: remove = %v, want it refused", c.name, err)
		}
		if _, err := os.Lstat(keep); err != nil {
			t.Errorf("%s: %s was deleted", c.name, keep)
		}
	}
}

// Removing a failed clone also removes the parents claimDir created for
// it, while they are empty; folders that were there before stay.
func TestClaimRemoveTakesMadeParents(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "grp"), 0o755)
	for _, sibling := range []string{"", filepath.Join(root, "grp", "a", "other")} {
		cl, err := claimDir(root, filepath.Join(root, "grp", "a", "b", "x"))
		if err != nil {
			t.Fatal(err)
		}
		if sibling != "" {
			os.Mkdir(sibling, 0o755)
		}
		if err := cl.remove(); err != nil {
			t.Fatal(err)
		}
		cl.release()
		left, _ := os.ReadDir(filepath.Join(root, "grp"))
		if sibling == "" && len(left) != 0 {
			t.Errorf("made parents left behind: %v", left)
		}
		if sibling != "" {
			if _, err := os.Stat(sibling); err != nil {
				t.Errorf("%s was removed", sibling)
			}
			if _, err := os.Stat(filepath.Join(root, "grp", "a", "b")); err == nil {
				t.Error("grp/a/b left behind")
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "grp")); err != nil {
		t.Error("a pre-existing folder was removed")
	}
}

// q after x: a cancelled clone whose git is still exiting counts as
// running work, so quitting asks and then waits for it, instead of
// leaving the process and its half-removed folder behind. With nothing
// running, q still quits through the lifetime (never tea.Quit at once).
func TestQuitWaitsForStoppingClone(t *testing.T) {
	keyQ := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	m := New(Options{Root: "/w"})
	t.Cleanup(m.life.cancel)
	j := credJob(&m, mustURL(t, "git@h:acme/x.git"), "/w/x")
	j.set(func(j *cloneJob) { j.state = jobStopping })
	if n := m.runningWork(); n != 1 {
		t.Errorf("runningWork = %d with a clone still stopping, want 1", n)
	}
	next, cmd := m.handleListKey(keyQ)
	if cmd != nil || !next.(Model).quitConfirm {
		t.Error("q quit at once while a clone was still stopping")
	}

	m = New(Options{Root: "/w"})
	t.Cleanup(m.life.cancel)
	_, cmd = m.handleListKey(keyQ)
	if cmd == nil {
		t.Fatal("q did nothing")
	}
	if _, now := cmd().(tea.QuitMsg); now {
		t.Error("q quit without waiting for the lifetime's work")
	}
}

// A search answer that arrives after its dialog was closed is dropped,
// also when the dialog has been opened again since.
func TestCloneSearchAfterCloseDropped(t *testing.T) {
	m := New(Options{Root: t.TempDir()})
	defer m.life.cancel()
	m.openClone("")
	m.clone.query = "tools"
	m.serverSearch()
	late := cloneSearchMsg{seq: m.cloneSeq, host: "h", err: errors.New("cancelled")}
	m.closeClone()
	m.openClone("")
	m.handleCloneSearchMsg(late)
	if m.clone.err != nil || m.clone.searching {
		t.Errorf("late answer landed in the new dialog: err %v", m.clone.err)
	}
}

// The search-results picker's shortcuts ignore case like every other:
// J and K move, Q goes back to the query.
func TestClonePickKeysIgnoreCase(t *testing.T) {
	m := Model{width: 200, height: 30, opts: Options{Root: t.TempDir()},
		profiles: []profileState{{Profile: Profile{Name: "gitlab", Forge: forge.GitLab{Host: "host"}}}}}
	m.clone = &cloneState{query: "tools", results: []forge.Project{{Path: "acme/a"}, {Path: "acme/b"}, {Path: "acme/c"}}}
	m.mode = modeClonePick
	pick := func(r rune) {
		next, _ := m.handleClonePickKey(runeKey(r))
		m = next.(Model)
	}
	for _, c := range []struct {
		key  rune
		want int
	}{{'j', 1}, {'J', 2}, {'K', 1}, {'k', 0}, {'K', 0}} {
		if pick(c.key); m.clone.cursor != c.want || m.mode != modeClonePick {
			t.Fatalf("after %c: cursor %d (want %d), mode %v", c.key, m.clone.cursor, c.want, m.mode)
		}
	}
	if pick('Q'); m.mode != modeCloneQuery || m.clone == nil {
		t.Errorf("Q: mode %v clone %v", m.mode, m.clone)
	}
}
