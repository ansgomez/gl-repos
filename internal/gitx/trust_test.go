package gitx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.com",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.com", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// evilProgram writes a script that records each run in a marker file and
// passes its input through.
func evilProgram(t *testing.T) (prog, marker string) {
	dir := t.TempDir()
	marker = filepath.Join(dir, "ran")
	prog = filepath.Join(dir, "evil.sh")
	script := "#!/bin/sh\necho ran >> '" + marker + "'\ncat\n"
	if err := os.WriteFile(prog, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return prog, marker
}

// writeValueless adds key without a value ("mirror" alone, true to
// git) to dir's config; git config cannot write a key without "=".
func writeValueless(t *testing.T, dir, key string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	sect, name, _ := strings.Cut(key, ".")
	if sub, k, ok := strings.Cut(name, "."); ok {
		sect, name = fmt.Sprintf("%s %q", sect, sub), k
	}
	fmt.Fprintf(f, "[%s]\n\t%s\n", sect, name)
	f.Close()
}

func ran(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

func newRepo(t *testing.T) string {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-qm", "init")
	return dir
}

// makeStatDirty changes f.txt's mtime so git has to hash it again (and
// would run its clean filter) to know whether it changed.
func makeStatDirty(t *testing.T, path string) {
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// readAll runs every read-only path gl-repos takes on a listed repository.
func readAll(t *testing.T, dir string) Status {
	ctx := context.Background()
	st := ReadStatus(ctx, dir)
	if st.Err != nil {
		t.Fatalf("ReadStatus: %v", st.Err)
	}
	_ = Describe(ctx, st, 10000)
	if _, err := Graph(ctx, dir, 10); err != nil {
		t.Fatalf("Graph: %v", err)
	}
	return st
}

func TestListingRunsNoFilterFromRepoConfig(t *testing.T) {
	for _, name := range []string{"x", "a.b=c"} {
		t.Run(name, func(t *testing.T) {
			prog, marker := evilProgram(t)
			dir := newRepo(t)
			// the filter comes from a file the repo config includes, the
			// attribute from .git/info/attributes: nothing in the work tree
			inc := filepath.Join(t.TempDir(), "inc")
			runGit(t, dir, "config", "--file", inc, "filter."+name+".clean", prog)
			runGit(t, dir, "config", "--file", inc, "filter."+name+".required", "true")
			runGit(t, dir, "config", "include.path", inc)
			if err := os.WriteFile(filepath.Join(dir, ".git", "info", "attributes"), []byte("* filter="+name+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			makeStatDirty(t, filepath.Join(dir, "f.txt"))
			runGit(t, dir, "status", "--porcelain")
			if !ran(marker) {
				t.Fatal("setup: plain git status should run the filter")
			}
			os.Remove(marker)
			makeStatDirty(t, filepath.Join(dir, "f.txt"))

			st := readAll(t, dir)
			if ran(marker) {
				t.Error("listing the repository ran its clean filter")
			}
			if !slices.Contains(st.Risky, "filter."+name+".clean") {
				t.Errorf("Risky = %q, want filter.%s.clean", st.Risky, name)
			}
		})
	}
}

func TestListingRunsNoGPGFromRepoConfig(t *testing.T) {
	prog, marker := evilProgram(t)
	dir := newRepo(t)
	// a commit carrying a (fake) signature
	raw := runGit(t, dir, "cat-file", "commit", "HEAD")
	head, body, _ := strings.Cut(raw, "\n\n")
	signed := head + "\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n AAAA\n -----END PGP SIGNATURE-----\n\n" + body
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir, cmd.Stdin = dir, strings.NewReader(signed)
	id, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "update-ref", "HEAD", strings.TrimSpace(string(id)))
	runGit(t, dir, "config", "log.showSignature", "true")
	runGit(t, dir, "config", "gpg.program", prog)

	runGit(t, dir, "log", "-1")
	if !ran(marker) {
		t.Fatal("setup: plain git log should run gpg.program")
	}
	os.Remove(marker)

	st := readAll(t, dir)
	if ran(marker) {
		t.Error("listing the repository ran its gpg.program")
	}
	if !slices.Contains(st.Risky, "gpg.program") {
		t.Errorf("Risky = %q, want gpg.program", st.Risky)
	}
}

func TestListingRunsNothingFromSubmoduleConfig(t *testing.T) {
	prog, marker := evilProgram(t)
	sub := newRepo(t)
	dir := newRepo(t)
	runGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "lib")
	runGit(t, dir, "commit", "-qm", "add lib")
	runGit(t, filepath.Join(dir, "lib"), "config", "filter.z.clean", prog)
	info := filepath.Join(dir, ".git", "modules", "lib", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "attributes"), []byte("* filter=z\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	makeStatDirty(t, filepath.Join(dir, "lib", "f.txt"))
	runGit(t, dir, "status", "--porcelain")
	if !ran(marker) {
		t.Fatal("setup: plain git status should run the submodule's filter")
	}
	os.Remove(marker)
	makeStatDirty(t, filepath.Join(dir, "lib", "f.txt"))

	st := readAll(t, dir)
	if ran(marker) {
		t.Error("listing the repository ran its submodule's filter")
	}
	if !slices.Contains(st.Risky, "submodule lib: filter.z.clean") {
		t.Errorf("Risky = %q, want the submodule's filter", st.Risky)
	}
}

// A repository's config can allow the ext transport, whose remote URL
// "ext::<command>" runs the command on fetch, pull and push; gl-repos
// switches it off for every git call.
func TestActionsRunNoExtTransportFromRepoConfig(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	prog := filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\necho ran >> '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := newRepo(t)
	runGit(t, dir, "config", "protocol.ext.allow", "always")
	runGit(t, dir, "remote", "add", "origin", "ext::"+prog)

	cmd := exec.Command("git", "fetch", "origin")
	cmd.Dir = dir
	_ = cmd.Run() // fails: the program is no git server
	if !ran(marker) {
		t.Fatal("setup: plain git fetch should run the ext command")
	}
	os.Remove(marker)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := ReadStatus(ctx, dir)
	if st.Err != nil {
		t.Fatalf("ReadStatus: %v", st.Err)
	}
	if _, err := Run(ctx, st, Fetch); err == nil {
		t.Error("fetch over ext:: succeeded")
	}
	if ran(marker) {
		t.Error("fetch ran the ext command")
	}
}

// A config that cannot be read is not known to run nothing: the
// repository is flagged, as is one whose submodule's config cannot be.
func TestUnreadableConfigFlagged(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	sub := filepath.Join(dir, ".git", "modules", "lib")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	broken := []byte("[core\n") // no closing bracket: git refuses to read it
	if err := os.WriteFile(filepath.Join(sub, "config"), broken, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ConfigRisks(ctx, dir); !slices.Contains(got, "submodule lib: "+uncheckedConfig) {
		t.Errorf("broken submodule config: risks %q", got)
	}
	// a submodule directory that cannot be entered hides its config as well
	if os.Geteuid() != 0 { // root reads it regardless
		if err := os.Chmod(sub, 0); err != nil {
			t.Fatal(err)
		}
		got := ConfigRisks(ctx, dir)
		if err := os.Chmod(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(got, "submodule lib: "+uncheckedConfig) {
			t.Errorf("unreadable submodule directory: risks %q", got)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), broken, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ConfigRisks(ctx, dir); !slices.Contains(got, uncheckedConfig) {
		t.Errorf("broken config: risks %q", got)
	}
	// the status flags it too, so the list shows the red mark and -list warns
	if st := ReadStatus(ctx, dir); st.Err == nil || !slices.Contains(st.Risky, uncheckedConfig) {
		t.Errorf("broken config: status err %v, risks %q", st.Err, st.Risky)
	}
}

// Keys that name a program are listed by name, a remote or submodule
// URL only when it names a transport helper, and a key that is on no
// list (neither allowed nor a known program, network, push or fetch
// setting) as "config: <key>". The user's global config never counts.
func TestConfigRisks(t *testing.T) {
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[core]\n\tpager = less\n[credential]\n\thelper = osxkeychain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global) // the user's own settings never count

	dir := newRepo(t)
	if got := ConfigRisks(context.Background(), dir); len(got) != 0 {
		t.Fatalf("plain repository: risks %q", got)
	}
	for _, kv := range [][2]string{
		{"core.fsmonitor", "false"}, // booleans run nothing
		{"pager.log", "false"},
		{"credential.helper", ""}, // an empty helper resets the list
		{"core.sshCommand", "ssh -i key"},
		{"core.hooksPath", "hooks"},
		{"remote.origin.uploadpack", "x"},
		{"credential.https://gitlab.example.com.helper", "x"},
		{"diff.pdf.textconv", "x"},
		{"gpg.ssh.program", "x"},
		{"user.name", "a"},
		{"submodule.lib.update", "rebase"},                    // a mode runs nothing
		{"submodule.evil.update", "!rm -rf"},                  // a command does
		{"remote.origin.url", "ssh://git@[::1]:2222/a/b.git"}, // git's own transports run nothing
		{"remote.origin.pushurl", "git@h:a/b.git"},            // but a pushurl sends the push elsewhere
		{"submodule.lib.url", "https://gitlab.example.com/a/lib.git"},
		{"remote.hg.url", "evil::x"}, // a transport helper, git-remote-evil, does
		{"remote.hg.pushurl", "evil://h/x"},
		{"submodule.hg.url", "evil::y"},
		{"remote.svn.vcs", "evil"},
		{"url.evil::.insteadof", "https://"},
		{"core.excludesFile", ".gitignore-local"}, // not on the allowlist: listed as "config: <key>"
		{"includeIf.onbranch:main.path", "x"},
		{"gc.auto", "0"},              // gc.* is allowed
		{"gc.recentObjectsHook", "x"}, // but its hook runs a command
	} {
		runGit(t, dir, "config", kv[0], kv[1])
	}
	want := []string{"core.sshcommand", "core.hookspath", "remote.origin.uploadpack",
		"credential.https://gitlab.example.com.helper", "diff.pdf.textconv", "gpg.ssh.program",
		"submodule.evil.update", "push: remote.origin.pushurl", "remote.hg.url", "remote.hg.pushurl",
		"submodule.hg.url", "remote.svn.vcs", "url.evil::.insteadof",
		"config: core.excludesfile", "config: includeif.onbranch:main.path", "gc.recentobjectshook"}
	got := ConfigRisks(context.Background(), dir)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("risks = %q\nwant    %q", got, want)
	}
}

// A repository's own network settings (an http proxy, TLS switched off
// or pinned to a planted certificate, extra headers, cookies, a URL
// rewrite) send a fetch, and the user's credentials for the real host,
// wherever the config says: flagged like a program, as "network: <key>".
// A boolean at its harmless default, an empty proxy and the user's own
// settings are not; any other http.* key is off the allowlist and listed
// as "config: <key>".
func TestNetworkKeysFlagged(t *testing.T) {
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[http]\n\tproxy = http://proxy.example.com:3128\n\tsslVerify = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global) // the user's own settings never count

	ctx := context.Background()
	dir := newRepo(t)
	for _, kv := range [][2]string{
		{"remote.origin.url", "https://gitlab.example.com/a/b.git"},
		{"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		{"http.sslVerify", "true"}, // the defaults change nothing
		{"http.saveCookies", "no"},
		{"http.https://gitlab.example.com/.sslVerify", "1"},
		{"http.proxy", ""}, // an empty proxy means none
		{"remote.origin.proxy", ""},
	} {
		runGit(t, dir, "config", kv[0], kv[1])
	}
	if got := ConfigRisks(ctx, dir); len(got) != 0 {
		t.Fatalf("plain repository: risks %q", got)
	}

	for _, c := range []struct{ key, val, want string }{
		// a URL rewrite sends fetch and push, with the credentials for the
		// real host, to another host while status shows the real one
		{"url.https://attacker.example/.insteadOf", "https://gitlab.example.com/", "network: url.https://attacker.example/.insteadof"},
		{"url.https://attacker.example/.pushInsteadOf", "https://gitlab.example.com/", "network: url.https://attacker.example/.pushinsteadof"},
		{"url.https://attacker.example/.insteadOf", "", "network: url.https://attacker.example/.insteadof"}, // an empty prefix rewrites every URL
		// any other http.* key is off the allowlist: listed as "config: <key>"
		{"http.postBuffer", "1048576", "config: http.postbuffer"},
		{"http.followRedirects", "true", "config: http.followredirects"},
		{"http.proxy", "http://attacker.example:8080", "network: http.proxy"},
		{"http.sslVerify", "false", "network: http.sslverify"},
		{"http.sslCAInfo", "/tmp/ca.pem", "network: http.sslcainfo"},
		{"http.sslCAPath", "/tmp/certs", "network: http.sslcapath"},
		{"http.sslCert", "/tmp/c.pem", "network: http.sslcert"},
		{"http.sslKey", "/tmp/k.pem", "network: http.sslkey"},
		{"http.sslBackend", "openssl", "network: http.sslbackend"},
		{"http.pinnedPubkey", "sha256//AAAA", "network: http.pinnedpubkey"},
		{"http.cookieFile", "/tmp/cookies", "network: http.cookiefile"},
		{"http.saveCookies", "true", "network: http.savecookies"},
		{"http.extraHeader", "Authorization: Basic x", "network: http.extraheader"},
		{"http.curloptResolve", "gitlab.example.com:443:10.0.0.1", "network: http.curloptresolve"},
		{"http.https://gitlab.example.com/.proxy", "http://attacker.example:8080", "network: http.https://gitlab.example.com/.proxy"},
		{"http.https://gitlab.example.com/.sslVerify", "off", "network: http.https://gitlab.example.com/.sslverify"},
		{"http.https://gitlab.example.com/.extraHeader", "X: y", "network: http.https://gitlab.example.com/.extraheader"},
		{"remote.origin.proxy", "http://attacker.example:8080", "network: remote.origin.proxy"},
		{"remote.origin.proxyAuthMethod", "basic", "network: remote.origin.proxyauthmethod"},
	} {
		t.Run(c.key, func(t *testing.T) {
			dir := newRepo(t)
			runGit(t, dir, "config", c.key, c.val)
			got := ConfigRisks(ctx, dir)
			if !slices.Equal(got, []string{c.want}) {
				t.Errorf("risks = %q, want %q", got, c.want)
			}
			// the status flags it too (the red mark, -list's warning), and
			// the unattended fetch (-fetch, fetch_on_start) skips such a row
			if st := ReadStatus(ctx, dir); !slices.Equal(st.Risky, []string{c.want}) || !st.NeedsAttention() {
				t.Errorf("status: risky %q, attention %v", st.Risky, st.NeedsAttention())
			}
		})
	}
}

// Every key a normal clone or user writes into a repository's own
// config is on the allowlist: git init, every clone variant (also
// --single-branch, --depth, --filter, --sparse, -o, --no-tags,
// --recurse-submodules and the submodule's own config), a linked
// worktree, a submodule added by hand, a second remote, an upstream,
// and the settings a user keeps per repository. None is flagged.
func TestNormalConfigsClean(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	ctx := context.Background()
	src := newRepo(t)
	runGit(t, src, "tag", "-a", "-m", "t", "v1")
	runGit(t, src, "-c", "protocol.file.allow=always", "submodule", "add", "-q", newRepo(t), "lib")
	runGit(t, src, "commit", "-qm", "add lib")
	clean := func(what, dir string) {
		t.Helper()
		if got := ConfigRisks(ctx, dir); len(got) != 0 {
			t.Errorf("%s: risks %q", what, got)
		}
		if st := ReadStatus(ctx, dir); st.Err != nil || len(st.Risky) != 0 {
			t.Errorf("%s: status err %v, risks %q", what, st.Err, st.Risky)
		}
	}
	clean("superproject", src)
	clean("submodule", filepath.Join(src, "lib"))
	for _, args := range [][]string{
		{"clone", "-q"},
		{"clone", "-q", "--single-branch", "-b", "main"},
		{"clone", "-q", "--depth", "1"},
		{"clone", "-q", "--filter=blob:none"},
		{"clone", "-q", "--sparse"},
		{"clone", "-q", "--no-tags"},
		{"clone", "-q", "-o", "up"},
		{"-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules"},
	} {
		dir := filepath.Join(t.TempDir(), "clone")
		runGit(t, filepath.Dir(dir), append(args, "file://"+src, dir)...)
		clean("git "+strings.Join(args, " "), dir)
		if slices.Contains(args, "--recurse-submodules") {
			clean("git "+strings.Join(args, " ")+", the submodule", filepath.Join(dir, "lib"))
		}
	}
	dir := filepath.Join(t.TempDir(), "init")
	runGit(t, filepath.Dir(dir), "init", "-q", dir)
	clean("git init", dir)

	dir = filepath.Join(t.TempDir(), "clone")
	runGit(t, filepath.Dir(dir), "clone", "-q", src, dir)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, dir, "worktree", "add", "-q", "-b", "wt", wt)
	runGit(t, dir, "remote", "add", "upstream", src)
	runGit(t, dir, "branch", "-u", "origin/main")
	runGit(t, dir, "config", "branch.main.description", "the main line")
	for _, kv := range [][2]string{
		{"user.name", "a"}, {"user.email", "a@example.com"}, {"user.signingKey", "ABCD"},
		{"commit.gpgSign", "true"}, {"tag.gpgSign", "true"},
		{"pull.rebase", "true"}, {"pull.ff", "only"}, {"branch.main.rebase", "true"},
		{"merge.ff", "false"}, {"rebase.autoStash", "true"}, {"fetch.prune", "true"},
		{"init.defaultBranch", "main"}, {"core.autocrlf", "input"}, {"core.eol", "lf"},
		{"core.ignoreCase", "false"}, {"core.untrackedCache", "true"}, {"core.fsmonitor", "false"},
		{"color.ui", "auto"}, {"color.branch.current", "yellow"}, {"diff.renames", "copies"},
		{"status.showUntrackedFiles", "all"}, {"log.date", "iso"}, {"advice.detachedHead", "false"},
		{"rerere.enabled", "true"}, {"gc.auto", "0"}, {"maintenance.auto", "false"},
		{"index.version", "4"}, {"feature.manyFiles", "true"}, {"lfs.repositoryFormatVersion", "0"},
		{"extensions.worktreeConfig", "true"}, {"commit.template", ".gitmessage"},
		{"remote.origin.gh-resolved", "base"}, // gh repo clone of a fork, gh repo set-default
	} {
		runGit(t, dir, "config", kv[0], kv[1])
	}
	runGit(t, dir, "sparse-checkout", "init", "--cone") // config.worktree
	clean("a repository with its user's settings", dir)
	clean("its linked worktree", wt)
}

// A repository's own push and fetch settings change what a bare "git
// push" or "git fetch --all --prune" sends, where, or what it writes
// and deletes, while the confirmation shows only the command:
// remote.<name>.mirror=true deletes every remote ref the repository
// lacks, remote.<name>.push and push.default push other branches,
// push.followTags publishes tags, a pushurl, remote.pushDefault or
// branch.<name>.pushRemote sends the push to another host or remote
// than the one status shows, a fetch refspec outside refs/remotes/<name>/
// overwrites and prunes local branches and tags, pruneTags deletes local
// tags, and fetch.bundleURI downloads from an unreviewed source. Flagged
// as "push: <key>" or "fetch: <key>", like every other push.* and
// fetch.* key. What a clone writes (also --single-branch, a second
// remote), a forge's merge/pull-request refspec, a false boolean and
// the prune settings that change nothing for the fetch gl-repos runs
// are not.
func TestSyncKeysFlagged(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "bare.git")
	runGit(t, newRepo(t), "clone", "-q", "--bare", ".", bare)
	for _, args := range [][]string{{"clone", "-q"}, {"clone", "-q", "--single-branch", "-b", "main"}} {
		dir := filepath.Join(t.TempDir(), "clone")
		runGit(t, filepath.Dir(dir), append(args, bare, dir)...)
		if got := ConfigRisks(ctx, dir); len(got) != 0 {
			t.Fatalf("git %s: risks %q", strings.Join(args, " "), got)
		}
	}
	dir := newRepo(t)
	for _, kv := range [][2]string{
		{"remote.origin.url", "https://gitlab.example.com/a/b.git"},
		{"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		{"remote.upstream.url", "https://gitlab.example.com/c/b.git"},
		{"remote.upstream.fetch", "+refs/heads/main:refs/remotes/upstream/main"},
		{"remote.origin.mirror", "false"},
		{"remote.origin.pruneTags", "no"},
		{"fetch.pruneTags", "0"},
		{"push.followTags", "false"},
		{"fetch.prune", "true"}, // the fetch gl-repos runs prunes anyway
		{"remote.origin.prune", "true"},
		{"remote.origin.tagOpt", "--no-tags"},
		// the refspecs a forge's docs add for merge/pull requests write
		// only below refs/remotes/origin/
		{"remote.origin.fetch", "+refs/merge-requests/*/head:refs/remotes/origin/mr/*"},
		{"remote.origin.fetch", "+refs/pull/*/head:refs/remotes/origin/pr/*"},
		{"remote.origin.fetch", "HEAD:refs/remotes/origin/HEAD"},
	} {
		runGit(t, dir, "config", "--add", kv[0], kv[1])
	}
	if got := ConfigRisks(ctx, dir); len(got) != 0 {
		t.Fatalf("plain repository: risks %q", got)
	}

	for _, c := range []struct{ key, val, want string }{
		{"remote.origin.mirror", "true", "push: remote.origin.mirror"},
		{"remote.origin.mirror", "", "push: remote.origin.mirror"}, // "mirror" alone is true
		{"remote.origin.push", "+refs/heads/*:refs/heads/*", "push: remote.origin.push"},
		{"remote.origin.push", "HEAD", "push: remote.origin.push"},
		{"remote.origin.push", "", "push: remote.origin.push"},
		{"push.default", "matching", "push: push.default"},
		{"push.default", "simple", "push: push.default"}, // every push.* key is off the allowlist
		{"push.autoSetupRemote", "true", "push: push.autosetupremote"},
		// a push goes to another host or remote than the one status shows
		{"remote.origin.pushurl", "https://attacker.example/a/b.git", "push: remote.origin.pushurl"},
		{"remote.origin.pushurl", "git@attacker.example:a/b.git", "push: remote.origin.pushurl"},
		{"remote.pushDefault", "evil", "push: remote.pushdefault"},
		{"branch.main.pushRemote", "evil", "push: branch.main.pushremote"},
		// a plain push publishes annotated tags
		{"push.followTags", "true", "push: push.followtags"},
		{"push.followTags", "", "push: push.followtags"},
		{"remote.origin.fetch", "+refs/*:refs/*", "fetch: remote.origin.fetch"},
		{"remote.origin.fetch", "+refs/heads/*:refs/heads/*", "fetch: remote.origin.fetch"},
		{"remote.origin.fetch", "+refs/heads/*:refs/remotes/upstream/*", "fetch: remote.origin.fetch"},
		{"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin", "fetch: remote.origin.fetch"},
		{"remote.origin.fetch", "+refs/heads/*:refs/remotes/originx/*", "fetch: remote.origin.fetch"},
		{"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/main", "fetch: remote.origin.fetch"}, // "*" on one side only
		{"remote.origin.fetch", "+refs/heads/main:refs/remotes/origin/*", "fetch: remote.origin.fetch"},
		{"remote.origin.fetch", "+refs/tags/*:refs/tags/*", "fetch: remote.origin.fetch"},
		{"remote.origin.fetch", "^refs/heads/secret", "fetch: remote.origin.fetch"},
		{"remote.origin.fetch", "", "fetch: remote.origin.fetch"},
		{"remote.origin.pruneTags", "true", "fetch: remote.origin.prunetags"},
		{"fetch.pruneTags", "yes", "fetch: fetch.prunetags"},
		// the fetch (the unattended one at start too) downloads from a
		// bundle source the user never saw
		{"fetch.bundleURI", "https://attacker.example/b.bundle", "fetch: fetch.bundleuri"},
		{"fetch.recurseSubmodules", "false", "fetch: fetch.recursesubmodules"}, // every fetch.* key but prune
	} {
		t.Run(c.key+"="+c.val, func(t *testing.T) {
			dir := newRepo(t)
			runGit(t, dir, "remote", "add", "origin", bare)
			if c.val == "" {
				writeValueless(t, dir, c.key)
			} else {
				runGit(t, dir, "config", "--replace-all", c.key, c.val)
			}
			got := ConfigRisks(ctx, dir)
			if !slices.Equal(got, []string{c.want}) {
				t.Errorf("risks = %q, want %q", got, c.want)
			}
			// the status flags it, so h, the plan's push step and the
			// fetch ask with the key shown, and the startup fetch skips it
			if st := ReadStatus(ctx, dir); !slices.Equal(st.Risky, []string{c.want}) || !st.NeedsAttention() {
				t.Errorf("status: risky %q, attention %v", st.Risky, st.NeedsAttention())
			}
		})
	}
}

func TestNoFiltersKeepsExistingConfigEnv(t *testing.T) {
	env := noFilters([]string{"A=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=x.y", "GIT_CONFIG_VALUE_0=z"}, []string{"f"})
	if !slices.Contains(env, "GIT_CONFIG_KEY_0=x.y") || !slices.Contains(env, "GIT_CONFIG_KEY_1=filter.f.clean") ||
		!slices.Contains(env, "GIT_CONFIG_COUNT=5") {
		t.Errorf("env = %q", env)
	}
	if n := len(slices.DeleteFunc(slices.Clone(env), func(s string) bool { return !strings.HasPrefix(s, "GIT_CONFIG_COUNT=") })); n != 1 {
		t.Errorf("%d GIT_CONFIG_COUNT entries", n)
	}
}

// Only maxSubmoduleConfigs submodule configs are read (each is a git
// process); a repository with more is flagged, since the rest could run
// programs. One fewer is read in full and flags nothing.
func TestTooManySubmodulesFlagged(t *testing.T) {
	dir := newRepo(t)
	mods := filepath.Join(dir, ".git", "modules")
	for i := range maxSubmoduleConfigs + 1 {
		d := filepath.Join(mods, "sub"+strconv.Itoa(i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "config"), []byte("[core]\n\tbare = false\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if got := ConfigRisks(ctx, dir); !slices.Contains(got, uncheckedSubmodules) {
		t.Errorf("%d submodules: risks %q, want %q", maxSubmoduleConfigs+1, got, uncheckedSubmodules)
	}
	os.RemoveAll(filepath.Join(mods, "sub0"))
	if got := ConfigRisks(ctx, dir); len(got) != 0 {
		t.Errorf("%d submodules: risks %q, want none", maxSubmoduleConfigs, got)
	}
}

// A repository whose work tree is not the listed folder is flagged:
// core.worktree in its own config, or in the config a planted .git file
// points to, would make pull write files elsewhere. A linked worktree
// and a submodule keep only their git directory elsewhere: not flagged.
func TestWorkTreeElsewhereFlagged(t *testing.T) {
	ctx := context.Background()
	main := newRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, main, "worktree", "add", "-q", "-b", "wt", wt)
	if got := ConfigRisks(ctx, wt); len(got) != 0 {
		t.Errorf("linked worktree: risks %q", got)
	}

	super := newRepo(t)
	runGit(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", newRepo(t), "lib")
	if got := ConfigRisks(ctx, filepath.Join(super, "lib")); len(got) != 0 {
		t.Errorf("submodule: risks %q", got)
	}
	if got := ConfigRisks(ctx, super); len(got) != 0 {
		t.Errorf("superproject: risks %q", got)
	}

	elsewhere := t.TempDir()
	runGit(t, main, "config", "core.worktree", elsewhere)
	if got := ConfigRisks(ctx, main); !slices.Contains(got, foreignWorkTree) {
		t.Errorf("core.worktree elsewhere: risks %q, want %q", got, foreignWorkTree)
	}
	if st := ReadStatus(ctx, main); !slices.Contains(st.Risky, foreignWorkTree) || !st.NeedsAttention() {
		t.Errorf("status: risky %q, attention %v", st.Risky, st.NeedsAttention())
	}

	planted := t.TempDir()
	if err := os.WriteFile(filepath.Join(planted, ".git"), []byte("gitdir: "+filepath.Join(main, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ConfigRisks(ctx, planted); !slices.Contains(got, foreignWorkTree) {
		t.Errorf("planted .git file: risks %q, want %q", got, foreignWorkTree)
	}
	runGit(t, main, "config", "--unset", "core.worktree")
	if got := ConfigRisks(ctx, main); len(got) != 0 {
		t.Errorf("work tree back here: risks %q", got)
	}
}

// embeddedSubmodule builds a repository whose submodule lib keeps its git
// directory in the work tree (lib/.git, as an unpacked archive would)
// and whose origin has moved lib's gitlink to a commit lib lacks: git's
// default fetch recursion (on demand) would then fetch inside lib and
// run lib's own config, here core.alternateRefsCommand.
func embeddedSubmodule(t *testing.T) (dir, marker string) {
	t.Helper()
	prog, marker := evilProgram(t)
	subsrc := newRepo(t)
	dir = newRepo(t)
	runGit(t, dir, "clone", "-q", subsrc, "lib")
	runGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subsrc, "lib")
	runGit(t, dir, "commit", "-qm", "add lib")
	if _, err := os.Stat(filepath.Join(dir, "lib", ".git", "config")); err != nil {
		t.Fatal("setup: the submodule's git directory should be in the work tree")
	}
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(origin), "clone", "-q", "--bare", dir, origin)
	runGit(t, dir, "remote", "add", "origin", origin)
	runGit(t, dir, "fetch", "-q", "origin")
	runGit(t, dir, "branch", "-q", "-u", "origin/main")

	if err := os.WriteFile(filepath.Join(subsrc, "g.txt"), []byte("more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, subsrc, "add", "g.txt")
	runGit(t, subsrc, "commit", "-qm", "c2")
	c2 := strings.TrimSpace(runGit(t, subsrc, "rev-parse", "HEAD"))
	other := filepath.Join(t.TempDir(), "other")
	runGit(t, filepath.Dir(other), "clone", "-q", origin, other)
	runGit(t, other, "update-index", "--add", "--cacheinfo", "160000,"+c2+",lib")
	runGit(t, other, "commit", "-qm", "bump lib")
	runGit(t, other, "push", "-q", "origin", "main")

	info := filepath.Join(dir, "lib", ".git", "objects", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(filepath.Join(subsrc, ".git", "objects")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Join(dir, "lib"), "config", "core.alternateRefsCommand", prog)
	return dir, marker
}

// The fetch and pull gl-repos runs never recurse into submodules: a
// submodule whose git directory is in the work tree is not checked
// (only .git/modules is), so its config must not run either, while
// plain git would fetch inside it as soon as the gitlink moves.
func TestActionsDoNotRecurseIntoSubmodules(t *testing.T) {
	dir, marker := embeddedSubmodule(t)
	runGit(t, dir, "fetch", "--all", "--prune")
	if !ran(marker) {
		t.Fatal("setup: plain git fetch should recurse into the submodule and run its config")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, act := range []Action{Fetch, Pull} {
		dir, marker := embeddedSubmodule(t)
		st := ReadStatus(ctx, dir)
		if st.Err != nil || len(st.Risky) != 0 {
			t.Fatalf("ReadStatus: %v, risks %q", st.Err, st.Risky)
		}
		before := runGit(t, filepath.Join(dir, "lib"), "rev-parse", "refs/remotes/origin/main")
		if _, err := Run(ctx, st, act); err != nil {
			t.Fatalf("%s: %v", act, err)
		}
		if ran(marker) {
			t.Errorf("%s ran the submodule's config", act)
		}
		if after := runGit(t, filepath.Join(dir, "lib"), "rev-parse", "refs/remotes/origin/main"); after != before {
			t.Errorf("%s fetched inside the submodule", act)
		}
		if st := ReadStatus(ctx, dir); act == Fetch && st.Behind != 1 || act == Pull && (st.Behind != 0 || st.Ahead != 0) {
			t.Errorf("after %s: ahead %d behind %d", act, st.Ahead, st.Behind)
		}
	}
}

// A remote whose URL is a local path into the repository's own git
// directory (a planted bare repository at .git/x) would run that
// repository's hooks and config on a confirmed push: flagged as
// "push: remote.<name>.url" (or pushurl), also as an absolute path, a
// file:// URL, through a symlink in the work tree, and from a linked
// worktree into the common directory. Other local paths stay allowed:
// users push to local bare repositories.
func TestRemoteInsideGitDirFlagged(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	runGit(t, dir, "init", "-q", "--bare", filepath.Join(dir, ".git", "x"))
	if err := os.Symlink(filepath.Join(".git", "x"), filepath.Join(dir, "mirror")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, dir, "worktree", "add", "-q", "-b", "wt", wt)
	bare := filepath.Join(t.TempDir(), "backup.git")
	runGit(t, filepath.Dir(bare), "init", "-q", "--bare", bare)

	for _, c := range []struct {
		where, key, val, want string
	}{
		{dir, "remote.origin.url", ".git/x", "push: remote.origin.url"},
		{dir, "remote.origin.pushurl", ".git/x", "push: remote.origin.pushurl"},
		{dir, "remote.origin.url", filepath.Join(dir, ".git", "x"), "push: remote.origin.url"},
		{dir, "remote.origin.url", "file://" + filepath.Join(dir, ".git", "x"), "push: remote.origin.url"},
		{dir, "remote.origin.url", "./mirror", "push: remote.origin.url"},
		{dir, "remote.origin.url", ".git", "push: remote.origin.url"},
		{dir, "remote.origin.url", "./vendor/evil.git", "push: remote.origin.url"},
		{dir, "remote.origin.pushurl", filepath.Join(dir, "vendor", "evil.git"), "push: remote.origin.pushurl"},
		{wt, "remote.origin.url", filepath.Join(dir, ".git", "x"), "push: remote.origin.url"},
		{dir, "remote.origin.url", bare, ""},
		{dir, "remote.origin.url", "../backup.git", ""},
		{dir, "remote.origin.url", "file://" + bare, ""},
		{dir, "remote.origin.url", "git.example.com:.git/x", ""}, // scp-like: a host, not a path
		{dir, "remote.origin.url", "ssh://git.example.com/.git/x", ""},
	} {
		runGit(t, c.where, "config", c.key, c.val)
		got := ConfigRisks(ctx, c.where)
		runGit(t, c.where, "config", "--unset", c.key)
		want := []string{}
		if c.want != "" {
			want = []string{c.want}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s = %s: risks %q, want %q", c.key, c.val, got, want)
		}
	}
}

// git reads an empty value ("sslVerify =") as false and a key without
// a value ("sslVerify" alone) as true, and so does the check:
// http.sslVerify= switches verification off and is flagged, while
// http.saveCookies=, remote.<name>.mirror= and push.followTags= change
// nothing; the keys alone the other way round. An empty program resets
// it, a key that cannot be true (a program, a proxy) is flagged alone.
func TestEmptyValueIsFalse(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	ctx := context.Background()
	for _, c := range []struct {
		key   string
		empty bool // "key =" (false); else the key alone (true)
		want  string
	}{
		{"http.sslVerify", true, "network: http.sslverify"},
		{"http.sslVerify", false, ""},
		{"http.https://gitlab.example.com/.sslVerify", true, "network: http.https://gitlab.example.com/.sslverify"},
		{"http.saveCookies", true, ""},
		{"http.saveCookies", false, "network: http.savecookies"},
		{"http.proxy", true, ""},
		{"http.proxy", false, "network: http.proxy"},
		{"remote.origin.mirror", true, ""},
		{"remote.origin.mirror", false, "push: remote.origin.mirror"},
		{"remote.origin.pruneTags", true, ""},
		{"push.followTags", true, ""},
		{"push.followTags", false, "push: push.followtags"},
		{"credential.helper", true, ""},
		{"credential.helper", false, "credential.helper"},
		{"core.fsmonitor", true, ""},
		{"core.fsmonitor", false, ""}, // a boolean, forced off anyway
		{"pager.log", true, ""},
		{"remote.origin.url", false, ""}, // no helper, no path into the repository
	} {
		name := c.key + " alone"
		if c.empty {
			name = c.key + "="
		}
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			if c.empty {
				runGit(t, dir, "config", "--replace-all", c.key, "")
			} else {
				writeValueless(t, dir, c.key)
			}
			var want []string
			if c.want != "" {
				want = []string{c.want}
			}
			if got := ConfigRisks(ctx, dir); !slices.Equal(got, want) {
				t.Errorf("risks = %q, want %q", got, want)
			}
		})
	}
}

// dropObject removes object oid from dir's object store: the packs are
// unpacked into loose objects first, then that one is deleted.
func dropObject(t *testing.T, dir, oid string) {
	t.Helper()
	packs, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
	aside := t.TempDir()
	for _, pack := range packs {
		moved := filepath.Join(aside, filepath.Base(pack))
		if err := os.Rename(pack, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(strings.TrimSuffix(pack, ".pack")+".idx", strings.TrimSuffix(moved, ".pack")+".idx"); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(moved)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "unpack-objects", "-q")
		cmd.Dir, cmd.Stdin = dir, f
		out, err := cmd.CombinedOutput()
		f.Close()
		if err != nil {
			t.Fatalf("unpack-objects: %v\n%s", err, out)
		}
	}
	if err := os.Remove(filepath.Join(dir, ".git", "objects", oid[:2], oid[2:])); err != nil {
		t.Fatal(err)
	}
}

// A partial clone (git clone --filter) fetches an object it lacks the
// moment a command reads one, from its promisor remote with the
// repository's own config (here remote.origin.uploadpack, a program).
// Listing never does that: a normal partial clone lists cleanly, and
// one that lacks an object a read needs shows an error instead of
// connecting, before any y.
func TestListingNeverLazyFetches(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	ctx := context.Background()
	src := newRepo(t)
	runGit(t, src, "config", "uploadpack.allowFilter", "true")
	dir := filepath.Join(t.TempDir(), "partial")
	runGit(t, filepath.Dir(dir), "clone", "-q", "--no-local", "--filter=blob:none", "file://"+src, dir)
	if st := readAll(t, dir); len(st.Risky) != 0 {
		t.Fatalf("partial clone: risks %q", st.Risky)
	}

	marker := filepath.Join(t.TempDir(), "ran")
	prog := filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\necho ran >> '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "config", "remote.origin.uploadpack", prog)
	runGit(t, dir, "config", "remote.origin.url", src)
	// status compares the index with HEAD's tree: drop it
	dropObject(t, dir, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD^{tree}")))

	st := ReadStatus(ctx, dir)
	if st.Err == nil {
		t.Error("status with HEAD's tree missing: no error")
	}
	if !slices.Equal(st.Risky, []string{"remote.origin.uploadpack"}) {
		t.Errorf("risks = %q", st.Risky)
	}
	_ = Describe(ctx, st, 10000)
	_, _ = Graph(ctx, dir, 10)
	_ = ConfigRisks(ctx, dir)
	if ran(marker) {
		t.Fatal("listing ran the promisor remote's upload-pack")
	}

	// setup check: plain git does fetch the tree (and runs the program)
	cmd := exec.Command("git", "status", "--porcelain=v2", "--branch")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull)
	_ = cmd.Run()
	if !ran(marker) {
		t.Fatal("setup: plain git status should lazy-fetch the missing tree")
	}
}

// git takes a remote's URL and refspecs from the legacy files
// .git/remotes/<name> and .git/branches/<name> when the config names no
// URL for it ("Push: :refs/heads/keep" makes a bare push delete keep).
// They are not checked, so any is a mark; an empty folder is not.
func TestLegacyRemoteFilesFlagged(t *testing.T) {
	ctx := context.Background()
	for _, d := range legacyRemotes {
		dir := newRepo(t)
		legacy := filepath.Join(dir, ".git", d)
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := ConfigRisks(ctx, dir); len(got) != 0 {
			t.Errorf("empty %s: risks %q", d, got)
		}
		if err := os.WriteFile(filepath.Join(legacy, "origin"), []byte("URL: /srv\nPush: :refs/heads/keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		want := "push: .git/" + d
		if st := ReadStatus(ctx, dir); !slices.Contains(st.Risky, want) || !st.NeedsAttention() {
			t.Errorf("%s/origin: risky %q, want %q", d, st.Risky, want)
		}
	}
}

// A fetch runs none of the repository's hooks (reference-transaction
// runs on every ref update): it may run unattended at start.
func TestFetchRunsNoHook(t *testing.T) {
	ctx := context.Background()
	src := newRepo(t)
	dir := filepath.Join(t.TempDir(), "clone")
	runGit(t, filepath.Dir(dir), "clone", "-q", src, dir)
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(dir, ".git", "hooks", "reference-transaction")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho ran >> '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "commit", "-q", "--allow-empty", "-m", "next")
	st := readAll(t, dir)
	if out, err := Run(ctx, st, Fetch); err != nil {
		t.Fatalf("fetch: %v: %s", err, out)
	}
	if ran(marker) {
		t.Fatal("fetch ran the reference-transaction hook")
	}
	if got := runGit(t, dir, "rev-parse", "origin/main"); strings.TrimSpace(got) != strings.TrimSpace(runGit(t, src, "rev-parse", "HEAD")) {
		t.Errorf("fetch did not update origin/main")
	}
	// setup check: plain git does run the hook
	runGit(t, src, "commit", "-q", "--allow-empty", "-m", "again")
	runGit(t, dir, "fetch", "-q")
	if !ran(marker) {
		t.Fatal("setup: plain fetch ran no hook")
	}
}
