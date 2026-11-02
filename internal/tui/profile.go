package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gl-repos/internal/forge"
)

var badgeColors = []string{"4", "5", "3", "2", "6"}

// forge returns the active profile's forge (GitLab with an unknown host
// if no profile is configured).
func (m Model) forge() forge.Forge {
	if len(m.profiles) == 0 {
		return forge.GitLab{}
	}
	return m.profiles[m.prof].Forge
}

// setHost remembers a detected host for the active profile.
func (m *Model) setHost(h string) {
	if len(m.profiles) > 0 && h != "" && m.profiles[m.prof].Forge.Hostname() == "" {
		m.profiles[m.prof].Forge = m.profiles[m.prof].Forge.WithHost(h)
	}
}

// badge labels a profile, colored so the active account is never in
// doubt: " work · GitLab ".
func (m Model) badge(i int) string {
	if i < 0 || i >= len(m.profiles) {
		return ""
	}
	p := m.profiles[i]
	color := p.Color
	if color == "" {
		color = badgeColors[i%len(badgeColors)]
	}
	// black on the badge color reads poorly in light themes, whose black is gray
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "15", Dark: "0"}).
		Background(lipgloss.Color(color)).Render(" " + p.Name + " · " + p.Forge.Name() + " ")
}

// profileInfo describes where a profile points: "GitHub · github.com via gh".
func (m Model) profileInfo(i int) string {
	f := m.profiles[i].Forge
	host := f.Hostname()
	if host == "" {
		host = "host from " + f.CLI() + " login"
	}
	s := f.Name() + " · " + host + " via " + f.CLI()
	if me := m.profiles[i].me; me != "" {
		s += " as " + me
	}
	return s
}

func (m *Model) openProfileSwitch() {
	if len(m.profiles) < 2 {
		m.flash = "only one profile configured ([[profile]] in the config)"
		return
	}
	m.mode = modeProfile
	m.profSel = (m.prof + 1) % len(m.profiles)
}

// handleProfileKey confirms a profile switch: y switches, ←→ choose
// another target when there are more than two, anything else cancels.
func (m Model) handleProfileKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.profiles)
	switch lowerKey(msg) {
	case "y":
		m.prof = m.profSel
		m.projLoading = false // a load for the old profile is ignored on arrival
		m.mode = modeList
		m.flash = "profile: " + m.profiles[m.prof].Name + " (" + m.profileInfo(m.prof) + ")"
	case "left", "h", "shift+tab", "right", "l", "tab":
		step := 1
		if k := lowerKey(msg); k == "left" || k == "h" || k == "shift+tab" {
			step = n - 1
		}
		for {
			m.profSel = (m.profSel + step) % n
			if m.profSel != m.prof {
				break
			}
		}
	default:
		m.mode = modeList
		m.flash = "profile unchanged: " + m.profiles[m.prof].Name
	}
	return m, nil
}

func (m Model) profileLine() string {
	s := fmt.Sprintf("switch profile %s → %s  %s?  y/N", m.badge(m.prof), m.badge(m.profSel), m.profileInfo(m.profSel))
	if len(m.profiles) > 2 {
		s += styleDim.Render("  ←→ other profile")
	}
	return s
}
