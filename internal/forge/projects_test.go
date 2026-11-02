package forge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func names(ps []Project) string {
	var s []string
	for _, p := range ps {
		s = append(s, p.Path)
	}
	return strings.Join(s, ",")
}

var sample = []Project{ // most recent first, as GitLab returns them
	{Path: "acme/tools/cli/deploykit", Name: "deploykit"},
	{Path: "acme/platform/services/payments/billing-v3", Name: "billing-v3"},
	{Path: "acme/platform/handbook/documentation", Name: "documentation"},
	{Path: "acme/platform/services/payments/lib/billing_core", Name: "billing_core"},
	{Path: "acme/people/sam/workspace/deploykit-notes", Name: "deploykit-notes"},
	{Path: "acme/tools/deploy", Name: "deploy"},
}

func TestMatch(t *testing.T) {
	for _, c := range []struct{ q, want string }{
		{"billing", "acme/platform/services/payments/billing-v3,acme/platform/services/payments/lib/billing_core"},
		{"billing payments lib", "acme/platform/services/payments/lib/billing_core"},
		{"handbook/doc", "acme/platform/handbook/documentation"},
		{"deploy", "acme/tools/deploy,acme/tools/cli/deploykit,acme/people/sam/workspace/deploykit-notes"},
		{"bilv3", "acme/platform/services/payments/billing-v3"}, // subsequence fallback
		{"nomatch", ""},
	} {
		if got := names(Match(sample, c.q, 10)); got != c.want {
			t.Errorf("Match(%q)\n got  %s\n want %s", c.q, got, c.want)
		}
	}
	if got := Match(sample, "", 2); len(got) != 2 || got[0].Name != "deploykit" {
		t.Errorf("empty query should list most recent: %s", names(got))
	}
	if got := Match(sample, "i", 3); len(got) != 3 {
		t.Errorf("limit not applied: %d", len(got))
	}
}

func TestListAllPaginated(t *testing.T) {
	var gotFlags []string
	g := GitLab{Host: "h", API: func(_ context.Context, _, ep string, flags ...string) ([]byte, error) {
		gotFlags = flags
		return []byte(`[{"path_with_namespace":"a/x","path":"x"}]` + "\n" + `[{"path_with_namespace":"a/y","path":"y"}]`), nil
	}}
	ps, err := g.ListAll(context.Background())
	if err != nil || names(ps) != "a/x,a/y" {
		t.Fatalf("got %s, %v", names(ps), err)
	}
	if strings.Join(gotFlags, " ") != "--paginate" {
		t.Errorf("flags = %v", gotFlags)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	if ps, at := LoadCache("work", "git.example.org", GitLab{}); ps != nil || !at.IsZero() {
		t.Fatalf("empty cache returned %v %v", ps, at)
	}
	if err := SaveCache("work", "git.example.org", sample); err != nil {
		t.Fatal(err)
	}
	ps, at := LoadCache("work", "git.example.org", GitLab{})
	if names(ps) != names(sample) || at.IsZero() {
		t.Errorf("round trip: %s at %v", names(ps), at)
	}
	// a file written before exclusion existed is filtered on read
	if ps, _ := LoadCache("work", "git.example.org", GitLab{Exclude: []string{"payments"}}); strings.Contains(names(ps), "payments") || len(ps) != 4 {
		t.Errorf("exclusion on load: %s", names(ps))
	}
	if cachePath("projects", "work", "../evil") != "" {
		t.Errorf("host with path separators must not form a cache path")
	}
	if p := cachePath("projects", "../x/y", "h"); filepath.Dir(p) != CacheDir() {
		t.Errorf("profile name escapes the cache dir: %s", p)
	}
	if left, _ := filepath.Glob(filepath.Join(CacheDir(), "*.tmp")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// A group excluded after the lists were cached leaves the cache files the
// next time they are read, not only the screen: the files are rewritten
// without it, keeping their fetch time, for projects and for groups.
func TestCacheRewrittenWithoutExcluded(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	if err := SaveCache("work", "h", sample); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroupCache("work", "h", []Project{{Path: "acme", Name: "acme"}, {Path: "acme/payments", Name: "payments"}}); err != nil {
		t.Fatal(err)
	}
	f := GitLab{Host: "h", Exclude: []string{"payments"}}
	ps, at := LoadCache("work", "h", f)
	gs, gat := LoadGroupCache("work", "h", f)
	if strings.Contains(names(ps), "payments") || len(ps) != 4 || names(gs) != "acme" || at.IsZero() || gat.IsZero() {
		t.Fatalf("loaded %s at %v, groups %s at %v", names(ps), at, names(gs), gat)
	}
	for _, kind := range []string{"projects", "groups"} {
		data, err := os.ReadFile(cachePath(kind, "work", "h"))
		if err != nil || strings.Contains(string(data), "payments") {
			t.Errorf("%s cache still holds the excluded group (%v): %s", kind, err, data)
		}
		var c projectCache
		if json.Unmarshal(data, &c) != nil || !c.Fetched.Equal(map[string]time.Time{"projects": at, "groups": gat}[kind]) {
			t.Errorf("%s cache rewritten with another fetch time: %s", kind, data)
		}
	}
	if ps, _ := LoadCache("work", "h", GitLab{Host: "h"}); strings.Contains(names(ps), "payments") {
		t.Errorf("the excluded group came back from the file: %s", names(ps))
	}
}

// Two profiles on one host exclude different groups: each keeps its own
// list, so neither hides the other's projects (nor caches excluded ones).
func TestCacheSharedHostTwoProfiles(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	all := func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(`[{"path_with_namespace":"acme/legal/x","path":"x"},{"path_with_namespace":"acme/tools/y","path":"y"}]`), nil
	}
	for _, p := range []struct{ name, exclude string }{{"work", "legal"}, {"audit", "tools"}} {
		g := GitLab{Host: "h", Exclude: []string{p.exclude}, API: all}
		ps, err := g.ListAll(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := SaveCache(p.name, "h", ps); err != nil {
			t.Fatal(err)
		}
	}
	if ps, _ := LoadCache("work", "h", GitLab{Exclude: []string{"legal"}}); names(ps) != "acme/tools/y" {
		t.Errorf("work = %s", names(ps))
	}
	if ps, _ := LoadCache("audit", "h", GitLab{Exclude: []string{"tools"}}); names(ps) != "acme/legal/x" {
		t.Errorf("audit = %s (filtered by the other profile's exclude?)", names(ps))
	}
}

func TestExcluded(t *testing.T) {
	ex := []string{"legal", "finance", "acme/legal"}
	for _, c := range []struct {
		path string
		want bool
	}{
		{"org/lab/legal/projA/repo", true}, // group name at any depth
		{"org/legal/repo", true},
		{"Org/Finance/payroll", true},  // case-insensitive
		{"acme/legal/contracts", true}, // full group path prefix
		{"acme/legal", true},
		{"acme/legalese/x", false}, // prefix must end at a segment
		{"org/tools/legal", false}, // project *named* legal is not a group
		{"org/legalese/x", false},  // whole segment only
		{"org/tools/deploykit", false},
		{"", false},
	} {
		if got := Excluded(c.path, ex); got != c.want {
			t.Errorf("Excluded(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestExclusionAppliedEverywhere(t *testing.T) {
	list := `[{"path_with_namespace":"org/tools/deploykit","path":"deploykit"},` +
		`{"path_with_namespace":"org/legal/secret","path":"secret"},` +
		`{"path_with_namespace":"org/finance/payroll","path":"payroll"}]`
	g := GitLab{Host: "h", Exclude: []string{"legal", "finance"}, API: func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
		if strings.HasPrefix(ep, "projects/") {
			return []byte(`{"path_with_namespace":"org/legal/secret","path":"secret"}`), nil
		}
		return []byte(list), nil
	}}
	ps, err := g.ListAll(context.Background())
	if err != nil || names(ps) != "org/tools/deploykit" {
		t.Errorf("ListAll: %s, %v", names(ps), err)
	}
	ps, err = g.Search(context.Background(), "secret payroll kit")
	if err != nil || names(ps) != "org/tools/deploykit" {
		t.Errorf("Search: %s, %v", names(ps), err)
	}
	if _, err := g.Search(context.Background(), "org/legal/secret"); err == nil {
		t.Errorf("direct lookup of an excluded project must fail")
	}
	for u, want := range map[string]bool{
		"git@h:org/legal/secret.git":        false,
		"https://h/org/finance/payroll.git": false,
		"h:org/legal/secret.git":            false, // scp-like without user
		"me@h:org/finance/payroll.git":      false,
		"git@h:org/tools/deploykit.git":     true,
		"https://h/org/tools/deploykit":     true,
	} {
		if cu, err := ParseCloneURL(u); err != nil || g.AllowsURL(cu) != want {
			t.Errorf("AllowsURL(%q) = %v (%v)", u, !want, err)
		}
	}
}

// A project whose path is allowed but whose clone URL (which is what git
// gets) points into an excluded group is dropped from every list a clone
// is picked from: the project list, a group's projects and the cache.
func TestExcludedCloneURLDropped(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	list := `[{"path_with_namespace":"org/tools/kit","path":"kit","ssh_url_to_repo":"git@h:org/tools/kit.git","http_url_to_repo":"https://h/org/tools/kit.git"},` +
		`{"path_with_namespace":"org/tools/moved","path":"moved","ssh_url_to_repo":"git@h:org/legal/moved.git","http_url_to_repo":"https://h/org/tools/moved.git"},` +
		`{"path_with_namespace":"org/tools/mirror","path":"mirror","http_url_to_repo":"https://h/org/legal/mirror.git"}]`
	g := GitLab{Host: "h", Exclude: []string{"legal"}, API: func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(list), nil
	}}
	ps, err := g.ListAll(context.Background())
	if err != nil || names(ps) != "org/tools/kit" {
		t.Errorf("ListAll: %s, %v", names(ps), err)
	}
	ps, err = g.GroupProjects(context.Background(), "org/tools")
	if err != nil || names(ps) != "org/tools/kit" {
		t.Errorf("GroupProjects: %s, %v", names(ps), err)
	}
	var all []Project
	if err := json.Unmarshal([]byte(list), &all); err != nil {
		t.Fatal(err)
	}
	if err := SaveCache("work", "h", all); err != nil {
		t.Fatal(err)
	}
	if ps, _ := LoadCache("work", "h", g); names(ps) != "org/tools/kit" {
		t.Errorf("LoadCache: %s", names(ps))
	}
}

// A URL whose path is not plain names may reach an excluded project on
// the server ("acme//legal" is "acme/legal" there): refused.
func TestAllowsURLRefusesNonCanonicalPaths(t *testing.T) {
	for _, f := range []Forge{GitLab{Host: "gitlab.example.com", Exclude: []string{"acme/legal"}},
		GitHub{Exclude: []string{"acme/legal"}}} {
		for u, want := range map[string]bool{
			"https://gitlab.example.com/acme/legal/x.git":          false,
			"https://gitlab.example.com/acme//legal/x.git":         false,
			"https://gitlab.example.com/acme/./legal/x.git":        false,
			"https://gitlab.example.com/acme/tools/../legal/x.git": false,
			"https://gitlab.example.com/acme/tools/..":             false,
			"git@gitlab.example.com:acme//legal/x.git":             false,
			"gitlab.example.com:acme/./legal/x.git":                false,
			"ssh://git@gitlab.example.com/acme/./legal/x.git":      false,
			"ssh://git@gitlab.example.com:2222/acme//legal/x.git":  false,
			"https://gitlab.example.com/acme/tools/x.git":          true,
			"git@gitlab.example.com:acme/tools/x.git":              true,
			"ssh://git@gitlab.example.com:2222/acme/tools/x.git":   true,
			"/srv/git/some dir/x.git":                              true, // local: no server project
			"file:///srv/git//x.git":                               true,
		} {
			cu, err := ParseCloneURL(u)
			if got := err == nil && f.AllowsURL(cu); got != want {
				t.Errorf("%s AllowsURL(%q) = %v, want %v", f.Name(), u, got, want)
			}
		}
	}
}

// A cache written with credentials in its URLs (by an older version) is
// rewritten cleaned, private, with its fetch time kept.
func TestOldCacheWithCredentialsRewritten(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	p := cachePath("projects", "work", "h")
	fetched := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	old, _ := json.Marshal(projectCache{Fetched: fetched, Projects: []Project{{Path: "acme/a", Name: "a",
		HTTPURL: "https://oauth2:glpat-xxx@h/acme/a.git", SSHURL: "ssh://git:pw@h/acme/a.git",
		Description: "mirror of https://oauth2:glpat-xxx@h/x ?private_token=glpat-xxx"}}})
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, old, 0o644); err != nil {
		t.Fatal(err)
	}
	ps, at := LoadCache("work", "h", GitLab{})
	if len(ps) != 1 || ps[0].HTTPURL != "https://h/acme/a.git" || ps[0].SSHURL != "ssh://git@h/acme/a.git" || !at.Equal(fetched) ||
		ps[0].Description != "mirror of https://***@h/x ?private_token=***" {
		t.Fatalf("loaded %+v at %v", ps, at)
	}
	data, err := os.ReadFile(p)
	if err != nil || strings.Contains(string(data), "glpat-xxx") || strings.Contains(string(data), ":pw@") {
		t.Fatalf("file not rewritten (%v): %s", err, data)
	}
	var c projectCache
	if json.Unmarshal(data, &c) != nil || !c.Fetched.Equal(fetched) || len(c.Projects) != 1 {
		t.Errorf("rewritten cache: %s", data)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v (%v), want 0600", fi.Mode().Perm(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(CacheDir(), "*.tmp")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// What a server sends is cleaned as the cache is written: a credential
// in a description or URL never reaches the disk, for projects and for
// groups.
func TestCacheWrittenWithoutCredentials(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	for _, sec := range credentialShapes {
		desc := "see " + sec + " now"
		if err := SaveCache("work", "h", []Project{{Path: "acme/a", Name: "a", SSHURL: "git@h:acme/a.git",
			HTTPURL: "https://oauth2:SECRET@h/acme/a.git", Description: desc}}); err != nil {
			t.Fatal(err)
		}
		if err := SaveGroupCache("work", "h", []Project{{Path: "acme", Name: "acme", Group: true, Description: desc}}); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"projects", "groups"} {
			data, err := os.ReadFile(cachePath(kind, "work", "h"))
			if err != nil || strings.Contains(string(data), "SECRET") {
				t.Errorf("%s cache for %q (%v): %s", kind, sec, err, data)
			}
		}
		ps, _ := LoadCache("work", "h", GitLab{})
		gs, _ := LoadGroupCache("work", "h", GitLab{})
		if len(ps) != 1 || len(gs) != 1 || strings.Contains(ps[0].Description, "SECRET") || strings.Contains(gs[0].Description, "SECRET") ||
			ps[0].HTTPURL != "https://h/acme/a.git" || !gs[0].Group {
			t.Errorf("loaded %+v %+v", ps, gs)
		}
	}
}

// useDefaultCacheDir points the OS cache directory, and so CacheDir(),
// at a fresh folder: gl-repos's own default, not one chosen with
// GL_REPOS_CACHE_DIR.
func useDefaultCacheDir(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	t.Setenv(EnvCacheDir, "")
	t.Setenv(LegacyEnvCacheDir, "")
	t.Setenv("XDG_CACHE_HOME", base) // Linux
	t.Setenv("HOME", base)           // macOS: ~/Library/Caches
	if d := CacheDir(); d == "" || d != defaultCacheDir() || !strings.HasPrefix(d, base) {
		t.Fatalf("default cache directory %q, want one below %s", d, base)
	}
}

// A clean cache written 0644 in a 0755 directory by an older version is
// tightened to 0600 in a 0700 directory on load, without being rewritten.
// The directory is tightened only when it is gl-repos's own default: one
// chosen with GL_REPOS_CACHE_DIR keeps its mode (the file is 0600 still).
func TestOldCachePermissionsTightened(t *testing.T) {
	for _, own := range []bool{true, false} {
		if own {
			useDefaultCacheDir(t)
		} else {
			t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
		}
		testOldCachePermissions(t, own)
	}
}

func testOldCachePermissions(t *testing.T, own bool) {
	t.Helper()
	p := cachePath("projects", "work", "h")
	data, _ := json.Marshal(projectCache{Fetched: time.Now(), Projects: []Project{{Path: "acme/a", Name: "a",
		HTTPURL: "https://h/acme/a.git", SSHURL: "git@h:acme/a.git"}}})
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(p), 0o755); err != nil { // umask aside
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if ps, _ := LoadCache("work", "h", GitLab{}); len(ps) != 1 {
		t.Fatalf("loaded %+v", ps)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("own %v: mode %v, %v", own, fi.Mode().Perm(), err)
	}
	want := os.FileMode(0o755)
	if own {
		want = 0o700
	}
	if fi, err := os.Stat(filepath.Dir(p)); err != nil || fi.Mode().Perm() != want {
		t.Errorf("own %v: directory mode %v, %v; want %v", own, fi.Mode().Perm(), err, want)
	}
	if now, _ := os.ReadFile(p); string(now) != string(data) {
		t.Error("a clean cache was rewritten")
	}
}

// A cache whose mode cannot be tightened is dropped, and none is read
// from or written into a folder of gl-repos's own that cannot be made
// private.
func TestCacheModeFailures(t *testing.T) {
	useDefaultCacheDir(t)
	ps := []Project{{Path: "acme/a", Name: "a", SSHURL: "git@h:acme/a.git"}}
	if err := SaveCache("work", "h", ps); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chmod, fchmod = (*os.Root).Chmod, (*os.File).Chmod })
	fchmod = func(*os.File, os.FileMode) error { return os.ErrPermission }
	if got, at := LoadCache("work", "h", GitLab{}); got != nil || !at.IsZero() {
		t.Errorf("loaded %v", got)
	}
	if _, err := os.Stat(cachePath("projects", "work", "h")); !os.IsNotExist(err) {
		t.Errorf("cache kept: %v", err)
	}
	fchmod = (*os.File).Chmod
	if err := SaveCache("work", "h", ps); err != nil {
		t.Fatal(err)
	}
	chmod = func(*os.Root, string, os.FileMode) error { return os.ErrPermission }
	if got, at := LoadCache("work", "h", GitLab{}); got != nil || !at.IsZero() {
		t.Errorf("loaded from a folder that is not private: %v", got)
	}
	if err := os.Remove(cachePath("projects", "work", "h")); err != nil {
		t.Fatal(err)
	}
	if err := SaveCache("work", "h", ps); err == nil {
		t.Error("saved into a folder that is not private")
	}
	if _, err := os.Stat(cachePath("projects", "work", "h")); !os.IsNotExist(err) {
		t.Errorf("cache written: %v", err)
	}
}

// A cache file over MaxOutput bytes (none gl-repos writes is) is not read
// into memory: it is no cache.
func TestCacheTooLargeNotRead(t *testing.T) {
	old := MaxOutput
	MaxOutput = 16
	t.Cleanup(func() { MaxOutput = old })
	p := filepath.Join(t.TempDir(), "c.json")
	if err := WriteCacheFile(p, []byte(strings.Repeat("x", 17))); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadCacheFile(p); err == nil {
		t.Errorf("read %d bytes of an oversized cache", len(data))
	}
	if err := WriteCacheFile(p, []byte(strings.Repeat("x", 16))); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadCacheFile(p); err != nil || len(data) != 16 {
		t.Errorf("read %d bytes, %v; want 16", len(data), err)
	}
}

// A symlink planted at a cache file's path is never followed: not read,
// not chmod'ed, not written through; it is removed and the cache saved
// afresh as a regular file. The same for any other non-regular file.
func TestCacheSymlinkNotFollowed(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	victim := filepath.Join(t.TempDir(), "victim.json")
	old, _ := json.Marshal(projectCache{Fetched: time.Now(), Projects: []Project{{Path: "acme/a", Name: "a",
		HTTPURL: "https://oauth2:glpat-xxx@h/acme/a.git"}}}) // valid, and "old": a load would rewrite it
	if err := os.WriteFile(victim, old, 0o644); err != nil {
		t.Fatal(err)
	}
	p := cachePath("projects", "work", "h")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, p); err != nil {
		t.Fatal(err)
	}
	if ps, at := LoadCache("work", "h", GitLab{}); ps != nil || !at.IsZero() {
		t.Errorf("loaded through a symlink: %+v", ps)
	}
	if fi, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("link kept: %v %v", fi, err)
	}
	if now, err := os.ReadFile(victim); err != nil || string(now) != string(old) {
		t.Errorf("target changed (%v): %s", err, now)
	}
	if fi, err := os.Stat(victim); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("target mode %v (%v): chmod followed the link", fi.Mode().Perm(), err)
	}

	// a link planted before the save: replaced, the target untouched
	if err := os.Symlink(victim, p); err != nil {
		t.Fatal(err)
	}
	ps := []Project{{Path: "acme/b", Name: "b", SSHURL: "git@h:acme/b.git"}}
	if err := SaveCache("work", "h", ps); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("saved cache: %v %v", fi, err)
	}
	if now, err := os.ReadFile(victim); err != nil || string(now) != string(old) {
		t.Errorf("target written through (%v): %s", err, now)
	}
	if got, _ := LoadCache("work", "h", GitLab{}); len(got) != 1 || got[0].Path != "acme/b" {
		t.Errorf("loaded %+v", got)
	}

	// another non-regular file at the path: no cache either
	os.Remove(p)
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, at := LoadCache("work", "h", GitLab{}); got != nil || !at.IsZero() {
		t.Errorf("loaded a directory: %+v", got)
	}
}

// A symlink at the cache directory's path is refused, on a read as on a
// write: nothing is read, chmod'ed, removed or written where it points.
func TestCacheDirSymlinkRefused(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "cache")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil { // a write would tighten it
		t.Fatal(err)
	}
	t.Setenv("GL_REPOS_CACHE_DIR", link)
	// the target holds a valid "old" cache (0644, with a credential: a
	// load that followed the link would tighten and rewrite it)
	victim := filepath.Join(target, filepath.Base(cachePath("projects", "work", "h")))
	old, _ := json.Marshal(projectCache{Fetched: time.Now(), Projects: []Project{{Path: "acme/a", Name: "a",
		HTTPURL: "https://oauth2:glpat-xxx@h/acme/a.git"}}})
	if err := os.WriteFile(victim, old, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, at := LoadCache("work", "h", GitLab{}); got != nil || !at.IsZero() {
		t.Errorf("loaded through a symlinked directory: %+v", got)
	}
	ps := []Project{{Path: "acme/b", Name: "b", SSHURL: "git@h:acme/b.git"}}
	if err := SaveCache("work", "h", ps); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("saved through a symlinked directory: %v", err)
	}
	if now, err := os.ReadFile(victim); err != nil || string(now) != string(old) {
		t.Errorf("target changed or removed (%v): %s", err, now)
	}
	if fi, err := os.Stat(victim); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("target mode %v (%v): chmod followed the link", fi.Mode().Perm(), err)
	}
	if left, _ := os.ReadDir(target); len(left) != 1 {
		t.Errorf("written where the link points: %v", left)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v %v", fi, err)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("target directory mode %v (%v): chmod followed the link", fi.Mode().Perm(), err)
	}
}

// A cache with a URL that is no CloneURL is rewritten without it (and a
// project left without URLs is dropped).
func TestCacheKeepsOnlyCloneURLs(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	p := cachePath("projects", "work", "h")
	old, _ := json.Marshal(projectCache{Fetched: time.Now(), Projects: []Project{
		{Path: "acme/a", Name: "a", HTTPURL: "https://h/acme/a.git;private_token=SECRET", SSHURL: "git@h:acme/a.git"},
		{Path: "acme/b", Name: "b", HTTPURL: "https://h/acme/b%2F.git"}}})
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, old, 0o600); err != nil {
		t.Fatal(err)
	}
	ps, _ := LoadCache("work", "h", GitLab{})
	if len(ps) != 1 || ps[0].HTTPURL != "" || ps[0].SSHURL != "git@h:acme/a.git" {
		t.Errorf("loaded %+v", ps)
	}
	if data, _ := os.ReadFile(p); strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "acme/b") {
		t.Errorf("not rewritten: %s", data)
	}
}

// A search error never quotes the query.
func TestSearchErrorDoesNotQuote(t *testing.T) {
	api := func(context.Context, string, string, ...string) ([]byte, error) { return []byte(`[]`), nil }
	for _, f := range []Forge{GitLab{Host: "h", API: api}, GitHub{API: api}} {
		if _, err := f.Search(context.Background(), "tools?private_token=SECRET"); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: %v", f.Name(), err)
		}
	}
}

// A clone URL a server sends loses its credentials and is kept only in a
// form gl-repos can rebuild (ParseCloneURL), as git would get it: a
// query, a bare name, a scheme-less credential drop the URL.
func TestServerURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://oauth2:SECRET@h/acme/a.git":    "https://h/acme/a.git",
		"ssh://git:SECRET@h/acme/a.git":         "ssh://git@h/acme/a.git",
		"git@h:acme/a.git":                      "git@h:acme/a.git",
		"git@h:/srv/git/a.git":                  "git@h:/srv/git/a.git",
		"/srv/git/a.git":                        "/srv/git/a.git",
		"git@h:acme/a.git?private_token=SECRET": "",
		"oauth2:SECRET@h:acme/a.git":            "",
		"oauth2:SECRET@h/acme/a.git":            "",
		"tools.git":                             "",
		"acme/a.git":                            "",
		"git@h:~user/a.git":                     "",
		"":                                      "",
	} {
		if got := serverURL(in); got != want {
			t.Errorf("serverURL(%q) = %q, want %q", in, got, want)
		}
	}
}
