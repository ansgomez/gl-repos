package forge

import (
	"errors"
	"regexp"
	"strings"
)

// status matches an HTTP status code as a word of its own: not inside a
// longer number, an id or an IPv6 address ("[2001:db8::403]").
func status(code, words string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[\s("'{=])` + code + `($|[^\w.\]])` + words)
}

var (
	status403 = status("403", `|Forbidden`)
	status401 = status("401", `|Unauthorized|not logged in|not logged into|authentication required|To get started with GitHub CLI`)
	status404 = status("404", `|(?i)not found`)
	// code404 is a 404 reply only, not "glab not found" or the like.
	code404 = status("404", "")
	// unreachable is a CLI failing before any server answered.
	// (gh says "error connecting to <host>" and "check your internet
	// connection" when DNS or the network fails.)
	unreachable = regexp.MustCompile(`no such host|dial tcp|connection refused|i/o timeout|` +
		`context deadline exceeded|network is unreachable|no route to host|TLS handshake timeout|` +
		`error connecting to |check your internet connection`)
)

// ErrNotLoggedIn means the CLI has no login for the host.
var ErrNotLoggedIn = errors.New("not logged in")

// LoginCmd is the command that logs cli in to host (again); gh needs no
// host for github.com.
func LoginCmd(cli, host string) string {
	if host == "" || cli == "gh" && host == githubHost {
		return cli + " auth login"
	}
	return cli + " auth login --hostname " + host
}

// TokenStore is where a CLI keeps its token for a host, as its "auth
// status" reports it. gl-repos never reads the token itself.
type TokenStore int

// Where a token can be: unknown (an older CLI, or no login), the OS
// keyring, the CLI's config file in plain text, or an environment
// variable.
const (
	StoreUnknown TokenStore = iota
	StoreKeyring
	StorePlaintext
	StoreEnv
)

var (
	// glab 1.123: "Token found in configuration file (plaintext): ****"
	// or "Token found in operating system keyring: ****".
	glabTokenLine = regexp.MustCompile(`Token found in ([^:\n]+):`)
	// gh: "Logged in to github.com account octo (keyring)"; the parentheses
	// hold a config file path when stored in plain text, or the name of an
	// environment variable (GH_TOKEN). A token gh rejects shows as
	// "Failed to log in to github.com account octo (keyring)".
	ghLoginLine = regexp.MustCompile(`(?:Logged in|Failed to log in) to \S+ (?:account|as) \S+ \(([^)\n]*)\)`)
	envName     = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// glabTokenStore reads where glab keeps the token from "glab auth status
// --hostname host" output. Only the place is taken; the masked token
// after it is never kept.
func glabTokenStore(out string) TokenStore {
	m := glabTokenLine.FindStringSubmatch(out)
	if m == nil {
		return StoreUnknown
	}
	switch place := strings.ToLower(m[1]); {
	case strings.Contains(place, "keyring"):
		return StoreKeyring
	case strings.Contains(place, "plaintext"), strings.Contains(place, "configuration file"):
		return StorePlaintext
	case strings.Contains(place, "environment"):
		return StoreEnv
	}
	return StoreUnknown
}

// ghTokenStore reads where gh keeps the token of the first (active)
// account from "gh auth status --hostname host" output.
func ghTokenStore(out string) TokenStore {
	m := ghLoginLine.FindStringSubmatch(out)
	if m == nil {
		return StoreUnknown
	}
	switch place := strings.TrimSpace(m[1]); {
	case place == "keyring":
		return StoreKeyring
	case envName.MatchString(place):
		return StoreEnv
	case place != "":
		return StorePlaintext // the path of gh's hosts.yml
	}
	return StoreUnknown
}

// IsUnauthorized reports whether a CLI's output is a 401: no valid login,
// or a token that may not use this endpoint.
func IsUnauthorized(out string) bool { return status401.MatchString(out) }

// Unreachable reports whether a forge CLI's output says the server could
// not be reached at all (DNS, VPN, firewall), as opposed to a refusal.
func Unreachable(out string) bool { return unreachable.MatchString(out) }

// NetHint is the hint for a server that cannot be reached.
func NetHint(host string) string {
	if host == "" {
		host = "the server"
	}
	return "cannot reach " + host + ": check the VPN, DNS or firewall"
}

// AuthHint explains a forge CLI failure caused by the connection or the
// login, from the CLI's whole output: an unreachable server, a token
// without the needed scope or role, or no valid login. "" if the output
// shows none of these.
func AuthHint(cli, host, out string) string {
	switch {
	case Unreachable(out):
		return NetHint(host)
	case strings.Contains(out, "insufficient_scope"):
		if cli == "gh" {
			return ghRefresh(host)
		}
		return "the glab token needs the api scope (a legacy personal access token): " + LoginCmd(cli, host)
	case status403.MatchString(out):
		if cli == "gh" {
			return ghRefresh(host)
		}
		return "403 Forbidden: your role in the group may not allow this (creating subgroups needs Maintainer/Owner), " +
			"or the token lacks the api scope: " + LoginCmd(cli, host)
	case status401.MatchString(out):
		on := ""
		if host != "" {
			on = " to " + host
		}
		return "not logged in" + on + ": " + LoginCmd(cli, host)
	}
	return ""
}

// ghRefresh is the fix for a gh login that may not do something.
func ghRefresh(host string) string {
	h := ""
	if host != "" && host != githubHost {
		h = " --hostname " + host
	}
	return "the gh login may not do this: gh auth refresh" + h + " --scopes repo"
}

// Hint explains why a forge CLI command (creating a project or a
// subgroup) failed, from its whole output, in plain words with the fix;
// "" if the cause is not one gl-repos knows.
func Hint(cli, host, out string) string {
	where := ""
	if host != "" {
		where = " on " + host
	}
	switch {
	case strings.Contains(out, cli+" not found") || strings.Contains(out, `"`+cli+`": executable file not found`):
		return cli + " not found: install it"
	case AuthHint(cli, host, out) != "":
		return AuthHint(cli, host, out)
	case strings.Contains(out, "has already been taken") || strings.Contains(out, "already exists"):
		return "the name is already taken" + where + ": choose another"
	case status404.MatchString(out):
		return "not found" + where + ": check that the group exists and that you may create projects in it"
	}
	return ""
}

// Banner lines glab prints around replies that are not about the
// request: the NO_PROMPT deprecation warning and update notices. Only
// these known lines are dropped from error output.
var (
	// bannerStart begins a banner; indented lines after it continue it.
	bannerStart = regexp.MustCompile(`(?i)^(DEPRECATION WARNING\b|WARNING:.*\bNO_PROMPT\b|A new version of glab\b)`)
	bannerLine  = regexp.MustCompile(`(?i)^(To upgrade, run\b|.*\bbrew upgrade glab\b|.*\bglab check-update\b|` +
		`https://gitlab\.com/gitlab-org/cli/-/releases/\S*$|-+$)`)
	// errorLine is never a banner, whatever it says.
	errorLine = regexp.MustCompile(`^(?i:error|fatal)\b|^(glab|gh)( \(|:)`)
)

// cleanStderr keeps the meaningful lines of a CLI's error output: known
// glab banners go (with their indented continuation lines), and so do
// blank lines. A line that starts like an error is always kept, and if
// nothing would be left, the output is kept as it is.
func cleanStderr(s string) string {
	var keep []string
	inBanner := false
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			inBanner = false
		case errorLine.MatchString(t):
			inBanner = false
			keep = append(keep, t)
		case bannerStart.MatchString(t):
			inBanner = true
		case bannerLine.MatchString(t):
		case inBanner && l != strings.TrimLeft(l, " \t"):
			// an indented continuation of the banner
		default:
			inBanner = false
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		return strings.TrimSpace(s)
	}
	return strings.Join(keep, "\n")
}
