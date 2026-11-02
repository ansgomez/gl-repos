package gitx

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gl-repos/internal/safe"
)

// A repository's own config (.git/config, files it includes, a
// worktree's config.worktree, and the configs of its submodules below
// .git/modules) can
// name programs that git runs: clean/smudge filters, gpg, ssh, pagers,
// credential helpers, remote transport helpers, hooks. It can also
// redirect where a fetch or push connects (an http proxy, TLS switched
// off or pinned to a planted certificate, extra request headers, a
// pushurl or an insteadOf rewrite to another host): git's credential
// helper, keyed by the real host, would hand the token to whoever
// answers. And it can change what a bare push or fetch sends, writes
// or deletes. Anyone who hands you a .git directory (an unpacked
// archive, a copied disk) controls them. gl-repos reads status without
// running any of them, flags such repositories, and asks before it runs
// a command there that would.
//
// Which keys are fine is an allowlist, not a list of dangerous ones: a
// key in the repository's own scope is fine only when it is one a
// normal clone or user writes there and cannot run a program, redirect
// a connection or change what push and fetch send, write or delete
// (allowedKey, plus the few keys riskOf allows by value); every other
// key is flagged. The user's own global and system config is
// never read (readRepoConfig skips those scopes).

// allowedKey matches the keys (as git prints them: section and key
// lowercased, subsection as written) that are fine with any value.
// What writes them: git init and clone (core.*, remote.<name>.*,
// branch.<name>.*, also --depth, --filter, --sparse, -o,
// --recurse-submodules), git worktree add, submodule add,
// maintenance register, sparse-checkout, lfs install --local
// (lfs.repositoryformatversion; its filter.lfs.* keys name programs
// and are flagged), gh repo clone of a fork and gh repo set-default
// (remote.<name>.gh-resolved), and the settings a user keeps per
// repository (user.email, commit.gpgSign, commit.template, pull.rebase,
// branch.<name>.rebase).
// The log.* keys are fine because log.showSignature is forced off for
// every git call (command). include.path is fine because the listing
// follows it: the included keys are checked like the rest.
var allowedKey = regexp.MustCompile(`^(?:` +
	`core\.(?:repositoryformatversion|filemode|bare|logallrefupdates|ignorecase|precomposeunicode|symlinks` +
	`|autocrlf|eol|safecrlf|quotepath|sparsecheckout|sparsecheckoutcone|longpaths|checkstat|trustctime|untrackedcache|worktree)` +
	`|extensions\.[^.]+|include\.path` +
	`|remote\..+\.(?:tagopt|prune|promisor|partialclonefilter|gh-resolved)` +
	`|branch\..+\.(?:remote|merge|rebase|description)` +
	`|submodule\.active|submodule\..+\.(?:active|branch)` +
	`|user\.(?:name|email|signingkey)|commit\.(?:gpgsign|template)|tag\.gpgsign` +
	`|pull\.(?:rebase|ff)|fetch\.prune|init\.defaultbranch|rebase\.autostash|merge\.ff` +
	`|color\..+|diff\.renames|status\..+|log\..+|advice\..+|rerere\.enabled` +
	`|gc\..+|maintenance\..+|index\..+|feature\..+|lfs\.repositoryformatversion` +
	`)$`)

// The value-checked keys are the ones a clone or user writes that are
// fine only with some values (riskOf): a remote or submodule URL that names
// no transport helper (helperURL) and, for a remote's URL or pushurl, no
// local path inside the repository's own folder or git directory (insideRepo),
// a fetch refspec that writes only
// below refs/remotes/<name>/ (safeFetchRefspec), core.fsmonitor and
// pager.<cmd> as a boolean (git runs a program otherwise), and a
// submodule.<name>.update mode (checkout, rebase, merge, none; a
// "!command" runs it).
var (
	urlKeys       = regexp.MustCompile(`^(?:remote|submodule)\..+\.url$`)
	remoteURLKeys = regexp.MustCompile(`^remote\..+\.(?:url|pushurl)$`)
	fetchRefspec  = regexp.MustCompile(`^remote\..+\.fetch$`)
	boolKeys      = regexp.MustCompile(`^(?:core\.fsmonitor|pager\..+)$`)
	cmdKeys       = regexp.MustCompile(`^submodule\..+\.update$`)
	pushURLKeys   = regexp.MustCompile(`^remote\..+\.pushurl$`)
	insteadOfKeys = regexp.MustCompile(`^url\..+\.(?:insteadof|pushinsteadof)$`)
	urlScheme     = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*)(::|://)`)
)

// Every other key is flagged, listed by what it changes so the user
// knows what to look at: a key whose value is a program or a hook
// directory (programKey) by its name, a network setting as
// "network: <key>", a setting of what a bare push or fetch does as
// "push: <key>" or "fetch: <key>", and any other key as "config: <key>".

// programKey matches the keys whose value is a program or a hook
// directory; an empty value (credential.helper="" resets the list)
// names none. A remote URL, pushurl, submodule URL or insteadOf base
// that names a transport helper (git-remote-<transport> from PATH)
// counts as one too, by its key.
var programKey = regexp.MustCompile(`^(?:` +
	`filter\..+\.(?:clean|smudge|process)` +
	`|gpg\.program|gpg\.[^.]+\.program|gpg\.ssh\.defaultkeycommand` +
	`|core\.(?:sshcommand|pager|askpass|gitproxy|hookspath|fsmonitor|editor|alternaterefscommand)` +
	`|sequence\.editor|diff\.external|interactive\.difffilter` +
	`|credential\.helper|credential\..+\.helper` +
	`|remote\..+\.(?:uploadpack|receivepack|vcs)` +
	`|diff\..+\.(?:command|textconv)|merge\..+\.driver` +
	`|pager\..+|submodule\..+\.update|gc\.recentobjectshook` +
	`)$`)

// httpNetKeys are the http.<key> (and http.<url>.<key>) settings that
// decide where a fetch connects and whether the server is checked.
const httpNetKeys = `proxy|sslverify|sslcainfo|sslcapath|sslcert|sslkey|sslbackend|pinnedpubkey|cookiefile|savecookies|extraheader|curloptresolve`

// netKeys redirect or weaken the connection of fetch, pull and push
// (the user's credential helper then answers for the real host); an
// empty string (no proxy) or a boolean at its harmless default
// (safeNetValue) changes nothing. url.<base>.insteadOf and
// pushInsteadOf (insteadOfKeys) rewrite remote URLs to <base> and are
// flagged with any value: an empty prefix rewrites every URL.
var netKeys = regexp.MustCompile(`^(?:remote\..+\.(?:proxy|proxyauthmethod)|http\.(?:.+\.)?(?:` + httpNetKeys + `))$`)

// netDefault holds, for the boolean network keys, the value that changes
// nothing (http.sslVerify=true, http.saveCookies=false): not flagged.
var netDefault = map[string]bool{"sslverify": true, "savecookies": false}

// safeNetValue reports whether a netKeys key holds its harmless value:
// for a boolean key its default (netDefault; an empty value is false, as
// git reads it: "sslVerify =" switches verification off), for a string
// key the empty string (no proxy, no cookie file).
func safeNetValue(key, val string) bool {
	def, ok := netDefault[key[strings.LastIndex(key, ".")+1:]]
	if !ok {
		return val == ""
	}
	switch strings.ToLower(val) {
	case "true", "yes", "on", "1":
		return def
	case "", "false", "no", "off", "0":
		return !def
	}
	return false
}

// pushKeys and fetchKeys change which refs a bare "git push" sends or
// where, or what "git fetch --all --prune" writes or deletes, while the
// confirmation shows only the command: remote.<name>.mirror=true makes
// a push delete every remote ref the repository lacks, a
// remote.<name>.push refspec or push.default pushes other branches
// (forced with "+"), push.followTags publishes tags, remote.pushDefault,
// branch.<name>.pushRemote and remote.<name>.pushurl send the push to
// another remote or host than the one status shows, a
// remote.<name>.fetch refspec outside refs/remotes/<name>/ (a mirror's
// "+refs/*:refs/*") overwrites local branches and tags and, with the
// --prune of the fetch gl-repos runs, deletes the ones the remote lacks,
// pruneTags=true deletes local tags, and fetch.bundleURI downloads from
// a source the user never saw. Any other push.* or fetch.* key counts
// as well. The boolean ones among them (offKeys) change nothing when
// false, an empty value ("mirror =") included; a key without a value
// ("mirror" alone) is true (add).
var (
	pushKeys  = regexp.MustCompile(`^(?:remote\..+\.(?:mirror|push|pushurl)|remote\.pushdefault|branch\..+\.pushremote|push\..+)$`)
	fetchKeys = regexp.MustCompile(`^(?:remote\..+\.(?:fetch|prunetags)|fetch\..+)$`)
	offKeys   = regexp.MustCompile(`^(?:remote\..+\.(?:mirror|prunetags)|push\.followtags|fetch\.prunetags)$`)
)

// isOff reports whether v is a false boolean, as git reads it: an
// empty value is false too.
func isOff(v string) bool {
	switch strings.ToLower(v) {
	case "", "false", "no", "off", "0":
		return true
	}
	return false
}

// safeFetchRefspec reports whether the remote.<name>.fetch refspec val
// writes only remote-tracking refs of that remote: "[+]<src>:<dst>" with
// dst below refs/remotes/<name>/ and the same number of "*" (at most
// one) on both sides. That is what a clone writes
// ("+refs/heads/*:refs/remotes/<name>/*", or one branch for
// --single-branch), and what a forge's merge-request or pull-request
// refspec adds ("+refs/merge-requests/*/head:refs/remotes/<name>/mr/*").
// Anything else (a negative refspec, a mirror's "+refs/*:refs/*",
// another remote's namespace, tags, an empty value) is not.
func safeFetchRefspec(key, val string) bool {
	name := key[len("remote."):strings.LastIndex(key, ".")]
	src, dst, ok := strings.Cut(strings.TrimPrefix(val, "+"), ":")
	rest, under := strings.CutPrefix(dst, "refs/remotes/"+name+"/")
	return ok && src != "" && under && rest != "" &&
		strings.Count(src, "*") == strings.Count(dst, "*") && strings.Count(dst, "*") <= 1
}

// helperURL reports whether git would run a program named by remote URL
// u: "git-remote-<transport>" from PATH for "<transport>::<address>" or
// for a scheme git does not handle itself (gitremote-helpers(7)). The
// ext:: helper is switched off for every git call (command), but counts
// too.
func helperURL(u string) bool {
	m := urlScheme.FindStringSubmatch(strings.TrimSpace(u))
	if m == nil {
		return false // a local path or git's scp-like form
	}
	if m[2] == "::" {
		return true
	}
	switch m[1] {
	case "file", "git", "ssh", "git+ssh", "ssh+git", "http", "https", "ftp", "ftps":
		return false
	}
	return true
}

// isBool reports whether v is a boolean (an empty value is false).
func isBool(v string) bool {
	switch strings.ToLower(v) {
	case "", "true", "false", "yes", "no", "on", "off", "1", "0":
		return true
	}
	return false
}

// riskOf classifies one key of the repository's own config: the risk
// to list for it, or "" when the key is fine (allowedKey, or a
// value-checked key with a fine value).
func riskOf(key, val string) string {
	val = strings.TrimSpace(val)
	switch {
	case allowedKey.MatchString(key) && !programKey.MatchString(key): // gc.* holds a hook
		return ""
	case urlKeys.MatchString(key):
		if helperURL(val) {
			return key
		}
		return ""
	case fetchRefspec.MatchString(key):
		if safeFetchRefspec(key, val) {
			return ""
		}
		return "fetch: " + key
	case boolKeys.MatchString(key) && isBool(val),
		cmdKeys.MatchString(key) && !strings.HasPrefix(val, "!"):
		return ""
	case pushURLKeys.MatchString(key) && helperURL(val),
		insteadOfKeys.MatchString(key) && helperURL(key[len("url."):strings.LastIndex(key, ".")]):
		return key
	case programKey.MatchString(key):
		if val == "" {
			return ""
		}
		return key
	case netKeys.MatchString(key):
		if safeNetValue(key, val) {
			return ""
		}
		return "network: " + key
	case insteadOfKeys.MatchString(key):
		return "network: " + key
	case offKeys.MatchString(key) && isOff(val):
		return ""
	case pushKeys.MatchString(key):
		return "push: " + key
	case fetchKeys.MatchString(key):
		return "fetch: " + key
	}
	return "config: " + key
}

// repoConfig is what gl-repos needs to know about a repository's own
// config: the keys that are not fine (riskOf), and the filter drivers
// it defines.
type repoConfig struct {
	risky   []string // sanitized risks, e.g. "filter.x.clean", "push: remote.origin.pushurl", "submodule lib: core.pager"
	filters []string // names of filter drivers defined by the repository

	// the layout git reports (layout): the top of the work tree, and the
	// git directory and common directory; empty when not known
	top, gitDir, common string
}

// readRepoConfig lists the repository's config without running anything:
// reading config never starts a program.
func readRepoConfig(ctx context.Context, dir string) (repoConfig, error) {
	var rc repoConfig
	out, err := output(ctx, dir, "config", "--show-scope", "--null", "--list")
	if err != nil {
		return rc, err
	}
	rc.top, rc.gitDir, rc.common = layout(ctx, dir)
	f := strings.Split(out, "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		switch f[i] {
		case "system", "global", "command":
			continue // the user's own settings
		}
		rc.add("", f[i+1])
	}
	rc.addLayout(ctx, dir)
	return rc, nil
}

// layout asks git for the absolute top of dir's work tree, its git
// directory and its common directory (for a linked worktree, the main
// repository's); all empty when git cannot say.
func layout(ctx context.Context, dir string) (top, gitDir, common string) {
	out, err := output(ctx, dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	if p := strings.Split(strings.TrimSpace(out), "\n"); err == nil && len(p) == 3 {
		return p[0], p[1], p[2]
	}
	return "", "", ""
}

// add records one "key\nvalue" entry (git config --null --list: a key
// without "=" comes alone, one with an empty value with the newline);
// prefix names a submodule. A key without a value is true to git
// ("mirror" alone), an empty one is false ("sslVerify ="): the first is
// recorded as "true" so riskOf tells them apart.
func (rc *repoConfig) add(prefix, entry string) {
	key, val, hasVal := strings.Cut(entry, "\n")
	if !hasVal {
		val = "true"
	}
	if strings.HasPrefix(key, "filter.") && strings.Count(key, ".") >= 2 && prefix == "" {
		name := key[len("filter."):strings.LastIndex(key, ".")]
		if !slices.Contains(rc.filters, name) {
			rc.filters = append(rc.filters, name)
		}
	}
	risk := riskOf(key, val)
	// A remote whose URL is a local path inside the repository's own git
	// directory (a planted bare repository at .git/x) passes riskOf like
	// any local path, but a confirmed push would run that repository's
	// hooks and config. Only the repository's own remotes: a submodule's
	// relative URL resolves elsewhere, and gl-repos never contacts a
	// submodule's remote (fetch, pull and push do not recurse).
	if risk == "" && prefix == "" && hasVal && remoteURLKeys.MatchString(key) && rc.insideRepo(val) {
		risk = "push: " + key
	}
	if risk == "" {
		return
	}
	k := safe.Line(prefix + risk)
	if !slices.Contains(rc.risky, k) {
		rc.risky = append(rc.risky, k)
	}
}

// localPath returns the path on this machine that remote URL u names,
// or "" when u is no local path: a plain path (git's rule, as for a
// config value: no colon before the first slash, else it is the
// scp-like host:path) or file://<path>.
func localPath(u string) string {
	u = strings.TrimSpace(u)
	if p, ok := strings.CutPrefix(u, "file://"); ok {
		if i := strings.Index(p, "/"); i > 0 { // file://host/path: the host is ignored
			p = p[i:]
		}
		return p
	}
	colon, slash := strings.Index(u, ":"), strings.Index(u, "/")
	if colon >= 0 && (slash < 0 || colon < slash) {
		return "" // scp-like, or a URL
	}
	return u
}

// insideRepo reports whether remote URL u is a local path that resolves
// (relative to the work tree, where git runs; through symlinks) into
// the repository's own work tree, git directory or common directory:
// whoever handed over the folder also controls a bare repository there,
// and its hooks.
func (rc repoConfig) insideRepo(u string) bool {
	p := localPath(u)
	if p == "" || rc.top == "" {
		return false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(rc.top, p)
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	for _, g := range []string{rc.top, rc.gitDir, rc.common} {
		if r, err := filepath.EvalSymlinks(g); err == nil {
			g = r
		}
		if rel, err := filepath.Rel(g, p); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return true
		}
	}
	return false
}

// maxSubmoduleConfigs bounds the submodule configs read for one status
// (each is a git process). A repository with more is flagged as risky
// instead, since the rest could run programs: unchecked counts as risky.
const maxSubmoduleConfigs = 100

// uncheckedSubmodules is the risky key of such a repository.
var uncheckedSubmodules = fmt.Sprintf("submodules: more than %d, not all checked", maxSubmoduleConfigs)

// uncheckedConfig is the risky key of a repository whose config cannot
// be read (prefixed with the submodule for a submodule's): it is not
// known to run nothing.
const uncheckedConfig = "config not readable, not checked"

// foreignWorkTree is the risky key of a repository whose work tree is
// not the listed folder itself: core.worktree (in its own config, or
// in the one a planted .git file points to) can name any folder, and
// pull would then write files there while the folder looks clean. A
// linked worktree or a submodule keeps its git directory elsewhere, but
// its work tree is the folder: not flagged.
const foreignWorkTree = "work tree: not this folder"

// legacyRemotes are the git directory's folders of legacy remote files.
var legacyRemotes = []string{"remotes", "branches"}

// addLayout checks that the work tree is dir and adds the configs of
// submodules (below the common git directory's "modules"): the fetch,
// pull and push gl-repos runs never recurse into them (command), but
// the tool, an agent or a shell opened there would run them.
func (rc *repoConfig) addLayout(ctx context.Context, dir string) {
	top, common := rc.top, rc.common
	if top == "" {
		rc.risky = append(rc.risky, "work tree: not checked", "submodules: not checked")
		return
	}
	if !sameDir(dir, top) {
		rc.risky = append(rc.risky, foreignWorkTree)
	}
	// git still takes a remote's URL and refspecs from the legacy files
	// remotes/<name> and branches/<name> when the config names no URL
	// for it; they are not checked, so any is a mark.
	for _, d := range legacyRemotes {
		ents, err := os.ReadDir(filepath.Join(common, d))
		if len(ents) > 0 || err != nil && !errors.Is(err, fs.ErrNotExist) {
			rc.risky = append(rc.risky, "push: .git/"+d)
		}
	}
	mods := filepath.Join(common, "modules")
	if fi, err := os.Stat(mods); err != nil || !fi.IsDir() {
		return
	}
	seen := 0
	_ = filepath.WalkDir(mods, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) { // gone meanwhile: nothing to check
				// an unreadable directory: its submodules' configs are not known to run nothing
				rel, _ := filepath.Rel(mods, p)
				rc.risky = append(rc.risky, safe.Line("submodule "+filepath.ToSlash(rel)+": "+uncheckedConfig))
			}
			return fs.SkipDir
		}
		if d.IsDir() {
			switch d.Name() {
			case "objects", "refs", "logs", "hooks", "info", "lfs":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "config" {
			return nil
		}
		if seen >= maxSubmoduleConfigs {
			rc.risky = append(rc.risky, uncheckedSubmodules)
			return filepath.SkipAll
		}
		seen++
		rel, _ := filepath.Rel(mods, filepath.Dir(p))
		prefix := "submodule " + filepath.ToSlash(rel) + ": "
		out, err := output(ctx, dir, "config", "--file", p, "--includes", "--null", "--list")
		for _, e := range strings.Split(out, "\x00") {
			if e != "" {
				rc.add(prefix, e)
			}
		}
		if err != nil { // the walk goes on: the rest may be readable
			rc.risky = append(rc.risky, safe.Line(prefix+uncheckedConfig))
		}
		return nil
	})
}

// sameDir reports whether a and b name the same directory (through
// symlinks and case-insensitive names too; git prints resolved paths).
func sameDir(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// noFilters returns environment entries that switch off the given filter
// drivers (as git -c would, but safe for any name), appended after any
// GIT_CONFIG_COUNT entries already in env.
func noFilters(env []string, filters []string) []string {
	if len(filters) == 0 {
		return env
	}
	n := 0
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT="); ok {
			n, _ = strconv.Atoi(v)
		}
	}
	out := make([]string, 0, len(env)+1+len(filters)*8)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_CONFIG_COUNT=") {
			out = append(out, kv)
		}
	}
	for _, name := range filters {
		for _, kv := range [][2]string{{"clean", ""}, {"smudge", ""}, {"process", ""}, {"required", "false"}} {
			out = append(out,
				"GIT_CONFIG_KEY_"+strconv.Itoa(n)+"=filter."+name+"."+kv[0],
				"GIT_CONFIG_VALUE_"+strconv.Itoa(n)+"="+kv[1])
			n++
		}
	}
	return append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(n))
}

// readOnly runs a git command that only reads, with the repository's
// own filter drivers switched off, and returns stdout.
func readOnly(ctx context.Context, dir string, filters []string, args ...string) (string, error) {
	return readOnlyLimited(ctx, dir, filters, 0, args...)
}

// readOnlyLimited is readOnly reading at most limit bytes (0: all).
func readOnlyLimited(ctx context.Context, dir string, filters []string, limit int, args ...string) (string, error) {
	return outputLimited(ctx, dir, func(env []string) []string { return noFilters(env, filters) }, limit, args...)
}

// ConfigRisks lists the keys of dir's own config (and its submodules')
// that are not on the allowlist (riskOf), sanitized for display; empty
// when there are none. A config that cannot be read counts as one (uncheckedConfig).
func ConfigRisks(ctx context.Context, dir string) []string {
	rc, err := readRepoConfig(ctx, dir)
	if err != nil {
		return append(rc.risky, uncheckedConfig)
	}
	return rc.risky
}
