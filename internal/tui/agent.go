package tui

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// openAgents starts the only configured agent directly, or lets the user
// pick one (the first is highlighted, so enter starts the default).
func (m *Model) openAgents() tea.Cmd {
	if m.current() == nil {
		return nil
	}
	switch len(m.opts.Agents) {
	case 0:
		m.flash = "no agents configured ([[agent]] in the config)"
		return nil
	case 1:
		return m.execInRepo(m.currentIdx(), m.opts.Agents[0].Command)
	}
	m.openAgentPicker()
	return nil
}

// openAgentPicker shows the agent picker for the current repository,
// checking once which agents are installed (not on every redraw). The
// row is remembered: statuses arriving while the picker is open re-sort
// the list, so the cursor may sit on another repository by enter.
func (m *Model) openAgentPicker() {
	m.mode, m.agentSel, m.agentRow = modeAgent, 0, m.currentIdx()
	m.agentNote = make([]string, len(m.opts.Agents))
	for i, a := range m.opts.Agents {
		f, err := SplitCommand(a.Command)
		switch {
		case err != nil || len(f) == 0:
			m.agentNote[i] = "no command"
		default:
			if _, err := exec.LookPath(f[0]); err != nil {
				m.agentNote[i] = "not installed"
			}
		}
	}
}

func (m Model) handleAgentKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.opts.Agents)
	switch k := lowerKey(msg); k {
	case "esc", "q", "t":
		m.mode = modeList
		m.flash = ""
	case "left", "up", "h", "k", "shift+tab":
		m.agentSel = (m.agentSel + n - 1) % n
	case "right", "down", "l", "j", "tab":
		m.agentSel = (m.agentSel + 1) % n
	case "enter":
		m.mode, m.flash = modeList, ""
		return m, m.execInRepo(m.agentRow, m.opts.Agents[m.agentSel].Command)
	default:
		if d, err := strconv.Atoi(k); err == nil && d >= 1 && d <= n {
			m.mode, m.flash = modeList, ""
			return m, m.execInRepo(m.agentRow, m.opts.Agents[d-1].Command)
		}
	}
	return m, nil
}

// agentLine renders the picker: "agent ▸ 1 claude  2 codex  3 kimi".
func (m Model) agentLine() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("agent ▸"))
	for i, a := range m.opts.Agents {
		label := fmt.Sprintf("%d %s", i+1, a.Name)
		if i < len(m.agentNote) && m.agentNote[i] != "" {
			label += " (" + m.agentNote[i] + ")"
		}
		b.WriteString("  ")
		switch {
		case i == m.agentSel:
			b.WriteString(styleCursor.Reverse(true).Render(" " + label + " "))
		case strings.Contains(label, "("):
			b.WriteString(styleDim.Render(label))
		default:
			b.WriteString(label)
		}
	}
	return b.String()
}
