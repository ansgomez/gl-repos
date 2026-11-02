package tui

import (
	"context"
	"errors"
	"fmt"
	"gl-repos/internal/safe"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

// groupState is the preview of a GitLab group clone: every project of the
// group and its subgroups, cloned into nested folders below a destination
// named after the group.
type groupState struct {
	group    forge.Project
	loading  bool
	projects []forge.Project
	err      error
	archived bool // include archived projects
	offset   int  // list scroll
	confirm  bool // asking y/N
	note     string

	// the plan for the current destination; recomputed in a command
	// when its inputs change (it stats every project's folder)
	items       []groupItem
	hidden      int
	planErr     error
	planDest    string // destination the plan is for, relative to root
	planning    bool
	wantConfirm bool // enter pressed: ask y/N once the plan is in
}

type groupPlanMsg struct {
	seq    int
	items  []groupItem
	hidden int
	dest   string
	err    error
}

type groupProjectsMsg struct {
	seq      int
	projects []forge.Project
	err      error
}

// groupRun counts a group clone in progress.
type groupRun struct {
	name        string
	total, left int
	failed      []string
	cancelled   int
	depth       int // deepest new repository, as a -depth value
}

// groupItem is one project of the plan.
type groupItem struct {
	p    forge.Project
	rel  string // below the destination, mirrors the subgroups
	dest string // absolute
	here bool   // already exists: skipped
}

func (m *Model) openGroup(g forge.Project) tea.Cmd {
	gf, ok := m.forge().(forge.Groups)
	if !ok {
		m.clone.err = fmt.Errorf("%s has no groups", m.forge().Name())
		return nil
	}
	c := m.clone
	c.group = &groupState{group: g, loading: true}
	m.mode = modeCloneGroup
	m.input.Reset()
	m.input.Prompt = "into ▸ "
	m.input.Placeholder = ""
	m.input.SetValue(g.Name)
	m.input.CursorEnd()
	m.cloneSeq++
	seq, path := m.cloneSeq, g.Path
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	c.cancel = cancel
	return func() tea.Msg {
		defer cancel()
		ps, err := gf.GroupProjects(ctx, path)
		return groupProjectsMsg{seq: seq, projects: ps, err: err}
	}
}

func (m *Model) handleGroupProjects(msg groupProjectsMsg) tea.Cmd {
	c := m.clone
	if c == nil || c.group == nil || msg.seq != m.cloneSeq {
		return nil
	}
	c.group.loading = false
	c.group.projects, c.group.err = msg.projects, msg.err
	return m.replanGroup()
}

// replanGroup recomputes the plan for the typed destination in a
// command; an answer for an older input is dropped.
func (m *Model) replanGroup() tea.Cmd {
	g := m.clone.group
	m.groupPlanSeq++
	g.planning = true
	seq, root, dest := m.groupPlanSeq, m.opts.Root, m.input.Value()
	group, projects, archived := g.group, g.projects, g.archived
	return func() tea.Msg {
		items, hidden, err := groupPlan(root, dest, group, projects, archived)
		rel := ""
		if err == nil {
			rel, _ = filepath.Rel(root, filepath.Join(root, filepath.Clean(strings.TrimSpace(dest))))
		}
		return groupPlanMsg{seq: seq, items: items, hidden: hidden, dest: rel, err: err}
	}
}

func (m *Model) handleGroupPlan(msg groupPlanMsg) {
	c := m.clone
	if c == nil || c.group == nil || msg.seq != m.groupPlanSeq {
		return
	}
	g := c.group
	g.planning = false
	g.items, g.hidden, g.planErr, g.planDest = msg.items, msg.hidden, msg.err, msg.dest
	if g.wantConfirm {
		g.wantConfirm = false
		if m.groupRun != nil {
			g.note = "wait until the clone of " + m.groupRun.name + " finishes"
		} else if g.planErr == nil && todo(g.items) > 0 {
			g.confirm = true
		}
	}
}

// groupDest validates the destination folder of a group clone: inside
// root; unlike a single clone it may exist (cloning again adds what is
// missing).
func groupDest(root, dest string) (string, error) {
	dest = strings.TrimSpace(dest)
	if err := gitx.CheckDest(dest); err != nil {
		return "", err
	}
	if filepath.IsAbs(dest) {
		return "", fmt.Errorf("use a path relative to %s", root)
	}
	clean := filepath.Clean(dest)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("destination must be inside %s", root)
	}
	abs := filepath.Join(root, clean)
	if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
		return "", fmt.Errorf("%s is a file", clean)
	}
	return abs, nil
}

// groupPlan lists the projects of group to clone into dest (below
// root) and how many are skipped. It stats every project's folder: run
// it in a command.
func groupPlan(root, dest string, group forge.Project, projects []forge.Project, archived bool) (items []groupItem, archivedHidden int, err error) {
	root, err = groupDest(root, dest)
	if err != nil {
		return nil, 0, err
	}
	for _, p := range projects {
		if p.Archived && !archived {
			archivedHidden++
			continue
		}
		rel := p.Path // below the group; its path is canonical, group.Path may differ in case (like forge's inGroup)
		if n := len(group.Path); len(rel) > n && rel[n] == '/' && strings.EqualFold(rel[:n], group.Path) {
			rel = rel[n+1:]
		}
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			continue // never clone outside the destination (forge also drops these)
		}
		if p.SSHURL == "" && p.HTTPURL == "" {
			continue // no clone URL (forge drops the URLs that do not parse)
		}
		it := groupItem{p: p, rel: rel, dest: filepath.Join(root, filepath.FromSlash(rel))}
		if _, err := os.Stat(it.dest); err == nil {
			it.here = true
		}
		items = append(items, it)
	}
	return items, archivedHidden, nil
}

func todo(items []groupItem) int {
	n := 0
	for _, it := range items {
		if !it.here {
			n++
		}
	}
	return n
}

func (m Model) handleGroupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.clone
	g := c.group
	if g.confirm {
		if lowerKey(msg) == "y" {
			return m, m.startGroupClone()
		}
		g.confirm = false
		return m, nil
	}
	switch msg.String() {
	case "esc":
		if c.cancel != nil {
			c.cancel()
		}
		c.group = nil
		m.input.Reset()
		m.input.Prompt = "clone ▸ "
		m.input.Placeholder = "URL, group/project, or search words"
		m.input.SetValue(c.query)
		m.input.CursorEnd()
		m.mode = modeCloneQuery
		m.updateSuggestions()
		return m, nil
	case "tab":
		g.archived = !g.archived
		return m, m.replanGroup()
	case "up", "ctrl+p":
		g.offset = max(g.offset-1, 0)
		return m, nil
	case "down", "ctrl+n":
		g.offset++
		return m, nil
	case "enter":
		if g.loading || g.err != nil {
			return m, nil
		}
		if m.groupRun != nil {
			g.note = "wait until the clone of " + m.groupRun.name + " finishes"
			return m, nil
		}
		// a fresh plan (folders may have appeared), then y/N
		g.wantConfirm = true
		return m, m.replanGroup()
	}
	before := m.input.Value()
	m.input.Update(msg)
	if m.input.Value() == before {
		return m, nil
	}
	g.wantConfirm = false
	return m, m.replanGroup()
}

// startGroupClone clones the plan the user confirmed.
func (m *Model) startGroupClone() tea.Cmd {
	g := m.clone.group
	if g.planning || g.planErr != nil {
		return nil
	}
	items := g.items
	name := m.clone.group.group.Path
	run := &groupRun{name: name}
	m.closeClone()
	m.groupRun = run
	var cmds []tea.Cmd
	first := -1
	for _, it := range items {
		if it.here {
			continue
		}
		u, err := m.cloneURL(it.p)
		if err != nil {
			continue // no valid URL: not in the plan (see groupPlan)
		}
		if idx, cmd := m.enqueueClone(u, it.p.Path, it.dest, name); cmd != nil {
			cmds = append(cmds, cmd)
			run.total++
			run.depth = max(run.depth, relDepth(m.rows[idx].rel))
			if first < 0 {
				first = idx
			}
		}
	}
	run.left = run.total
	if run.total == 0 {
		m.groupRun = nil
		return nil
	}
	for vi, i := range m.view { // cursor on the first new repository
		if i == first {
			m.cursor = vi
		}
	}
	m.clamp()
	m.flash = fmt.Sprintf("cloning %d projects of %s · x cancels · r retries failed ones", run.total, name)
	return tea.Batch(cmds...)
}

// groupJobDone counts a finished clone of the running group clone and
// reports the result when the last one is done.
func (m *Model) groupJobDone(v jobView, err error) {
	run := m.groupRun
	if run == nil || run.name != v.group {
		return
	}
	run.left--
	if err != nil && !errors.Is(err, errCancelled) {
		run.failed = append(run.failed, v.label)
	}
	if errors.Is(err, errCancelled) {
		run.cancelled++
	}
	if run.left > 0 {
		return
	}
	m.groupRun = nil
	ok := run.total - len(run.failed) - run.cancelled
	m.flash = fmt.Sprintf("cloned %d of %d projects of %s", ok, run.total, run.name)
	if run.cancelled > 0 {
		m.flash += fmt.Sprintf(" · %d cancelled", run.cancelled)
	}
	if len(run.failed) > 0 {
		m.flash += fmt.Sprintf(" · %d failed (marked ✗: r retries, x removes)", len(run.failed))
	}
	if run.depth > m.opts.Depth {
		m.flash += fmt.Sprintf(" · next time start with -d %d to list them", run.depth)
	}
}

// groupURLPath returns the group path of a pasted GitLab web URL on the
// active profile's host ("https://host/acme/tools" → "acme/tools"), or ""
// for anything else (a clone URL ends in .git or is ssh). The whole
// input is checked before its "/-/…" tail is dropped: a credential
// there ("…/-/tree/main?private_token=…") makes it no group URL, so it
// is refused as a clone URL, never searched.
func (m Model) groupURLPath(q string) string {
	if _, ok := m.forge().(forge.Groups); !ok {
		return ""
	}
	q = strings.TrimSpace(q)
	if safe.Line(q) != q || strings.ContainsAny(q, "?#;%@") {
		return ""
	}
	if i := strings.Index(q, "/-/"); i >= 0 { // e.g. …/-/tree/main
		q = q[:i]
	}
	if !strings.HasPrefix(q, "https://") || strings.HasSuffix(strings.TrimRight(q, "/"), ".git") {
		return ""
	}
	u, err := forge.ParseCloneURL(q) // a URL with credentials (or ?, #) is no group URL
	if err != nil {
		return ""
	}
	if h := m.forge().Hostname(); h == "" || forge.HostFromURL(u.String()) != forge.HostName(h) {
		return ""
	}
	return strings.TrimPrefix(u.Path(), "groups/")
}

// --- view ---

func (m Model) viewGroup() string {
	c := m.clone
	g := c.group
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s  clone group · %s\n\n", styleTitle.Render("gl-repos"), m.badge(m.prof), g.group.Path)
	help := "esc back"
	switch {
	case g.loading:
		b.WriteString(m.spin.View() + " listing the projects of " + g.group.Path + " and its subgroups…\n")
	case g.err != nil:
		b.WriteString(styleBad.Render(wrap(showURL(g.err.Error()), m.width)) + "\n")
	default:
		items, hidden, err := g.items, g.hidden, g.planErr
		n := todo(items)
		summary := fmt.Sprintf("%d projects", len(items))
		if here := len(items) - n; here > 0 {
			summary += fmt.Sprintf(" · %d already here", here)
		}
		if hidden > 0 {
			summary += fmt.Sprintf(" · %d archived hidden", hidden)
		} else if g.archived {
			summary += " · archived included"
		}
		b.WriteString(summary + "\n\n")
		h := max(m.height-12, 3)
		g.offset = min(g.offset, max(len(items)-h, 0))
		for i := g.offset; i < len(items) && i < g.offset+h; i++ {
			it := items[i]
			line := "  " + it.rel
			switch {
			case it.here:
				line = styleDim.Render(line + "  already here")
			case it.p.Archived:
				line += styleDim.Render("  archived")
			}
			b.WriteString(ansiTruncate(line, m.width) + "\n")
		}
		if len(items) > h {
			b.WriteString(styleDim.Render(fmt.Sprintf("  … %d of %d shown (↑↓ scroll)", min(h, len(items)), len(items))) + "\n")
		}
		b.WriteString("\n" + m.input.View() + "\n")
		switch {
		case err != nil:
			b.WriteString(styleBad.Render(showURL(err.Error())) + "\n")
		case n == 0:
			b.WriteString(styleDim.Render("nothing to clone") + "\n")
		default:
			var first groupItem
			for _, it := range items {
				if !it.here {
					first = it
					break
				}
			}
			rel, _ := filepath.Rel(m.opts.Root, first.dest)
			b.WriteString(styleDim.Render(fmt.Sprintf("will run %d × git clone in %s (%d at a time), e.g.:", n, showPath(m.opts.Root), m.opts.Jobs)) + "\n")
			u, _ := m.cloneURL(first.p)
			b.WriteString(ansiTruncate("  "+gitx.CommandLine(gitx.CloneArgs(u, rel)), m.width) + "\n")
		}
		if g.note != "" {
			b.WriteString(styleWarn.Render(g.note) + "\n")
		}
		if g.confirm {
			b.WriteString("\n" + styleWarn.Render(fmt.Sprintf("Clone %d projects into %s/? [y/N]", n, g.planDest)) + "\n")
		}
		help = "enter clone · tab archived on/off · ↑↓ scroll · esc back"
	}
	b.WriteString("\n" + styleDim.Render(help))
	return b.String()
}
