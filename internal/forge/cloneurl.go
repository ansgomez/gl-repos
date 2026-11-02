package forge

import (
	"errors"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"gl-repos/internal/safe"
)

// CloneURL is a clone source that passed ParseCloneURL, the only way to
// build one: it holds the URL rebuilt from the parts that were checked,
// so git gets exactly what was checked. The zero value is no URL.
type CloneURL struct {
	s    string
	path string // project path on the server, without ".git"; "" for a local source
}

// ErrBadCloneURL refuses input that looks like a clone URL but is not
// one gl-repos accepts (ErrURLCredentials when it has a user or
// password where git would send them). Neither quotes the input, which
// may hold a secret.
var ErrBadCloneURL = errors.New("not a clone URL gl-repos accepts: use https://host/group/project.git, " +
	"ssh://[user@]host/group/project.git, [user@]host:group/project.git or a local path, " +
	"without credentials, ?, #, ; or % escapes; the project path must be names separated by single slashes (no //, . or ..)")

var (
	hostLabel = `[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?`
	hostName  = `(?:` + hostLabel + `\.)*` + hostLabel
	hostIPv6  = `\[[0-9A-Fa-f:.]+\]`
	// scpHost is the host of git's scp-like form, which has no port.
	scpHost = regexp.MustCompile(`^(?:` + hostName + `|` + hostIPv6 + `)$`)
	urlHost = regexp.MustCompile(`^(?:` + hostName + `|` + hostIPv6 + `)(?::\d{1,5})?$`)
	sshUser = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
)

// errBadHost refuses a host that is no plain host name; the host is not
// quoted (it came from a CLI's output or a config file).
var errBadHost = errors.New("use the bare host name, e.g. gitlab.example.com")

// ValidHost accepts a bare host name with an optional port, as glab and
// gh take it: a DNS name or a bracketed IPv6 address, the host a clone
// URL may have (urlHost), "gitlab.example.com", "gitlab.example.com:8443"
// or "[::1]:8443". It is the one host check: the config's host settings,
// the hosts "glab auth status" reports (LoggedInHosts) and the hosts of
// remotes (HostFromURL) pass it before a host is stored, shown, named in
// a cache file or given to a CLI as --hostname.
func ValidHost(h string) error {
	if !urlHost.MatchString(h) || !bracketsIPv6(h) {
		return errBadHost
	}
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.HasSuffix(h, "]") { // "host:port", "[::1]:port"
		if n, err := strconv.Atoi(h[i+1:]); err != nil || n < 1 || n > 65535 {
			return errBadHost
		}
	}
	return nil
}

// validSCPHost reports whether h is the host of git's scp-like form: a
// DNS name or a bracketed IPv6 address, without a port.
func validSCPHost(h string) bool { return scpHost.MatchString(h) && bracketsIPv6(h) }

// bracketsIPv6 reports whether a bracketed host ("[::1]", "[::1]:22")
// holds an IPv6 address; the hostIPv6 pattern only limits its
// characters ("[:::]" matches it). Other hosts pass.
func bracketsIPv6(h string) bool {
	if !strings.HasPrefix(h, "[") {
		return true
	}
	addr, _, ok := strings.Cut(h[1:], "]")
	return ok && strings.Contains(addr, ":") && net.ParseIP(addr) != nil
}

// The forms of a clone source are told apart as git does, by one
// classifier that IsURL (search or clone?) and ParseCloneURL share, so
// the two cannot disagree: "://" is a URL, a colon before the first
// slash is scp-like (host:path), an absolute path or one starting with
// ./ or ../ is local, and a bare name is none of them (a search).

// IsURL reports whether s is meant as a clone source rather than a
// search query: it has a scheme, is a local path (IsLocalPath), has
// git's scp-like shape (a colon before the first slash: "host:path",
// so also "user:secret@host"), starts with git@ or ends in .git. Text
// with a space or tab is a search unless it has a scheme or is a local
// path ("fix: parser" is a phrase, "foo:bar" is scp-like, as for git).
// Such input is parsed (ParseCloneURL) and never searched.
func IsURL(s string) bool {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") || IsLocalPath(s) {
		return true
	}
	if strings.ContainsAny(s, " \t") {
		return false
	}
	return scpLike(s) || strings.HasPrefix(s, "git@") || strings.HasSuffix(s, ".git") ||
		(strings.Contains(s, "@") && strings.Contains(s, ":"))
}

// IsLocalPath reports whether s names a local clone source: an absolute
// path, or one starting with ./ or ../ (a bare name is a search).
func IsLocalPath(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")
}

// scpLike reports whether s has git's scp-like shape, [user@]host:path:
// a colon before the first slash (or with no slash at all), not at the
// start. It is git's own rule (url_is_local_not_ssh), checked before a
// local path: "gitlab.example.com:acme/tools" is a host and a path,
// "./a:b" a local path.
func scpLike(s string) bool {
	colon, slash := strings.Index(s, ":"), strings.Index(s, "/")
	return colon > 0 && (slash < 0 || colon < slash)
}

// ParseCloneURL checks a clone source and rebuilds it from its parts.
// The forms are git's own, told apart as git does (see IsURL):
//
//   - https, http, git://host[:port]/path, with no user at all (there,
//     a user is a token: plainUser in creds.go, the one scheme policy
//     that display and refusal share);
//   - ssh, git+ssh, ssh+git://[user@]host[:port]/path and scp-like
//     [user@]host:path, where the user is a plain name and there is no
//     password; the scp-like path may be absolute ("host:/srv/git/x",
//     kept as it is: "host:srv/git/x" would be another repository,
//     relative to the home directory);
//   - file:///path and local paths (IsLocalPath), which reach no
//     server; a bare name is no local path (it is a search), also when
//     a server sends it as a clone URL.
//
// A host is a DNS name or a bracketed IPv6 address. A server path is
// any number of plain names (GitLab subgroups), ".git" optional; one
// trailing slash is dropped. A "~user" home-relative path
// ("host:~user/x", "ssh://host/~user/x") is no plain name and refused,
// as is a doubled slash anywhere ("ssh://host//srv/x"): git would send
// those on as they are, and gl-repos rebuilds only what it checked. ?,
// #, ; and % are refused anywhere, as is "@" outside an ssh user: they
// are where credentials hide (query or fragment tokens, ";param=",
// escapes that hide either). Other schemes (ext::, fd::, ftp://…) and
// control characters are refused too. All of this is refused rather
// than cleaned: a URL is never half-used.
func ParseCloneURL(s string) (CloneURL, error) {
	s = strings.TrimSpace(s)
	if s == "" || safe.Line(s) != s || strings.ContainsAny(s, "?#;%") || strings.HasPrefix(s, "-") {
		return CloneURL{}, ErrBadCloneURL
	}
	if strings.Contains(s, "://") {
		return parseURL(s)
	}
	if scpLike(s) {
		return parseSCP(s)
	}
	if strings.Contains(s, "@") || !IsLocalPath(s) {
		return CloneURL{}, ErrBadCloneURL
	}
	return CloneURL{s: s}, nil // a local path
}

// parseSCP checks git's scp-like [user@]host:path.
func parseSCP(s string) (CloneURL, error) {
	info, host, p, hasUser := splitSCP(s)
	user := ""
	if hasUser {
		var err error
		if user, err = urlUser("ssh", info); err != nil {
			return CloneURL{}, err
		}
	}
	proj, ok := serverPath(p)
	if !validSCPHost(host) || !ok {
		return CloneURL{}, ErrBadCloneURL
	}
	// p keeps its leading slash (an absolute path on the server; see
	// ParseCloneURL) and loses the one trailing slash serverPath allowed.
	return CloneURL{s: user + host + ":" + strings.TrimSuffix(p, "/"), path: proj}, nil
}

// urlUser checks the userinfo "user[:password]" of a clone URL by the
// one scheme policy (credentials, plainUser): a secret user or any
// password, even an empty one, is refused; a plain user must be a plain
// name. It returns the "user@" to rebuild the URL with.
func urlUser(scheme, info string) (string, error) {
	keep, secret := credentials(scheme, info)
	if secret != "" || strings.HasSuffix(keep, ":") {
		return "", ErrURLCredentials
	}
	if !sshUser.MatchString(keep) {
		return "", ErrBadCloneURL
	}
	return keep + "@", nil
}

// splitSCP splits an scp-like s into user (hasUser), host and path:
// "git@[::1]:acme/x" is "git", "[::1]", "acme/x". The host of a
// malformed s (no "]", nothing after it) is "", which no host check
// accepts.
func splitSCP(s string) (user, host, p string, hasUser bool) {
	user, rest, hasUser := strings.Cut(s, "@")
	if !hasUser {
		user, rest = "", s
	}
	if strings.HasPrefix(rest, "[") { // [::1]:path
		i := strings.Index(rest, "]")
		if i < 0 || !strings.HasPrefix(rest[i+1:], ":") {
			return user, "", "", hasUser
		}
		return user, rest[:i+1], rest[i+2:], hasUser
	}
	host, p, _ = strings.Cut(rest, ":")
	return user, host, p, hasUser
}

// parseURL checks a scheme://… URL.
func parseURL(s string) (CloneURL, error) {
	u, err := url.Parse(s)
	if err != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return CloneURL{}, ErrBadCloneURL
	}
	scheme, user := strings.ToLower(u.Scheme), ""
	switch scheme {
	case "file":
		if u.User != nil || u.Host != "" || !strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "@") {
			return CloneURL{}, ErrBadCloneURL
		}
		return CloneURL{s: "file://" + u.Path}, nil
	case "https", "http", "git", "ssh", "git+ssh", "ssh+git":
		if u.User != nil {
			info := u.User.Username()
			if pw, ok := u.User.Password(); ok {
				info += ":" + pw
			}
			var err error
			if user, err = urlUser(scheme, info); err != nil {
				return CloneURL{}, err
			}
		}
	default:
		return CloneURL{}, ErrBadCloneURL
	}
	proj, ok := serverPath(u.Path)
	if ValidHost(u.Host) != nil || !ok {
		return CloneURL{}, ErrBadCloneURL
	}
	// u.Path starts with the one slash serverPath allowed (it is not
	// empty: "" is no project path) and loses the one trailing slash.
	return CloneURL{s: scheme + "://" + user + u.Host + strings.TrimSuffix(u.Path, "/"), path: proj}, nil
}

// serverPath is the project path of a server URL's path p, if it is
// plain names: p without one leading slash (an scp-like path has none,
// or one for an absolute path), one trailing slash and ".git". More
// slashes ("//srv/x", "x//") are no plain names and so refused: git
// would send them on as they are, and the rebuilt URL must be what was
// checked.
func serverPath(p string) (string, bool) {
	p = strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/"), ".git")
	return p, safePath(p)
}

// String is the URL as git gets it.
func (u CloneURL) String() string { return u.s }

// IsZero reports whether u is no URL.
func (u CloneURL) IsZero() bool { return u.s == "" }

// Path is the project path on the server ("acme/tools"), or "" for a
// local source.
func (u CloneURL) Path() string { return u.path }

// Name is the folder git would clone into ("tools"), or "" when the
// last path segment is no plain name.
func (u CloneURL) Name() string {
	p := u.path
	if p == "" {
		p = strings.TrimSuffix(strings.TrimRight(strings.TrimPrefix(u.s, "file://"), "/"), ".git")
	}
	n := path.Base(p)
	if !safePath(n) {
		return ""
	}
	return n
}
