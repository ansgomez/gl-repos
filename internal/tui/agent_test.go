package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/config"
)

func agentKey(m Model, keys ...tea.KeyMsg) Model {
	for _, k := range keys {
		next, _ := m.handleAgentKey(k)
		m = next.(Model)
	}
	return m
}

func runeKey(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// The picker starts on the first agent, moves both ways with wrap-around,
// and starts the highlighted or numbered agent. The agents here are not
// installed, so which one was started shows in the flash.
func TestAgentPicker(t *testing.T) {
	m := New(Options{Root: "/w", Repos: []string{"/w/a"}, Agents: []config.Agent{
		{Name: "one", Command: "gl-repos-no-agent-one"},
		{Name: "two", Command: "gl-repos-no-agent-two --flag"},
		{Name: "three", Command: "sh -c true"},
	}})
	defer m.life.cancel()
	m.rows[0].loaded = true
	if cmd := m.openAgents(); cmd != nil || m.mode != modeAgent || m.agentSel != 0 {
		t.Fatalf("picker: mode %v sel %d", m.mode, m.agentSel)
	}
	line := ansi.Strip(m.agentLine())
	if !strings.Contains(line, "1 one (not installed)") || !strings.Contains(line, "2 two (not installed)") ||
		strings.Contains(line, "3 three (") {
		t.Errorf("line: %q", line)
	}

	for _, c := range []struct {
		key  tea.KeyMsg
		want int
	}{
		{tea.KeyMsg{Type: tea.KeyRight}, 1}, {runeKey('j'), 2}, {tea.KeyMsg{Type: tea.KeyTab}, 0},
		{tea.KeyMsg{Type: tea.KeyLeft}, 2}, {runeKey('K'), 1}, {tea.KeyMsg{Type: tea.KeyShiftTab}, 0},
		{tea.KeyMsg{Type: tea.KeyUp}, 2}, {tea.KeyMsg{Type: tea.KeyDown}, 0},
	} {
		if m = agentKey(m, c.key); m.agentSel != c.want || m.mode != modeAgent {
			t.Fatalf("after %s: sel %d (want %d), mode %v", c.key, m.agentSel, c.want, m.mode)
		}
	}

	m = agentKey(m, runeKey('l'), keyEnter)
	if m.mode != modeList || m.flash != "gl-repos-no-agent-two not found in PATH" {
		t.Errorf("enter: mode %v flash %q", m.mode, m.flash)
	}
	m.openAgentPicker()
	if m = agentKey(m, runeKey('1')); m.mode != modeList || m.flash != "gl-repos-no-agent-one not found in PATH" {
		t.Errorf("1: mode %v flash %q", m.mode, m.flash)
	}
	m.openAgentPicker()
	if m = agentKey(m, runeKey('9')); m.mode != modeAgent {
		t.Errorf("9 (no such agent) left the picker")
	}
	if m = agentKey(m, runeKey('T')); m.mode != modeList || m.flash != "" {
		t.Errorf("t: mode %v flash %q", m.mode, m.flash)
	}
}

// A status arriving while the picker is open can hide the row it was
// opened on (attention filter) and leave the cursor on another one: enter
// and the help still mean the repository the picker was opened on.
func TestAgentPickerKeepsRepo(t *testing.T) {
	m := New(Options{Root: "/w", Repos: []string{"/w/a", "/w/b"}, Agents: []config.Agent{
		{Name: "one", Command: "sh -c true"}, {Name: "two", Command: "sh -c true"},
	}})
	defer m.life.cancel()
	m.width = 200
	for _, r := range m.rows { // both need attention (no upstream)
		r.loaded = true
	}
	m.attnOnly = true
	m.rebuildView()
	a := m.currentIdx() // a, first by name
	m.openAgentPicker()
	m.rows[a].st.Upstream = "origin/main" // a status arrives: a is fine now (and marked with no risk) and leaves the view
	m.rebuildView()
	if m.currentIdx() == a {
		t.Fatal("test needs the cursor to move off the picker's row")
	}
	if help := strings.Join(m.helpLines(), " "); !strings.Contains(help, "start in "+m.rows[a].rel) {
		t.Errorf("help: %q", help)
	}
	next, cmd := m.handleAgentKey(keyEnter)
	m = next.(Model)
	if m.mode != modeList || cmd == nil {
		t.Fatalf("enter: mode %v, cmd %v", m.mode, cmd != nil)
	}
	// the start reads the target's config first; /w/a does not exist, so
	// that fails and the done message names the row
	if done, ok := cmd().(execDoneMsg); !ok || done.idx != a || done.err == nil {
		t.Fatalf("enter targeted %+v, want row %d", done, a)
	}
	m.openAgentPicker()
	m.rows[m.agentRow].gone = true
	if m = agentKey(m, keyEnter); m.confirm != nil || !strings.HasSuffix(m.flash, "is no longer listed") {
		t.Errorf("gone row: confirm %+v flash %q", m.confirm, m.flash)
	}
}

func TestAgentPickerSkippedForOneOrNone(t *testing.T) {
	m := New(Options{Root: "/w", Repos: []string{"/w/a"}, Agents: []config.Agent{{Name: "one", Command: "gl-repos-no-agent-one"}}})
	defer m.life.cancel()
	m.rows[0].loaded = true
	if m.openAgents(); m.mode == modeAgent || m.flash != "gl-repos-no-agent-one not found in PATH" {
		t.Errorf("one agent: mode %v flash %q", m.mode, m.flash)
	}
	m.opts.Agents = nil
	if m.openAgents(); m.mode == modeAgent || !strings.Contains(m.flash, "no agents configured") {
		t.Errorf("no agents: mode %v flash %q", m.mode, m.flash)
	}
}

// A relative command with a slash would run from the repository's folder,
// so a file the repository ships: refused before anything is looked up.
func TestExecRefusesRelativePath(t *testing.T) {
	m := New(Options{Root: "/w", Repos: []string{"/w/a"}})
	for _, cmd := range []string{"bin/tig", "./x", "node_modules/.bin/x"} {
		m.flash = ""
		if c := m.execArgv(0, []string{cmd}); c != nil || !strings.Contains(m.flash, "give a command name or an absolute path") {
			t.Errorf("%s: cmd %v, flash %q", cmd, c != nil, m.flash)
		}
	}
}
