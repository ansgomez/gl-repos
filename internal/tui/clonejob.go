package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

const (
	cloneAttempts = 3
	// cloneStall aborts an attempt when git prints nothing for this long
	// (dead connection); ssh itself gives up connecting after 15s.
	cloneStall = 90 * time.Second
)

// cloneBackoff is the wait before attempt 2, 3, ….
var cloneBackoff = []time.Duration{3 * time.Second, 10 * time.Second}

type jobState int

const (
	jobQueued   jobState = iota // waiting for a free slot (-jobs)
	jobRunning                  // git clone is running
	jobWaiting                  // failed, retrying soon
	jobFailed                   // gave up; r retries, x removes
	jobStopping                 // cancelled, waiting for git to exit
)

var errCancelled = errors.New("cancelled")

// cloneJob is a clone shown as a row while it runs. The clone goroutine
// updates it under mu; the view reads it on every spinner tick. url,
// label and group change when a failed clone is started again, so they
// are written under mu too; dest never changes.
type cloneJob struct {
	url   forge.CloneURL
	label string // what is being cloned, safe to show (no userinfo)
	dest  string
	group string // group clone it belongs to, "" if none

	mu      sync.Mutex
	state   jobState
	phase   string
	pct     int
	attempt int
	lastErr string
	cancel  context.CancelFunc
}

type jobView struct {
	state          jobState
	phase          string
	pct, attempt   int
	lastErr, label string
	url            forge.CloneURL // as git gets it
	group          string
}

func (j *cloneJob) view() jobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return jobView{j.state, j.phase, j.pct, j.attempt, j.lastErr, j.label, j.url, j.group}
}

func (j *cloneJob) set(f func(j *cloneJob)) {
	j.mu.Lock()
	f(j)
	j.mu.Unlock()
}

func (j *cloneJob) active() bool {
	s := j.view().state
	return s == jobQueued || s == jobRunning || s == jobWaiting
}

type cloneJobDoneMsg struct {
	job *cloneJob
	out string
	err error
}

// enqueueClone lists dest right away as a cloning row and starts the
// clone. dest must not exist: a failed attempt's leftovers are removed
// before the next one.
// It returns the row, or -1 (and a nil command) if nothing was started.
// label is shown (sanitized here); url goes to git.
func (m *Model) enqueueClone(url forge.CloneURL, label, dest, group string) (int, tea.Cmd) {
	label = showURL(label)
	if url.IsZero() {
		m.flash = "clone of " + label + " refused: " + forge.ErrBadCloneURL.Error()
		return -1, nil
	}
	for i, r := range m.rows { // cloned before and failed: retry that row
		if !r.gone && r.job != nil && r.st.Path == dest {
			if r.job.active() {
				return -1, nil
			}
			if r.job.view().state == jobStopping {
				// its git may still run (and remove its folder): reusing
				// the job now would mix the old run's end into the new one
				m.flash = "the clone into " + r.rel + " is still being cancelled, try again"
				return -1, nil
			}
			r.job.set(func(j *cloneJob) { j.url, j.label, j.group = url, label, group })
			r.lastErr = nil
			return i, m.runJob(r.job)
		}
	}
	if _, err := os.Stat(dest); err == nil {
		m.flash = dest + " already exists"
		return -1, nil
	}
	j := &cloneJob{url: url, label: label, dest: dest, group: group}
	idx := m.addRow(dest, "clone", "")
	m.rows[idx].job = j
	return idx, m.runJob(j)
}

func (m *Model) runJob(j *cloneJob) tea.Cmd {
	ctx, cancel := context.WithCancel(m.life.ctx)
	j.set(func(j *cloneJob) { j.state, j.pct, j.phase, j.attempt, j.cancel = jobQueued, 0, "", 0, cancel })
	root, slots, life := m.opts.Root, m.cloneSlots, m.life
	life.wg.Add(1)
	return func() tea.Msg {
		defer life.wg.Done()
		defer cancel()
		// Clones have their own slots so status, fetch and AI keep going;
		// a slot is given back while waiting to retry.
		acquire := func() bool {
			select {
			case slots <- struct{}{}:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !acquire() {
			return cloneJobDoneMsg{job: j, err: errCancelled}
		}
		held := true
		defer func() {
			if held {
				<-slots
			}
		}()
		for attempt := 1; ; attempt++ {
			j.set(func(j *cloneJob) { j.state, j.attempt, j.phase, j.pct = jobRunning, attempt, "connecting", 0 })
			url := j.view().url
			// Claim the destination now, not when the clone was queued: an
			// empty folder created here is ours, and anything already
			// there is never touched or removed.
			c, err := claimDir(root, j.dest)
			if err != nil {
				return cloneJobDoneMsg{job: j, err: err}
			}
			actx, acancel := context.WithTimeout(ctx, cloneTimeout)
			out, err := gitx.CloneProgress(actx, root, url, j.dest, cloneStall, func(phase string, pct int) {
				j.set(func(j *cloneJob) {
					j.phase = phase
					if pct > j.pct {
						j.pct = pct
					}
				})
			})
			acancel()
			out, err = redactOutput(out, err)
			if err == nil {
				c.release()
				return cloneJobDoneMsg{job: j, out: out}
			}
			// created empty by claimDir above: only the failed attempt is in
			// it, unless something replaced it meanwhile (then it stays)
			if rerr := c.remove(); rerr != nil {
				out = strings.TrimSpace(out + "\n" + rerr.Error())
			}
			c.release()
			if ctx.Err() != nil {
				return cloneJobDoneMsg{job: j, out: out, err: errCancelled}
			}
			timedOut := errors.Is(actx.Err(), context.DeadlineExceeded)
			if timedOut {
				err = fmt.Errorf("timed out after %s", cloneTimeout)
			}
			if attempt >= cloneAttempts || timedOut || permanentCloneError(out) {
				return cloneJobDoneMsg{job: j, out: out, err: err}
			}
			msg := lastLine(out, err)
			j.set(func(j *cloneJob) { j.state, j.lastErr = jobWaiting, msg })
			<-slots
			held = false
			select {
			case <-time.After(cloneBackoff[min(attempt-1, len(cloneBackoff)-1)]):
			case <-ctx.Done():
				return cloneJobDoneMsg{job: j, out: out, err: errCancelled}
			}
			if !acquire() {
				return cloneJobDoneMsg{job: j, out: out, err: errCancelled}
			}
			held = true
		}
	}
}

// redactOutput hides the credentials git may echo from the clone URL
// ("fatal: unable to access 'https://oauth2:<token>@host/…'"), before
// the output is stored in the row or shown.
func redactOutput(out string, err error) (string, error) {
	out = forge.RedactURLs(out)
	if err != nil {
		if s := forge.RedactURLs(err.Error()); s != err.Error() {
			err = errors.New(s)
		}
	}
	return out, err
}

// claimDir creates dest as a new empty directory (and its parents). It
// fails if dest exists in any form, so a clone never works in, or later
// removes, a folder it did not create. It also fails while another claim
// that is not released yet lies inside dest or contains it: removing a
// failed clone of acme/a would delete a clone of acme/a/b running in it.
// release ends the claim once the folder is a repository or removed.
func claimDir(root, dest string) (c *claim, err error) {
	dest = filepath.Clean(dest)
	if err := claims.add(dest); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			claims.remove(dest)
		}
	}()
	if err := checkParents(root, dest); err != nil {
		return nil, err
	}
	var made []string // the parents MkdirAll is about to create, deepest first
	for d := filepath.Dir(dest); d != root; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		made = append(made, d)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, err
	}
	if err := checkParents(root, dest); err != nil { // again, after creating them
		return nil, err
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%s already exists: not touched", dest)
		}
		return nil, err
	}
	c = &claim{root: root, dest: dest, made: made}
	if c.dir, err = os.Lstat(dest); err != nil {
		return nil, err
	}
	if c.parent, err = os.Stat(filepath.Dir(dest)); err != nil {
		return nil, err
	}
	return c, nil
}

// claim is a folder created by claimDir, with the identity of it and of
// its parent as they were then, and the parents created for it.
type claim struct {
	root, dest  string
	dir, parent os.FileInfo
	made        []string
}

// release ends the claim: the folder is a repository now, or removed.
func (c *claim) release() { claims.remove(c.dest) }

// remove deletes the claimed folder after a failed clone, only if it is
// still the one claimDir created: no symlink on the way from the root,
// the same parent, the same directory. Deleting by path would follow a
// parent swapped for a symlink, or empty a folder put in the claimed
// one's place; so the folder is removed by name in its opened parent
// (os.Root, which resolves nothing again), and left alone if anything
// changed.
func (c *claim) remove() error {
	changed := fmt.Errorf("%s is not the folder this clone created (changed meanwhile): not removed", c.dest)
	if err := checkParents(c.root, c.dest); err != nil {
		return changed
	}
	root, err := os.OpenRoot(c.root)
	if err != nil {
		return err
	}
	defer root.Close()
	rel, err := filepath.Rel(c.root, c.dest)
	if err != nil || !filepath.IsLocal(rel) {
		return changed
	}
	parent := root
	if dir := filepath.Dir(rel); dir != "." {
		if parent, err = root.OpenRoot(dir); err != nil {
			return changed
		}
		defer parent.Close()
	}
	if fi, err := parent.Stat("."); err != nil || !os.SameFile(fi, c.parent) {
		return changed
	}
	name := filepath.Base(rel)
	if fi, err := parent.Lstat(name); err != nil || !fi.IsDir() || !os.SameFile(fi, c.dir) {
		return changed
	}
	// A same-user process could still swap name between the check and the
	// removal; that window is accepted (the removal stays inside parent).
	if err := parent.RemoveAll(name); err != nil {
		return err
	}
	// The parents created for it go too while they are empty (Remove
	// refuses a folder with anything in it).
	for _, d := range c.made {
		if rel, err := filepath.Rel(c.root, d); err != nil || root.Remove(rel) != nil {
			break
		}
	}
	return nil
}

// claims are the destinations claimed by clones and new repositories
// that are still running.
var claims = &claimSet{dirs: map[string]bool{}}

type claimSet struct {
	mu   sync.Mutex
	dirs map[string]bool
}

func (c *claimSet) add(dest string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for d := range c.dirs {
		if d == dest || within(dest, d) || within(d, dest) {
			return fmt.Errorf("%s overlaps %s, which is still being created: not touched (try again when it is done)", dest, d)
		}
	}
	c.dirs[dest] = true
	return nil
}

func (c *claimSet) remove(dest string) {
	c.mu.Lock()
	delete(c.dirs, dest)
	c.mu.Unlock()
}

// within reports whether path lies below dir.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// checkParents makes sure the folders between root and dest are real
// directories below root (no symlink leading elsewhere) and not inside
// another repository (the root itself included), so a server cannot
// steer a clone into, say, a symlink planted by an earlier clone.
func checkParents(root, dest string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, filepath.Dir(dest))
	if err != nil || !filepath.IsLocal(rel) && rel != "." {
		return fmt.Errorf("%s is outside %s", dest, root)
	}
	dir := root
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return fmt.Errorf("%s is a repository: not cloning inside it", dir)
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			break
		}
		dir = filepath.Join(dir, part)
		fi, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			break // the rest is created by MkdirAll, below this real directory
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink: not cloning through it", dir)
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return fmt.Errorf("%s is a repository: not cloning inside it", dir)
		}
	}
	real, err := filepath.EvalSymlinks(filepath.Dir(dest))
	if err == nil && real != realRoot && !within(real, realRoot) { // a prefix test would fail for the root "/"
		return fmt.Errorf("%s leads outside %s", dest, root)
	}
	return nil
}

// destBusy reports whether a listed clone (queued, running or failed)
// is reserved for dest, so no second clone or new repository uses it.
func (m Model) destBusy(dest string) bool {
	for _, r := range m.rows {
		if !r.gone && r.job != nil && r.st.Path == dest {
			return true
		}
	}
	return false
}

// permanentCloneError reports failures that retrying cannot fix.
func permanentCloneError(out string) bool {
	out = strings.ToLower(out)
	for _, s := range []string{"not found", "could not be found", "does not exist", "permission denied",
		"authentication failed", "access denied", "already exists", "not a valid repository name"} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

func (m *Model) handleCloneJobDone(msg cloneJobDoneMsg) tea.Cmd {
	idx := -1
	for i, r := range m.rows {
		if r.job == msg.job {
			idx = i
		}
	}
	if idx < 0 {
		return nil
	}
	r, j := m.rows[idx], msg.job
	v := j.view()
	r.setLast("clone", msg.out, msg.err)
	var cmd tea.Cmd
	switch {
	case errors.Is(msg.err, errCancelled):
		r.gone = true
		m.rebuildView()
		m.flash = "clone of " + v.label + " cancelled"
	case msg.err != nil:
		last := lastLine(msg.out, msg.err)
		j.set(func(j *cloneJob) { j.state, j.lastErr = jobFailed, last })
		m.flash = fmt.Sprintf("clone of %s failed after %d attempt(s): %s", v.label, v.attempt, last)
	default:
		r.job = nil
		r.busy = "status"
		m.flash = "cloned " + v.label + " into " + r.rel
		cmd = m.loadStatus(idx)
	}
	if v.group != "" {
		m.groupJobDone(v, msg.err)
	}
	return cmd
}

// cancelOrDismiss handles x: cancel running clones, remove failed ones.
func (m *Model) cancelOrDismiss() {
	n := 0
	for _, i := range m.jobTargets() {
		r := m.rows[i]
		switch {
		case r.job.active():
			r.job.set(func(j *cloneJob) { j.state = jobStopping })
			r.job.cancel()
			n++
		case r.job.view().state == jobFailed:
			r.gone = true
			n++
		}
	}
	if n == 0 {
		m.flash = "x cancels a running clone or removes a failed one"
		return
	}
	m.rebuildView()
}

// retryFailed restarts failed clones among the targets.
func (m *Model) retryFailed() tea.Cmd {
	var cmds []tea.Cmd
	for _, i := range m.jobTargets() {
		if j := m.rows[i].job; j.view().state == jobFailed {
			j.set(func(j *cloneJob) { j.group = "" }) // a running group clone already counted it as failed
			m.rows[i].lastErr = nil
			cmds = append(cmds, m.runJob(j))
		}
	}
	return tea.Batch(cmds...)
}

// jobTargets are the selected rows (or the current one) with a clone job.
func (m Model) jobTargets() []int {
	var t []int
	for i, r := range m.rows {
		if r.selected && r.job != nil && !r.gone {
			t = append(t, i)
		}
	}
	if len(t) == 0 {
		if i := m.currentIdx(); i >= 0 && m.rows[i].job != nil {
			t = []int{i}
		}
	}
	return t
}

func (m Model) activeJobs() int {
	n := 0
	for _, r := range m.rows {
		if r.job != nil && !r.gone && r.job.active() {
			n++
		}
	}
	return n
}

// --- view ---

func progressBar(pct, width int) string {
	pct = max(0, min(pct, 100))
	full := pct * width / 100
	return styleGood.Render(strings.Repeat("█", full)) + styleDim.Render(strings.Repeat("░", width-full))
}

// renderJob renders the status columns of a cloning row.
func (m Model) renderJob(j *cloneJob, width int) string {
	v := j.view()
	barW := max(10, min(30, width/3))
	switch v.state {
	case jobQueued:
		return styleDim.Render(progressBar(0, barW) + "  waiting for a free slot")
	case jobRunning:
		s := progressBar(v.pct, barW) + fmt.Sprintf(" %3d%% %s", v.pct, v.phase)
		if v.attempt > 1 {
			s += styleWarn.Render(fmt.Sprintf("  attempt %d/%d", v.attempt, cloneAttempts))
		}
		return s
	case jobWaiting:
		return styleWarn.Render(fmt.Sprintf("%s retrying (attempt %d/%d failed: %s)", m.spin.View(), v.attempt, cloneAttempts, v.lastErr))
	case jobStopping:
		return styleDim.Render(m.spin.View() + " cancelling…")
	}
	return styleBad.Render("✗ clone failed: "+v.lastErr) + styleDim.Render("  · r retry · x remove")
}

// cloneSummary is the title's clone progress: "cloning acme/tools
// ▕██░░▏ 4/9 · 1 failed".
func (m Model) cloneSummary() string {
	var active, failed, sum int
	for _, r := range m.rows {
		if r.gone || r.job == nil {
			continue
		}
		v := r.job.view()
		switch v.state {
		case jobFailed:
			failed++
		case jobQueued, jobRunning, jobWaiting:
			active++
			sum += v.pct
		}
	}
	if g := m.groupRun; g != nil {
		done := g.total - g.left
		pct := (100*done + sum) / max(g.total, 1)
		s := fmt.Sprintf("%s cloning %s %s %d/%d", m.spin.View(), g.name, progressBar(pct, 12), done, g.total)
		if failed > 0 {
			s += fmt.Sprintf(" · %d failed", failed)
		}
		return s
	}
	switch {
	case active > 0 && failed > 0:
		return fmt.Sprintf("%s cloning %d · %d failed", m.spin.View(), active, failed)
	case active > 0:
		return fmt.Sprintf("%s cloning %d", m.spin.View(), active)
	case failed > 0:
		return fmt.Sprintf("%d clone(s) failed", failed)
	}
	return ""
}

// relDepth is the -depth needed to list a repository at rel.
func relDepth(rel string) int { return strings.Count(rel, string(filepath.Separator)) + 1 }

// newDest validates a destination typed for a clone or new repository:
// inside root, not existing, and not reserved by a listed clone.
func (m Model) newDest(input string) (string, error) {
	dest, err := gitx.CloneDest(m.opts.Root, input)
	if err == nil && m.destBusy(dest) {
		err = fmt.Errorf("a clone into %s is already listed", strings.TrimSpace(input))
	}
	return dest, err
}

// quit stops all background work (clones, git actions, AI steps and
// requests, create/publish; git gets SIGTERM and cleans up), waits up to
// 5 s for it to finish, then exits.
func (m *Model) quit() tea.Cmd {
	m.life.cancel()
	if m.plan != nil && m.plan.cancel != nil {
		m.plan.cancel()
	}
	wg := &m.life.wg
	return tea.Sequence(func() tea.Msg {
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return nil
	}, tea.Quit)
}
