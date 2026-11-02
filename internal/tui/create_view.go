package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"gl-repos/internal/forge"
	"gl-repos/internal/safe"
)

func (m Model) viewCreate() string {
	w, inner := m.createFrame()
	body := m.createBody(w)
	// The key help sits on the window's last line, as in the list view.
	help := wrapHelp(m.createHelp(), w)
	pad := max(inner-m.bodyLines(body, w)-len(help), 1)
	out := body + strings.Repeat("\n", pad) + styleDim.Render(strings.Join(help, "\n"))
	if m.publicAlert() {
		return styleAlertFrame.Width(max(m.width-2, 12)).Render(out)
	}
	return out
}

// createFrame is the width and height inside the box's red frame, if any.
func (m Model) createFrame() (w, inner int) {
	if m.publicAlert() {
		return max(m.width-4, 10), m.height - 2 // border and padding; the frame's top and bottom border
	}
	return m.width, m.height
}

// reviewFits reports whether the review and its key help fit the window.
// A review cut off at the bottom would hide commands y runs, so y is
// refused until the window is taller (the AI plan scrolls instead; a
// review is a few lines).
func (m Model) reviewFits() bool {
	w, inner := m.createFrame()
	if w <= reviewIndent { // every command line would be cut to its prefix
		return false
	}
	return inner-m.bodyLines(m.createBody(w), w)-len(wrapHelp("y run · esc back", w)) >= 1
}

// reviewIndent is the width of the prefix before each command line of
// the review: two spaces, the 12-column place it runs at, a space.
const reviewIndent = 15

// bodyLines is the number of lines body takes on screen. Inside the red
// frame lipgloss wraps a line wider than w (a long name, the public
// warning in a window under 20 columns, which wrap leaves alone) onto
// more lines; elsewhere the terminal cuts it, so a line is a line.
func (m Model) bodyLines(body string, w int) int {
	if m.publicAlert() {
		return lipgloss.Height(lipgloss.NewStyle().Width(w).Render(strings.TrimSuffix(body, "\n")))
	}
	return strings.Count(body, "\n")
}

// createBody is the box above the key help: the step's field, or the review.
func (m Model) createBody(w int) string {
	c := m.create
	f := m.forge()
	alert := m.publicAlert()
	var b strings.Builder
	title := "new repository"
	if c.publish {
		title = "publish · " + m.rows[c.idx].rel
	}
	fmt.Fprintf(&b, "%s %s  %s\n\n", styleTitle.Render("gl-repos"), m.badge(m.prof), title)
	host := f.Hostname()
	if host == "" {
		host = "host from " + f.CLI() + " login"
	}
	where := fmt.Sprintf("on %s · %s · %s · remote %s", f.Name(), host, visLabel(m.visibility()), c.remoteName)
	warning := ""
	if alert {
		warning = styleAlert.Render(wrap(publicWarning, w)) + "\n"
	}

	switch {
	case c.loading:
		b.WriteString(m.spin.View() + " reading " + m.rows[c.idx].rel + "…\n")
	case c.step == stepFolder:
		b.WriteString(ansiTruncate(m.input.View(), w) + "\n\n")
		if c.remote {
			b.WriteString("remote: " + styleGood.Render("create project on "+f.Name()+" ("+host+")") + styleDim.Render("  · tab: local only") + "\n")
		} else {
			b.WriteString("remote: " + styleGood.Render("none, local only") + styleDim.Render("  · tab: also create on "+f.Name()+" ("+host+")") + "\n")
		}
		if c.remote {
			b.WriteString(warning)
		}
	case c.step == stepPath:
		b.WriteString(ansiTruncate(m.input.View(), w) + "\n\n")
		b.WriteString(ansiTruncate(where+styleDim.Render("  · ctrl+v: visibility"), w) + "\n")
		b.WriteString(warning)
		if c.visMenu {
			b.WriteString("\n" + m.visMenu(w))
			break
		}
		if c.checking {
			b.WriteString("\n" + m.spin.View() + " checking group " + m.ownerTyped() + " on " + host + "…\n")
			break
		}
		b.WriteString(m.renderGroupSuggestions(w))
		if c.meErr != nil {
			b.WriteString(styleDim.Render(wrap("could not read your user name: "+c.meErr.Error(), w)) + "\n")
		}
		if c.publish && !c.hasCommits {
			b.WriteString(styleDim.Render("no commits yet: nothing will be pushed") + "\n")
		}
	case c.step == stepReview:
		if c.publish || c.remote {
			b.WriteString(ansiTruncate(where, w) + "\n")
			b.WriteString(warning + "\n")
		}
		if len(c.sub.Missing) > 0 {
			b.WriteString(styleWarn.Render(wrap(newGroupsWarning(c.sub, m.visibility()), w)) + "\n\n")
		}
		if err := m.visibilityErr(); err != nil {
			b.WriteString(styleBad.Render(wrap(err.Error(), w)) + "\n\n")
		}
		if c.publish && len(c.risky) > 0 {
			b.WriteString(styleAlert.Render(wrap(riskMark+" "+m.rows[c.idx].rel+": "+riskText(c.risky)+
				"; the push may run them", w)) + "\n\n")
		}
		b.WriteString(styleDim.Render("will run:") + "\n")
		for _, s := range c.steps {
			at := ""
			switch s.dir {
			case "":
				at = f.CLI()
			case m.opts.Root:
				at = showPath(s.dir)
			default:
				rel, _ := filepath.Rel(m.opts.Root, s.dir)
				at = safe.Line(rel)
			}
			// wrapped, not cut: every argument of a command that changes
			// the forge must be visible before y, as it runs (wrapExact
			// keeps every space; a folder name may hold two in a row)
			prefix := "  " + styleDim.Render(fitLeft(at, reviewIndent-3)) + " "
			for j, l := range wrapExact(safe.Line(s.show), max(w-reviewIndent, 1)) {
				if j > 0 {
					prefix = strings.Repeat(" ", reviewIndent)
				}
				b.WriteString(ansiTruncate(prefix+l, w) + "\n")
			}
		}
		if c.publish && !c.hasCommits {
			b.WriteString("\n" + styleDim.Render("no commits yet: push later with h") + "\n")
		}
		if c.running {
			b.WriteString("\n" + m.spin.View() + " running…\n")
		}
	}
	if c.err != nil {
		b.WriteString("\n" + styleBad.Render(wrap(c.err.Error(), w)) + "\n")
	}
	return b.String()
}

// createHelp lists the keys of the current step of "n"/"u".
func (m Model) createHelp() string {
	c := m.create
	switch {
	case c.running:
		return "running… · ctrl+c stop and quit"
	case c.loading:
		return "esc cancel"
	case c.checking:
		return "checking the group… · esc stop"
	case c.step == stepFolder:
		return "enter next · tab toggle remote · ctrl+u clear · esc cancel"
	case c.step == stepPath && c.visMenu:
		return "↑↓/jk choose · enter confirm · esc cancel"
	case c.step == stepPath:
		keys := "enter review · ctrl+v visibility · ctrl+u clear"
		if _, ok := m.forge().(forge.Groups); ok {
			keys = "enter review · tab complete group · ctrl+v visibility · ctrl+u clear"
		}
		if c.publish {
			return keys + " · esc cancel"
		}
		return keys + " · esc back"
	case !m.reviewFits():
		return "enlarge the window to see every command before y · esc back"
	}
	return "y run · esc back"
}

// renderGroupSuggestions lists the groups matching the path field, or
// says why there are none.
func (m Model) renderGroupSuggestions(w int) string {
	c := m.create
	if _, ok := m.forge().(forge.Groups); !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n")
	switch {
	case len(c.groups) == 0 && c.gloading:
		b.WriteString(m.spin.View() + styleDim.Render(" loading your groups…") + "\n")
	case len(c.groups) == 0 && c.groupErr != nil:
		b.WriteString(styleDim.Render(wrap("could not list your groups: "+c.groupErr.Error(), w)) + "\n")
	case len(c.groups) == 0:
		return ""
	case len(c.gsugg) == 0 && !m.ownerDone():
		b.WriteString(styleDim.Render("  no matching group · type words of a group path") + "\n")
	case len(c.gsugg) == 0:
		b.WriteString(styleDim.Render("  in group "+m.ownerTyped()) + "\n")
	}
	for i, g := range c.gsugg {
		cur, name := "  ", g.Path+"/"
		if i == c.gsel {
			cur, name = "▸ ", styleCursor.Render(name)
		}
		line := ansiTruncate(cur+name, w)
		if i == c.gsel {
			line = highlightRow(line, w)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// visMenu lists the forge's visibilities with the menu cursor.
func (m Model) visMenu(w int) string {
	c := m.create
	vis := m.forge().Visibilities()
	nameW := 0
	for _, v := range vis {
		nameW = max(nameW, len(v))
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render("visibility ▸") + "\n")
	for i, v := range vis {
		cur, name := "  ", fmt.Sprintf("%-*s", nameW, v)
		switch {
		case v == "public":
			name = styleAlert.Render(name)
		case i == c.visSel:
			name = styleCursor.Render(name)
		}
		if i == c.visSel {
			cur = "▸ "
		}
		desc := visDescription(v)
		if i == c.vis {
			desc += " (current)"
		}
		line := ansiTruncate(cur+name+"  "+styleDim.Render(desc), w)
		if i == c.visSel {
			line = highlightRow(line, w)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
