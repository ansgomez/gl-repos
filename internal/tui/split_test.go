package tui

import (
	"reflect"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	for in, want := range map[string][]string{
		"claude":                           {"claude"},
		"  codex   --model  o4 ":           {"codex", "--model", "o4"},
		`"/Applications/My Tool/bin/x" -v`: {"/Applications/My Tool/bin/x", "-v"},
		`kimi --prompt 'it''s'`:            {"kimi", "--prompt", "its"},
		`a "b c" 'd "e"' f\ g`:             {"a", "b c", `d "e"`, "f g"},
		`x ""`:                             {"x", ""},
		`echo $HOME;rm`:                    {"echo", "$HOME;rm"},
	} {
		got, err := SplitCommand(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("SplitCommand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := SplitCommand(`a "b`); err == nil {
		t.Errorf("unclosed quote accepted")
	}
}
