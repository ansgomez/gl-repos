package forge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGitHubListAndSearch(t *testing.T) {
	var flags []string
	g := GitHub{Exclude: []string{"secretorg"}, API: func(_ context.Context, host, ep string, fl ...string) ([]byte, error) {
		if host != "github.com" {
			t.Errorf("host = %q", host)
		}
		switch {
		case strings.HasPrefix(ep, "user/repos"):
			flags = fl
			return []byte(`[{"full_name":"me/dotfiles","name":"dotfiles","ssh_url":"git@github.com:me/dotfiles.git","pushed_at":"2026-01-02T03:04:05Z"}]` +
				`[{"full_name":"secretorg/plans","name":"plans"},{"full_name":"acme/weather-cli","name":"weather-cli","description":null,"pushed_at":null}]`), nil
		case ep == "repos/me/dotfiles":
			return []byte(`{"full_name":"me/dotfiles","name":"dotfiles"}`), nil
		}
		return nil, errors.New("404")
	}}
	ps, err := g.ListAll(context.Background())
	if err != nil || len(ps) != 2 || ps[0].Path != "me/dotfiles" || ps[1].Name != "weather-cli" {
		t.Fatalf("ListAll = %+v, %v", ps, err)
	}
	if ps[0].SSHURL == "" || ps[0].LastActivity.IsZero() {
		t.Errorf("fields not mapped: %+v", ps[0])
	}
	if len(flags) != 1 || flags[0] != "--paginate" {
		t.Errorf("flags = %v", flags)
	}
	if ps, err := g.Search(context.Background(), "me/dotfiles"); err != nil || len(ps) != 1 {
		t.Errorf("direct = %+v, %v", ps, err)
	}
	if ps, err := g.Search(context.Background(), "weather"); err != nil || len(ps) != 1 || ps[0].Path != "acme/weather-cli" {
		t.Errorf("match = %+v, %v", ps, err)
	}
	if _, err := g.Search(context.Background(), "plans"); !errors.Is(err, ErrNotFound) {
		t.Errorf("excluded org found: %v", err)
	}
}

func TestCreateCmds(t *testing.T) {
	for _, c := range []struct {
		f    Forge
		path string
		vis  string
		want string
	}{
		{GitHub{}, "me/tool", "private", "GH_HOST=github.com gh repo create me/tool --private"},
		{GitHub{Host: "ghe.example.com"}, "org/tool", "public", "GH_HOST=ghe.example.com gh repo create org/tool --public"},
		{GitLab{Host: "git.example.edu"}, "me/tool", "private", "GITLAB_HOST=git.example.edu glab repo create tool --private --skipGitInit"},
		{GitLab{Host: "git.example.edu"}, "acme/tools/cli", "internal", "GITLAB_HOST=git.example.edu glab repo create cli --group acme/tools --internal --skipGitInit"},
	} {
		if got := c.f.CreateCmd(c.path, c.vis, "me").String(); got != c.want {
			t.Errorf("got  %s\nwant %s", got, c.want)
		}
	}
	if u := (GitHub{}).RemoteURL("me/tool", true); u != "git@github.com:me/tool.git" {
		t.Errorf("ssh url %q", u)
	}
	if u := (GitLab{Host: "h"}).RemoteURL("a/b", false); u != "https://h/a/b.git" {
		t.Errorf("https url %q", u)
	}
}

func TestCheckPath(t *testing.T) {
	for p, ok := range map[string]bool{
		"me/tool": true, "acme/tools/cli-2": true, "tool": false, "me/../x": false,
		"me/my tool": false, "me/tool.git": false, "/me/tool": false,
	} {
		if (CheckPath(p) == nil) != ok {
			t.Errorf("CheckPath(%q) ok=%v", p, !ok)
		}
	}
}
