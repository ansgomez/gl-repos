// Package tui implements the interactive repository dashboard.
package tui

import (
	"context"
	"errors"
	"fmt"
	"gl-repos/internal/safe"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"gl-repos/internal/ai"
	"gl-repos/internal/config"
	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

const (
	statusTimeout = 30 * time.Second
	actionTimeout = 5 * time.Minute
	aiTimeout     = 5 * time.Minute
	graphLimit    = 500
	detailHeight  = 9
)

// Options configures the dashboard.
type Options struct {
	Root         string
	Depth        int
	Repos        []string
	FetchOnStart bool
	Jobs         int    // max concurrent git processes
	Sort         string // name | attention | recent
	Tool         string // external git UI, e.g. "lazygit"
	Agents       []config.Agent
	AI           ai.Client
	MaxDiff      int // diff bytes sent to the model
	Profiles     []Profile
	Profile      int // index of the profile active at start
}

// Profile is a forge account (GitLab via glab, GitHub via gh) used to
// clone, create and publish; one is active at a time.
type Profile struct {
	Name  string
	Color string // badge color, "" for a default
	Forge forge.Forge
	// DefaultGroup is the owner prefilled for new projects; "" means the
	// logged-in user.
	DefaultGroup string
	// ExcludeKey names the config setting of the forge's excluded groups
	// in messages ("[clone] exclude" or "exclude in [[profile]] \"work\"").
	ExcludeKey string
}

type mode int

const (
	modeList mode = iota
	modeGraph
	modePrompt
	modePlan
	modeEditMsg
	modeCloneQuery
	modeClonePick
	modeCloneDest
	modeAgent
	modeProfile
	modeCreate
	modeCloneGroup
)

var sortModes = []string{"name", "attention", "recent"}

type row struct {
	rel      string
	st       gitx.Status
	loaded   bool
	busy     string // name of the running operation, "" when idle
	selected bool
	lastAct  string
	lastOut  string
	lastErr  error
	job      *cloneJob // being cloned (or failed to); not a repository yet
	gone     bool      // removed from the list (cancelled or dismissed clone)
}

// setLast records a finished action for the details pane, keeping of
// its output only what the pane can show (keepOut).
func (r *row) setLast(act, out string, err error) {
	r.lastAct, r.lastOut, r.lastErr = act, keepOut(out), err
}

type statusMsg struct {
	idx int
	st  gitx.Status
}

type actionMsg struct {
	idx  int
	name string
	out  string
	err  error
}

type graphMsg struct {
	idx int
	out string
	err error
}

type execDoneMsg struct {
	idx  int
	name string
	err  error
}

// pending is an action waiting for y: a git action on targets, or a
// program (exec) to open in targets[0].
type pending struct {
	act     gitx.Action
	targets []int
	exec    []string
	risky   []string         // targets whose own git config runs programs
	ack     map[int][]string // each target's risky keys as shown: what y covers
}

// Model is the Bubble Tea model for the dashboard.
type Model struct {
	opts     Options
	rows     []*row
	view     []int // indices into rows currently shown, in display order
	cursor   int   // index into view
	offset   int
	width    int
	height   int
	mode     mode
	sortIdx  int
	attnOnly bool
	detail   bool
	confirm  *pending
	flash    string
	spin     spinner.Model
	sem      chan struct{}
	// clone jobs have their own slots
	cloneSlots chan struct{}
	// life is cancelled on quit; work that changes repositories runs
	// under it and is counted, so quitting can wait for git to stop
	life *lifetime

	graph    viewport.Model
	graphIdx int

	input   lineInput
	plan    *planState
	planSeq int
	// groupPlanSeq numbers group clone plans (replanGroup), so a late one
	// is dropped
	groupPlanSeq int
	// checkSeq numbers owner lookups (checkGroup) across create dialogs,
	// so a late one from a closed dialog is dropped; publishSeq does the
	// same for the repository reads of u
	checkSeq   int
	publishSeq int

	clone       *cloneState
	cloneSeq    int
	projLoading bool // project list (host or projects) is being fetched

	agentSel    int      // highlighted agent in modeAgent
	agentRow    int      // row the picker was opened on (an index into rows: the list may re-sort meanwhile)
	quitConfirm bool     // asked whether to quit with clones running
	agentNote   []string // per agent: "not installed", … (set when the picker opens)

	profiles []profileState
	prof     int // active profile
	profSel  int // switch target in modeProfile

	create   *createState
	groupRun *groupRun // group clone in progress
}

type profileState struct {
	Profile
	me     string // logged-in user, once looked up
	whoami bool   // the user name is being looked up
}

// lookPath finds a program in PATH; a variable for tests.
var lookPath = exec.LookPath

// New builds a model from opts.
func New(opts Options) Model {
	if opts.Jobs < 1 {
		opts.Jobs = 1
	}
	rows := make([]*row, len(opts.Repos))
	for i, p := range opts.Repos {
		rel, err := filepath.Rel(opts.Root, p)
		if err != nil {
			rel = p
		}
		rows[i] = &row{rel: safe.Line(rel), st: gitx.Status{Path: p}}
	}
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	in := lineInput{Prompt: "ask ▸ ", CharLimit: 500}

	m := Model{
		opts:       opts,
		rows:       rows,
		spin:       sp,
		sem:        make(chan struct{}, max(opts.Jobs, 1)), // 0 would block every git call
		cloneSlots: make(chan struct{}, max(opts.Jobs, 1)),
		life:       newLifetime(),
		graph:      viewport.New(0, 0),
		input:      in,
	}
	for i, s := range sortModes {
		if s == opts.Sort {
			m.sortIdx = i
		}
	}
	for _, p := range opts.Profiles {
		m.profiles = append(m.profiles, profileState{Profile: p})
	}
	if opts.Profile >= 0 && opts.Profile < len(m.profiles) {
		m.prof = opts.Profile
	}
	m.rebuildView()
	return m
}

// Init starts the spinner and the first status read (or fetch) of every
// repository.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spin.Tick}
	for i := range m.rows {
		if m.opts.FetchOnStart {
			cmds = append(cmds, m.startFetchOnStart(i))
		} else {
			cmds = append(cmds, m.startRefresh(i))
		}
	}
	return tea.Batch(cmds...)
}

// Update applies a message: results of background work, or a key press
// dispatched to the handler of the current mode. Every result is
// scrubbed of URL credentials first (see scrub).
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := scrub(msg).(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.graph.Width, m.graph.Height = msg.Width, max(msg.Height-2, 1)
		m.clamp()
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case statusMsg:
		r := m.rows[msg.idx]
		r.st = msg.st
		r.loaded = true
		if r.busy == "status" { // never end another job's busy state
			r.busy = ""
		}
		m.rebuildView()
		return m, nil
	case actionMsg:
		r := m.rows[msg.idx]
		if errors.Is(msg.err, errRiskyAtStart) { // nothing ran: only read the status, as without -fetch
			r.busy = "status"
			return m, m.loadStatus(msg.idx)
		}
		r.setLast(msg.name, msg.out, msg.err)
		if msg.err != nil {
			m.flash = fmt.Sprintf("%s failed in %s (enter for details)", msg.name, r.rel)
		}
		r.busy = "status"
		return m, m.loadStatus(msg.idx)
	case execDoneMsg:
		if msg.err != nil {
			m.flash = fmt.Sprintf("%s: %v", msg.name, msg.err)
		}
		return m, m.startRefresh(msg.idx)
	case graphMsg:
		if m.mode == modeGraph && msg.idx == m.graphIdx {
			if msg.err != nil {
				m.graph.SetContent(styleBad.Render(msg.err.Error()))
			} else {
				m.graph.SetContent(msg.out)
			}
		}
		return m, nil
	case planMsg:
		return m, m.handlePlanMsg(msg)
	case planValidMsg:
		m.handlePlanValid(msg)
		return m, nil
	case cloneSearchMsg:
		return m, m.handleCloneSearchMsg(msg)
	case groupProjectsMsg:
		return m, m.handleGroupProjects(msg)
	case groupPlanMsg:
		m.handleGroupPlan(msg)
		return m, nil
	case projectsMsg:
		return m, m.handleProjectsMsg(msg)
	case whoamiMsg:
		return m, m.handleWhoami(msg)
	case groupCheckMsg:
		m.handleGroupCheck(msg)
		return m, nil
	case createGroupsMsg:
		return m, m.handleCreateGroups(msg)
	case createDoneMsg:
		return m, m.handleCreateDone(msg)
	case publishInfoMsg:
		m.handlePublishInfo(msg)
		return m, nil
	case cloneJobDoneMsg:
		return m, m.handleCloneJobDone(msg)
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, m.quit()
		}
		switch m.mode {
		case modeGraph:
			return m.handleGraphKey(msg)
		case modePrompt:
			return m.handlePromptKey(msg)
		case modePlan:
			return m.handlePlanKey(msg)
		case modeEditMsg:
			return m.handleEditKey(msg)
		case modeCloneQuery:
			return m.handleCloneQueryKey(msg)
		case modeClonePick:
			return m.handleClonePickKey(msg)
		case modeAgent:
			return m.handleAgentKey(msg)
		case modeProfile:
			return m.handleProfileKey(msg)
		case modeCreate:
			return m.handleCreateKey(msg)
		case modeCloneGroup:
			return m.handleGroupKey(msg)
		case modeCloneDest:
			return m.handleCloneDestKey(msg)
		}
		return m.handleListKey(msg)
	}
	return m, nil
}

func (m Model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.quitConfirm {
		m.quitConfirm = false
		if lowerKey(msg) == "y" {
			return m, m.quit()
		}
		m.flash = "clones keep running"
		return m, nil
	}
	if m.confirm != nil {
		p := m.confirm
		m.confirm = nil
		if lowerKey(msg) == "y" {
			if p.exec != nil {
				return m, m.runExec(p.targets[0], p.exec, p.ack[p.targets[0]])
			}
			return m, m.startMany(p.act, p.targets, p.ack)
		}
		m.flash = "cancelled"
		return m, nil
	}
	m.flash = ""

	key := lowerKey(msg)
	if r := m.current(); r != nil && r.job != nil && repoOnlyKey(key) {
		if r.job.active() {
			m.flash = r.rel + " is still being cloned (x cancels)"
		} else {
			m.flash = r.rel + " failed to clone: r retries, x removes"
		}
		return m, nil
	}

	var cmd tea.Cmd
	switch key {
	case "q", "esc":
		if n := m.runningWork(); n > 0 {
			m.quitConfirm = true
			m.flash = fmt.Sprintf("%d operation(s) still running (clone, fetch, pull, push, AI steps…): quit and stop them? [y/N]", n)
			return m, nil
		}
		return m, m.quit() // nothing counted, but still waits for whatever is under life
	case "x":
		m.cancelOrDismiss()
	case "up", "k":
		m.cursor--
	case "down", "j":
		m.cursor++
	case "pgup", "ctrl+u":
		m.cursor -= m.listHeight()
	case "pgdown", "ctrl+d":
		m.cursor += m.listHeight()
	case "home", "g":
		m.cursor = 0
	case "end":
		m.cursor = len(m.view) - 1
	case " ":
		if r := m.current(); r != nil {
			r.selected = !r.selected
			m.cursor++
		}
	case "a":
		m.toggleAll()
	case "enter", "tab":
		m.detail = !m.detail
	case "d":
		m.attnOnly = !m.attnOnly
		m.rebuildView()
	case "s":
		m.sortIdx = (m.sortIdx + 1) % len(sortModes)
		m.flash = "sort: " + sortModes[m.sortIdx]
		m.rebuildView()
	case "r":
		cmd = tea.Batch(m.refreshMany(m.targets()), m.retryFailed())
	case "f", "p":
		act := map[string]gitx.Action{"f": gitx.Fetch, "p": gitx.Pull}[key]
		t := m.targets()
		if risky := m.riskyRows(t); len(risky) > 0 {
			m.confirm = &pending{act: act, targets: t, risky: risky, ack: m.ackRisks(t)}
			break
		}
		cmd = m.startMany(act, t, nil)
	case "h":
		if t := m.targets(); len(t) > 0 {
			m.confirm = &pending{act: gitx.Push, targets: t, risky: m.riskyRows(t), ack: m.ackRisks(t)}
		}
	case "l":
		cmd = m.openGraph()
	case "o":
		cmd = m.execInRepo(m.currentIdx(), m.opts.Tool)
	case "!":
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		cmd = m.execArgv(m.currentIdx(), []string{sh}) // a path, never split
	case "t":
		cmd = m.openAgents()
	case "w":
		m.openProfileSwitch()
	case "n":
		cmd = m.openCreate(false)
	case "u":
		cmd = m.openCreate(true)
	case "c":
		cmd = m.openClone("")
	case "i":
		if m.current() != nil {
			m.mode = modePrompt
			m.input.Reset()
			m.input.Placeholder = ai.DefaultPrompt
		}
	}
	m.clamp()
	return m, cmd
}

func (m Model) handleGraphKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch lowerKey(msg) {
	case "q", "esc", "l":
		m.mode = modeList
		return m, nil
	}
	var cmd tea.Cmd
	m.graph, cmd = m.graph.Update(msg)
	return m, cmd
}

// --- selection and layout ---

// targets returns the selected rows, or the row under the cursor if none
// are selected.
func (m Model) targets() []int {
	var t []int
	for i, r := range m.rows {
		if r.selected && r.job == nil && !r.gone {
			t = append(t, i)
		}
	}
	if len(t) == 0 && len(m.view) > 0 && m.rows[m.view[m.cursor]].job == nil {
		t = []int{m.view[m.cursor]}
	}
	return t
}

func (m Model) currentIdx() int {
	if len(m.view) == 0 {
		return -1
	}
	return m.view[m.cursor]
}

func (m Model) current() *row {
	if i := m.currentIdx(); i >= 0 {
		return m.rows[i]
	}
	return nil
}

// toggleAll selects every visible row, or clears the selection if all
// visible rows are already selected.
func (m *Model) toggleAll() {
	allSel := true
	for _, i := range m.view {
		if !m.rows[i].selected {
			allSel = false
			break
		}
	}
	for _, i := range m.view {
		m.rows[i].selected = !allSel
	}
}

func (m *Model) rebuildView() {
	keep := m.currentIdx()
	m.view = m.view[:0]
	for i, r := range m.rows {
		if r.gone {
			continue
		}
		if m.attnOnly && r.loaded && !r.st.NeedsAttention() {
			continue
		}
		m.view = append(m.view, i)
	}

	sort.SliceStable(m.view, func(a, b int) bool { return m.rows[m.view[a]].rel < m.rows[m.view[b]].rel })
	switch sortModes[m.sortIdx] {
	case "attention":
		rank := func(r *row) int {
			switch {
			case !r.loaded:
				return 2
			case r.st.NeedsAttention():
				return 0
			}
			return 1
		}
		sort.SliceStable(m.view, func(a, b int) bool {
			return rank(m.rows[m.view[a]]) < rank(m.rows[m.view[b]])
		})
	case "recent":
		sort.SliceStable(m.view, func(a, b int) bool {
			return m.rows[m.view[a]].st.LastCommit.After(m.rows[m.view[b]].st.LastCommit)
		})
	}

	for vi, i := range m.view {
		if i == keep {
			m.cursor = vi
			break
		}
	}
	m.clamp()
}

func (m *Model) clamp() {
	if m.cursor >= len(m.view) {
		m.cursor = len(m.view) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m Model) listHeight() int {
	h := m.height - 2 - len(m.flashLines()) - len(m.helpLines()) // title, column header, flash/input line(s), help
	if m.detail {
		h -= detailHeight
	}
	return max(h, 1)
}

// --- commands ---

func (m Model) loadStatus(i int) tea.Cmd {
	path, sem := m.rows[i].st.Path, m.sem
	return func() tea.Msg {
		sem <- struct{}{}
		defer func() { <-sem }()
		ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
		defer cancel()
		return statusMsg{idx: i, st: gitx.ReadStatus(ctx, path)}
	}
}

func (m Model) startRefresh(i int) tea.Cmd {
	r := m.rows[i]
	if r.busy != "" {
		return nil
	}
	r.busy = "status"
	return m.loadStatus(i)
}

func (m Model) refreshMany(idx []int) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(idx))
	for _, i := range idx {
		cmds = append(cmds, m.startRefresh(i))
	}
	return tea.Batch(cmds...)
}

// startJob marks row i busy with name and runs fn under the concurrency
// limit, reporting the result as an actionMsg.
func (m Model) startJob(i int, name string, timeout time.Duration, fn func(ctx context.Context, st gitx.Status) (string, error)) tea.Cmd {
	r := m.rows[i]
	if r.busy != "" {
		return nil
	}
	r.busy = name
	st, sem, life := r.st, m.sem, m.life
	life.wg.Add(1)
	return func() tea.Msg {
		defer life.wg.Done()
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-life.ctx.Done():
			return actionMsg{idx: i, name: name, err: errCancelled}
		}
		ctx, cancel := context.WithTimeout(life.ctx, timeout)
		defer cancel()
		out, err := fn(ctx, st)
		return actionMsg{idx: i, name: name, out: out, err: err}
	}
}

// startAction runs act on row i. ack is the row's risky keys the user
// said y to, as shown (nil: nothing confirmed); a repository whose
// config runs other programs is left alone.
func (m Model) startAction(i int, act gitx.Action, ack []string) tea.Cmd {
	return m.startJob(i, act.String(), actionTimeout, func(ctx context.Context, st gitx.Status) (string, error) {
		if err := checkRisks(ctx, st.Path, ack); err != nil {
			return "", err
		}
		// A tracked branch with no local commits has nothing to push; git
		// would only report a confusing rejection if it is also behind.
		// The listed status may be stale (a commit made in a shell), so
		// it is read again first.
		if act == gitx.Push && st.Upstream != "" && st.Ahead == 0 && st.Err == nil {
			if cur := gitx.ReadStatus(ctx, st.Path); cur.Err == nil && cur.Ahead == 0 {
				return "nothing to push", nil
			}
		}
		return gitx.Run(ctx, st, act)
	})
}

// errRiskyAtStart is the result of a startup fetch that left a repository
// alone because its own config runs programs (see startFetchOnStart).
var errRiskyAtStart = errors.New("own git config runs programs: not fetched at start")

// startFetchOnStart fetches row i at startup (-fetch). Nothing has been
// confirmed yet, so a repository whose own config runs programs is left
// alone, with no failed fetch on the row: Update reads its status as
// without -fetch, it is marked !, and f asks there.
func (m Model) startFetchOnStart(i int) tea.Cmd {
	return m.startJob(i, gitx.Fetch.String(), actionTimeout, func(ctx context.Context, st gitx.Status) (string, error) {
		if err := checkRisks(ctx, st.Path, nil); err != nil {
			return "", errRiskyAtStart
		}
		return gitx.Run(ctx, st, gitx.Fetch)
	})
}

// startMany runs act on rows idx; ack holds the confirmed keys per row
// (pending.ack), nil when nothing was confirmed.
func (m Model) startMany(act gitx.Action, idx []int, ack map[int][]string) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(idx))
	for _, i := range idx {
		cmds = append(cmds, m.startAction(i, act, ack[i]))
	}
	return tea.Batch(cmds...)
}

func (m *Model) openGraph() tea.Cmd {
	i := m.currentIdx()
	if i < 0 {
		return nil
	}
	m.mode = modeGraph
	m.graphIdx = i
	m.graph.SetContent(styleDim.Render("loading…"))
	m.graph.GotoTop()
	path, sem := m.rows[i].st.Path, m.sem
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
		defer cancel()
		// git log takes a slot (-jobs) like a status read: l on row after
		// row must not start more git processes than asked for
		var out string
		var err error
		if werr := withSlot(ctx, sem, func() { out, err = gitx.Graph(ctx, path, graphLimit) }); werr != nil {
			err = werr
		}
		return graphMsg{idx: i, out: out, err: err}
	}
}

// execInRepo suspends the TUI and runs a configured command (program
// and arguments, quotes allowed) in row i's repository, refreshing it
// afterwards.
func (m *Model) execInRepo(i int, command string) tea.Cmd {
	fields, err := SplitCommand(command)
	if err != nil {
		m.flash = fmt.Sprintf("bad command %q: %v", command, err)
		return nil
	}
	return m.execArgv(i, fields)
}

func (m *Model) execArgv(i int, fields []string) tea.Cmd {
	if i < 0 || len(fields) == 0 {
		return nil
	}
	if !filepath.IsAbs(fields[0]) && strings.ContainsRune(fields[0], '/') {
		// It would run from the repository's folder: its own file.
		m.flash = fmt.Sprintf("%s: give a command name or an absolute path", fields[0])
		return nil
	}
	if _, err := exec.LookPath(fields[0]); err != nil {
		m.flash = fmt.Sprintf("%s not found in PATH", fields[0])
		return nil
	}
	// git run by the program would run what the repository's own config
	// names: ask first.
	switch r := m.rows[i]; {
	case r.gone:
		m.flash = r.rel + " is no longer listed"
		return nil
	case !r.loaded:
		m.flash = "still reading " + r.rel + ", try again"
		return nil
	case len(r.st.Risky) > 0:
		m.confirm = &pending{targets: []int{i}, exec: fields, risky: []string{r.rel}, ack: m.ackRisks([]int{i})}
		return nil
	}
	return m.runExec(i, fields, nil)
}

// runExec suspends the TUI and runs fields in row i's repository. ack is
// the row's risky keys the user said y to, as shown (nil: nothing
// confirmed). Like a git action, it reads the config again right before
// the program starts (in the command, not on the UI goroutine): a key
// that appeared since the status was read, or since the confirmation,
// stops it with the usual "not run" message instead of a program that
// would run it.
func (m *Model) runExec(i int, fields []string, ack []string) tea.Cmd {
	path := m.rows[i].st.Path
	c := repoCommand(path, fields)
	start := tea.ExecProcess(c, func(err error) tea.Msg {
		return execDoneMsg{idx: i, name: fields[0], err: err}
	})
	return func() tea.Msg {
		// two quick local git reads: no -jobs slot, so the tool opens
		// while fetches run
		ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
		defer cancel()
		if err := checkRisks(ctx, path, ack); err != nil {
			return execDoneMsg{idx: i, name: fields[0], err: err}
		}
		return start()
	}
}

// repoCommand builds the tool, agent or shell command fields for the
// repository at path: run there, without the variables that would pin
// the git it runs to another repository (gitx.Env, as for git itself).
func repoCommand(path string, fields []string) *exec.Cmd {
	c := exec.Command(fields[0], fields[1:]...)
	c.Dir = path
	c.Env = gitx.Env()
	return c
}

// lowerKey returns the key with single letters lowercased: shortcuts never
// depend on case (shift or caps lock does the same as the plain key).
func lowerKey(msg tea.KeyMsg) string {
	k := msg.String()
	if len([]rune(k)) == 1 {
		return strings.ToLower(k)
	}
	return k
}

// SplitCommand splits a configured command line into words like a shell
// would for quoting only: '…' and "…" group words, \ escapes outside
// single quotes. Nothing is expanded or interpreted.
func SplitCommand(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord, quote := false, rune(0)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\\' && i+1 < len(runes):
			i++
			cur.WriteRune(runes[i])
			inWord = true
		case quote == '"':
			if r == '"' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c quote", quote)
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// repoOnlyKey reports keys that need a cloned repository (not a row that
// is still cloning or failed to).
func repoOnlyKey(key string) bool {
	switch key {
	case "o", "!", "t", "i", "u", "l":
		return true
	}
	return false
}

// lifetime is the context of all background work that changes
// repositories; cancelling it (on quit) stops git with SIGTERM.
type lifetime struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newLifetime() *lifetime {
	ctx, cancel := context.WithCancel(context.Background())
	return &lifetime{ctx: ctx, cancel: cancel}
}

// runningWork counts operations that quitting would interrupt: clones
// (a cancelled one too, while its git still runs and removes its
// folder), git actions and AI steps on rows, and a running
// create/publish.
func (m Model) runningWork() int {
	n := m.activeJobs()
	for _, r := range m.rows {
		if !r.gone && r.job != nil && r.job.view().state == jobStopping {
			n++
		}
		if !r.gone && r.busy != "" && r.busy != "status" {
			n++
		}
	}
	if m.create != nil && m.create.running && !m.create.publish { // a publish counts as its busy row
		n++
	}
	return n
}
