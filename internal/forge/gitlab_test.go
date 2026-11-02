package forge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func fake(responses map[string]string) func(context.Context, string, string, ...string) ([]byte, error) {
	return func(_ context.Context, host, endpoint string, _ ...string) ([]byte, error) {
		for prefix, body := range responses {
			if strings.HasPrefix(endpoint, prefix) {
				return []byte(body), nil
			}
		}
		return nil, errors.New("404 Not Found")
	}
}

func TestSearchDirectPath(t *testing.T) {
	g := GitLab{Host: "git.example.org", API: fake(map[string]string{
		"projects/group%2Fsub%2Ftool": `{"path_with_namespace":"group/sub/tool","path":"tool","ssh_url_to_repo":"git@git.example.org:group/sub/tool.git"}`,
	})}
	ps, err := g.Search(context.Background(), "group/sub/tool")
	if err != nil || len(ps) != 1 || ps[0].Name != "tool" {
		t.Fatalf("got %+v, %v", ps, err)
	}
}

// A direct project lookup is only made for a plain path (safePath), as
// for a group: anything else goes to the search, where it is a query.
func TestSearchDirectPathNeedsPlainPath(t *testing.T) {
	for _, q := range []string{"group/../tool", "group/.git/tool", "group//tool", "group/to ol/x", "-group/tool", "group/tool%2Fx"} {
		var endpoints []string
		g := GitLab{Host: "h", API: func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
			endpoints = append(endpoints, ep)
			return []byte(`[]`), nil
		}}
		_, _ = g.Search(context.Background(), q)
		for _, ep := range endpoints {
			if strings.HasPrefix(ep, "projects/") {
				t.Errorf("%q: direct lookup %q", q, ep)
			}
		}
	}
}

func TestSearchFallsBackToQuery(t *testing.T) {
	var endpoint string
	g := GitLab{Host: "h", API: func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
		if strings.HasPrefix(ep, "projects?") {
			endpoint = ep
		}
		if strings.HasPrefix(ep, "projects/") || strings.HasPrefix(ep, "groups") {
			return nil, errors.New("404")
		}
		return []byte(`[{"path_with_namespace":"a/billing-v3","path":"billing-v3"}]`), nil
	}}
	ps, err := g.Search(context.Background(), "billing payments")
	if err != nil || len(ps) != 1 {
		t.Fatalf("got %+v, %v", ps, err)
	}
	for _, want := range []string{"membership=true", "search_namespaces=true", "search=billing+payments"} {
		if !strings.Contains(endpoint, want) {
			t.Errorf("endpoint %q missing %q", endpoint, want)
		}
	}
}

func TestSearchErrors(t *testing.T) {
	g := GitLab{Host: "h", API: fake(map[string]string{"projects?": `[]`})}
	if _, err := g.Search(context.Background(), "nothing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty result: %v", err)
	}
	if _, err := g.Search(context.Background(), "ab"); err == nil {
		t.Errorf("short query should fail")
	}
	if _, err := (GitLab{}).Search(context.Background(), "tool"); err == nil || !strings.Contains(err.Error(), "no GitLab host") {
		t.Errorf("missing host: %v", err)
	}
}

// IsURL, ParseCloneURL and HostFromURL tell the forms apart the same
// way (one classifier): what IsURL takes for a clone source parses, or
// is refused, never searched; what it takes for a search is never
// parsed.
func TestURLHelpers(t *testing.T) {
	for _, c := range []struct {
		in        string
		url       bool
		name, hst string
	}{
		{"git@git.example.edu:acme/tools/deploykit.git", true, "deploykit", "git.example.edu"},
		{"https://gitlab.com/group/proj", true, "proj", "gitlab.com"},
		{"https://gitlab.com/group/proj.git/", true, "proj", "gitlab.com"},
		{"https://gitlab.example.com:8443/group/proj", true, "proj", "gitlab.example.com"},
		{"ssh://deploy@[::1]:2222/group/proj", true, "proj", "::1"},
		// git's scp-like form: a colon before the first slash, with or
		// without a user and ".git"
		{"gitlab.example.com:acme/tools", true, "tools", "gitlab.example.com"},
		{"deploy@gitlab.example.com:acme/tools", true, "tools", "gitlab.example.com"},
		{"git@[::1]:acme/tools.git", true, "tools", "::1"},
		{"git@gitlab.example.com:/srv/git/tools.git", true, "tools", "gitlab.example.com"},
		{"/srv/git/repo", true, "repo", ""},
		{"./repo", true, "repo", ""},
		{"../repos/tools.git", true, "tools", ""},
		{"/srv/git/some dir/x", true, "x", ""},
		{"./a:b", true, "", ""}, // a local path (no plain name: the user types one)
		{"billing payments", false, "", ""},
		{"group/proj", false, "", ""},
		{"repo", false, "", ""},
		{"srv/git/repo", false, "", ""},
		// a colon after a space, after a slash or first is no scp-like form
		{"fix: parser", false, "", ""},
		{"foo: bar", false, "", ""},
		{"release notes 2026:Q4", false, "", ""},
		{"acme/tools:v2", false, "", ""},
		{":tools", false, "", ""},
	} {
		if IsURL(c.in) != c.url {
			t.Errorf("IsURL(%q) = %v", c.in, !c.url)
		}
		if c.url {
			if u, err := ParseCloneURL(c.in); err != nil || u.Name() != c.name {
				t.Errorf("ParseCloneURL(%q).Name() = %q, %v", c.in, u.Name(), err)
			}
		} else if _, err := ParseCloneURL(c.in); !errors.Is(err, ErrBadCloneURL) {
			t.Errorf("ParseCloneURL(%q) accepts a search: %v", c.in, err)
		}
		if got := HostFromURL(c.in); got != c.hst {
			t.Errorf("HostFromURL(%q) = %q", c.in, got)
		}
	}
	// URL-shaped but no form gl-repos accepts: refused, never searched
	for _, in := range []string{"tools.git", "foo:", "git@gitlab.example.com/acme/tools.git", "a/b:c@d",
		"oauth2:SECRET@gitlab.example.com/acme/tools.git", "oauth2:SECRET@gitlab.example.com:acme/tools.git"} {
		if !IsURL(in) {
			t.Errorf("IsURL(%q) = false", in)
		}
		if _, err := ParseCloneURL(in); !errors.Is(err, ErrBadCloneURL) && !errors.Is(err, ErrURLCredentials) {
			t.Errorf("ParseCloneURL(%q): %v", in, err)
		}
	}
}

func TestGuessHost(t *testing.T) {
	got := GuessHost([]string{
		"git@github.com:me/a.git",
		"git@github.com:me/b.git",
		"git@git.example.edu:x/y.git",
		"https://git.example.edu/x/z.git",
		"/local/path/origin.git",
	})
	if got != "git.example.edu" {
		t.Errorf("GuessHost = %q", got)
	}
	if GuessHost(nil) != "" {
		t.Errorf("no remotes should give no host")
	}
}

func TestPickHost(t *testing.T) {
	remotes := []string{
		"git@git.other.example:a/b.git", "git@git.other.example:a/c.git", // most common, but no glab login
		"git@git.example.edu:x/y.git",
	}
	for _, c := range []struct {
		name     string
		remotes  []string
		loggedIn []string
		want     string
	}{
		{"prefer origin host glab is logged in to", remotes, []string{"gitlab.com", "git.example.edu"}, "git.example.edu"},
		{"no matching origins: first glab login", []string{"git@github.com:me/x.git"}, []string{"git.example.edu"}, "git.example.edu"},
		{"empty folder: glab login", nil, []string{"git.example.edu"}, "git.example.edu"},
		{"no glab: most common origin", remotes, nil, "git.other.example"},
		{"nothing at all", nil, nil, ""},
		{"login with a port matches the remote's host, and is kept as glab names it",
			[]string{"https://git.example.edu:8443/x/y.git", "git@git.example.edu:x/z.git"},
			[]string{"[::1]:8443", "git.example.edu:8443"}, "git.example.edu:8443"},
		{"a remote with a port matches a login without one", []string{"https://git.example.edu:8443/x/y.git"},
			[]string{"gitlab.com", "git.example.edu"}, "git.example.edu"},
	} {
		if got := PickHost(c.remotes, c.loggedIn); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLoggedInHostsParsesGlab(t *testing.T) {
	out := []byte("git.example.edu\n  ✓ Logged in to git.example.edu as me (/x/config.yml)\n" +
		"gitlab.com\n  x gitlab.com: API call failed\n  ✓ Logged in to gitlab.example.org as me (/x)\n" +
		"  ✓ Logged in to [::1]:8443 as me (/x)\n")
	if hosts := loggedInHosts(out); strings.Join(hosts, ",") != "git.example.edu,gitlab.example.org,[::1]:8443" {
		t.Errorf("hosts = %v", hosts)
	}
	// a status glab did not write (a wrapper, a tampered config): a host
	// that is no plain host name is not a host, whatever surrounds it
	bad := []byte("x\n  ✓ Logged in to -hostname=evil.example.com as me (/x)\n" +
		"  ✓ Logged in to h;private_token=SECRET as me (/x)\n" +
		"  ✓ Logged in to oauth2:SECRET@h as me (/x)\n" +
		"  ✓ Logged in to h/../../etc as me (/x)\n" +
		"  ✓ Logged in to h\x1b[2Jx as me (/x)\n" +
		"  ✓ Logged in to h:http as me (/x)\n" +
		"  ✓ Logged in to h:70000 as me (/x)\n" +
		"  ✓ Logged in to h%2Fx as me (/x)\n" +
		"  ✓ Logged in to good.example.com as me (/x)\n")
	if hosts := loggedInHosts(bad); strings.Join(hosts, ",") != "good.example.com" {
		t.Errorf("hosts = %q", hosts)
	}
	for _, h := range []string{"gitlab.example.com", "gitlab.example.com:8443", "[::1]", "[::1]:22", "h", "a-b.c1"} {
		if err := ValidHost(h); err != nil {
			t.Errorf("ValidHost(%q) = %v", h, err)
		}
	}
	for _, h := range []string{"", "https://h", "me@h", "h x", "h:http", "h:0", "h:65536", "-h", ".h", "h.", "h/x",
		"h;x", "h%41", "h\x1b[2J", "a..b", "a-.b", "[::1]:", "[::1]x", "[:::]", "[deadbeef]", "[1.2.3.4]", "[::1]:0", "[::1]:65536"} {
		if err := ValidHost(h); err == nil {
			t.Errorf("ValidHost(%q) = nil", h)
		}
	}
	// a remote whose host is no plain host name names none
	for _, r := range []string{"https://h;private_token=SECRET/acme/x.git", "https://-h/acme/x.git", "https://h%41/x.git"} {
		if got := HostFromURL(r); got != "" {
			t.Errorf("HostFromURL(%q) = %q", r, got)
		}
	}
}

func TestRemoteURLWithPort(t *testing.T) {
	for _, c := range []struct {
		host string
		ssh  bool
		want string
	}{
		{"gitlab.example.com", true, "git@gitlab.example.com:acme/tool.git"},
		{"gitlab.example.com:8443", true, "git@gitlab.example.com:acme/tool.git"},
		{"gitlab.example.com:8443", false, "https://gitlab.example.com:8443/acme/tool.git"},
		{"[::1]:8443", true, "git@[::1]:acme/tool.git"},
		{"[::1]:8443", false, "https://[::1]:8443/acme/tool.git"},
	} {
		if got := (GitLab{Host: c.host}).RemoteURL("acme/tool", c.ssh); got != c.want {
			t.Errorf("RemoteURL(%s, ssh %v) = %s, want %s", c.host, c.ssh, got, c.want)
		}
		got := (GitLab{Host: c.host}).RemoteURL("acme/tool", c.ssh)
		if HostFromURL(got) != HostName(c.host) {
			t.Errorf("HostFromURL(%s) = %s, want %s", got, HostFromURL(got), HostName(c.host))
		}
	}
}

// A one-word query that names a group also lists the projects that match
// it, so picking the group is a choice, not automatic.
func TestSearchGroupNameAlsoFindsProjects(t *testing.T) {
	api := func(projects string) func(context.Context, string, string, ...string) ([]byte, error) {
		return func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
			switch {
			case ep == "groups/tools":
				return []byte(`{"id":3,"full_path":"tools","path":"tools"}`), nil
			case strings.HasPrefix(ep, "projects?"):
				return []byte(projects), nil
			case strings.HasPrefix(ep, "groups?"):
				return []byte(`[{"full_path":"tools","path":"tools"},{"full_path":"acme/tools","path":"tools"}]`), nil
			}
			return nil, errors.New(`{"message":"404 Not Found"}`)
		}
	}
	g := GitLab{Host: "h", API: api(`[{"path_with_namespace":"acme/tools-cli","path":"tools-cli"}]`)}
	ps, err := g.Search(context.Background(), "tools")
	if err != nil || names(ps) != "tools,acme/tools,acme/tools-cli" || !ps[0].Group {
		t.Errorf("Search(tools) = %s, %v", names(ps), err)
	}
	g.API = api(`[]`)
	if ps, err := g.Search(context.Background(), "tools"); err != nil || names(ps) != "tools,acme/tools" {
		t.Errorf("Search(tools) without projects = %s, %v", names(ps), err)
	}
}

func TestWhoamiErrors(t *testing.T) {
	gh := GitHub{API: func(context.Context, string, string, ...string) ([]byte, error) { return []byte(`{}`), nil }}
	if _, err := gh.Whoami(context.Background()); err == nil || strings.Contains(err.Error(), "%!") {
		t.Errorf("empty login: %v", err)
	}
	gh.API = func(context.Context, string, string, ...string) ([]byte, error) { return []byte(`not json`), nil }
	if _, err := gh.Whoami(context.Background()); err == nil || strings.Contains(err.Error(), "%!") {
		t.Errorf("bad reply: %v", err)
	}
	gl := GitLab{Host: "h", API: func(context.Context, string, string, ...string) ([]byte, error) { return []byte(`{}`), nil }}
	if _, err := gl.Whoami(context.Background()); err == nil {
		t.Error("empty username accepted")
	}
	if _, err := (GitLab{HostKey: "[clone] host"}).Whoami(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "set [clone] host") {
		t.Errorf("no host: %v", err)
	}
	// a user name from the server is prefilled as an owner: a plain name only
	for _, bad := range []string{"-x", "a/b", "oauth2:SECRET@h", "me?private_token=SECRET", "x\x1b]0;y\x07"} {
		reply := func(context.Context, string, string, ...string) ([]byte, error) {
			b, _ := json.Marshal(map[string]string{"username": bad, "login": bad})
			return b, nil
		}
		for _, f := range []Forge{GitLab{Host: "h", API: reply}, GitHub{API: reply}} {
			if me, err := f.Whoami(context.Background()); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Errorf("%s Whoami = %q, %v", f.Name(), me, err)
			}
		}
	}
	gl.API = func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(`{"username":"sam.b-2"}`), nil
	}
	if me, err := gl.Whoami(context.Background()); err != nil || me != "sam.b-2" {
		t.Errorf("plain name: %q, %v", me, err)
	}
}

func TestGroupLookupsNeedHostAnd404(t *testing.T) {
	if _, err := (GitLab{}).GroupProjects(context.Background(), "acme"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("GroupProjects without host = %v", err)
	}
	g := GitLab{Host: "h", API: func(context.Context, string, string, ...string) ([]byte, error) {
		return nil, errors.New("glab not found: install it")
	}}
	if _, err := g.GroupID(context.Background(), "acme"); errors.Is(err, ErrNotFound) {
		t.Error("a missing glab counted as a missing group")
	}
	g.API = func(context.Context, string, string, ...string) ([]byte, error) {
		return nil, errors.New(`glab (h): {"message":"404 Group Not Found"}`)
	}
	if _, err := g.GroupID(context.Background(), "acme"); !errors.Is(err, ErrNotFound) {
		t.Errorf("404 = %v", err)
	}
}

func TestHostName(t *testing.T) {
	for in, want := range map[string]string{
		"gitlab.example.com":      "gitlab.example.com",
		"gitlab.example.com:8443": "gitlab.example.com",
		"gitlab.example.com:":     "gitlab.example.com",
		"[::1]:8443":              "::1",
		"[::1]":                   "::1",
		"::1":                     "::1",
		"192.0.2.1:8443":          "192.0.2.1",
	} {
		if got := HostName(in); got != want {
			t.Errorf("HostName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := HostFromURL("git@[::1]:acme/tool.git"); got != "::1" {
		t.Errorf("HostFromURL(git@[::1]:…) = %q", got)
	}
}
