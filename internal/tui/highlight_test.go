package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestPaintRow(t *testing.T) {
	const on, off = "\x1b[48;5;236m", "\x1b[0m"
	line := "▸ " + "\x1b[1mrepo\x1b[0m" + " " + "\x1b[32mclean\x1b[0m"
	got := paintRow(line, on, off, 20)

	if w := ansi.StringWidth(got); w != 20 {
		t.Errorf("width %d, want 20: %q", w, got)
	}
	if !strings.HasPrefix(got, on) || !strings.HasSuffix(got, off) {
		t.Errorf("not wrapped in background: %q", got)
	}
	// Every inner reset must turn the background back on.
	body := strings.TrimSuffix(got, off)
	if n, m := strings.Count(body, "\x1b[0m"), strings.Count(body, "\x1b[0m"+on); n != m {
		t.Errorf("%d resets, only %d re-apply the background: %q", n, m, got)
	}
	if ansi.Strip(got) != ansi.Strip(line)+strings.Repeat(" ", 20-ansi.StringWidth(line)) {
		t.Errorf("text changed: %q", ansi.Strip(got))
	}
}

func TestPaintRowNoColour(t *testing.T) {
	if got := paintRow("▸ repo", "", "", 20); got != "▸ repo" {
		t.Errorf("colourless terminal got %q", got)
	}
}
