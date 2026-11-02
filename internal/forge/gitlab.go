// Package forge finds projects to clone on a GitLab server. It talks to
// the API through the glab CLI so it reuses the user's existing login and
// never handles tokens itself.
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Project is a clonable GitLab project.
type Project struct {
	Path         string    `json:"path_with_namespace"` // e.g. group/sub/name
	Name         string    `json:"path"`                // last path segment
	SSHURL       string    `json:"ssh_url_to_repo"`
	HTTPURL      string    `json:"http_url_to_repo"`
	Description  string    `json:"description"`
	LastActivity time.Time `json:"last_activity_at"`
	Archived     bool      `json:"archived,omitempty"`
	Group        bool      `json:"group,omitempty"` // a GitLab group, not a project
}

// GitLab searches one GitLab host.
type GitLab struct {
	Host string
	// Exclude lists groups whose projects are never listed, cached or
	// cloned (see Excluded).
	Exclude []string
	// HostKey names the config setting for Host in messages, e.g.
	// "[clone] host"; "" means a generic "host in the config".
	HostKey string
	// API runs "glab api --hostname Host endpoint [flags]" and returns
	// stdout. Overridable for tests.
	API func(ctx context.Context, host, endpoint string, flags ...string) ([]byte, error)
}

// ErrNotFound means no project matched.
var ErrNotFound = errors.New("no matching project")

func glabAPI(ctx context.Context, host, endpoint string, flags ...string) ([]byte, error) {
	if _, err := exec.LookPath("glab"); err != nil {
		return nil, errors.New("glab not found: install it and run 'glab auth login', or paste a clone URL")
	}
	args := append([]string{"api", "--hostname", host, endpoint}, flags...)
	cmd, err := cliCommand(ctx, "glab", args...)
	if err != nil {
		return nil, err
	}
	out, stderr, err := Output(cmd)
	if err != nil {
		msg := cleanStderr(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		if hint := AuthHint("glab", host, msg); hint != "" {
			msg += " (" + hint + ")"
		}
		return out, fmt.Errorf("glab (%s): %s", host, msg)
	}
	return out, nil
}

func (g GitLab) api(ctx context.Context, endpoint string, v any) error {
	if g.Host == "" {
		return errors.New(g.NoHost() + ", or paste a clone URL")
	}
	call := g.API
	if call == nil {
		call = glabAPI
	}
	out, err := call(ctx, g.Host, endpoint)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

// Search finds projects the user is a member of. A full project path
// ("group/sub/name") is looked up directly; anything else goes to
// GitLab's search, which matches names, paths and namespaces.
func (g GitLab) Search(ctx context.Context, query string) ([]Project, error) {
	query = strings.Trim(strings.TrimSpace(query), "/")
	if query == "" {
		return nil, errors.New("type a project name, path or URL")
	}
	if err := searchable(query); err != nil {
		return nil, err
	}
	var exact *Project // a group whose path is the query
	if !strings.ContainsAny(query, " \t") {
		if strings.Contains(query, "/") && safePath(query) { // a plain path, as for a group
			var p Project
			if err := g.api(ctx, "projects/"+url.PathEscape(query), &p); err == nil && p.Path != "" {
				ps := g.Keep([]Project{p})
				if len(ps) == 0 {
					return nil, fmt.Errorf("%w on %s", ErrNotFound, g.Host)
				}
				return ps, nil
			}
		}
		// A group path (or group URL) clones the whole group. A single
		// word may also name projects: then the group is listed first,
		// with them.
		if grp, err := g.LookupGroup(ctx, query); err == nil {
			if strings.Contains(query, "/") || len([]rune(query)) < 3 {
				return []Project{grp}, nil
			}
			exact = &grp
		} else if errors.Is(err, errExcludedGroup) {
			return nil, fmt.Errorf("%w on %s", ErrNotFound, g.Host)
		}
	}
	// "tools group": the user means a group named tools.
	wantGroup := false
	if w := strings.Fields(query); len(w) > 1 && strings.EqualFold(w[len(w)-1], "group") {
		query, wantGroup = strings.Join(w[:len(w)-1], " "), true
	}
	if len([]rune(query)) < 3 {
		return nil, errors.New("search needs at least 3 characters")
	}
	var ps []Project
	err := g.api(ctx, "projects?membership=true&simple=true&search_namespaces=true"+
		"&order_by=last_activity_at&per_page=50&search="+url.QueryEscape(query), &ps)
	if err != nil {
		if exact != nil {
			return []Project{*exact}, nil
		}
		return nil, err
	}
	ps = g.Keep(ps)
	if exact != nil {
		wantGroup = true // the typed name is the group's: list it first
	}
	var gs []glGroup // groups are a bonus: a failure here is not fatal
	if g.api(ctx, "groups?min_access_level=10&per_page=20&search="+url.QueryEscape(query), &gs) == nil {
		var groups []Project
		if exact != nil {
			groups = append(groups, *exact)
		}
		for _, gr := range gs {
			if safePath(gr.FullPath) && !groupExcluded(gr.FullPath, g.Exclude) && (exact == nil || gr.FullPath != exact.Path) {
				groups = append(groups, gr.project())
			}
		}
		if wantGroup {
			ps = append(groups, ps...)
		} else {
			ps = append(ps, groups...)
		}
	} else if exact != nil {
		ps = append([]Project{*exact}, ps...)
	}
	if len(ps) == 0 {
		return nil, fmt.Errorf("%w on %s", ErrNotFound, g.Host)
	}
	return ps, nil
}

// ErrNotSearched refuses search text shaped like a URL or credentials:
// it would go to the server, and into glab's or gh's arguments.
var ErrNotSearched = errors.New("not searched: this looks like a URL or credentials (user:secret@host, ?token=); " +
	"paste a clone URL without credentials, or type a project name or path")

// searchable refuses search text that is a clone source by the one
// classifier (IsURL: "://", scp-like "host:path" and so "user:secret@host",
// a local path) or has any credential shape RedactURLs would hide
// (HasCredentials: "?private_token=…", "#access_token=…", …), so the
// forge and the CLI never see what the screen hides, and the TUI (which
// parses IsURL input, never searches it) and the forge cannot classify
// one text differently. It never quotes the text.
func searchable(q string) error {
	if IsURL(q) || HasCredentials(q) {
		return ErrNotSearched
	}
	return nil
}

// NoHost explains that no GitLab host is known and how to set one.
func (g GitLab) NoHost() string {
	key := g.HostKey
	if key == "" {
		key = "host in the config"
	}
	return "no GitLab host found: glab auth login --hostname <host>, or set " + key
}

// Kind returns "gitlab".
func (GitLab) Kind() string { return "gitlab" }

// Name returns "GitLab".
func (GitLab) Name() string { return "GitLab" }

// CLI returns "glab".
func (GitLab) CLI() string { return "glab" }

// Hostname returns the configured host, or "" while it is not known yet.
func (g GitLab) Hostname() string { return g.Host }

// WithHost returns a copy that talks to h.
func (g GitLab) WithHost(h string) Forge { g.Host = h; return g }

// DetectHost picks the GitLab glab is logged in to, preferring the one
// the listed repositories come from (see PickHost).
func (g GitLab) DetectHost(ctx context.Context, remotes []string) string {
	if g.Host != "" {
		return g.Host
	}
	return PickHost(remotes, LoggedInHosts(ctx))
}

// Whoami returns the username glab is authenticated as on Host.
func (g GitLab) Whoami(ctx context.Context) (string, error) {
	var u struct {
		Username string `json:"username"`
	}
	if err := g.api(ctx, "user", &u); err != nil {
		return "", err
	}
	if u.Username == "" {
		return "", errors.New("glab: cannot read user")
	}
	if !userName(u.Username) {
		return "", errors.New("glab: the user name in the reply is not a plain name")
	}
	return u.Username, nil
}

// Visibilities lists the choices for new projects, private first.
func (GitLab) Visibilities() []string { return []string{"private", "internal", "public"} }

// CreateCmd runs "glab repo create name [--group group]". glab reads a
// slash path with three or more parts as host/…, so the group goes in
// --group; the user's own namespace is glab's default.
func (g GitLab) CreateCmd(path, visibility, me string) Cmd {
	group, name := "", path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		group, name = path[:i], path[i+1:]
	}
	args := []string{"repo", "create", name}
	if group != "" && !strings.EqualFold(group, me) { // GitLab user names ignore case
		args = append(args, "--group", group)
	}
	args = append(args, "--"+visibility, "--skipGitInit")
	return Cmd{Env: []string{"GITLAB_HOST=" + g.Host}, Name: "glab", Args: args}
}

// RemoteURL is the ssh or https clone URL of the project at path (see
// remoteURL for hosts with a port).
func (g GitLab) RemoteURL(path string, ssh bool) string { return remoteURL(g.Host, path, ssh) }

// HostFromURL returns the host of a remote, without port or IPv6
// brackets (HostName): of a URL ("https://host:8443/…" → "host") or of
// git's scp-like form ("git@host:path", "host:path", "deploy@[::1]:path"
// → "::1"), told apart as ParseCloneURL does (IsURL); "" for a local
// path, a host that is no plain host name (ValidHost, validSCPHost: the host
// may become a glab --hostname, a cache file name and a shown value) or
// anything else.
func HostFromURL(remote string) string {
	remote = strings.TrimSpace(remote)
	if strings.Contains(remote, "://") {
		if u, err := url.Parse(remote); err == nil && ValidHost(u.Host) == nil {
			return u.Hostname()
		}
		return ""
	}
	if !strings.ContainsAny(remote, " \t") && scpLike(remote) { // a phrase with a colon is none (IsURL)
		if _, host, _, _ := splitSCP(remote); validSCPHost(host) {
			return HostName(host)
		}
	}
	return ""
}

// GuessHost picks the most common non-GitHub host among remotes, so
// gl-repos searches the GitLab the user's repositories already come from.
func GuessHost(remotes []string) string {
	count := map[string]int{}
	for _, r := range remotes {
		if h := HostFromURL(r); h != "" && h != "github.com" {
			count[h]++
		}
	}
	hosts := make([]string, 0, len(count))
	for h := range count {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		if count[hosts[i]] != count[hosts[j]] {
			return count[hosts[i]] > count[hosts[j]]
		}
		return hosts[i] < hosts[j]
	})
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0]
}

var loggedInLine = regexp.MustCompile(`Logged in to (\S+) as `)

// GLAuthStatusAll runs "glab auth status" (every host glab knows) and
// returns its combined output. Overridable for tests.
var GLAuthStatusAll = func(ctx context.Context) []byte {
	if _, err := exec.LookPath("glab"); err != nil {
		return nil
	}
	cmd, err := cliCommand(ctx, "glab", "auth", "status")
	if err != nil {
		return nil
	}
	out, _ := CombinedOutput(cmd) // non-zero when any host fails; still parse the rest
	return out
}

// LoggedInHosts lists the GitLab hosts glab has a working login for, as
// reported by "glab auth status" (GLAuthStatusAll). Overridable for
// tests.
var LoggedInHosts = func(ctx context.Context) []string { return loggedInHosts(GLAuthStatusAll(ctx)) }

// loggedInHosts reads the hosts out of "glab auth status" output. Only
// a plain host name (ValidHost) is taken: a host becomes a glab
// --hostname argument, names a cache file and is shown in the badge, the
// clone box and -doctor, so glab's output is not trusted to hold one.
func loggedInHosts(out []byte) []string {
	var hosts []string
	for _, m := range loggedInLine.FindAllSubmatch(out, -1) {
		if h := string(m[1]); ValidHost(h) == nil {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// GLAuthStatus runs "glab auth status --hostname host" and returns its
// combined output. Overridable for tests.
var GLAuthStatus = func(ctx context.Context, host string) string {
	if _, err := exec.LookPath("glab"); err != nil {
		return ""
	}
	cmd, err := cliCommand(ctx, "glab", "auth", "status", "--hostname", host)
	if err != nil {
		return ""
	}
	out, _ := CombinedOutput(cmd) // non-zero without a login; the output says so
	return string(out)
}

// TokenStore reports where glab keeps its token for Host.
func (g GitLab) TokenStore(ctx context.Context) TokenStore {
	return glabTokenStore(GLAuthStatus(ctx, g.Host))
}

// PickHost chooses the GitLab to search: the most common origin host
// among remotes that glab is logged in to, else the first glab login,
// else the most common origin host. A login is returned as glab names it
// (a port kept, "host:8443"); it matches a remote by host name alone.
func PickHost(remotes, loggedIn []string) string {
	if len(loggedIn) > 0 {
		in := map[string]string{} // host name → the login
		for _, h := range loggedIn {
			if _, ok := in[HostName(h)]; !ok {
				in[HostName(h)] = h
			}
		}
		var known []string
		for _, r := range remotes {
			if _, ok := in[HostFromURL(r)]; ok {
				known = append(known, r)
			}
		}
		if h := GuessHost(known); h != "" {
			return in[h]
		}
		return loggedIn[0]
	}
	return GuessHost(remotes)
}

// TokenScopes returns the scopes of the personal access token glab uses
// on Host (GET personal_access_tokens/self). Other kinds of token (e.g.
// OAuth from a browser login) make this fail: the scopes are unknown.
func (g GitLab) TokenScopes(ctx context.Context) ([]string, error) {
	var t struct {
		Scopes []string `json:"scopes"`
	}
	if err := g.api(ctx, "personal_access_tokens/self", &t); err != nil {
		return nil, err
	}
	if len(t.Scopes) == 0 {
		return nil, errors.New("glab: no token scopes in the reply")
	}
	return t.Scopes, nil
}
