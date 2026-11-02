package tui

import (
	"context"
	"errors"
	"fmt"
	"gl-repos/internal/safe"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

const createTimeout = 5 * time.Minute

// createState tracks "n" (new repository, optionally with a project on
// the active profile's forge) and "u" (publish the selected repository):
// folder (n only) → owner/name (if a remote) → review → run.
type createState struct {
	publish bool
	prof    int // profile chosen when the box opened
	step    int

	// n: new repository
	folder string // relative to root, as typed
	dest   string // absolute
	remote bool   // also create the project on the forge

	// u: publish
	loading     bool     // reading the repository's remotes and commits
	idx         int      // row being published
	hasCommits  bool     // otherwise there is nothing to push yet
	hasUpstream bool     // keep it: push to the new remote without -u
	risky       []string // config keys of the repository that run programs

	path       string // owner/name on the forge
	edited     bool   // path typed by hand: keep it when the user name arrives
	vis        int    // index into Forge.Visibilities; 0 (private) by default
	visMenu    bool   // visibility menu open: only enter changes vis
	visSel     int    // menu cursor
	remoteName string

	// owner suggestions (GitLab groups) for the path step
	groups   []forge.Project
	gsugg    []forge.Project // groups matching what is typed
	gsel     int
	gloading bool
	groupErr error

	// subgroups of the owner that do not exist yet: created (after a
	// warning in the review) before the project
	checking bool            // looking the owner up on the forge
	sub      forge.Subgroups // the deepest existing group and the missing ones

	meErr      error
	retryEnter bool // enter waits for the user name, then runs again
	err        error
	running    bool

	// the plan as the review shows it, built once when the review is
	// entered (enterReview) and run as it is: what arrives meanwhile (the
	// user name, a profile's host, more rows deciding ssh or https) does
	// not change what y runs behind the screen the user read
	steps []createStep
}

const (
	stepFolder = iota
	stepPath
	stepReview
)

type whoamiMsg struct {
	prof     int
	host, me string
	err      error
}

type createGroupsMsg struct {
	prof   int
	groups []forge.Project
	cached bool      // read from the disk cache, fetched at
	at     time.Time // when cached
	err    error
}

// subgroupMaker is implemented by forges that can create the missing
// subgroups of a new project's owner (GitLab).
type subgroupMaker interface {
	MissingGroups(ctx context.Context, owner string) (forge.Subgroups, error)
	GroupID(ctx context.Context, path string) (int64, error)
	CreateSubgroupCmd(parent, path, visibility string) forge.Cmd
}

type groupCheckMsg struct {
	seq  int
	path string
	sub  forge.Subgroups
	err  error
}

type createDoneMsg struct {
	publish  bool
	idx      int
	dest     string
	initDone bool
	failed   string // command that failed, "" on success
	byForge  bool   // the failed command is the forge CLI's
	out      string // the failed command's output, or on success that of every step worth keeping
	err      error
}

// publishInfoMsg is what "u" needs to know about the repository before
// the form opens.
type publishInfoMsg struct {
	seq, idx                int
	remotes                 []string
	hasCommits, hasUpstream bool
	detached                bool // HEAD is not a branch: nothing to publish it as
	risky                   []string
	err                     error
}

// createStep is one command of the plan: shown as is, then run.
type createStep struct {
	dir   string // where it runs, for display
	show  string
	init  bool // creates the local repository
	forge bool // runs the forge CLI (glab, gh)
	quiet bool // its output (e.g. a JSON reply) is not worth keeping
	run   func(ctx context.Context) (string, error)
}

func (m *Model) openCreate(publish bool) tea.Cmd {
	c := &createState{publish: publish, prof: m.prof, remoteName: "origin"}
	var info tea.Cmd
	if publish {
		i := m.currentIdx()
		if i < 0 {
			return nil
		}
		if m.rows[i].busy != "" {
			m.flash = m.rows[i].rel + ": repository is busy, try again"
			return nil
		}
		c.idx, c.step, c.loading = i, stepPath, true
		dir := m.rows[i].st.Path
		m.publishSeq++
		seq := m.publishSeq
		info = func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
			defer cancel()
			names, err := gitx.Remotes(ctx, dir)
			if err != nil {
				return publishInfoMsg{seq: seq, idx: i, err: err}
			}
			return publishInfoMsg{seq: seq, idx: i, remotes: names, hasCommits: gitx.HasCommits(ctx, dir),
				hasUpstream: gitx.HasUpstream(ctx, dir), detached: !gitx.OnBranch(ctx, dir),
				risky: gitx.ConfigRisks(ctx, dir)}
		}
	}
	m.create = c
	m.mode = modeCreate
	m.input.Reset()
	if publish {
		m.prefillPath()
	} else {
		m.input.Prompt = "new repo ▸ "
		m.input.Placeholder = "folder, relative to " + showPath(m.opts.Root)
	}
	return tea.Batch(info, m.lookupUser(), m.loadCreateGroups())
}

// handlePublishInfo opens the "u" form once the repository is read, or
// closes it if the repository cannot be published.
func (m *Model) handlePublishInfo(msg publishInfoMsg) {
	c := m.create
	if c == nil || !c.publish || !c.loading || c.idx != msg.idx || msg.seq != m.publishSeq {
		return // closed meanwhile
	}
	rel := m.rows[c.idx].rel
	if msg.err != nil {
		m.closeCreate()
		m.flash = rel + ": " + msg.err.Error()
		return
	}
	if msg.detached {
		// checked before anything is created: a push of a detached HEAD
		// would fail after the project exists
		m.closeCreate()
		m.flash = rel + ": HEAD is detached (no branch): switch to a branch first (git switch -c <name>), then publish"
		return
	}
	if slices.Contains(msg.remotes, "origin") {
		c.remoteName = m.forge().Kind()
	}
	if slices.Contains(msg.remotes, c.remoteName) {
		m.closeCreate()
		m.flash = fmt.Sprintf("%s already has a remote named %s", rel, c.remoteName)
		return
	}
	c.loading, c.hasCommits, c.hasUpstream, c.risky = false, msg.hasCommits, msg.hasUpstream, msg.risky
}

// loadCreateGroups reads the owner suggestions from the cached group list
// (shared with clone); the answer refreshes it in the background when
// stale. It needs the host; without one it runs again once the login is
// known.
func (m *Model) loadCreateGroups() tea.Cmd {
	c, fg := m.create, m.forge()
	_, ok := fg.(forge.Groups)
	host := fg.Hostname()
	if c == nil || !ok || host == "" {
		return nil
	}
	prof, name := m.prof, m.profileName()
	return func() tea.Msg {
		gs, at := forge.LoadGroupCache(name, host, fg)
		return createGroupsMsg{prof: prof, groups: gs, cached: true, at: at}
	}
}

// fetchCreateGroups refreshes the group list from the forge.
func (m *Model) fetchCreateGroups() tea.Cmd {
	c, fg := m.create, m.forge()
	gf, ok := fg.(forge.Groups)
	host := fg.Hostname()
	if c == nil || !ok || host == "" || c.gloading {
		return nil
	}
	c.gloading = true
	prof, name := m.prof, m.profileName()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
		defer cancel()
		gs, err := gf.ListGroups(ctx)
		if err == nil {
			_ = forge.SaveGroupCache(name, host, gs) // the cache only speeds up the next start
		}
		return createGroupsMsg{prof: prof, groups: gs, err: err}
	}
}

func (m *Model) handleCreateGroups(msg createGroupsMsg) tea.Cmd {
	c := m.create
	if c == nil || c.prof != msg.prof {
		return nil // closed or another profile meanwhile; the cache file is updated
	}
	if msg.cached {
		if len(c.groups) == 0 {
			c.groups = forge.KeepGroups(m.forge(), msg.groups)
			m.updateGroupSuggestions()
		}
		if time.Since(msg.at) < projectsMaxAge {
			return nil
		}
		return m.fetchCreateGroups()
	}
	c.gloading = false
	if msg.err != nil {
		c.groupErr = msg.err
		return nil
	}
	c.groups, c.groupErr = forge.KeepGroups(m.forge(), msg.groups), nil
	m.updateGroupSuggestions()
	return nil
}

// ownerTyped is the owner part of the path field: everything before the
// last slash.
func (m Model) ownerTyped() string {
	v := strings.Trim(strings.TrimSpace(m.input.Value()), "/")
	if i := strings.LastIndex(v, "/"); i >= 0 {
		return v[:i]
	}
	return ""
}

// needsGroupCheck reports whether the owner of path must be looked up on
// the forge before the review: it is not the user and not a listed group.
func (m Model) needsGroupCheck(path string) bool {
	if _, ok := m.forge().(subgroupMaker); !ok {
		return false
	}
	owner := ""
	if i := strings.LastIndex(path, "/"); i >= 0 {
		owner = path[:i]
	}
	if owner == "" || strings.EqualFold(owner, m.profiles[m.prof].me) {
		return false
	}
	for _, g := range m.create.groups {
		if strings.EqualFold(g.Path, owner) {
			return false
		}
	}
	return true
}

// checkGroup looks up the owner of path and which of its subgroups are
// missing; the answer moves on to the review.
func (m *Model) checkGroup(path string) tea.Cmd {
	c := m.create
	sm := m.forge().(subgroupMaker)
	c.checking, c.err = true, nil
	m.checkSeq++
	seq, owner := m.checkSeq, path[:strings.LastIndex(path, "/")]
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
		defer cancel()
		sub, err := sm.MissingGroups(ctx, owner)
		return groupCheckMsg{seq: seq, path: path, sub: sub, err: err}
	}
}

func (m *Model) handleGroupCheck(msg groupCheckMsg) {
	c := m.create
	if c == nil || !c.checking || msg.seq != m.checkSeq {
		return // cancelled or closed meanwhile
	}
	c.checking = false
	if msg.err != nil {
		c.err = msg.err
		return
	}
	c.path, c.sub = msg.path, msg.sub
	m.enterReview()
}

// enterReview moves to the review step with the plan built now: the
// review shows c.steps and y runs c.steps, nothing rebuilt in between.
func (m *Model) enterReview() {
	m.create.step = stepReview
	m.create.steps = m.createSteps()
}

// ownerDone reports whether the owner part of the path field is a listed
// group and the text is not the start of a deeper one: nothing is left to
// complete.
func (m Model) ownerDone() bool {
	owner := m.ownerTyped()
	typed := strings.ToLower(strings.TrimSpace(m.input.Value()))
	known := false
	for _, g := range m.create.groups {
		p := strings.ToLower(g.Path)
		if strings.HasPrefix(p, typed) && len(p) > len(owner) {
			return false
		}
		known = known || p == strings.ToLower(owner)
	}
	return known
}

// updateGroupSuggestions ranks the groups for the path field, read as
// search words (slashes split words too). Nothing is suggested once the
// owner is complete (ownerDone).
func (m *Model) updateGroupSuggestions() {
	c := m.create
	var keep string
	if c.gsel < len(c.gsugg) {
		keep = c.gsugg[c.gsel].Path
	}
	c.gsugg, c.gsel = nil, 0
	if len(c.groups) == 0 || m.ownerDone() {
		return
	}
	q := strings.ReplaceAll(m.input.Value(), "/", " ")
	c.gsugg = forge.Match(c.groups, q, min(m.suggestionLimit(), 8))
	for i, g := range c.gsugg {
		if g.Path == keep {
			c.gsel = i
		}
	}
}

// completeGroup puts the highlighted group in front of the project name.
func (m *Model) completeGroup() {
	c := m.create
	if c.gsel >= len(c.gsugg) {
		return
	}
	m.input.SetValue(c.gsugg[c.gsel].Path + "/" + m.repoName())
	m.input.CursorEnd()
	c.edited = true
	m.updateGroupSuggestions()
}

// lookupUser fetches the logged-in user of the active profile (and its
// host, if not known yet) for the default owner of new projects.
func (m *Model) lookupUser() tea.Cmd {
	p := &m.profiles[m.prof]
	if p.me != "" || p.whoami {
		return nil
	}
	p.whoami = true
	prof, f, remotes := m.prof, p.Forge, m.remotes()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
		defer cancel()
		if f.Hostname() == "" {
			if _, err := lookPath(f.CLI()); err != nil {
				return whoamiMsg{prof: prof, err: fmt.Errorf("%s not found: install it", f.CLI())}
			}
			f = f.WithHost(f.DetectHost(ctx, remotes))
		}
		if f.Hostname() == "" {
			return whoamiMsg{prof: prof, err: fmt.Errorf("no %s login found: %s auth login --hostname <host>", f.Name(), f.CLI())}
		}
		me, err := f.Whoami(ctx)
		return whoamiMsg{prof: prof, host: f.Hostname(), me: me, err: err}
	}
}

func (m *Model) handleWhoami(msg whoamiMsg) tea.Cmd {
	if msg.prof < len(m.profiles) {
		p := &m.profiles[msg.prof]
		p.whoami = false
		if p.Forge.Hostname() == "" && msg.host != "" {
			p.Forge = p.Forge.WithHost(msg.host)
		}
		if msg.me != "" {
			p.me = msg.me
		}
	}
	c := m.create
	if c == nil || c.prof != msg.prof {
		return nil
	}
	c.meErr = msg.err
	if c.step == stepPath && !c.edited {
		m.prefillPath()
	}
	var retry tea.Cmd
	if c.retryEnter && c.step == stepPath && !c.checking {
		c.err = nil
		retry = m.submitPath() // enter was waiting for the user name
	}
	if len(c.groups) == 0 && !c.gloading {
		return tea.Batch(retry, m.loadCreateGroups()) // the host is known now
	}
	return retry
}

// repoName is the project name suggested for the new remote.
func (m Model) repoName() string {
	c := m.create
	if c.publish {
		return filepath.Base(m.rows[c.idx].st.Path)
	}
	return filepath.Base(filepath.Clean(c.folder))
}

func (m *Model) prefillPath() {
	m.input.Reset()
	m.input.Prompt = "as ▸ "
	m.input.Placeholder = "group/name"
	v := m.repoName()
	if g := m.profiles[m.prof].DefaultGroup; g != "" {
		v = g + "/" + v
	} else if me := m.profiles[m.prof].me; me != "" {
		v = me + "/" + v
	}
	m.input.SetValue(v)
	m.input.CursorEnd()
	if m.create != nil {
		m.updateGroupSuggestions()
	}
}

func (m *Model) closeCreate() {
	m.create = nil
	m.input.Prompt = "ask ▸ "
	m.input.Placeholder = ""
	m.mode = modeList
}

func (m Model) handleCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.create
	key := msg.String()
	if c.step == stepReview {
		key = lowerKey(msg)
	}
	if c.running {
		return m, nil // the commands finish on their own; a half-done create helps nobody
	}
	if c.loading {
		if key == "esc" {
			m.closeCreate()
		}
		return m, nil
	}
	switch c.step {
	case stepFolder:
		switch key {
		case "esc":
			m.closeCreate()
		case "tab":
			c.remote = !c.remote
		case "enter":
			dest, err := m.newDest(m.input.Value())
			if err != nil {
				c.err = err
				return m, nil
			}
			c.folder, c.dest, c.err = strings.TrimSpace(m.input.Value()), dest, nil
			if c.remote {
				c.step = stepPath
				if !c.edited {
					m.prefillPath()
				} else {
					m.input.Prompt = "as ▸ "
					m.input.SetValue(c.path)
					m.input.CursorEnd()
					m.updateGroupSuggestions()
				}
			} else {
				m.enterReview()
			}
		default:
			m.input.Update(msg)
			c.err = nil
		}

	case stepPath:
		if c.checking {
			if key == "esc" {
				c.checking = false // the answer is dropped
			}
			return m, nil
		}
		if c.visMenu {
			m.handleVisMenuKey(msg)
			return m, nil
		}
		switch key {
		case "esc":
			c.retryEnter = false
			if c.publish {
				m.closeCreate()
				return m, nil
			}
			c.path = strings.TrimSpace(m.input.Value())
			c.step = stepFolder
			m.input.Reset()
			m.input.Prompt = "new repo ▸ "
			m.input.SetValue(c.folder)
			m.input.CursorEnd()
		case "ctrl+v":
			c.visMenu, c.visSel = true, c.vis%len(m.forge().Visibilities())
		case "tab":
			m.completeGroup()
		case "up", "ctrl+p", "shift+tab":
			c.gsel = max(c.gsel-1, 0)
		case "down", "ctrl+n":
			c.gsel = min(c.gsel+1, max(len(c.gsugg)-1, 0))
		case "enter":
			return m, m.submitPath()
		default:
			before := m.input.Value()
			m.input.Update(msg)
			if m.input.Value() != before {
				c.edited = true
				c.retryEnter = false
				m.updateGroupSuggestions()
			}
			c.err = nil
		}

	case stepReview:
		switch key {
		case "esc":
			c.step = stepFolder
			if c.publish || c.remote {
				c.step = stepPath
			}
		case "y":
			if m.visibilityErr() != nil || !m.reviewFits() {
				return m, nil // shown in the review, or in place of the y hint
			}
			if c.publish {
				// the row stays busy for the whole run: no other git job
				// may start on it meanwhile
				r := m.rows[c.idx]
				if r.busy != "" {
					c.err = errors.New("repository is busy, try again")
					return m, nil
				}
				r.busy = "publish"
			}
			c.running, c.err = true, nil
			return m, m.runCreate()
		}
	}
	return m, nil
}

// submitPath checks the owner/name typed in the path step and moves on
// to the review, looking the owner up on the forge first if needed.
func (m *Model) submitPath() tea.Cmd {
	c := m.create
	c.retryEnter = false
	f := m.forge()
	p := strings.Trim(strings.TrimSpace(m.input.Value()), "/")
	if err := forge.CheckPath(p); err != nil {
		c.err = err
		return nil
	}
	if len(f.Keep([]forge.Project{{Path: p}})) == 0 {
		c.err = fmt.Errorf("%s is in a group this profile excludes (exclude in the config): pick another owner", p[:strings.LastIndex(p, "/")])
		return nil
	}
	if f.Hostname() == "" {
		c.err = fmt.Errorf("%s host not known yet (is %s logged in?)", f.Name(), f.CLI())
		return m.lookupUser()
	}
	c.sub = forge.Subgroups{}
	if m.needsGroupCheck(p) {
		if pr := m.profiles[m.prof]; pr.me == "" && pr.whoami {
			// the owner may be the user's own namespace: wait for the name
			// rather than look it up as a group
			c.err, c.retryEnter = errors.New("still reading your user name from "+f.CLI()+"… (continues by itself)"), true
			return nil
		}
		return m.checkGroup(p)
	}
	c.path, c.err = p, nil
	m.enterReview()
	return nil
}

// handleVisMenuKey moves the visibility menu cursor; only enter changes
// the visibility, esc leaves it as it was.
func (m *Model) handleVisMenuKey(msg tea.KeyMsg) {
	c := m.create
	n := len(m.forge().Visibilities())
	switch lowerKey(msg) {
	case "esc":
		c.visMenu = false
	case "up", "k", "shift+tab":
		c.visSel = (c.visSel + n - 1) % n
	case "down", "j", "tab":
		c.visSel = (c.visSel + 1) % n
	case "enter":
		c.vis, c.visMenu = c.visSel, false
	}
}

// visibility is the chosen visibility name.
func (m Model) visibility() string {
	vis := m.forge().Visibilities()
	return vis[m.create.vis%len(vis)]
}

// publicAlert is true when a project will be created that anyone can see.
func (m Model) publicAlert() bool {
	c := m.create
	return (c.publish || c.remote) && m.visibility() == "public"
}

// visDescription explains a visibility in the menu.
func visDescription(v string) string {
	switch v {
	case "private":
		return "only you and members"
	case "internal":
		return "any signed-in user on this instance"
	case "public":
		return "anyone on the internet"
	}
	return ""
}

// visLabel renders a visibility name: public in bold red capitals,
// internal in the warning colour, private plain.
func visLabel(v string) string {
	switch v {
	case "public":
		return styleAlert.Render("PUBLIC")
	case "internal":
		return styleWarn.Render(v)
	}
	return v
}

// newGroupsWarning says which groups the review will create and where,
// and why their visibility is narrower than the project's if it is.
func newGroupsWarning(sub forge.Subgroups, vis string) string {
	gvis := sub.SubgroupVisibility(vis)
	var s string
	if len(sub.Missing) == 1 {
		s = fmt.Sprintf("⚠ group %s does not exist: it will be created as a new %s subgroup of %s", sub.Missing[0], gvis, sub.Parent)
	} else {
		s = fmt.Sprintf("⚠ groups %s do not exist: they will be created as new %s subgroups below %s", strings.Join(sub.Missing, ", "), gvis, sub.Parent)
	}
	if gvis != vis {
		pvis := sub.ParentVis
		if pvis == "" {
			pvis = "of unknown visibility"
		}
		s += fmt.Sprintf(" (%s is %s, and a subgroup can't be more visible than its parent)", sub.Parent, pvis)
	}
	return s
}

// visibilityErr refuses a project more visible than the new subgroup it
// would go into: GitLab rejects it, after the subgroups were created.
func (m Model) visibilityErr() error {
	c, vis := m.create, m.visibility()
	if len(c.sub.Missing) == 0 || c.sub.SubgroupVisibility(vis) == vis {
		return nil
	}
	return fmt.Errorf("a %s project can't be in a %s group: choose %s or narrower with ctrl+v (esc back)",
		vis, c.sub.SubgroupVisibility(vis), c.sub.SubgroupVisibility(vis))
}

const publicWarning = "⚠ public: anyone on the internet can see this repository"

// createSteps builds the plan from the model as it is now; the review
// keeps the one built when it was entered (enterReview, createState.steps).
func (m Model) createSteps() []createStep {
	c := m.create
	f := m.forge()
	var steps []createStep
	dir := ""
	if c.publish {
		dir = m.rows[c.idx].st.Path
	} else {
		root, rel := m.opts.Root, filepath.Clean(c.folder)
		dest := c.dest
		steps = append(steps, createStep{dir: root, show: gitx.CommandLine(gitx.InitArgs(rel)), init: true,
			run: func(ctx context.Context) (string, error) {
				// the same checks as a clone: a new folder of our own, not
				// through a symlink or inside another repository
				c, err := claimDir(root, dest)
				if err != nil {
					return "", err
				}
				defer c.release()
				out, err := gitx.Init(ctx, root, dest)
				if err != nil {
					// created empty by claimDir above: only the failed attempt
					// is in it, unless something replaced it meanwhile
					if rerr := c.remove(); rerr != nil {
						out = strings.TrimSpace(out + "\n" + rerr.Error())
					}
				}
				return out, err
			}})
		dir = c.dest
		if !c.remote {
			return steps
		}
	}
	if sm, ok := f.(subgroupMaker); ok {
		vis := c.sub.SubgroupVisibility(m.visibility())
		for i, g := range c.sub.Missing {
			parentPath, parent := c.sub.Parent, strconv.FormatInt(c.sub.ParentID, 10)
			if i > 0 {
				parentPath = c.sub.Missing[i-1]
				parent = "<id of " + parentPath + ">" // created by the step before
			}
			steps = append(steps, createStep{dir: "", show: sm.CreateSubgroupCmd(parent, g, vis).String(), forge: true, quiet: true,
				run: func(ctx context.Context) (string, error) {
					id := parent
					if i > 0 {
						n, err := sm.GroupID(ctx, parentPath)
						if err != nil {
							return "", fmt.Errorf("new group %s: %w", parentPath, err)
						}
						id = strconv.FormatInt(n, 10)
					}
					return sm.CreateSubgroupCmd(id, g, vis).Run(ctx)
				}})
		}
	}
	create := f.CreateCmd(c.path, m.visibility(), m.profiles[m.prof].me)
	steps = append(steps, createStep{dir: "", show: create.String(), forge: true, run: create.Run})
	url := f.RemoteURL(c.path, m.useSSH(f.Hostname()))
	name := c.remoteName
	steps = append(steps, createStep{dir: dir, show: gitx.CommandLine(gitx.AddRemoteArgs(name, url)),
		run: func(ctx context.Context) (string, error) { return gitx.AddRemote(ctx, dir, name, url) }})
	if c.publish && c.hasCommits {
		track, ack := !c.hasUpstream, c.risky
		steps = append(steps, createStep{dir: dir, show: gitx.CommandLine(gitx.PushToArgs(name, track)),
			run: func(ctx context.Context) (string, error) {
				if err := checkRisks(ctx, dir, ack); err != nil { // the review showed these
					return "", err
				}
				return gitx.PushTo(ctx, dir, name, track)
			}})
	}
	return steps
}

// runCreate runs the plan the review showed (createState.steps).
func (m *Model) runCreate() tea.Cmd {
	c := m.create
	steps := c.steps
	msg := createDoneMsg{publish: c.publish, idx: c.idx, dest: c.dest}
	life, sem := m.life, m.sem
	life.wg.Add(1)
	return func() tea.Msg {
		defer life.wg.Done()
		ctx, cancel := context.WithTimeout(life.ctx, createTimeout)
		defer cancel()
		return runCreateSteps(ctx, sem, steps, msg)
	}
}

// runCreateSteps runs steps in order until one fails and fills in msg.
// git steps take a slot of sem (-jobs) like every other git operation.
func runCreateSteps(ctx context.Context, sem chan struct{}, steps []createStep, msg createDoneMsg) createDoneMsg {
	var outs []string
	for _, s := range steps {
		out, err := runStep(ctx, sem, s)
		if err != nil {
			// only the failed command's output: the reason is in it
			msg.failed, msg.byForge, msg.out, msg.err = s.show, s.forge, out, err
			return msg
		}
		if out != "" && !s.quiet {
			outs = append(outs, out)
		}
		msg.initDone = msg.initDone || s.init
	}
	msg.out = strings.Join(outs, "\n")
	return msg
}

// runStep runs s, a git step only once it has a slot of sem.
func runStep(ctx context.Context, sem chan struct{}, s createStep) (string, error) {
	if !s.forge && sem != nil {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return s.run(ctx)
}

func (m *Model) handleCreateDone(msg createDoneMsg) tea.Cmd {
	c := m.create
	label := ""
	if c != nil {
		label = c.path
	}
	reason := m.failReason(msg)
	if !msg.publish && !msg.initDone && c != nil {
		// nothing was created and there is no row for details: keep the
		// box open on the review with the whole reason
		c.running, c.err = false, errors.New("git init failed: "+reason)
		return nil
	}
	m.closeCreate()
	details := msg.out
	if msg.err != nil {
		details = "$ " + msg.failed + "\n" + msg.out
	}
	if msg.publish {
		r := m.rows[msg.idx]
		r.setLast("publish", details, msg.err)
		if msg.err != nil {
			m.flash = "publish failed: " + reason + " · enter: details"
		} else {
			m.flash = fmt.Sprintf("published %s as %s on %s", r.rel, label, m.forge().Hostname())
		}
		r.busy = "status" // was "publish": still ours
		return m.loadStatus(msg.idx)
	}
	if !msg.initDone {
		m.flash = "create failed: " + reason
		return nil
	}
	idx := m.addRow(msg.dest, "create", details)
	refresh := m.startRefresh(idx)
	if msg.err != nil {
		m.rows[idx].lastErr = msg.err
		m.flash = fmt.Sprintf("created %s locally, but publishing failed: %s · enter: details", m.rows[idx].rel, reason)
		return refresh
	}
	m.flash = "created " + m.rows[idx].rel
	if len(m.opts.Agents) > 0 {
		m.openAgentPicker()
		m.flash += " · start an agent in it? (esc to stay)"
	}
	return refresh
}

// failReason says in plain words why a create/publish step failed: a
// known cause found anywhere in a forge command's output (missing scope,
// no login, name taken, not found), else the output's last line.
func (m Model) failReason(msg createDoneMsg) string {
	if msg.err == nil {
		return ""
	}
	if msg.byForge {
		f := m.forge()
		if h := forge.Hint(f.CLI(), f.Hostname(), msg.out+"\n"+msg.err.Error()); h != "" {
			return h
		}
	}
	return lastLine(msg.out, msg.err)
}

// addRow lists a repository created or cloned below root and moves the
// cursor to it.
func (m *Model) addRow(dest, act, out string) int {
	rel, err := filepath.Rel(m.opts.Root, dest)
	if err != nil {
		rel = dest
	}
	m.rows = append(m.rows, &row{rel: safe.Line(rel), st: gitx.Status{Path: dest}, lastAct: act, lastOut: keepOut(out)})
	idx := len(m.rows) - 1
	m.rebuildView()
	for vi, i := range m.view {
		if i == idx {
			m.cursor = vi
		}
	}
	m.clamp()
	return idx
}
