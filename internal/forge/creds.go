package forge

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"gl-repos/internal/safe"
)

// The credential shapes gl-repos knows are defined once, in edit: what
// RedactURLs and MaskURLs hide on screen is exactly what HasCredentials
// finds, so a command can never run with an argument the screen showed
// without its secret.

// urlUserinfo finds the "user[:password]@" of every "scheme://" URL in a
// text. The authority ends at the first slash, as git reads it, so a
// "?", "#" or "@" before it belongs to the password.
var urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*)://([^/\s]*)@`)

// plainUser is the one scheme → userinfo policy: whether the user of a
// "scheme://user[:password]@host" URL is a plain name (ssh's git@) or a
// secret like its password. Only the ssh family, where git logs in as
// that user, has a plain one; http, https, git and every other or
// unknown scheme carry a token there ("https://oauth2:<token>@",
// "https://<token>@", "git://<token>@"). credentials (display,
// HasCredentials) and ParseCloneURL (refusal) both read it, so what is
// hidden is what is refused, for every scheme.
func plainUser(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "ssh", "git+ssh", "ssh+git":
		return true
	}
	return false
}

// credentials splits a URL's userinfo "user[:password]" into what may
// be shown or sent on (keep) and what is secret, by plainUser: the
// password always, the user too unless scheme has a plain one. keep
// ends in ":" when a secret follows it.
func credentials(scheme, info string) (keep, secret string) {
	if !plainUser(scheme) {
		return "", info
	}
	if user, pass, ok := strings.Cut(info, ":"); ok {
		return user + ":", pass
	}
	return info, ""
}

// ErrURLCredentials refuses a clone URL that carries credentials: git
// would get them in its arguments and keep them in .git/config.
var ErrURLCredentials = errors.New("this URL contains credentials; use an ssh URL or a credential helper " +
	"(gl-repos never passes credentials to git)")

// CheckArgs refuses command arguments that carry URL credentials (see
// HasCredentials). It is the last check before git, glab or gh runs:
// whatever got past the checks before is not passed on.
func CheckArgs(args []string) error {
	for _, a := range args {
		if HasCredentials(a) {
			return ErrURLCredentials
		}
	}
	return nil
}

// HasCredentials reports whether s holds a credential of any shape
// RedactURLs would hide: any user or password of a URL whose scheme is
// not ssh-like (plainUser: "https://oauth2:<token>@…", "https://user@…",
// "git://<token>@…"), a password of an ssh-like one
// ("ssh://git:<password>@…"), a scheme-less "user:secret@host", or a
// secret parameter ("?private_token=…", "#access_token=…",
// ";private_token=…", also percent-encoded), with or without a URL
// around it. A plain ssh user ("git@host:path", "ssh://git@host/…") is
// none. s is checked as it is and sanitized: an escape sequence must
// not hide a secret from the match.
func HasCredentials(s string) bool {
	if _, found := edit(s, nil); found {
		return true
	}
	t := safe.Text(s)
	if t == s {
		return false
	}
	_, found := edit(t, nil)
	return found
}

// encSep is a percent-encoded "?", "#", "&" or ";".
const encSep = `%(?i:3f|23|26|3b)`

// urlParam finds a "name=value" parameter in a query ("?", "&"), a
// fragment ("#access_token=…") or a path (";private_token=…"), the
// separators and "=" also percent-encoded ("?x=1%26private_token=…"):
// group 1 is the separator, name and "=", group 2 the name, group 3 the
// value.
var urlParam = regexp.MustCompile(`((?:[?&;#]|` + encSep + `)([A-Za-z0-9_.%+-]*?)(?:=|%3[Dd]))([^&;#\s'"]*)`)

// encSepIn finds a percent-encoded separator inside a value: the start
// of the next parameter.
var encSepIn = regexp.MustCompile(encSep)

// secretWord is in the name of a parameter that carries a credential
// (private_token, access_token, api_key, X-Amz-Signature, …).
var secretWord = regexp.MustCompile(`(?i)token|key|secret|passw(?:or)?d|pass|auth|sig|signature|credential|session`)

// secretName reports whether a parameter name names a credential, also
// when it is percent-encoded ("%70rivate_token").
func secretName(name string) bool {
	if d, err := url.QueryUnescape(name); err == nil {
		name = d
	}
	return secretWord.MatchString(name)
}

// editParams rewrites the values of the secret parameters in s with f.
// A lone "?token=…" counts, with no URL around it.
func editParams(s string, f func(value string) string) string {
	return urlParam.ReplaceAllStringFunc(s, func(m string) string {
		sm := urlParam.FindStringSubmatch(m)
		value, rest := sm[3], ""
		if i := encSepIn.FindStringIndex(value); i != nil { // "?x=1%26token=…": one more parameter
			value, rest = value[:i[0]], editParams(value[i[0]:], f)
		}
		if value == "" || !secretName(sm[2]) {
			return sm[1] + value + rest
		}
		return sm[1] + f(value) + rest
	})
}

// bareUserinfo finds a scheme-less "user:secret@host"
// ("oauth2:<token>@gitlab.example.com/…"), also after a slash (a ref
// name, "origin/oauth2:<token>@host"): group 1 is what precedes it, 2
// the user, 3 the secret. The userinfo of a "scheme://" URL matches too
// (editUserinfo runs first, so for display it is already hidden), one
// after ":" is the secret of the userinfo before it.
var bareUserinfo = regexp.MustCompile(`(^|[^A-Za-z0-9._~+:@-])([A-Za-z0-9._~+-]+):([^\s/@'"]+)@`)

// editBare rewrites the secret of every scheme-less userinfo in s.
func editBare(s string, f func(secret string) string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	return bareUserinfo.ReplaceAllStringFunc(s, func(m string) string {
		sm := bareUserinfo.FindStringSubmatch(m)
		return sm[1] + sm[2] + ":" + f(sm[3]) + "@"
	})
}

// editUserinfo rewrites the userinfo of every URL in s: f gets the
// scheme and the shown and secret parts and returns what replaces
// "userinfo@".
func editUserinfo(s string, f func(scheme, keep, secret string) string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	return urlUserinfo.ReplaceAllStringFunc(s, func(m string) string {
		sm := urlUserinfo.FindStringSubmatch(m)
		keep, secret := credentials(sm[1], sm[2])
		if secret == "" {
			return m
		}
		return sm[1] + "://" + f(sm[1], keep, secret)
	})
}

// edit rewrites every secret in s with f (a nil f only looks) and
// reports whether there was one. It is the one definition of a
// credential shape: RedactURLs, MaskURLs and HasCredentials share it.
func edit(s string, f func(secret string) string) (out string, found bool) {
	hide := func(secret string) string {
		found = true
		if f == nil {
			return secret
		}
		return f(secret)
	}
	s = editUserinfo(s, func(_, keep, secret string) string { return keep + hide(secret) + "@" })
	s = editBare(s, hide)
	return editParams(s, hide), found
}

// RedactURLs hides the credentials of every URL in s, for display:
// "https://oauth2:<token>@host/…" → "https://***@host/…",
// "git://<token>@host/…" → "git://***@host/…",
// "ssh://git:<password>@host/…" → "ssh://git:***@host/…". ssh's
// "git@host:path" and "ssh://git@host/…" stay as they are. s may be a
// single URL or a CLI's output. s is sanitized (safe.Text) first: an
// escape sequence inside a credential could otherwise hide it from the
// match and vanish later, when the text is sanitized for display.
func RedactURLs(s string) string { return redact(safe.Text(s)) }

// RedactLine is RedactURLs for a single-line field (safe.Line): a
// description, a folder name.
func RedactLine(s string) string { return redact(safe.Line(s)) }

// RedactColored is RedactURLs for text with colors ("git log --color"):
// sanitized with safe.Colored, which keeps them. A color code inside a
// credential shape ("private_<SGR>token=…") would hide it from the
// match: when the uncolored text redacts differently, that is shown.
func RedactColored(s string) string {
	s = safe.Colored(s)
	out := redact(s)
	if plain := redact(safe.Text(s)); safe.Text(out) != plain {
		return plain
	}
	return out
}

// redact hides every credential (see edit) as "***". It over-redacts
// rather than miss: it is for display, not a check.
func redact(s string) string {
	s, _ = edit(s, func(string) string { return "***" })
	return s
}

// MaskURLs is RedactURLs that keeps every rune where it is (each secret
// rune becomes "*"), so a cursor position in s stays valid.
func MaskURLs(s string) string {
	s, _ = edit(s, func(v string) string { return strings.Repeat("*", utf8.RuneCountInString(v)) })
	return s
}

// StripCredentials removes the credentials from a URL a server sent:
// the whole userinfo of a URL whose scheme is not ssh-like (plainUser),
// the password of an ssh-like one
// ("ssh://git:<password>@host/…" → "ssh://git@host/…"), and any query
// (a clone URL needs none; it could carry a token).
func StripCredentials(u string) string {
	u = editUserinfo(u, func(_, keep, _ string) string {
		if keep == "" {
			return ""
		}
		return strings.TrimSuffix(keep, ":") + "@"
	})
	if i := strings.IndexAny(u, "?#"); i >= 0 && strings.Contains(u, "://") {
		u = u[:i]
	}
	return u
}
