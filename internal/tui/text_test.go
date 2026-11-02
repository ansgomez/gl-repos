package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The details pane shows the first non-blank lines of a command's output,
// redacted and sanitized, and reads no further: the output may be as long
// as MaxOutput and the pane is drawn on every frame. The clone box's last
// line scans from the end the same way.
func TestOutputLinesAndLastLineReadOnlyWhatIsShown(t *testing.T) {
	out := "\r\n  \nCloning into 'p'...\r\nremote: https://oauth2:SECRET@h/p.git\x1b]52;c;x\x07\n\tdone\n\n"
	want := []string{"Cloning into 'p'...", "remote: https://***@h/p.git", " done"}
	if got := outputLines(out, 9); !slices.Equal(got, want) {
		t.Errorf("outputLines = %q, want %q", got, want)
	}
	if got := outputLines(out, 2); !slices.Equal(got, want[:2]) {
		t.Errorf("outputLines(2) = %q, want %q", got, want[:2])
	}
	if got := outputLines(out, 0); got != nil {
		t.Errorf("outputLines(0) = %q", got)
	}
	huge := "first\n" + strings.Repeat("x\n", 1<<20) + "last\n \n"
	if got := outputLines(huge, 1); !slices.Equal(got, []string{"first"}) {
		t.Errorf("outputLines(huge, 1) = %q", got)
	}
	if got := rawLastLine(huge, nil); got != "last" {
		t.Errorf("rawLastLine = %q, want last", got)
	}
	err := errors.New("exit status 128")
	for in, want := range map[string]string{"a\nb\n": "b", "a\n\n  b  \n\n": "b", "only": "only", "\n \n": err.Error(), "": err.Error()} {
		if got := rawLastLine(in, err); got != want {
			t.Errorf("rawLastLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortHome(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for p, want := range map[string]string{
		"/home/user":        "~",
		"/home/user/git/x":  "~/git/x",
		"/home/user2/git/x": "/home/user2/git/x", // a sibling, not home
		"/home/users":       "/home/users",
		"/srv/x":            "/srv/x",
	} {
		if got := shortHome(p); got != want {
			t.Errorf("shortHome(%q) = %q, want %q", p, got, want)
		}
	}
}

// fitLeft fills exactly w cells, also for wide (CJK) names: the create
// view's command column must not push the command past the width.
func TestFitLeft(t *testing.T) {
	for _, tc := range []struct {
		in   string
		w    int
		want string
	}{
		{"abc", 5, "abc  "},
		{"abcdef", 4, "…def"},
		{"abcdef", 1, "f"},
		{"日本語", 6, "日本語"},
		{"日本語", 4, "…語 "}, // 本 does not fit in the 3 cells left
		{"x日本語", 5, "…本語"},
	} {
		if got := fitLeft(tc.in, tc.w); got != tc.want {
			t.Errorf("fitLeft(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
		}
	}
}
