package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const githubHost = "github.com"

// GitHub reaches GitHub through the gh CLI.
type GitHub struct {
	Host    string // default github.com
	Exclude []string
	// API runs "gh api --hostname Host endpoint [flags]" and returns
	// stdout. Overridable for tests.
	API func(ctx context.Context, host, endpoint string, flags ...string) ([]byte, error)
}

// Kind returns "github".
func (GitHub) Kind() string { return "github" }

// Name returns "GitHub".
func (GitHub) Name() string { return "GitHub" }

// CLI returns "gh".
func (GitHub) CLI() string { return "gh" }

// Hostname returns the configured host, or github.com.
func (g GitHub) Hostname() string {
	if g.Host == "" {
		return githubHost
	}
	return g.Host
}

// WithHost returns a copy that talks to h.
func (g GitHub) WithHost(h string) Forge { g.Host = h; return g }

// DetectHost returns the host: GitHub needs no detection (github.com
// unless configured).
func (g GitHub) DetectHost(context.Context, []string) string { return g.Hostname() }

func ghAPI(ctx context.Context, host, endpoint string, flags ...string) ([]byte, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, errors.New("gh not found: install it and run 'gh auth login', or paste a clone URL")
	}
	args := append([]string{"api", "--hostname", host, endpoint}, flags...)
	cmd, err := cliCommand(ctx, "gh", args...)
	if err != nil {
		return nil, err
	}
	out, stderr, err := Output(cmd)
	if err != nil {
		msg := cleanStderr(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		if hint := AuthHint("gh", host, msg); hint != "" {
			msg += " (" + hint + ")"
		}
		return out, fmt.Errorf("gh (%s): %s", host, msg)
	}
	return out, nil
}

func (g GitHub) call(ctx context.Context, endpoint string, flags ...string) ([]byte, error) {
	call := g.API
	if call == nil {
		call = ghAPI
	}
	return call(ctx, g.Hostname(), endpoint, flags...)
}

type ghRepo struct {
	FullName    string    `json:"full_name"`
	Name        string    `json:"name"`
	SSHURL      string    `json:"ssh_url"`
	CloneURL    string    `json:"clone_url"`
	Description string    `json:"description"`
	PushedAt    time.Time `json:"pushed_at"`
}

func (r ghRepo) project() Project {
	return Project{Path: r.FullName, Name: r.Name, SSHURL: r.SSHURL, HTTPURL: r.CloneURL,
		Description: r.Description, LastActivity: r.PushedAt}
}

// ListAll fetches the repositories the user owns, collaborates on or can
// see through an organization, most recently pushed first.
func (g GitHub) ListAll(ctx context.Context) ([]Project, error) {
	out, err := g.call(ctx, "user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member", "--paginate")
	if err != nil {
		return nil, err
	}
	repos, err := decodePages[ghRepo](out)
	if err != nil {
		return nil, err
	}
	ps := make([]Project, 0, len(repos))
	for _, r := range repos {
		ps = append(ps, r.project())
	}
	return g.Keep(ps), nil
}

// Search looks up "owner/name" directly, else matches the query against
// the user's repositories.
func (g GitHub) Search(ctx context.Context, query string) ([]Project, error) {
	query = strings.Trim(strings.TrimSpace(query), "/")
	if query == "" {
		return nil, errors.New("type a repository name, owner/name or URL")
	}
	if err := searchable(query); err != nil {
		return nil, err
	}
	if strings.Contains(query, "/") && !strings.ContainsAny(query, " \t") {
		var r ghRepo
		if !safePath(query) || strings.Count(query, "/") != 1 {
			return nil, fmt.Errorf("%w on %s", ErrNotFound, g.Hostname())
		}
		if out, err := g.call(ctx, "repos/"+query); err == nil && json.Unmarshal(out, &r) == nil && r.FullName != "" {
			if ps := g.Keep([]Project{r.project()}); len(ps) == 1 {
				return ps, nil
			}
			return nil, fmt.Errorf("%w on %s", ErrNotFound, g.Hostname())
		}
	}
	all, err := g.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if ps := Match(all, query, 50); len(ps) > 0 {
		return ps, nil
	}
	return nil, fmt.Errorf("%w on %s", ErrNotFound, g.Hostname())
}

// Keep drops repositories in excluded owners or with unsafe paths or URLs.
func (g GitHub) Keep(ps []Project) []Project { return keepProjects(ps, g.Exclude) }

// AllowsURL reports whether a pasted clone URL is outside the excluded
// owners.
func (g GitHub) AllowsURL(u CloneURL) bool { return allowsURL(u, g.Exclude) }

// Whoami returns the login gh is authenticated as.
func (g GitHub) Whoami(ctx context.Context) (string, error) {
	out, err := g.call(ctx, "user")
	if err != nil {
		return "", err
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(out, &u); err != nil {
		return "", fmt.Errorf("gh: cannot read user: %w", err)
	}
	if u.Login == "" {
		return "", errors.New("gh: cannot read user: no login in the reply")
	}
	if !userName(u.Login) {
		return "", errors.New("gh: the login in the reply is not a plain name")
	}
	return u.Login, nil
}

// Visibilities lists the choices for new repositories, private first.
func (GitHub) Visibilities() []string { return []string{"private", "public"} }

// CreateCmd runs "gh repo create owner/name --visibility"; gh resolves
// the owner itself, so the logged-in user is not needed. GH_HOST is
// always set: an inherited one would send the repository elsewhere.
func (g GitHub) CreateCmd(path, visibility, _ string) Cmd {
	return Cmd{Env: []string{"GH_HOST=" + g.Hostname()}, Name: "gh",
		Args: []string{"repo", "create", path, "--" + visibility}}
}

// RemoteURL is the ssh or https clone URL of the repository at path.
func (g GitHub) RemoteURL(path string, ssh bool) string { return remoteURL(g.Hostname(), path, ssh) }

var tokenScopesLine = regexp.MustCompile(`Token scopes:\s*(.*)`)

// GHAuthStatus runs "gh auth status --hostname host" and returns its
// output and whether gh is logged in there. Overridable for tests.
var GHAuthStatus = func(ctx context.Context, host string) (string, bool) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "gh not found", false
	}
	cmd, err := cliCommand(ctx, "gh", "auth", "status", "--hostname", host)
	if err != nil {
		return err.Error(), false
	}
	out, err := CombinedOutput(cmd)
	return string(out), err == nil
}

// AuthStatus reports whether gh is logged in to the host, with the
// token's scopes when gh lists them ("'repo', 'read:org'") and where gh
// keeps the token. No login is ErrNotLoggedIn.
func (g GitHub) AuthStatus(ctx context.Context) (scopes string, store TokenStore, err error) {
	out, ok := GHAuthStatus(ctx, g.Hostname())
	if !ok {
		// a failing status (an expired token, say) may still say where
		// the token is kept
		store = ghTokenStore(out)
		if strings.Contains(out, "not logged in") || strings.Contains(out, "not logged into") {
			return "", store, fmt.Errorf("gh auth status: %w to %s", ErrNotLoggedIn, g.Hostname())
		}
		msg := cleanStderr(out)
		if i := strings.Index(msg, "\n"); i >= 0 && Unreachable(msg) {
			msg = msg[:i] // the cause first; the rest is advice
		}
		return "", store, fmt.Errorf("gh auth status: %s", lastMeaningful(msg))
	}
	if m := tokenScopesLine.FindStringSubmatch(out); m != nil {
		scopes = strings.TrimSpace(m[1])
	}
	return scopes, ghTokenStore(out), nil
}

// lastMeaningful is the last line of out that is neither advice about
// environment variables nor a "Token: …" line (gh may print the token
// there), with URL credentials redacted.
func lastMeaningful(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l != "" && !strings.HasPrefix(l, "Alternatively") && !tokenLine.MatchString(l) {
			return RedactURLs(l)
		}
	}
	return "failed"
}

// tokenLine matches a status line that shows a token ("- Token: gho_…").
var tokenLine = regexp.MustCompile(`(?i)\btoken\s*:`)
