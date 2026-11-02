package tui

import (
	"fmt"
	"gl-repos/internal/safe"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

var (
	styleTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleHeader = lipgloss.NewStyle().Bold(true).Underline(true)
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleGood   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleBad    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleCursor = lipgloss.NewStyle().Bold(true)
	// styleAlert marks a public project: ANSI red, so it reads on light
	// and dark terminals alike.
	styleAlert = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	// styleAlertFrame is the red border around the create/publish window
	// while the chosen visibility is public.
	styleAlertFrame = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("1")).Padding(0, 1)
	// styleCursorRow is the faint bar behind the row under the cursor:
	// light grey on light terminals, dark grey on dark ones.
	styleCursorRow = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "252", Dark: "239"})
)

// DetectBackground looks up whether the terminal is dark, for
// styleCursorRow. Call it before the program starts: the lookup queries
// the terminal, which must not happen in View or race Bubble Tea's input.
func DetectBackground() { lipgloss.HasDarkBackground() }

// highlightRow draws styleCursorRow behind an already styled line and
// pads it to width columns.
func highlightRow(line string, width int) string {
	on, off, _ := strings.Cut(styleCursorRow.Render("x"), "x")
	return paintRow(line, on, off, width)
}

// paintRow wraps line in the SGR sequences on/off and pads it to width.
// Inner styles end in a reset, so on is re-applied after each one.
func paintRow(line, on, off string, width int) string {
	if on == "" {
		return line // no colour support
	}
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+on)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+on)
	pad := max(width-ansi.StringWidth(line), 0)
	return on + line + strings.Repeat(" ", pad) + off
}

// listHelp names every key of the list view, most used first: narrow
// windows wrap it onto more lines (see listHelpLines).
const listHelp = "↑↓ move · enter details · space select · a all · f fetch · p pull · h push · i ask AI · " +
	"r refresh · c clone · n new · u publish · t agent · o tool · ! shell · l graph · s sort · d attention · " +
	"w profile · x cancel clone · q quit"

// listHelpLines is how many lines the list's key help may take, so that
// every key stays visible down to about 70 columns.
const listHelpLines = 4

// View renders the screen of the current mode.
func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	switch m.mode {
	case modeGraph:
		return m.viewGraph()
	case modePlan, modeEditMsg:
		return m.viewPlan()
	case modeCloneGroup:
		return m.viewGroup()
	case modeCloneQuery, modeClonePick, modeCloneDest:
		return m.viewClone()
	case modeCreate:
		return m.viewCreate()
	}
	return m.viewList()
}

func (m Model) viewList() string {
	var b strings.Builder
	b.WriteString(m.titleLine())
	b.WriteByte('\n')

	repoW, branchW := 10, 6
	for _, r := range m.rows {
		repoW = max(repoW, len([]rune(r.rel)))
		branchW = max(branchW, len([]rune(r.st.BranchText())))
	}
	repoW, branchW = min(repoW, 40), min(branchW, 24)
	const syncW, baseW, stateW = 9, 9, 16
	upW := max(12, min(28, m.width-(3+repoW+branchW+syncW+baseW+stateW+6)-12))
	w := widths{repoW, branchW, syncW, baseW, stateW, upW}

	b.WriteString(styleHeader.Render(truncate(
		"   "+fit("REPO", repoW)+" "+fit("BRANCH", branchW)+" "+fit("SYNC", syncW)+" "+
			fit("VS BASE", baseW)+" "+fit("STATE", stateW)+" "+fit("UPSTREAM", upW)+" ", m.width)))
	b.WriteByte('\n')

	h := m.listHeight()
	if m.cursor >= m.offset+h { // the flash grew since the last clamp
		m.offset = m.cursor - h + 1
	}
	if len(m.view) == 0 && !m.attnOnly {
		b.WriteString(styleDim.Render(ansi.Truncate("   "+m.emptyText(), m.width, "…")) + "\n")
		h--
	}
	for line := 0; line < h; line++ {
		if vi := m.offset + line; vi < len(m.view) {
			row := ansi.Truncate(m.renderRow(vi, w), m.width, "…")
			if vi == m.cursor {
				row = highlightRow(row, m.width)
			}
			b.WriteString(row)
		}
		b.WriteByte('\n')
	}

	if m.detail {
		b.WriteString(m.renderDetail())
	}

	switch {
	case m.mode == modePrompt:
		b.WriteString(m.input.View())
	case m.mode == modeAgent:
		b.WriteString(ansi.Truncate(m.agentLine(), m.width, "…"))
	case m.mode == modeProfile:
		b.WriteString(ansi.Truncate(m.profileLine(), m.width, "…"))
	case m.confirm != nil:
		b.WriteString(styleWarn.Render(ansiTruncate(m.confirmText(), max(m.width, 1))))
	case m.flash != "":
		b.WriteString(styleWarn.Render(strings.Join(m.flashLines(), "\n")))
	}
	b.WriteByte('\n')
	b.WriteString(styleDim.Render(strings.Join(m.helpLines(), "\n")))
	return b.String()
}

// flashLines is the list view's message, wrapped onto at most two lines
// so a reason that comes first is followed by what to do next.
func (m Model) flashLines() []string {
	if m.flash == "" || m.mode == modePrompt || m.mode == modeAgent || m.mode == modeProfile || m.confirm != nil {
		return []string{""}
	}
	w := max(m.width, 1)
	lines := wrapWords(safe.Line(m.flash), w)
	if len(lines) > 2 {
		lines = []string{lines[0], ansiTruncate(strings.Join(lines[1:], " "), w)}
	}
	return lines
}

// helpLines is the list view's key help, wrapped at " · " onto at most
// listHelpLines lines.
func (m Model) helpLines() []string {
	help := listHelp
	if m.mode == modePrompt {
		help = "enter ask " + m.opts.AI.Model + " (empty = commit all changes) · esc cancel"
	}
	if m.mode == modeAgent && m.flash != "" {
		help = m.flash + " · ←→ choose · enter start"
	} else if m.mode == modeAgent {
		help = "←→ choose · 1-9 pick · enter start in " + m.rows[m.agentRow].rel + " · esc cancel"
	}
	// Other modes show one line but keep the list's height steady.
	lines := wrapHelpN(listHelp, m.width, listHelpLines)
	if m.mode != modeList {
		pad := make([]string, len(lines))
		pad[0] = truncate(help, max(m.width, 1))
		return pad
	}
	return lines
}

// wrapHelp wraps key help at " · " onto at most two lines.
func wrapHelp(help string, width int) []string { return wrapHelpN(help, width, 2) }

// wrapHelpN wraps key help at " · " onto at most n lines; the last one
// is cut to the width when the rest does not fit.
func wrapHelpN(help string, width, n int) []string {
	if width <= 0 {
		return []string{help}
	}
	var lines []string
	cur := ""
	for _, part := range strings.Split(help, " · ") {
		switch {
		case cur == "":
			cur = part
		case len([]rune(cur+" · "+part)) <= width:
			cur += " · " + part
		default:
			lines = append(lines, cur)
			cur = part
		}
	}
	lines = append(lines, cur)
	if len(lines) > n {
		lines = append(lines[:n-1], truncate(strings.Join(lines[n-1:], " · "), width))
	}
	return lines
}

func (m Model) titleLine() string {
	var n, dirty, ahead, behind, errs, busy int
	for _, r := range m.rows {
		if r.gone {
			continue
		}
		n++
		if r.busy != "" {
			busy++
		}
		if !r.loaded {
			continue
		}
		switch {
		case r.st.Err != nil:
			errs++
		case r.st.Dirty():
			dirty++
		}
		if r.st.Ahead > 0 {
			ahead++
		}
		if r.st.Behind > 0 {
			behind++
		}
	}
	summary := fmt.Sprintf("%d repos · %d dirty · %d ahead · %d behind", n, dirty, ahead, behind)
	if errs > 0 {
		summary += fmt.Sprintf(" · %d errors", errs)
	}
	if busy > 0 {
		summary += fmt.Sprintf(" · %s %d running", m.spin.View(), busy)
	}
	if c := m.cloneSummary(); c != "" {
		summary += " · " + c
	}
	if s := sortModes[m.sortIdx]; s != "name" {
		summary += " · sort: " + s
	}
	if m.attnOnly {
		summary += " · [attention only]"
	}
	// The summary matters more than the path: shorten the path from the
	// left to whatever room is left.
	head := styleTitle.Render("gl-repos") + " " + m.badge(m.prof) + "  " + summary
	tail := fmt.Sprintf("depth %d · ", m.opts.Depth)
	room := m.width - ansi.StringWidth(head) - len(tail) - 2
	if room < 8 {
		return ansi.Truncate(head, m.width, "…")
	}
	return head + "  " + styleDim.Render(tail+fitLeft(showPath(m.opts.Root), room))
}

type widths struct{ repo, branch, sync, base, state, up int }

func (m Model) renderRow(vi int, w widths) string {
	r := m.rows[m.view[vi]]
	cur, sel, mark := " ", " ", " "
	if vi == m.cursor {
		cur = "▸"
	}
	if r.selected {
		sel = "●"
	}
	if len(r.st.Risky) > 0 { // runs programs from its own config
		mark = styleAlert.Render(riskMark)
	}

	repo := fitLeft(r.rel, w.repo)
	if vi == m.cursor {
		repo = styleCursor.Render(repo)
	}

	if r.job != nil {
		return cur + sel + mark + repo + " " + m.renderJob(r.job, m.width-w.repo-4)
	}
	if !r.loaded && r.st.Err == nil {
		return cur + sel + mark + repo + " " + styleDim.Render(m.spin.View()+" loading")
	}

	st := r.st
	branch := fit(st.BranchText(), w.branch)
	switch {
	case st.Detached:
		branch = styleBad.Render(branch)
	case st.DefaultBranch != "" && st.Branch != st.DefaultBranch:
		branch = styleWarn.Render(branch)
	}

	sync := fit(st.SyncText(), w.sync)
	switch {
	case st.Behind > 0:
		sync = styleBad.Render(sync)
	case st.Ahead > 0:
		sync = styleWarn.Render(sync)
	case st.Upstream != "":
		sync = styleGood.Render(sync)
	default:
		sync = styleDim.Render(sync)
	}

	base := fit(st.BaseText(), w.base)
	if st.BaseBehind > 0 {
		base = styleWarn.Render(base)
	}

	state := fit(st.StateText(), w.state)
	switch {
	case st.Err != nil || st.Conflicts > 0:
		state = styleBad.Render(state)
	case st.Dirty():
		state = styleWarn.Render(state)
	default:
		state = styleGood.Render(state)
	}

	up := fit(st.UpstreamText(), w.up)
	switch {
	case st.TracksOriginDefault():
		up = styleGood.Render(up)
	case st.Upstream == "":
		up = styleWarn.Render(up)
	}

	var tail string
	switch {
	case r.busy != "":
		tail = styleDim.Render(m.spin.View() + " " + r.busy)
	case r.lastErr != nil:
		tail = styleBad.Render("✗ " + r.lastAct)
	case r.lastAct != "":
		tail = styleGood.Render("✓ " + r.lastAct)
	}

	return cur + sel + mark + repo + " " + branch + " " + sync + " " + base + " " + state + " " + up + " " + tail
}

func (m Model) renderDetail() string {
	lines := []string{styleDim.Render(strings.Repeat("─", max(m.width, 1)))}
	if r := m.current(); r != nil {
		st := r.st
		lines = append(lines, "path:     "+showPath(st.Path))
		if j := r.job; j != nil {
			v := j.view()
			lines = append(lines, "clone:    "+showURL(v.url.String()))
			state := fmt.Sprintf("attempt %d/%d", max(v.attempt, 1), cloneAttempts)
			if v.phase != "" {
				state += fmt.Sprintf(" · %s · %d%%", v.phase, v.pct)
			}
			lines = append(lines, "progress: "+state)
			if v.lastErr != "" {
				lines = append(lines, styleBad.Render("error:    "+safe.Line(v.lastErr)))
			}
			for len(lines) < detailHeight {
				lines = append(lines, "")
			}
			lines = lines[:detailHeight]
			for i := range lines {
				lines[i] = ansi.Truncate(lines[i], m.width, "…")
			}
			return strings.Join(lines, "\n") + "\n"
		}
		origin := forge.RedactURLs(st.Origin)
		switch {
		case origin == "":
			origin = "(none)"
		case st.DefaultBranch != "":
			// default branch first so a long URL can't push it off-screen
			origin = "default branch " + st.DefaultBranch + " · " + origin
		}
		if len(st.Risky) > 0 {
			lines = append(lines, styleAlert.Render("config:   "+riskMark+" "+riskText(st.Risky)+
				"; reads by gl-repos don't run them, other actions ask first"))
		}
		lines = append(lines, "origin:   "+origin)
		branch := fmt.Sprintf("branch:   %s → %s  %s", st.BranchText(), st.UpstreamText(), st.SyncText())
		if st.HasBase {
			branch += fmt.Sprintf("  ·  vs origin/%s: %d ahead, %d behind", st.DefaultBranch, st.BaseAhead, st.BaseBehind)
		}
		lines = append(lines, branch)
		changes := fmt.Sprintf("changes:  %d staged, %d modified, %d untracked, %d conflicts",
			st.Staged, st.Modified, st.Untracked, st.Conflicts)
		if !st.LastCommit.IsZero() {
			changes += "  ·  last commit " + ago(st.LastCommit)
		}
		lines = append(lines, changes)
		switch {
		case st.Err != nil:
			lines = append(lines, styleBad.Render("error:    "+safe.Line(st.Err.Error())))
		case r.lastAct != "":
			res := styleGood.Render("ok")
			if r.lastErr != nil {
				res = styleBad.Render(showURL(r.lastErr.Error()))
			}
			lines = append(lines, "last:     "+r.lastAct+" "+res)
			for _, l := range outputLines(r.lastOut, detailHeight-len(lines)) {
				lines = append(lines, styleDim.Render("  "+l))
			}
		}
	}
	for len(lines) < detailHeight {
		lines = append(lines, "")
	}
	lines = lines[:detailHeight]
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "…")
	}
	return strings.Join(lines, "\n") + "\n"
}

// outputLines returns the first n non-blank lines of a command's output,
// redacted and sanitized (safe.Line, as the output's own newlines are
// the line breaks). It stops there: the output may be MaxOutput long and
// the pane is drawn on every frame (each line is at most maxOutLine,
// cut once by scrubOut when the output arrived).
func outputLines(out string, n int) []string {
	var lines []string
	for len(lines) < n && out != "" {
		var l string
		l, out, _ = strings.Cut(out, "\n")
		if l = safe.Line(forge.RedactURLs(l)); strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// keepOut is the part of a command's (redacted) output a row keeps: its
// first detailHeight non-blank lines, picked as outputLines does, which
// is all the details pane ever shows. The rest, up to MaxOutput for each
// action (or the joined steps of a publish), would otherwise be held for
// as long as the row.
func keepOut(out string) string {
	var lines []string
	for len(lines) < detailHeight && out != "" {
		var l string
		l, out, _ = strings.Cut(out, "\n")
		if strings.TrimSpace(safe.Line(l)) != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) viewGraph() string {
	rel := m.rows[m.graphIdx].rel
	title := styleTitle.Render("gl-repos") + "  commit graph · " + rel +
		styleDim.Render(fmt.Sprintf("  %3.0f%%", m.graph.ScrollPercent()*100))
	return title + "\n" + m.graph.View() + "\n" +
		styleDim.Render(truncate("↑↓/pgup/pgdn scroll · esc/q/l back", m.width))
}

// viewPlan renders the AI plan. Everything above the key help scrolls
// (planLines); y runs only once every line has been on screen, so no
// step runs that was not shown in full.
func (m Model) viewPlan() string {
	lines, help := m.planLines()
	room, off := m.planWindow(len(lines), help)
	var b strings.Builder
	for _, l := range lines[off:min(off+room, len(lines))] {
		b.WriteString(l + "\n")
	}
	for range room - min(room, len(lines)-off) {
		b.WriteByte('\n')
	}
	b.WriteString(strings.Join(m.planFooter(help, len(lines), off, room), "\n"))
	return b.String()
}

// planLines returns the plan screen's scrolling content, every line at
// most m.width wide, and the key help for the current state.
func (m Model) planLines() (lines []string, help string) {
	p := m.plan
	r := m.rows[p.idx]
	w := max(m.width, 1)
	add := func(s string) { lines = append(lines, strings.Split(s, "\n")...) }
	addWrapped := func(style lipgloss.Style, s string) {
		for _, l := range wrapExact(safe.Line(forge.RedactURLs(s)), w) { // a request or model text may quote a URL with credentials
			lines = append(lines, style.Render(l))
		}
	}
	add(ansiTruncate(styleTitle.Render("gl-repos")+"  AI plan · "+r.rel+" "+styleDim.Render("("+m.opts.AI.Model+")"), w))
	add("")
	addWrapped(lipgloss.NewStyle(), "request: "+p.request)
	add("")

	switch {
	case p.loading:
		elapsed := time.Since(p.started).Truncate(time.Second)
		phase := "reading repo, loading model…"
		if n := p.chunks.Load(); n > 0 {
			phase = fmt.Sprintf("writing plan… %d chunks", n)
		}
		add(ansiTruncate(fmt.Sprintf("%s %s %s", m.spin.View(), phase, styleDim.Render(elapsed.String())), w))
		help = "esc abort (stops the request; unloads the model if gl-repos loaded it)"
	case p.err != nil:
		addWrapped(styleBad, "error: "+p.err.Error())
		help = "r retry · esc close"
		if p.memoryRefused() {
			help = "f force load anyway · " + help
		}
	case p.validating:
		addWrapped(lipgloss.NewStyle(), p.plan.Summary)
		add("")
		addWrapped(lipgloss.NewStyle(), m.spin.View()+" checking the steps against "+r.rel+"…")
		help = "esc cancel"
	default:
		addWrapped(lipgloss.NewStyle(), p.plan.Summary)
		add("")
		if len(p.risky) > 0 {
			addWrapped(styleAlert, riskMark+" "+r.rel+": "+riskText(p.risky)+
				"; the steps may run them (y runs the plan anyway)")
			add("")
		}
		if len(p.plan.Steps) == 0 {
			add(styleWarn.Render("(no steps proposed)"))
		}
		for i, s := range p.checked {
			lines = append(lines, m.renderStep(i, s, p.stepErrs[i])...)
		}
		help = "r regenerate · esc cancel"
		if p.runnable() {
			help = "y run · e edit message · " + help
		}
	}
	if m.mode == modeEditMsg {
		help = "enter save · esc back"
	}
	return lines, help
}

// planFooter is what stays below the scrolling plan: the edit field,
// a notice, and the key help with the scroll position.
func (m Model) planFooter(help string, total, off, room int) []string {
	w := max(m.width, 1)
	out := []string{""}
	if m.mode == modeEditMsg {
		out = append(out, ansiTruncate(m.input.View(), w))
	}
	if m.plan.notice != "" {
		out = append(out, styleWarn.Render(ansiTruncate(m.plan.notice, w)))
	}
	if total > room {
		more := "end"
		if off+room < total {
			more = fmt.Sprintf("%d more lines below", total-off-room)
		}
		help = "↑↓/pgup/pgdn scroll (" + more + ") · " + help
	}
	for _, l := range wrapHelp(help, w) {
		out = append(out, styleDim.Render(l))
	}
	return out
}

// planWindow returns how many content lines fit and the clamped scroll
// offset.
func (m Model) planWindow(total int, help string) (room, off int) {
	// footer: blank line, edit field, notice, help (up to two lines)
	footer := 1 + len(wrapHelp("↑↓/pgup/pgdn scroll (999 more lines below) · "+help, max(m.width, 1)))
	if m.mode == modeEditMsg {
		footer++
	}
	if m.plan.notice != "" {
		footer++
	}
	room = max(m.height-footer, 1)
	off = min(max(m.plan.scroll, 0), max(total-room, 0))
	return room, off
}

// renderStep renders step i as lines no wider than the screen: the git
// command wrapped (never cut), a multi-line message below it.
func (m Model) renderStep(i int, s gitx.Step, err error) []string {
	w := max(m.width, 1)
	num := fmt.Sprintf("%2d. ", i+1)
	indent := func(prefix string, text string, style lipgloss.Style) []string {
		var out []string
		pad := strings.Repeat(" ", ansi.StringWidth(prefix))
		// model-written messages and errors may quote a URL with credentials
		for j, l := range wrapExact(forge.RedactURLs(text), max(w-ansi.StringWidth(prefix), 1)) {
			if j > 0 {
				prefix = pad
			}
			out = append(out, style.Render(prefix+l))
		}
		return out
	}
	if err != nil {
		return indent(num+"✗ ", safe.Line(s.Op)+": "+safe.Line(err.Error()), styleBad)
	}
	// A copy: the confirmed args must stay exactly what will run.
	args := slices.Clone(m.plan.args[i])
	msg := strings.TrimSpace(s.Message)
	if !strings.Contains(msg, "\n") {
		return indent(num, gitx.CommandLine(args), lipgloss.NewStyle())
	}
	// Show multi-line messages as text below the command.
	for j, a := range args {
		if a == msg {
			args[j] = "<message>"
		}
	}
	out := indent(num, gitx.CommandLine(args), lipgloss.NewStyle())
	for _, l := range strings.Split(msg, "\n") {
		out = append(out, indent("      │ ", safe.Line(l), styleDim)...)
	}
	return out
}

// emptyText explains an empty list: where gl-repos looked and how deep.
func (m Model) emptyText() string {
	return fmt.Sprintf("no repositories here yet · press c to clone one · searched to depth %d (try -d %d) in %s",
		m.opts.Depth, m.opts.Depth+1, showPath(m.opts.Root))
}
