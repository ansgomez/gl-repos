package forge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestHint(t *testing.T) {
	scope := "the glab token needs the api scope (a legacy personal access token): glab auth login --hostname h"
	role := "403 Forbidden: your role in the group may not allow this (creating subgroups needs Maintainer/Owner), " +
		"or the token lacks the api scope: glab auth login --hostname h"
	net := "cannot reach h: check the VPN, DNS or firewall"
	for _, tc := range []struct{ cli, out, want string }{
		{"glab", `glab: 403 Forbidden {"error":"insufficient_scope","error_description":"The request requires higher privileges"}`, scope},
		{"glab", "POST https://h/api/v4/projects: 403 {message: 403 Forbidden}", role},
		{"glab", `{"message":"403 Forbidden"}`, role},
		{"gh", "HTTP 403: Resource not accessible by integration", "the gh login may not do this: gh auth refresh --hostname h --scopes repo"},
		{"glab", `Get "https://h/api/v4/user": dial tcp: lookup h: no such host`, net},
		{"glab", `Get "https://h/api/v4/user": dial tcp [2001:db8::403]:443: i/o timeout`, net},
		{"gh", "Post \"https://h/api/graphql\": dial tcp 198.51.100.1:443: connect: connection refused", net},
		{"glab", "context deadline exceeded (Client.Timeout exceeded while awaiting headers)", net},
		{"glab", "dial tcp 192.0.2.1:443: connect: network is unreachable", net},
		{"glab", "error: 401 Unauthorized", "not logged in to h: glab auth login --hostname h"},
		{"gh", "HTTP 401: Bad credentials", "not logged in to h: gh auth login --hostname h"},
		{"glab", "glab: name has already been taken (HTTP 400)", "the name is already taken on h: choose another"},
		{"gh", "GraphQL: Name already exists on this account (createRepository)", "the name is already taken on h: choose another"},
		{"glab", `{"message":"404 Namespace Not Found"}`, "not found on h: check that the group exists and that you may create projects in it"},
		{"glab", "glab not found: install it and log in", "glab not found: install it"},
		{"glab", "fatal: something else", ""},
		{"gh", "error connecting to ghe.example.com\ncheck your internet connection or https://githubstatus.com", net},
		{"glab", `{"id":403123,"full_path":"acme/x"}`, ""}, // a number that only contains 403
	} {
		if got := Hint(tc.cli, "h", tc.out); got != tc.want {
			t.Errorf("Hint(%s, %q) = %q, want %q", tc.cli, tc.out, got, tc.want)
		}
	}
	if got := AuthHint("glab", "h", "404 Not Found"); got != "" {
		t.Errorf("AuthHint(404) = %q, want nothing (a lookup may miss)", got)
	}
	if got := AuthHint("gh", "github.com", "HTTP 403"); got != "the gh login may not do this: gh auth refresh --scopes repo" {
		t.Errorf("gh 403 on github.com = %q", got)
	}
	// an IPv6 address or a version is no status code
	for _, out := range []string{"connect to [2001:db8::403]:22", "fe80::401", "glab 1.404.0", "id=4031"} {
		if status403.MatchString(out) || status401.MatchString(out) || code404.MatchString(out) {
			t.Errorf("status code found in %q", out)
		}
	}
}

// glab 1.106 prints a deprecation banner on every call while NO_PROMPT is
// set; it must never become the error text.
func TestCleanStderr(t *testing.T) {
	banner := "DEPRECATION WARNING: The NO_PROMPT environment variable is deprecated and will be removed\n" +
		"  in a future release. Use GLAB_NO_PROMPT instead.\n\n"
	for in, want := range map[string]string{
		banner + `glab: 404 Not Found {"message":"404 Group Not Found"}` + "\n":                                `glab: 404 Not Found {"message":"404 Group Not Found"}`,
		banner + "ERROR: dial tcp: lookup h: no such host\n":                                                   "ERROR: dial tcp: lookup h: no such host",
		"DEPRECATION WARNING: NO_PROMPT is deprecated, use GLAB_NO_PROMPT.\nerror: 401 Unauthorized\n":         "error: 401 Unauthorized",
		"A new version of glab has been released: v1.107.0\nTo upgrade, run 'brew upgrade glab'\nfatal: 500\n": "fatal: 500",
		"plain error": "plain error",
		// nothing but a banner: better than an empty error
		banner + "\n\n": strings.TrimSpace(banner),
		// errors that mention deprecation are errors, not banners
		"glab: 410 Gone: {message: This API endpoint is deprecated}":                                                                                "glab: 410 Gone: {message: This API endpoint is deprecated}",
		"ERROR: the v3 API is deprecated, upgrade the server\n":                                                                                     "ERROR: the v3 API is deprecated, upgrade the server",
		"WARNING: NO_PROMPT is deprecated\nerror connecting to gitlab.example.com\nPlease check your network":                                       "error connecting to gitlab.example.com\nPlease check your network",
		"-----\nFailed to create project":                                                                                                           "Failed to create project",
		"Warning: deprecated option\n  set GLAB_X\nPOST https://h/api/v4/projects: 400 {message: {path: [is reserved]}}":                            "Warning: deprecated option\nset GLAB_X\nPOST https://h/api/v4/projects: 400 {message: {path: [is reserved]}}",
		"A new version of glab has been released: v1.106.0 -> v1.107.0\nhttps://gitlab.com/gitlab-org/cli/-/releases/v1.107.0\nglab: 404 Not Found": "glab: 404 Not Found",
	} {
		if got := cleanStderr(in); got != want {
			t.Errorf("cleanStderr(%q) = %q, want %q", in, got, want)
		}
	}
	err := fmt.Errorf("glab (h): %s", cleanStderr(banner+"error: 401 Unauthorized"))
	if strings.Contains(err.Error(), "DEPRECATION") {
		t.Errorf("banner kept: %v", err)
	}
}

func TestOwnerCaseInsensitive(t *testing.T) {
	g := GitLab{Host: "h"}
	if c := g.CreateCmd("Me/tool", "private", "me").String(); strings.Contains(c, "--group") {
		t.Errorf("own namespace in other case passed as a group: %s", c)
	}
	if c := g.CreateCmd("acme/tool", "private", "me").String(); !strings.Contains(c, "--group acme") {
		t.Errorf("group missing: %s", c)
	}
}

func TestExcludedOwnerNeverLookedUp(t *testing.T) {
	called := false
	g := GitLab{Host: "h", Exclude: []string{"legal"}, API: func(context.Context, string, string, ...string) ([]byte, error) {
		called = true
		return []byte(`{"id":5,"full_path":"acme/legal","path":"legal"}`), nil
	}}
	ctx := context.Background()
	if _, err := g.GroupID(ctx, "acme/legal"); !errors.Is(err, errExcludedGroup) {
		t.Errorf("GroupID(excluded) = %v", err)
	}
	if _, err := g.MissingGroups(ctx, "acme/legal/new"); err == nil || !strings.Contains(err.Error(), "excluded") {
		t.Errorf("MissingGroups(below excluded) = %v", err)
	}
	if called {
		t.Error("an excluded group was looked up on the server")
	}
}

func TestTokenStoreParsed(t *testing.T) {
	for out, want := range map[string]TokenStore{
		"h\n  ✓ Logged in to h as tester (/c/config.yml)\n  ✓ Token found in configuration file (plaintext): ****\n": StorePlaintext,
		"h\n  ✓ Token found in operating system keyring: ****\n":                                                     StoreKeyring,
		"h\n  ✓ Token found in environment variable: ****\n":                                                         StoreEnv,
		"h\n  ✓ Logged in to h as tester (/c/config.yml)\n  ✓ Token: ****\n":                                         StoreUnknown,
		"": StoreUnknown,
	} {
		if got := glabTokenStore(out); got != want {
			t.Errorf("glab %q: %v, want %v", out, got, want)
		}
	}
	for out, want := range map[string]TokenStore{
		"github.com\n  ✓ Logged in to github.com account octo (keyring)\n":                     StoreKeyring,
		"github.com\n  ✓ Logged in to github.com account octo (/c/gh/hosts.yml)\n":             StorePlaintext,
		"github.com\n  ✓ Logged in to github.com as octo (/c/gh/hosts.yml)\n":                  StorePlaintext,
		"github.com\n  ✓ Logged in to github.com account octo (GH_TOKEN)\n":                    StoreEnv,
		"ghe.example.com\n  ✓ Logged in to ghe.example.com account octo (GH_ENTERPRISE_TOKEN)": StoreEnv,
		"github.com\n  ✓ Logged in to github.com account octo\n":                               StoreUnknown,
	} {
		if got := ghTokenStore(out); got != want {
			t.Errorf("gh %q: %v, want %v", out, got, want)
		}
	}
}
