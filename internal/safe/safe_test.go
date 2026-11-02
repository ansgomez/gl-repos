package safe

import "testing"

func TestText(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":                                 "plain text",
		"two\nlines\tok":                             "two\nlines\tok",
		"clip\x1b]52;c;ZWNobyBoaQ==\x07board":        "clipboard",
		"title\x1b]0;pwned\x1b\\x":                   "titlex",
		"clear\x1b[2Jscreen\x1b[31mred":              "clearscreenred",
		"cr\roverwrite\bbs\x00nul\x7fdel":            "croverwritebsnuldel",
		"c1\u009b31mx":                               "c131mx",
		"link\x1b]8;;http://evil\x07txt\x1b]8;;\x07": "linktxt",
		"bad\xffutf8 ünïcode 日本":                     "badutf8 ünïcode 日本",
		"dangling\x1b":                               "dangling",
	} {
		if got := Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Text("main\u202Eslpmax\u200Be"); got != "mainslpmaxe" {
		t.Errorf("bidi/zero-width kept: %q", got)
	}
	if got := Line("a\nb\tc\x1b[1m"); got != "a b c" {
		t.Errorf("Line = %q", got)
	}
}

func TestColored(t *testing.T) {
	in := "\x1b[33m* abc\x1b[m \x1b]52;c;eA==\x07subject\x1b[2J\r"
	if got, want := Colored(in), "\x1b[33m* abc\x1b[m subject"; got != want {
		t.Errorf("Colored = %q, want %q", got, want)
	}
	if got := Colored("a\x1b[8mhidden\x1b[38;5;8mgrey"); got != "ahidden\x1b[38;5;8mgrey" {
		t.Errorf("conceal kept or 256-color 8 dropped: %q", got)
	}
	for _, sgr := range []string{"\x1b[08m", "\x1b[008m", "\x1b[1;08m", "\x1b[38;5;1;8m"} {
		if got := Colored("a" + sgr + "b"); got != "ab" {
			t.Errorf("conceal %q kept: %q", sgr, got)
		}
	}
	if in := "\x1b[38;2;8;8;8mx\x1b[48;5;08my\x1b[m"; Colored(in) != in {
		t.Errorf("color operand 8 dropped: %q", Colored(in))
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"main":                "main",
		"git@host:a/b.git":    "git@host:a/b.git",
		"Fix typo":            "'Fix typo'",
		"it's":                `'it'\''s'`,
		"a;rm -rf ~":          "'a;rm -rf ~'",
		"":                    "''",
		"x\x1b]52;c;eA==\x07": `"x\x1b]52;c;eA==\a"`,
		"two\nlines":          `"two\nlines"`,
		"ünïcode":             "ünïcode",
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}
