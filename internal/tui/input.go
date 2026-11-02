package tui

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gl-repos/internal/forge"
	"gl-repos/internal/safe"
)

// lineInput is a minimal single-line text field: insert, delete, cursor
// movement, ctrl+u/ctrl+w. Enough for prompts without extra dependencies.
type lineInput struct {
	Prompt      string
	Placeholder string
	CharLimit   int
	value       []rune
	pos         int
}

var styleCursorBlock = lipgloss.NewStyle().Reverse(true)

func (in *lineInput) Reset()        { in.value, in.pos = nil, 0 }
func (in *lineInput) Value() string { return string(in.value) }
func (in *lineInput) CursorEnd()    { in.pos = len(in.value) }

func (in *lineInput) SetValue(s string) {
	in.value = []rune(safe.Line(s)) // values may come from a server or the model
	in.pos = min(in.pos, len(in.value))
}

func (in *lineInput) Update(msg tea.Msg) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return
	}
	switch k.Type {
	case tea.KeyRunes:
		in.insert(k.Runes)
	case tea.KeySpace:
		in.insert([]rune{' '})
	case tea.KeyBackspace:
		if in.pos > 0 {
			in.value = append(in.value[:in.pos-1], in.value[in.pos:]...)
			in.pos--
		}
	case tea.KeyDelete:
		if in.pos < len(in.value) {
			in.value = append(in.value[:in.pos], in.value[in.pos+1:]...)
		}
	case tea.KeyLeft:
		in.pos = max(in.pos-1, 0)
	case tea.KeyRight:
		in.pos = min(in.pos+1, len(in.value))
	case tea.KeyHome, tea.KeyCtrlA:
		in.pos = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		in.pos = len(in.value)
	case tea.KeyCtrlU:
		in.value, in.pos = in.value[in.pos:], 0
	case tea.KeyCtrlW:
		start := in.pos
		for start > 0 && unicode.IsSpace(in.value[start-1]) {
			start--
		}
		for start > 0 && !unicode.IsSpace(in.value[start-1]) {
			start--
		}
		in.value = append(in.value[:start], in.value[in.pos:]...)
		in.pos = start
	}
}

func (in *lineInput) insert(rs []rune) {
	// pasted text may hold escape sequences: drop them before they can
	// hide a URL's credentials from MaskURLs or reach the terminal
	rs = []rune(safe.Line(string(rs)))
	if in.CharLimit > 0 && len(in.value)+len(rs) > in.CharLimit {
		rs = rs[:max(in.CharLimit-len(in.value), 0)]
	}
	v := make([]rune, 0, len(in.value)+len(rs))
	v = append(v, in.value[:in.pos]...)
	v = append(v, rs...)
	v = append(v, in.value[in.pos:]...)
	in.value = v
	in.pos += len(rs)
}

// View renders the field. Credentials in a typed or pasted URL show as
// "*" (forge.MaskURLs keeps every rune in place, so the cursor stays
// right; a wide rune in a credential becomes one narrow "*", which can
// shift the cursor a few cells, harmless for real tokens, which are
// ASCII); Value still returns them for the clone itself.
func (in lineInput) View() string {
	if len(in.value) == 0 {
		return in.Prompt + styleCursorBlock.Render(" ") + styleDim.Render(in.Placeholder)
	}
	shown := []rune(forge.MaskURLs(string(in.value)))
	if len(shown) != len(in.value) {
		shown = []rune(strings.Repeat("*", len(in.value))) // never: MaskURLs keeps the length
	}
	cur := " "
	if in.pos < len(shown) {
		cur = string(shown[in.pos])
	}
	after := ""
	if in.pos < len(shown) {
		after = string(shown[in.pos+1:])
	}
	return in.Prompt + string(shown[:in.pos]) + styleCursorBlock.Render(cur) + after
}
