package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/forge"
	"gl-repos/internal/safe"
)

// wrapWords breaks s into lines of at most w cells at spaces, so a
// command's arguments stay whole where they fit; a word longer than w is
// split across lines by display width, never cut.
func wrapWords(s string, w int) []string {
	w = max(w, 1)
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case ansi.StringWidth(line)+1+ansi.StringWidth(word) <= w:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
		for ansi.StringWidth(line) > w {
			head, rest := splitWidth(line, w)
			lines = append(lines, head)
			line = rest
		}
	}
	return append(lines, line)
}

// splitWidth splits s after at most w cells (at least one rune).
func splitWidth(s string, w int) (head, rest string) {
	n := 0
	for i, r := range s {
		rw := ansi.StringWidth(string(r))
		if n+rw > w && i > 0 {
			return s[:i], s[i:]
		}
		n += rw
	}
	return s, ""
}

// wrapExact splits s into lines of at most w cells, breaking after a
// space where it can. Unlike wrapWords nothing is dropped or collapsed:
// the lines joined give s back, so a command reads exactly as it runs.
func wrapExact(s string, w int) []string {
	w = max(w, 1)
	var lines []string
	for ansi.StringWidth(s) > w {
		head, rest := splitWidth(s, w)
		if i := strings.LastIndex(head, " "); i > 0 && i < len(head)-1 {
			head, rest = s[:i+1], s[i+1:]
		}
		lines = append(lines, head)
		s = rest
	}
	return append(lines, s)
}

// --- text helpers ---

// showURL is a clone URL (or any text with URLs) as it may be shown:
// credentials hidden (forge.RedactURLs) and sanitized for the terminal.
// git still gets the URL as typed.
func showURL(u string) string { return safe.Line(forge.RedactURLs(u)) }

// showPath is a local path (the root, a repository, a destination) as
// it may be shown: the home directory as ~, and sanitized for the
// terminal, since a folder name is data (an unpacked archive can carry
// an escape sequence in one). git still gets the path as it is.
func showPath(p string) string { return safe.Line(shortHome(p)) }

func ansiTruncate(s string, w int) string { return ansi.Truncate(s, w, "…") }

// fit pads or truncates s to exactly w runes, truncating on the right.
func fit(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		if w <= 1 {
			return string(r[:w])
		}
		return string(r[:w-1]) + "…"
	}
	return s + strings.Repeat(" ", w-len(r))
}

// fitLeft is like fit but truncates on the left, keeping the end of
// paths. It counts cells, not runes, so a wide (CJK) name takes no more
// than w columns.
func fitLeft(s string, w int) string {
	sw := ansi.StringWidth(s)
	if sw > w {
		mark, room := "…", w-1
		if w <= 1 {
			mark, room = "", w
		}
		// keep the runes at the end that fit in room cells
		r := []rune(s)
		i, n := len(r), 0
		for ; i > 0; i-- {
			rw := ansi.StringWidth(string(r[i-1]))
			if n+rw > room {
				break
			}
			n += rw
		}
		s, sw = mark+string(r[i:]), ansi.StringWidth(mark)+n
	}
	return s + strings.Repeat(" ", w-sw)
}

// truncate cuts plain (unstyled) text to w columns; it also strips
// escapes and controls, as such text often carries remote output.
func truncate(s string, w int) string {
	s = safe.Line(s)
	r := []rune(s)
	if w <= 0 || len(r) <= w {
		return s
	}
	return string(r[:w])
}

// wrap wraps plain text, which is sanitized first (see truncate).
func wrap(s string, w int) string {
	s = safe.Text(s)
	if w < 20 {
		return s
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// shortHome writes the home directory as "~" (only the directory itself
// or a path below it, not a sibling like /home/user2).
func shortHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("2006-01-02")
}
