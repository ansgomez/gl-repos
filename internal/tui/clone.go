package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

const (
	searchTimeout = 30 * time.Second
	// cloneTimeout only bounds a runaway attempt; dead connections are
	// caught much earlier by the stall check (cloneStall).
	cloneTimeout = 2 * time.Hour
	listTimeout  = 2 * time.Minute
	// projectsMaxAge is how old the cached project list may get before it
	// is refreshed in the background when the clone box opens.
	projectsMaxAge = 10 * time.Minute
)

// cloneState tracks one clone from query to confirmation:
// query → (search →) pick → destination → run.
type cloneState struct {
	query     string
	searching bool
	cancel    context.CancelFunc
	results   []forge.Project
	cursor    int
	offset    int
	err       error
	url       forge.CloneURL // chosen clone URL
	label     string         // what is being cloned, for display
	destErr   error

	group *groupState // cloning a whole GitLab group

	// autocomplete over all member projects and groups (cached on disk)
	groups []forge.Project
	all    []forge.Project
	allAt  time.Time // when the list was fetched; zero if none yet
	sugg   []forge.Project
	sel    int

	reading bool // the cached list is being read
	noHost  bool // the profile has no host and none was found
}

type cloneSearchMsg struct {
	seq     int
	prof    int
	host    string // host searched, remembered for later searches
	results []forge.Project
	err     error
}

// projectsMsg delivers the host to use and, once read from the cache or
// fetched, its projects.
type projectsMsg struct {
	prof     int // profile the list belongs to
	host     string
	projects []forge.Project
	groups   []forge.Project
	cached   bool      // projects came from the disk cache
	at       time.Time // cached: when the projects were fetched
	oldest   time.Time // cached: the older of the project and group lists
	fetched  bool      // projects came from the forge (else only the host was resolved)
	err      error
	groupErr error // fetched: the group list failed (projects is still good)
}

// openClone starts the clone flow. A non-empty query (from the AI
// assistant) is taken right away: a URL like one typed (never searched:
// it may carry a token), anything else searched on the forge; otherwise
// the user types with autocomplete from the cached project list.
func (m *Model) openClone(query string) tea.Cmd {
	m.clone = &cloneState{}
	m.input.Reset()
	m.input.Prompt = "clone ▸ "
	m.input.Placeholder = "URL, group/project, or search words"
	m.mode = modeCloneQuery
	cmd := m.loadProjects()
	if strings.TrimSpace(query) != "" {
		m.input.SetValue(query)
		m.input.CursorEnd()
		m.clone.query = strings.TrimSpace(query)
		if url, done := m.useURLQuery(); done {
			return tea.Batch(cmd, url)
		}
		return tea.Batch(cmd, m.serverSearch())
	}
	m.updateSuggestions()
	return cmd
}

// loadProjects reads the autocomplete list from the disk cache; the
// answer starts a background refresh when it is missing or stale. If the
// host is not known yet it is resolved first (glab login), which takes a
// moment.
func (m *Model) loadProjects() tea.Cmd {
	fg, prof := m.forge(), m.prof
	host := fg.Hostname()
	if host == "" {
		if m.projLoading {
			return nil
		}
		m.projLoading = true
		remotes := m.remotes()
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
			defer cancel()
			if _, err := lookPath(fg.CLI()); err != nil {
				return projectsMsg{prof: prof, err: fmt.Errorf("%s not found: install it, or paste a clone URL", fg.CLI())}
			}
			return projectsMsg{prof: prof, host: fg.DetectHost(ctx, remotes)}
		}
	}
	m.clone.reading = true
	name := m.profileName()
	_, hasGroups := fg.(forge.Groups)
	return func() tea.Msg {
		ps, at := forge.LoadCache(name, host, fg)
		msg := projectsMsg{prof: prof, host: host, projects: ps, cached: true, at: at, oldest: at}
		if hasGroups {
			gs, gat := forge.LoadGroupCache(name, host, fg)
			msg.groups = gs
			if gat.Before(at) {
				msg.oldest = gat // e.g. no group list yet: fetch it now
			}
		}
		return msg
	}
}

// fetchProjects refreshes the project (and group) list from the forge and
// saves it in the cache.
func (m *Model) fetchProjects(host string) tea.Cmd {
	if m.projLoading {
		return nil
	}
	m.projLoading = true
	fg, prof, name := m.forge(), m.prof, m.profileName()
	gf, hasGroups := fg.(forge.Groups)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
		defer cancel()
		ps, err := fg.ListAll(ctx)
		if err == nil {
			_ = forge.SaveCache(name, host, ps) // the cache only speeds up the next start
		}
		var gs []forge.Project
		var groupErr error // apart: the projects are still installed
		if hasGroups && err == nil {
			if gs, groupErr = gf.ListGroups(ctx); groupErr == nil {
				_ = forge.SaveGroupCache(name, host, gs)
			}
		}
		return projectsMsg{prof: prof, host: host, projects: ps, groups: gs, fetched: true, err: err, groupErr: groupErr}
	}
}

func (m *Model) handleProjectsMsg(msg projectsMsg) tea.Cmd {
	if msg.prof != m.prof {
		return nil // profile switched meanwhile; the cache file is updated
	}
	if !msg.cached {
		m.projLoading = false
	}
	m.setHost(msg.host)
	c := m.clone
	if c == nil {
		return nil // clone box closed meanwhile; the cache file is updated
	}
	switch {
	case msg.cached:
		c.reading = false
		if c.allAt.IsZero() || msg.at.After(c.allAt) { // a fetch may have been faster
			c.all, c.groups, c.allAt = msg.projects, msg.groups, msg.at
			m.updateSuggestions()
		}
		if time.Since(msg.oldest) < projectsMaxAge {
			return nil
		}
		return m.fetchProjects(msg.host)
	case !msg.fetched:
		if msg.host == "" {
			// no CLI login: say so now, not only after a search
			c.noHost = true
			if msg.err != nil {
				c.err = msg.err
			} else {
				c.err = errors.New(m.noHostText())
			}
			return nil
		}
		c.noHost = false
		cmd := m.loadProjects()
		m.updateSuggestions()
		return cmd
	}
	if msg.err != nil {
		if len(c.all) == 0 {
			c.err = msg.err
		}
		return nil
	}
	c.all, c.allAt = msg.projects, time.Now()
	switch {
	case msg.groupErr == nil:
		c.groups = msg.groups
	case len(c.groups) == 0: // else the cached groups stay
		c.err = fmt.Errorf("group list: %w", msg.groupErr)
	}
	m.updateSuggestions()
	return nil
}

// noHostText says that the active profile has no host and how to set one.
func (m Model) noHostText() string {
	if g, ok := m.forge().(forge.GitLab); ok {
		return g.NoHost() + ", or paste a clone URL"
	}
	f := m.forge()
	return "no " + f.Name() + " host found: " + f.CLI() + " auth login --hostname <host>, or paste a clone URL"
}

// profileName is the active profile's name, which keys its caches.
func (m Model) profileName() string {
	if len(m.profiles) == 0 {
		return ""
	}
	return m.profiles[m.prof].Name
}

// excludeKey names the config setting of the active profile's excluded
// groups.
func (m Model) excludeKey() string {
	if len(m.profiles) == 0 || m.profiles[m.prof].ExcludeKey == "" {
		return "exclude in the config"
	}
	return m.profiles[m.prof].ExcludeKey
}

// cloneURL picks the ssh or https URL of p, following how the listed
// repositories already talk to that host. It is parsed again here (the
// list may come from an old cache): a project without a valid URL
// cannot be cloned.
func (m Model) cloneURL(p forge.Project) (forge.CloneURL, error) {
	u := p.SSHURL
	if p.SSHURL == "" || (p.HTTPURL != "" && !m.useSSH(forge.HostFromURL(p.SSHURL))) {
		u = p.HTTPURL
	}
	return forge.ParseCloneURL(u)
}

// useSSH reports whether most listed origins on host use ssh (the
// default when there are none). Hosts compare without a port: an ssh
// remote rarely uses the web port.
func (m Model) useSSH(host string) bool {
	host = forge.HostName(host)
	ssh, https := 0, 0
	for _, r := range m.rows {
		if o := r.st.Origin; o != "" && forge.HostFromURL(o) == host {
			if strings.HasPrefix(o, "https://") || strings.HasPrefix(o, "http://") {
				https++
			} else {
				ssh++
			}
		}
	}
	return ssh >= https
}

func (m *Model) remotes() []string {
	var rs []string
	for _, r := range m.rows {
		rs = append(rs, r.st.Origin)
	}
	return rs
}

func (m Model) suggestionLimit() int {
	return min(max(m.height-9, 3), 12)
}

// updateSuggestions re-ranks the project list for the current input,
// keeping the highlighted project when it is still listed.
func (m *Model) updateSuggestions() {
	c := m.clone
	q := m.input.Value()
	var keep string
	if c.sel < len(c.sugg) {
		keep = c.sugg[c.sel].Path
	}
	if forge.IsURL(q) || len(c.all) == 0 {
		c.sugg = nil
	} else {
		c.sugg = forge.Match(append(append([]forge.Project{}, c.all...), c.groups...), q, m.suggestionLimit())
	}
	c.sel = 0
	for i, p := range c.sugg {
		if p.Path == keep {
			c.sel = i
		}
	}
}

func (m *Model) closeClone() {
	if m.clone != nil && m.clone.cancel != nil {
		m.clone.cancel()
	}
	m.cloneSeq++ // a late answer must not land in the next clone dialog
	m.clone = nil
	m.input.Prompt = "ask ▸ "
	m.mode = modeList
}

// submitCloneQuery handles Enter in the query box: a URL goes straight to
// the destination step, a highlighted suggestion is taken as is, and
// anything else is searched on the forge (the cached list may be stale).
func (m *Model) submitCloneQuery() tea.Cmd {
	c := m.clone
	c.query = strings.TrimSpace(m.input.Value())
	c.err = nil
	if c.query == "" && len(c.sugg) == 0 {
		return nil
	}
	if cmd, done := m.useURLQuery(); done {
		return cmd
	}
	if c.sel < len(c.sugg) {
		return m.choose(c.sugg[c.sel])
	}
	return m.serverSearch()
}

// useURLQuery handles c.query when it is a URL; done is false for
// anything else. A group's web URL is looked up like a typed path; a
// clone URL must parse (forge.ParseCloneURL) and goes straight to the
// destination step, never to a forge search: what does not parse is
// refused, before anything runs, so neither git nor a CLI ever gets a
// credential typed into the box. Earlier search results are dropped, so
// esc cannot bring the URL back as a search.
func (m *Model) useURLQuery() (cmd tea.Cmd, done bool) {
	c := m.clone
	if p := m.groupURLPath(c.query); p != "" {
		c.query = p // a group's web URL: look it up like a typed path
		return m.serverSearch(), true
	}
	if !forge.IsURL(c.query) {
		return nil, false
	}
	if c.cancel != nil {
		c.cancel() // a search still running is not wanted any more
	}
	m.cloneSeq++ // nor its answer
	c.searching, c.results, c.cursor, c.offset = false, nil, 0, 0
	u, err := forge.ParseCloneURL(c.query)
	switch {
	case err != nil:
		c.err = err
	case !m.forge().AllowsURL(u):
		c.err = fmt.Errorf("%s is in an excluded group (%s)", u.Path(), m.excludeKey())
	default:
		m.chooseClone(u, u.String(), u.Name())
	}
	return nil, true
}

// serverSearch asks the forge for c.query and shows the results to pick from.
func (m *Model) serverSearch() tea.Cmd {
	c := m.clone
	ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
	m.cloneSeq++
	seq, prof, fg, query, remotes := m.cloneSeq, m.prof, m.forge(), c.query, m.remotes()
	c.searching, c.cancel = true, cancel
	m.mode = modeClonePick
	return func() tea.Msg {
		defer cancel()
		if fg.Hostname() == "" {
			// Not configured: use the server the CLI is logged in to,
			// preferring the one the listed repositories come from.
			fg = fg.WithHost(fg.DetectHost(ctx, remotes))
		}
		ps, err := fg.Search(ctx, query)
		return cloneSearchMsg{seq: seq, prof: prof, host: fg.Hostname(), results: ps, err: err}
	}
}

func (m *Model) handleCloneSearchMsg(msg cloneSearchMsg) tea.Cmd {
	c := m.clone
	if c == nil || msg.seq != m.cloneSeq || msg.prof != m.prof {
		return nil // cancelled or superseded
	}
	c.searching = false
	m.setHost(msg.host)
	c.results, c.err, c.cursor, c.offset = msg.results, msg.err, 0, 0
	if c.err == nil && len(c.results) == 1 {
		return m.choose(c.results[0])
	}
	return nil
}

// choose continues with a picked project (destination step) or group
// (group preview).
func (m *Model) choose(p forge.Project) tea.Cmd {
	if p.Group {
		return m.openGroup(p)
	}
	u, err := m.cloneURL(p)
	if err != nil {
		m.clone.err = err
		return nil
	}
	m.chooseClone(u, p.Path, p.Name)
	return nil
}

// chooseClone moves to the destination step, prefilled with name. label
// is shown (sanitized); url goes to git.
func (m *Model) chooseClone(url forge.CloneURL, label, name string) {
	c := m.clone
	c.url, c.label = url, showURL(label)
	m.input.Reset()
	m.input.Prompt = "into ▸ "
	m.input.Placeholder = ""
	m.input.SetValue(name)
	m.input.CursorEnd()
	m.mode = modeCloneDest
	m.validateCloneDest()
}

func (m *Model) validateCloneDest() {
	_, m.clone.destErr = m.newDest(m.input.Value())
}

func (m Model) handleCloneQueryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.clone
	switch msg.String() {
	case "esc":
		m.closeClone()
		return m, nil
	case "enter":
		return m, m.submitCloneQuery()
	case "up", "ctrl+p", "shift+tab":
		c.sel = max(c.sel-1, 0)
		return m, nil
	case "down", "ctrl+n":
		c.sel = min(c.sel+1, max(len(c.sugg)-1, 0))
		return m, nil
	case "tab":
		// complete the field with the highlighted project's path
		if c.sel < len(c.sugg) {
			m.input.SetValue(c.sugg[c.sel].Path)
			m.input.CursorEnd()
			m.updateSuggestions()
		}
		return m, nil
	}
	m.input.Update(msg)
	m.updateSuggestions()
	return m, nil
}

func (m Model) handleClonePickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.clone
	switch lowerKey(msg) {
	case "esc", "q":
		if c.searching {
			m.closeClone()
			m.flash = "search cancelled"
			return m, nil
		}
		// back to the query to refine it
		m.input.Prompt = "clone ▸ "
		m.mode = modeCloneQuery
		return m, nil
	case "up", "k":
		c.cursor = max(c.cursor-1, 0)
	case "down", "j":
		c.cursor = min(c.cursor+1, max(len(c.results)-1, 0))
	case "enter":
		if !c.searching && c.cursor < len(c.results) {
			return m, m.choose(c.results[c.cursor])
		}
	}
	return m, nil
}

func (m Model) handleCloneDestKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.clone
	switch msg.String() {
	case "esc":
		if len(c.results) > 1 {
			m.mode = modeClonePick
		} else {
			m.input.Reset()
			m.input.Prompt = "clone ▸ "
			m.input.Placeholder = "URL, group/project, or search words"
			m.input.SetValue(c.query)
			m.input.CursorEnd()
			m.mode = modeCloneQuery
		}
		return m, nil
	case "enter":
		dest, err := m.newDest(m.input.Value())
		if err != nil {
			c.destErr = err
			return m, nil
		}
		return m, m.startClone(c.url, c.label, dest)
	}
	m.input.Update(msg)
	m.validateCloneDest()
	return m, nil
}

// startClone lists the repository right away and clones it in the
// background, showing progress in its row.
func (m *Model) startClone(url forge.CloneURL, label, dest string) tea.Cmd {
	m.closeClone()
	_, cmd := m.enqueueClone(url, label, dest, "")
	return cmd
}

// lastLine is the last non-empty line of git/CLI output (else the
// error), safe to show: sanitized, URL credentials hidden.
func lastLine(out string, err error) string {
	return showURL(rawLastLine(out, err))
}

func rawLastLine(out string, err error) string {
	for { // from the end, without splitting all of it (up to MaxOutput)
		i := strings.LastIndexByte(out, '\n')
		if l := strings.TrimSpace(out[i+1:]); l != "" {
			return l
		}
		if i < 0 {
			return err.Error()
		}
		out = out[:i]
	}
}

// --- view ---

func (m Model) viewClone() string {
	c := m.clone
	var b strings.Builder
	f := m.forge()
	host := f.Hostname()
	switch {
	case host == "" && c.noHost:
		host = f.Name() + " · no host"
	case host == "":
		host = f.Name() + " · looking for your " + f.CLI() + " login…"
	}
	fmt.Fprintf(&b, "%s %s  clone · %s\n\n", styleTitle.Render("gl-repos"), m.badge(m.prof), styleDim.Render(host))

	help := ""
	switch m.mode {
	case modeCloneQuery:
		b.WriteString(m.input.View() + "\n\n")
		b.WriteString(m.renderSuggestions())
		if c.err != nil {
			b.WriteString("\n" + styleBad.Render(wrap(forge.RedactURLs(c.err.Error()), m.width)) + "\n")
		}
		b.WriteString("\n" + styleDim.Render(m.projectsStatus()) + "\n")
		help = "enter search " + f.Name() + " / use URL · esc cancel"
		if len(c.sugg) > 0 {
			help = "↑↓ choose · tab complete · enter clone selected · esc cancel"
		}

	case modeClonePick:
		fmt.Fprintf(&b, "%s %s\n\n", styleDim.Render("search:"), showURL(c.query))
		switch {
		case c.searching:
			b.WriteString(m.spin.View() + " searching your projects…\n")
			help = "esc cancel"
		case c.err != nil:
			b.WriteString(styleBad.Render(wrap(forge.RedactURLs(c.err.Error()), m.width)) + "\n")
			help = "esc back"
		default:
			b.WriteString(m.renderResults())
			help = fmt.Sprintf("↑↓ choose · enter select · esc refine search  (%d matches)", len(c.results))
		}

	case modeCloneDest:
		fmt.Fprintf(&b, "%s %s\n\n", styleDim.Render("project:"), c.label)
		b.WriteString(m.input.View() + "\n\n")
		if c.destErr != nil {
			b.WriteString(styleBad.Render(showURL(c.destErr.Error())) + "\n")
		} else {
			dest, _ := gitx.CloneDest(m.opts.Root, m.input.Value())
			rel, _ := filepath.Rel(m.opts.Root, dest)
			b.WriteString(styleDim.Render("will run in "+showPath(m.opts.Root)+":") + "\n")
			b.WriteString("  " + gitx.CommandLine(gitx.CloneArgs(c.url, rel)) + "\n")
		}
		help = "enter clone · esc back"
	}
	b.WriteString("\n" + styleDim.Render(help))
	return b.String()
}

func (m Model) renderSuggestions() string {
	c := m.clone
	var b strings.Builder
	for i, p := range c.sugg {
		ns, name := "", p.Path
		if j := strings.LastIndex(p.Path, "/"); j >= 0 {
			ns, name = p.Path[:j+1], p.Path[j+1:]
		}
		if p.Group {
			name += "/"
		}
		cur := "  "
		if i == c.sel {
			cur = "▸ "
			name = styleCursor.Render(name)
		}
		meta := ""
		switch {
		case p.Group:
			meta = "  " + styleWarn.Render("group: clone all its projects")
		case !p.LastActivity.IsZero():
			meta = "  " + styleDim.Render(ago(p.LastActivity))
		}
		line := ansiTruncate(cur+styleDim.Render(ns)+name+meta, m.width)
		if i == c.sel {
			line = highlightRow(line, m.width)
		}
		b.WriteString(line + "\n")
	}
	if len(c.sugg) == 0 && len(c.all) > 0 && strings.TrimSpace(m.input.Value()) != "" && !forge.IsURL(m.input.Value()) {
		b.WriteString(styleDim.Render("  no match in your projects · enter searches "+m.forge().Name()) + "\n")
	}
	return b.String()
}

// projectsStatus says where suggestions come from and how fresh they are.
func (m Model) projectsStatus() string {
	c := m.clone
	switch {
	case (m.projLoading || c.reading) && len(c.all) == 0:
		return m.spin.View() + " loading your project list…"
	case c.noHost:
		return "paste a clone URL"
	case len(c.all) == 0:
		return "no project list yet · enter searches " + m.forge().Name()
	}
	s := fmt.Sprintf("%d projects", len(c.all))
	if len(c.groups) > 0 {
		s += fmt.Sprintf(" · %d groups", len(c.groups))
	}
	s += " · updated " + ago(c.allAt)
	if m.projLoading {
		s += " · " + m.spin.View() + " refreshing"
	}
	return s
}

func (m Model) renderResults() string {
	c := m.clone
	h := max(m.height-7, 3)
	if c.cursor < c.offset {
		c.offset = c.cursor
	}
	if c.cursor >= c.offset+h {
		c.offset = c.cursor - h + 1
	}
	pathW := 10
	for _, p := range c.results {
		pathW = max(pathW, ansi.StringWidth(p.Path))
	}
	pathW = min(pathW, max(m.width-20, 20))

	var b strings.Builder
	for i := c.offset; i < len(c.results) && i < c.offset+h; i++ {
		p := c.results[i]
		cur := "  "
		path := fitLeft(p.Path, pathW)
		if i == c.cursor {
			cur = "▸ "
			path = styleCursor.Render(path)
		}
		meta := ""
		if p.Group {
			meta = "group"
		} else if !p.LastActivity.IsZero() {
			meta = ago(p.LastActivity)
		}
		if d := strings.TrimSpace(p.Description); d != "" {
			meta += " · " + d
		}
		line := ansiTruncate(cur+path+"  "+styleDim.Render(meta), m.width)
		if i == c.cursor {
			line = highlightRow(line, m.width)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
