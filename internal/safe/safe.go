// Package safe makes untrusted text (git output, API data, model output)
// harmless to print in a terminal.
package safe

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text removes escape sequences and control characters, keeping
// newlines and tabs. Remote text could otherwise rewrite the clipboard
// (OSC 52), set links or the title, or redraw the screen.
func Text(s string) string { return clean(s, false) }

// Line is Text for single-line fields: newlines and tabs become spaces.
func Line(s string) string { return clean(s, true) }

// Colored keeps SGR color sequences (ESC [ … m), as printed by
// "git log --color", and removes every other escape or control.
func Colored(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == ';') {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				if visibleSGR(s[i+2 : j]) {
					b.WriteString(s[i : j+1])
				}
				i = j + 1
				continue
			}
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		if r == '\x1b' {
			i = skipEscape(s, i)
			continue
		}
		if keep(r, false) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func clean(s string, oneLine bool) string {
	if isPlain(s, oneLine) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == '\x1b':
			i = skipEscape(s, i)
		case oneLine && (r == '\n' || r == '\t'):
			b.WriteByte(' ')
		case keep(r, oneLine):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isPlain(s string, oneLine bool) bool {
	for _, r := range s {
		if r == '\x1b' || !keep(r, oneLine) || r == utf8.RuneError {
			return false
		}
	}
	return true
}

func keep(r rune, oneLine bool) bool {
	switch {
	case r == '\n' || r == '\t':
		return !oneLine
	case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0: // C0, DEL, C1
		return false
	case unicode.Is(unicode.Cf, r): // bidi overrides, zero-width: visual spoofing
		return false
	case r == utf8.RuneError:
		return false
	}
	return true
}

// skipEscape skips the rest of an escape sequence whose ESC ended at i:
// CSI (ESC [ … final), OSC/DCS/APC/PM/SOS (… BEL or ESC \), or a
// two-byte sequence.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		i++
		for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
			i++
		}
		return min(i+1, len(s))
	case ']', 'P', '_', '^', 'X':
		for i++; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return i
	}
	return i + 1
}

// Quote renders one command-line argument for display so that it reads
// exactly as it will be passed: plain words as is, anything with shell
// metacharacters in single quotes, and anything with control or invalid
// characters as a Go-quoted string, which shows them escaped.
func Quote(a string) string {
	if a == "" {
		return "''"
	}
	plain := true
	for _, r := range a {
		switch {
		case r == utf8.RuneError || r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 || unicode.Is(unicode.Cf, r):
			return strconv.QuoteToASCII(a)
		case !wordRune(r):
			plain = false
		}
	}
	if plain {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// wordRune reports runes that need no shell quoting.
func wordRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		strings.ContainsRune("@%+=:,./_-", r) || r > 0x7f
}

// visibleSGR reports whether SGR parameters only set colors and plain
// attributes: "conceal" (8, also written "08") could hide text, so such
// a sequence is dropped. Parameters are read as numbers, as a terminal
// reads them ("" is 0).
func visibleSGR(params string) bool {
	ps := strings.Split(params, ";")
	num := func(i int) int {
		if i >= len(ps) {
			return -1
		}
		n, _ := strconv.Atoi(ps[i])
		return n
	}
	for i := 0; i < len(ps); i++ {
		switch num(i) {
		case 38, 48, 58: // extended color: 5;n or 2;r;g;b
			switch num(i + 1) {
			case 5:
				i += 2
			case 2:
				i += 4
			}
		case 8:
			return false
		}
	}
	return true
}
