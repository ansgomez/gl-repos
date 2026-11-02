package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/forge"
)

func profileKey(m Model, keys ...tea.KeyMsg) Model {
	for _, k := range keys {
		next, _ := m.handleProfileKey(k)
		m = next.(Model)
	}
	return m
}

// A profile switch needs y: any other key keeps the active profile, and
// ←→ choose among the others (never the active one).
func TestProfileSwitchNeedsY(t *testing.T) {
	m := New(Options{Root: "/w", Profiles: []Profile{
		{Name: "work", Forge: forge.GitLab{Host: "gitlab.example.com"}},
		{Name: "home", Forge: forge.GitHub{}},
		{Name: "lab", Forge: forge.GitLab{}},
	}})
	defer m.life.cancel()

	m.openProfileSwitch()
	if m.mode != modeProfile || m.profSel != 1 {
		t.Fatalf("open: mode %v target %d", m.mode, m.profSel)
	}
	if line := ansi.Strip(m.profileLine()); !strings.Contains(line, "work · GitLab") || !strings.Contains(line, "home · GitHub") ||
		!strings.Contains(line, "y/N") || !strings.Contains(line, "←→ other profile") {
		t.Errorf("line: %q", line)
	}
	if m = profileKey(m, runeKey('n')); m.mode != modeList || m.prof != 0 || m.flash != "profile unchanged: work" {
		t.Errorf("n: mode %v prof %d flash %q", m.mode, m.prof, m.flash)
	}
	m.openProfileSwitch()
	if m = profileKey(m, keyEnter); m.prof != 0 || m.mode != modeList {
		t.Errorf("enter switched: prof %d", m.prof)
	}

	m.openProfileSwitch()
	for _, c := range []struct {
		key  tea.KeyMsg
		want int
	}{
		{tea.KeyMsg{Type: tea.KeyRight}, 2}, {runeKey('l'), 1}, // skips the active work
		{tea.KeyMsg{Type: tea.KeyLeft}, 2}, {runeKey('h'), 1}, {tea.KeyMsg{Type: tea.KeyTab}, 2},
		{runeKey('H'), 1}, {runeKey('L'), 2}, // shortcuts ignore case, the direction too
	} {
		if m = profileKey(m, c.key); m.profSel != c.want || m.prof != 0 || m.mode != modeProfile {
			t.Fatalf("after %s: target %d (want %d), prof %d", c.key, m.profSel, c.want, m.prof)
		}
	}
	m.projLoading = true
	m = profileKey(m, runeKey('Y'))
	if m.prof != 2 || m.mode != modeList || m.projLoading || m.flash != "profile: lab (GitLab · host from glab login via glab)" {
		t.Errorf("y: prof %d mode %v loading %v flash %q", m.prof, m.mode, m.projLoading, m.flash)
	}
	if m.forge().Name() != "GitLab" || m.forge().Hostname() != "" {
		t.Errorf("forge after switch: %s %q", m.forge().Name(), m.forge().Hostname())
	}
}

func TestProfileSwitchNeedsTwo(t *testing.T) {
	m := New(Options{Root: "/w", Profiles: []Profile{{Name: "work", Forge: forge.GitLab{}}}})
	defer m.life.cancel()
	if m.openProfileSwitch(); m.mode == modeProfile || !strings.Contains(m.flash, "only one profile") {
		t.Errorf("mode %v flash %q", m.mode, m.flash)
	}
}
