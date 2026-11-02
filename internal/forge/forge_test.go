package forge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUntrustedProjectsCleaned(t *testing.T) {
	g := GitLab{Host: "h", API: func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(`[` +
			`{"path_with_namespace":"acme/tools/../../../../tmp/x","path":"x"},` +
			`{"path_with_namespace":"acme/-oops","path":"-oops"},` +
			`{"path_with_namespace":"acme/ti\u001b]52;c;eA==\u0007tle","path":"t"},` +
			`{"path_with_namespace":"acme/ok","path":"ok","ssh_url_to_repo":"git@h:acme/ok.git","description":"fine\u001b]0;pwned\u0007 text\nmore"},` +
			`{"path_with_namespace":"acme/.github","path":".github"},` +
			`{"path_with_namespace":"acme/x/.Git","path":".Git"},` +
			`{"path_with_namespace":"acme/.git/x","path":"x"},` +
			`{"path_with_namespace":"acme/badurl","path":"badurl","ssh_url_to_repo":"git@h:x\n.git"}]`), nil
	}}
	ps, err := g.ListAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if names(ps) != "acme/ok,acme/.github" {
		t.Fatalf("kept %s", names(ps))
	}
	if ps[0].Description != "fine text more" {
		t.Errorf("description = %q", ps[0].Description)
	}
}

// A server-supplied clone URL never brings credentials along.
func TestProjectURLUserinfoStripped(t *testing.T) {
	g := GitLab{Host: "h", API: func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(`[{"path_with_namespace":"acme/a","path":"a","ssh_url_to_repo":"ssh://git:pw@h/acme/a.git",` +
			`"http_url_to_repo":"https://oauth2:glpat-xxx@h/acme/a.git"}]`), nil
	}}
	ps, err := g.ListAll(context.Background())
	if err != nil || len(ps) != 1 {
		t.Fatalf("%v: kept %s", err, names(ps))
	}
	if ps[0].HTTPURL != "https://h/acme/a.git" || ps[0].SSHURL != "ssh://git@h/acme/a.git" {
		t.Errorf("urls %q, %q", ps[0].HTTPURL, ps[0].SSHURL)
	}
	for in, want := range map[string]string{
		"https://oauth2:glpat-xxx@host/p.git":         "https://host/p.git",
		"HTTP://user@gitlab.example.com:8443/acme/x":  "HTTP://gitlab.example.com:8443/acme/x",
		"https://u:t?k@gitlab.example.com/acme/x.git": "https://gitlab.example.com/acme/x.git",
		"https://gitlab.example.com/acme/a@b.git":     "https://gitlab.example.com/acme/a@b.git",
		"ssh://git:secret@gitlab.example.com/acme/x":  "ssh://git@gitlab.example.com/acme/x",
		"ssh://git@gitlab.example.com/acme/x":         "ssh://git@gitlab.example.com/acme/x",
		"git@gitlab.example.com:acme/x.git":           "git@gitlab.example.com:acme/x.git",
		"":                                            "",
	} {
		if got := StripCredentials(in); got != want {
			t.Errorf("StripCredentials(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactURLs(t *testing.T) {
	for in, want := range map[string]string{
		"https://oauth2:glpat-secret@gitlab.example.com/acme/tools.git":              "https://***@gitlab.example.com/acme/tools.git",
		"https://ghp_secret@github.com/acme/tools":                                   "https://***@github.com/acme/tools",
		"HTTP://u:p@gitlab.example.com:8443":                                         "HTTP://***@gitlab.example.com:8443",
		"https://oauth2:glpat-xxx@host/p.git":                                        "https://***@host/p.git",
		"https://u:p@ss@gitlab.example.com/acme/tools.git":                           "https://***@gitlab.example.com/acme/tools.git",
		"https://u:t?k#n@gitlab.example.com/acme/tools.git":                          "https://***@gitlab.example.com/acme/tools.git",
		"https://gitlab.example.com/acme/tools.git":                                  "https://gitlab.example.com/acme/tools.git",
		"https://gitlab.example.com/acme/a@b.git":                                    "https://gitlab.example.com/acme/a@b.git",
		"ssh://git:secret@gitlab.example.com/acme/tools.git":                         "ssh://git:***@gitlab.example.com/acme/tools.git",
		"ssh://git@gitlab.example.com/acme/tools.git":                                "ssh://git@gitlab.example.com/acme/tools.git",
		"git@gitlab.example.com:acme/tools.git":                                      "git@gitlab.example.com:acme/tools.git",
		"":                                                                           "",
		"fatal: unable to access 'https://oauth2:glpat-xxx@host/p.git/': 403 by a@b": "fatal: unable to access 'https://***@host/p.git/': 403 by a@b",
		"from https://a:x@h1/p and ssh://git:y@h2/q":                                 "from https://***@h1/p and ssh://git:***@h2/q",
		"https://oauth2:glpat\x1b]8;; /x\x07SECRET@host/p.git":                       "https://***@host/p.git",
		"https://host/p.git?private_token=glpat-x&ref=main":                          "https://host/p.git?private_token=***&ref=main",
		"see https://h/x?page=2&access_token=abc#top":                                "see https://h/x?page=2&access_token=***#top",
		"https://h/p.git?%70rivate_token=SECRET":                                     "https://h/p.git?%70rivate_token=***",
		"https://h/p.git#access_token=SECRET":                                        "https://h/p.git#access_token=***",
		"https://h/p.git;private_token=SECRET":                                       "https://h/p.git;private_token=***",
		"lone ?private_token=SECRET":                                                 "lone ?private_token=***",
		"oauth2:glpat-SECRET@gitlab.example.com/acme/tools.git":                      "oauth2:***@gitlab.example.com/acme/tools.git",
		"fatal: 'oauth2:SECRET@h/p.git' does not appear":                             "fatal: 'oauth2:***@h/p.git' does not appear",
		"https://h/p.git?x=1%26private_token=SECRET":                                 "https://h/p.git?x=1%26private_token=***",
		"?a=b%26token%3DSECRET%26c=d":                                                "?a=b%26token%3D***%26c=d",
		"%3Fprivate_token%3DSECRET":                                                  "%3Fprivate_token%3D***",
		"tools?private_token=SECRET":                                                 "tools?private_token=***",
	} {
		if got := RedactURLs(in); got != want {
			t.Errorf("RedactURLs(%q) = %q, want %q", in, got, want)
		}
	}
	in := "https://oauth2:glpat-x@host/p and ssh://git:pw@h/q"
	if got, want := MaskURLs(in), "https://**************@host/p and ssh://git:**@h/q"; got != want {
		t.Errorf("MaskURLs = %q, want %q", got, want)
	}
	in = "oauth2:glpat-x@host/p.git#access_token=ab"
	if got, want := MaskURLs(in), "oauth2:*******@host/p.git#access_token=**"; got != want {
		t.Errorf("MaskURLs = %q, want %q", got, want)
	}
	in = "\x1b[33mcommit\x1b[m see https://oauth2:SECRET@h/p.git;private_token=SECRET"
	if got, want := RedactColored(in), "\x1b[33mcommit\x1b[m see https://***@h/p.git;private_token=***"; got != want {
		t.Errorf("RedactColored = %q, want %q", got, want)
	}
	// a color code inside the parameter name splits the match: the text
	// is shown uncolored, redacted
	in = "\x1b[33mcommit\x1b[m ?private_\x1b[1mtoken=SECRET"
	if got, want := RedactColored(in), "commit ?private_token=***"; got != want {
		t.Errorf("RedactColored = %q, want %q", got, want)
	}
}

func TestCheckPathRejectsOptions(t *testing.T) {
	for _, p := range []string{"me/-gother", "-x/y", "me/.", "me/..", "me/a b", "acme/x.git/y", "acme/y.GIT"} {
		if CheckPath(p) == nil {
			t.Errorf("CheckPath(%q) accepted", p)
		}
	}
	if CheckPath("me/.dotfiles") != nil {
		t.Errorf(".dotfiles rejected")
	}
}

func TestCLIEnvDropsNoPrompt(t *testing.T) {
	t.Setenv("NO_PROMPT", "1")
	env := strings.Join(cliEnv(), "\n")
	if strings.Contains("\n"+env, "\nNO_PROMPT=") || !strings.Contains(env, "GLAB_NO_PROMPT=1") {
		t.Errorf("env: NO_PROMPT kept or GLAB_NO_PROMPT missing")
	}
}

func TestHasCredentials(t *testing.T) {
	for u, want := range map[string]bool{
		"https://oauth2:glpat-x@host/p.git": true,
		"https://user@host/p.git":           true,
		"HTTP://u:p@host:8443/p":            true,
		"ssh://git:pw@host/p.git":           true,
		"git+ssh://git:pw@host/p.git":       true,
		"ssh://git@host/p.git":              false,
		"git@host:acme/p.git":               false,
		"https://host/acme/a@b.git":         false,
		"https://host/p.git":                false,
		"acme/tools":                        false,
		"https://h/p.git?%70rivate_token=x": true,
		"https://h/p.git#access_token=x":    true,
		"https://h/p.git;private_token=x":   true,
		"https://h/docs#section=2":          false,
	} {
		if got := HasCredentials(u); got != want {
			t.Errorf("HasCredentials(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestQueryCredentials(t *testing.T) {
	for in, want := range map[string]bool{
		"https://host/p.git?private_token=glpat-x": true,
		"https://host/p.git?api_key=k":             true,
		"https://host/docs?page=2":                 false,
		"https://host/p.git":                       false,
		"plain text ?token=x without a URL":        true, // hidden on screen, so refused too
	} {
		if got := HasCredentials(in); got != want {
			t.Errorf("HasCredentials(%q) = %v", in, got)
		}
	}
	if got := StripCredentials("https://h/acme/a.git?private_token=x"); got != "https://h/acme/a.git" {
		t.Errorf("StripCredentials = %q", got)
	}
	in := "https://h/p?private_token=abc"
	if got := MaskURLs(in); got != "https://h/p?private_token=***" || len(got) != len(in) {
		t.Errorf("MaskURLs = %q", got)
	}
}

func TestLastMeaningfulHidesToken(t *testing.T) {
	out := "github.com\n  X Failed to log in to github.com account octo (keyring)\n  - Token: gho_secretvalue\n"
	if got := lastMeaningful(out); strings.Contains(got, "gho_") {
		t.Errorf("lastMeaningful = %q", got)
	}
	if got := lastMeaningful("  - Token: gho_secretvalue\n"); got != "failed" {
		t.Errorf("only a token line: %q", got)
	}
}

// credentialShapes are the inputs of every finding so far: each is
// hidden on screen, found by HasCredentials, refused as an argument and
// never searched. A new finding goes here (and in the TUI's
// secretShapes).
var credentialShapes = []string{
	"https://oauth2:SECRET@h/p.git",
	"https://SECRET@h/p.git",
	"git://SECRET@h/p.git",
	"git://oauth2:SECRET@h/p.git",
	"ftp://SECRET@h/p.git", // a scheme gl-repos does not clone: its user is a secret too
	"ssh://git:SECRET@h/p.git",
	"oauth2:SECRET@h/p.git",
	"oauth2:SECRET@h:acme/p.git",
	"https://h/p.git?private_token=SECRET",
	"https://h/p.git?%70rivate_token=SECRET",
	"https://h/p.git#access_token=SECRET",
	"https://h/p.git;private_token=SECRET",
	"https://h/p.git?x=1%26private_token=SECRET",
	"?private_token=SECRET",
	"#access_token=SECRET",
	";private_token=SECRET",
	"?%70rivate_token=SECRET",
	"?x=1%26private_token=SECRET",
	"%3Fprivate_token%3DSECRET",
	"tools?private_token=SECRET",
	"acme/tools#access_token=SECRET",
	"acme/tools;private_token=SECRET",
	"tools oauth2:SECRET@h",
	"https://oauth2:SEC\x1b[0mRET@h/p.git",
	// valid ref names (git check-ref-format allows # ; = : @)
	"main#access_token=SECRET",
	"fix/x;private_token=SECRET",
	"origin/main#access_token=SECRET",
	"fix/oauth2:SECRET@h/p.git",
	"origin/oauth2:SECRET@h",
}

// What RedactURLs and MaskURLs hide, HasCredentials finds, and the
// other way round: one definition.
func TestCredentialShapesAgree(t *testing.T) {
	for _, s := range credentialShapes {
		if !HasCredentials(s) {
			t.Errorf("HasCredentials(%q) = false", s)
		}
		for name, f := range map[string]func(string) string{"RedactURLs": RedactURLs, "RedactLine": RedactLine, "MaskURLs": MaskURLs} {
			if got := f(s); strings.Contains(got, "SECRET") || got == s {
				t.Errorf("%s(%q) = %q", name, s, got)
			}
		}
		if err := CheckArgs([]string{"x", s}); !errors.Is(err, ErrURLCredentials) {
			t.Errorf("CheckArgs(%q): %v", s, err)
		}
	}
	for _, s := range []string{"git@h:acme/p.git", "ssh://git@h/p.git", "https://h/p.git", "https://h/docs#section=2",
		"acme/tools", "search=tools", "projects?membership=true&simple=true&search_namespaces=true&order_by=last_activity_at&per_page=50&search=x",
		"groups?min_access_level=10&per_page=100&order_by=path", "user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member",
		"name=tools", "parent_id=12", "visibility=private", "fix: 100% done?", "see a:b"} {
		if HasCredentials(s) || RedactURLs(s) != s || MaskURLs(s) != s {
			t.Errorf("%q taken for a credential: %q", s, RedactURLs(s))
		}
	}
}

// glab, gh and forge commands never get an argument with credentials.
func TestCLIArgsWithCredentialsRefused(t *testing.T) {
	for _, a := range credentialShapes {
		if _, err := cliCommand(context.Background(), "glab", "api", a); !errors.Is(err, ErrURLCredentials) {
			t.Errorf("cliCommand(%q): %v", a, err)
		}
		out, err := Cmd{Name: "sh", Args: []string{"-c", "echo ran", a}}.Run(context.Background())
		if !errors.Is(err, ErrURLCredentials) || out != "" {
			t.Errorf("Cmd.Run(%q): %q, %v", a, out, err)
		}
	}
	if _, err := cliCommand(context.Background(), "glab", "api", "--hostname", "h", "user"); err != nil {
		t.Errorf("plain args refused: %v", err)
	}
}

// Search text shaped like a URL or credentials never reaches the
// server (or glab's and gh's arguments).
func TestSearchRefusesCredentialShapes(t *testing.T) {
	api := func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
		t.Errorf("asked for %s", ep)
		return nil, errors.New("no")
	}
	for _, f := range []Forge{GitLab{Host: "h", API: api}, GitHub{API: api}} {
		for _, q := range append([]string{"oauth2:glpat-SECRET@gitlab.example.com/acme/tools", "find https://h/p.git"}, credentialShapes...) {
			if _, err := f.Search(context.Background(), q); !errors.Is(err, ErrNotSearched) || strings.Contains(err.Error(), "SECRET") {
				t.Errorf("%s Search(%q): %v", f.Name(), q, err)
			}
		}
	}
}

// A CLI that writes more than MaxOutput is stopped, and the call fails
// with ErrTooLarge instead of buffering the rest.
func TestOutputCapped(t *testing.T) {
	old := MaxOutput
	MaxOutput = 4096
	t.Cleanup(func() { MaxOutput = old })
	ctx := context.Background()
	cmd, err := cliCommand(ctx, "sh", "-c", "head -c 100000 /dev/zero")
	if err != nil {
		t.Fatal(err)
	}
	if out, _, err := Output(cmd); !errors.Is(err, ErrTooLarge) || int64(len(out)) > MaxOutput {
		t.Errorf("stdout: %d bytes, %v", len(out), err)
	}
	cmd, _ = cliCommand(ctx, "sh", "-c", "head -c 100000 /dev/zero >&2")
	if out, err := CombinedOutput(cmd); !errors.Is(err, ErrTooLarge) || int64(len(out)) > MaxOutput {
		t.Errorf("combined stderr: %d bytes, %v", len(out), err)
	}
	cmd, _ = cliCommand(ctx, "sh", "-c", "echo ok; echo warn >&2")
	if out, stderr, err := Output(cmd); err != nil || string(out) != "ok\n" || string(stderr) != "warn\n" {
		t.Errorf("small output: %q %q %v", out, stderr, err)
	}
	cmd, _ = cliCommand(ctx, "sh", "-c", "echo ok; echo warn >&2")
	if out, err := CombinedOutput(cmd); err != nil || string(out) != "ok\nwarn\n" {
		t.Errorf("small combined output: %q %v", out, err)
	}
}
