package forge

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCloneURL(t *testing.T) {
	for _, c := range []struct{ in, url, path, name string }{
		{"https://gitlab.example.com/acme/tools.git", "https://gitlab.example.com/acme/tools.git", "acme/tools", "tools"},
		{"  HTTPS://gitlab.example.com/acme/tools/  ", "https://gitlab.example.com/acme/tools", "acme/tools", "tools"},
		{"https://gitlab.example.com:8443/acme/sub/deep/tools.git", "https://gitlab.example.com:8443/acme/sub/deep/tools.git", "acme/sub/deep/tools", "tools"},
		{"http://[::1]:8080/acme/tools.git", "http://[::1]:8080/acme/tools.git", "acme/tools", "tools"},
		{"git://gitlab.example.com/acme/tools", "git://gitlab.example.com/acme/tools", "acme/tools", "tools"},
		{"ssh://git@gitlab.example.com:2222/acme/tools.git", "ssh://git@gitlab.example.com:2222/acme/tools.git", "acme/tools", "tools"},
		{"git+ssh://gitlab.example.com/acme/tools.git", "git+ssh://gitlab.example.com/acme/tools.git", "acme/tools", "tools"},
		{"ssh://flaky/tmp/tmp.Ab12/fx/origin.git", "ssh://flaky/tmp/tmp.Ab12/fx/origin.git", "tmp/tmp.Ab12/fx/origin", "origin"},
		{"git@gitlab.example.com:acme/tools.git", "git@gitlab.example.com:acme/tools.git", "acme/tools", "tools"},
		{"gitlab.example.com:acme/tools", "gitlab.example.com:acme/tools", "acme/tools", "tools"},
		{"git@[::1]:acme/tools.git", "git@[::1]:acme/tools.git", "acme/tools", "tools"},
		// an absolute path on the server keeps its slash: without it git
		// would ask for another repository, relative to the home directory
		{"git@gitlab.example.com:/srv/git/tools.git", "git@gitlab.example.com:/srv/git/tools.git", "srv/git/tools", "tools"},
		{"gitlab.example.com:/srv/git/tools/", "gitlab.example.com:/srv/git/tools", "srv/git/tools", "tools"},
		{"git@[::1]:/srv/git/tools.git", "git@[::1]:/srv/git/tools.git", "srv/git/tools", "tools"},
		{"ssh://git@gitlab.example.com/srv/git/tools.git/", "ssh://git@gitlab.example.com/srv/git/tools.git", "srv/git/tools", "tools"},
		{"file:///srv/git//x.git", "file:///srv/git//x.git", "", "x"},
		{"/srv/git/some dir/x.git", "/srv/git/some dir/x.git", "", "x"},
		{"../repos/tools.git", "../repos/tools.git", "", "tools"},
		{"/srv/git/my repo", "/srv/git/my repo", "", ""}, // no plain name: the user types one
	} {
		u, err := ParseCloneURL(c.in)
		if err != nil || u.String() != c.url || u.Path() != c.path || u.Name() != c.name {
			t.Errorf("ParseCloneURL(%q) = %q path %q name %q, %v; want %q %q %q", c.in, u, u.Path(), u.Name(), err, c.url, c.path, c.name)
		}
	}
}

// Every shape a credential can hide in is refused, not cleaned, and
// the error never quotes the input.
func TestParseCloneURLRefuses(t *testing.T) {
	for _, in := range []string{
		"https://gitlab.example.com:0/acme/tools.git",
		"ssh://git@gitlab.example.com:65536/acme/tools.git",
		"https://[:::]/acme/tools.git",
		"https://[deadbeef]:8443/acme/tools.git",
		"git@[:::]:acme/tools.git",
		"git@[1.2.3.4]:acme/tools.git",
		"https://oauth2:SECRET@gitlab.example.com/acme/tools.git",
		"https://SECRET@gitlab.example.com/acme/tools.git",
		"ssh://git:SECRET@gitlab.example.com/acme/tools.git",
		"ssh://git:@gitlab.example.com/acme/tools.git",
		"git://SECRET@gitlab.example.com/acme/tools.git",
		"GIT://SECRET@gitlab.example.com/acme/tools.git",
		"git://oauth2:SECRET@gitlab.example.com/acme/tools.git",
		"https://@gitlab.example.com/acme/tools.git",
		"oauth2:glpat-SECRET@gitlab.example.com/acme/tools.git",
		"oauth2:glpat-SECRET@gitlab.example.com:acme/tools.git",
		"https://gitlab.example.com/acme/tools.git?private_token=SECRET",
		"https://gitlab.example.com/acme/tools.git?%70rivate_token=SECRET",
		"https://gitlab.example.com/acme/tools.git#access_token=SECRET",
		"https://h/p.git#access_token=SECRET",
		"https://gitlab.example.com/acme/tools.git;private_token=SECRET",
		"https://gitlab.example.com/acme/tools.git?",
		"https://gitlab.example.com/acme%2Ftools.git",
		"https://gitlab.example.com/acme//tools.git",
		"https://gitlab.example.com/acme/./tools.git",
		"https://gitlab.example.com/acme/../tools.git",
		"https://gitlab.example.com/acme/.git",
		"https://gitlab.example.com/",
		"https://-oProxyCommand=x/acme/tools.git",
		"https://gitlab.example.com/acme/tools.git\x1b]0;x\x07",
		"ssh://-oProxyCommand=x@h/acme/tools.git",
		"ext::sh -c touch",
		"ftp://gitlab.example.com/acme/tools.git",
		"file://host/srv/x.git",
		"-u/srv/x.git",
		"/srv/SECRET@x.git",
		"git@gitlab.example.com:acme/SECRET@x.git",
		"git@gitlab.example.com:../x.git",
		// paths git would send on as they are but gl-repos cannot rebuild
		// from plain names: home-relative, doubled slashes
		"git@gitlab.example.com:~user/tools.git",
		"gitlab.example.com:~/tools.git",
		"ssh://gitlab.example.com/~user/tools.git",
		"ssh://gitlab.example.com//srv/git/tools.git",
		"git@gitlab.example.com://srv/git/tools.git",
		"https://gitlab.example.com/acme/tools.git//",
		"https://gitlab.example.com//acme/tools.git",
		"git@gitlab.example.com:",
		"git@gitlab.example.com:/",
		"git@[::1/x.git",
		// a bare name is a search, never a local path (also from a server)
		"tools.git",
		"srv/git/tools",
		"foo:",
		":tools",
		"git@gitlab.example.com/acme/tools.git",
		"",
	} {
		u, err := ParseCloneURL(in)
		if !errors.Is(err, ErrBadCloneURL) && !errors.Is(err, ErrURLCredentials) {
			t.Errorf("ParseCloneURL(%q) = %q, %v", in, u, err)
		}
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Errorf("error quotes the input: %v", err)
		}
	}
}

// ParseCloneURL and the screen read one scheme policy (plainUser): a
// URL with a user is refused as credentials exactly when the user would
// be hidden, for every scheme gl-repos clones; a password always is.
// A scheme it does not clone is refused anyway, and its user hidden.
func TestUserinfoPolicyAgrees(t *testing.T) {
	cloned := map[string]bool{"https": true, "http": true, "git": true, "ssh": true, "git+ssh": true, "ssh+git": true, "file": true}
	for _, scheme := range []string{"https", "http", "git", "ssh", "git+ssh", "ssh+git", "ftp", "ext", "x-unknown", "HTTPS", "SSH"} {
		plain := scheme + "://git@h/acme/p.git"
		_, err := ParseCloneURL(plain)
		hidden := HasCredentials(plain) || RedactURLs(plain) != plain || StripCredentials(plain) != plain
		switch {
		case hidden != !plainUser(scheme):
			t.Errorf("%s: user hidden %v, plainUser %v", plain, hidden, plainUser(scheme))
		case cloned[strings.ToLower(scheme)] && hidden != errors.Is(err, ErrURLCredentials):
			t.Errorf("%s: user hidden %v, ParseCloneURL %v", plain, hidden, err)
		case !cloned[strings.ToLower(scheme)] && (err == nil || !hidden):
			t.Errorf("%s: cloned (%v), hidden %v", plain, err, hidden)
		}
		pw := scheme + "://git:SECRET@h/acme/p.git"
		if _, err := ParseCloneURL(pw); err == nil || !HasCredentials(pw) || strings.Contains(RedactURLs(pw), "SECRET") ||
			strings.Contains(StripCredentials(pw), "SECRET") {
			t.Errorf("%s: password: %v, HasCredentials %v, %q", pw, err, HasCredentials(pw), RedactURLs(pw))
		}
	}
	// file:// has no user at all
	if _, err := ParseCloneURL("file://git@/srv/p.git"); err == nil {
		t.Error("file:// URL with a user cloned")
	}
}
