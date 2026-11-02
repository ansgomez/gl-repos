package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gl-repos/internal/safe"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Forge is a code host reached through its own CLI (glab or gh), so
// gl-repos reuses that CLI's login and never handles tokens.
type Forge interface {
	Kind() string // "gitlab" or "github"
	Name() string // "GitLab" or "GitHub", for display
	CLI() string  // "glab" or "gh"
	// Hostname is the server, or "" while it is not known yet.
	Hostname() string
	WithHost(host string) Forge
	// DetectHost finds the server when none is configured, preferring
	// one the listed repositories' remotes point to.
	DetectHost(ctx context.Context, remotes []string) string

	ListAll(ctx context.Context) ([]Project, error)
	Search(ctx context.Context, query string) ([]Project, error)
	// Keep drops projects in excluded groups.
	Keep(ps []Project) []Project
	AllowsURL(u CloneURL) bool

	// Whoami returns the logged-in user's name, the default owner of new
	// projects.
	Whoami(ctx context.Context) (string, error)
	// Visibilities lists the choices for new projects, private first.
	Visibilities() []string
	// CreateCmd creates an empty project at path ("owner/name"); me is
	// the logged-in user (from Whoami), whose namespace needs no group.
	CreateCmd(path, visibility, me string) Cmd
	// RemoteURL is the clone URL of the project at path.
	RemoteURL(path string, ssh bool) string
}

// Cmd is a forge CLI invocation, shown to the user before it runs.
type Cmd struct {
	Env  []string // extra environment, e.g. GITLAB_HOST=…
	Name string
	Args []string
}

// String renders the command as it will run, with arguments quoted for
// display (see safe.Quote).
func (c Cmd) String() string {
	parts := append([]string{}, c.Env...)
	parts = append(parts, c.Name)
	for _, a := range c.Args {
		parts = append(parts, safe.Quote(a))
	}
	return strings.Join(parts, " ")
}

// Run executes c in an empty temporary directory, so the CLI cannot
// mistake the current directory for a repository to change.
func (c Cmd) Run(ctx context.Context) (string, error) {
	if _, err := exec.LookPath(c.Name); err != nil {
		return "", fmt.Errorf("%s not found: install it and log in", c.Name)
	}
	dir, err := os.MkdirTemp("", "gl-repos-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	cmd, err := cliCommand(ctx, c.Name, c.Args...)
	if err != nil {
		return "", err
	}
	cmd.Dir = dir
	cmd.Env = append(cmd.Env, c.Env...)
	out, err := CombinedOutput(cmd)
	return strings.TrimSpace(string(out)), err
}

// noPrompt keeps glab and gh from asking questions: the TUI owns the
// terminal. (glab 1.106+ warns on every call about the older NO_PROMPT.)
var noPrompt = []string{"GLAB_NO_PROMPT=1", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"}

// cliCommand builds a glab or gh command with cliEnv. Every call of
// either CLI is built here, and arguments with URL credentials are
// refused (CheckArgs).
func cliCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	if err := CheckArgs(args); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = cliEnv()
	return cmd, nil
}

// cliEnv is the environment for glab and gh: ours, without the
// deprecated NO_PROMPT (glab warns about it on every call), plus noPrompt.
func cliEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "NO_PROMPT=") {
			env = append(env, e)
		}
	}
	return append(env, noPrompt...)
}

// MaxOutput caps what one glab, gh or ssh call may write on stdout or on
// stderr. Real answers stay far below it (a project list is a few KB per
// project). Past it the program is killed and the call fails with
// ErrTooLarge, rather than buffering whatever a server sends.
var MaxOutput int64 = 128 << 20

// ErrTooLarge is returned when a capped call wrote more than MaxOutput bytes.
var ErrTooLarge = errors.New("response too large")

// CappedWriter buffers at most MaxOutput bytes of cmd's stdout or
// stderr. The first write past that kills cmd (it is stopped, not read
// forever) and fails.
type CappedWriter struct {
	buf  bytes.Buffer
	cmd  *exec.Cmd
	over bool
}

// NewCappedWriter is a CappedWriter for cmd, to set as its Stdout or
// Stderr (Output and CombinedOutput do both).
func NewCappedWriter(cmd *exec.Cmd) *CappedWriter { return &CappedWriter{cmd: cmd} }

func (w *CappedWriter) Write(p []byte) (int, error) {
	if int64(w.buf.Len())+int64(len(p)) > MaxOutput {
		w.over = true
		if w.cmd.Process != nil {
			_ = w.cmd.Process.Kill() // the error is ErrTooLarge either way
		}
		return 0, ErrTooLarge
	}
	return w.buf.Write(p)
}

// Bytes is what was written so far.
func (w *CappedWriter) Bytes() []byte { return w.buf.Bytes() }

// Err is the ErrTooLarge error once the cap was hit, else err (cmd's).
func (w *CappedWriter) Err(err error) error {
	if w.over {
		return fmt.Errorf("%w (over %d MB)", ErrTooLarge, MaxOutput>>20)
	}
	return err
}

// Output runs cmd (a cliCommand, or a git command) and returns its
// stdout and stderr, each capped at MaxOutput: past that the error is
// ErrTooLarge.
func Output(cmd *exec.Cmd) (stdout, stderr []byte, err error) {
	out, errb := NewCappedWriter(cmd), NewCappedWriter(cmd)
	cmd.Stdout, cmd.Stderr = out, errb
	err = out.Err(errb.Err(cmd.Run()))
	return out.Bytes(), errb.Bytes(), err
}

// CombinedOutput is Output with stdout and stderr in one buffer (also
// used for other programs that might write without bound, e.g. ssh).
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	w := NewCappedWriter(cmd)
	cmd.Stdout, cmd.Stderr = w, w
	err := cmd.Run()
	return w.Bytes(), w.Err(err)
}

// New builds the forge for kind ("gitlab" or "github").
func New(kind, host string, exclude []string) (Forge, error) {
	switch strings.ToLower(kind) {
	case "gitlab":
		return GitLab{Host: host, Exclude: exclude}, nil
	case "github":
		return GitHub{Host: host, Exclude: exclude}, nil
	}
	return nil, fmt.Errorf("unknown forge %q (want gitlab or github)", kind)
}

// CheckPath validates an "owner/name" project path typed by the user.
// Segments may not start with "-" or ".", so they can never be read as
// options by glab or gh.
func CheckPath(p string) error {
	if !strings.Contains(p, "/") || !safePath(p) || strings.Contains(p, "..") || hasGitSuffix(p) {
		return fmt.Errorf("use owner/name (letters, digits, . _ -; not starting with -)")
	}
	return nil
}

// CheckGroup validates a group path such as "acme/tools" (from the
// config) with the same rules as CheckPath, so a configured group is
// always one that can be typed.
func CheckGroup(g string) error {
	if !safePath(g) || strings.Contains(g, "..") || hasGitSuffix(g) {
		return fmt.Errorf("want a group path like acme/tools (letters, digits, . _ -; not starting with -)")
	}
	return nil
}

// hasGitSuffix reports whether any segment of p ends in ".git" (in any
// case): such a name is read as a clone URL, or as a repository folder.
func hasGitSuffix(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if strings.HasSuffix(strings.ToLower(s), ".git") {
			return true
		}
	}
	return false
}

// remoteURL is the clone URL of path on host. The port of a host like
// "gitlab.example.com:8443" is the web port: https keeps it, ssh drops it
// (ssh runs on its own port, 22 or whatever ~/.ssh/config says).
func remoteURL(host, path string, ssh bool) string {
	if !ssh {
		return "https://" + host + "/" + path + ".git"
	}
	h := HostName(host)
	if strings.Contains(h, ":") { // IPv6: git's scp form needs brackets
		h = "[" + h + "]"
	}
	return "git@" + h + ":" + path + ".git"
}

// HostName is host without a port and without IPv6 brackets:
// "gitlab.example.com:8443" → "gitlab.example.com", "[::1]:8443" →
// "::1", the form HostFromURL returns.
func HostName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	if strings.Count(host, ":") == 1 { // "host:" with an empty port
		h, _, _ := strings.Cut(host, ":")
		return h
	}
	return host // a bare IPv6 address, or no port
}

// decodePages decodes "--paginate" output: one JSON array per page,
// back to back (or a single merged array).
func decodePages[T any](out []byte) ([]T, error) {
	var all []T
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var page []T
		if err := dec.Decode(&page); err == io.EOF {
			return all, nil
		} else if err != nil {
			return nil, err
		}
		all = append(all, page...)
	}
}

// keepProjects cleans projects from a server or cache and drops those in
// excluded groups or with unsafe paths or URLs.
func keepProjects(ps []Project, exclude []string) []Project {
	out := ps[:0:0]
	for _, p := range ps {
		if p, ok := cleanProject(p); ok && !Excluded(p.Path, exclude) && !excludedURL(p, exclude) {
			out = append(out, p)
		}
	}
	return out
}

// excludedURL reports whether a clone URL of p leads into an excluded
// group. The server's path and its URLs normally agree, but only a URL
// reaches git, so each is checked like a pasted one (AllowsURL).
func excludedURL(p Project, exclude []string) bool {
	for _, u := range []string{p.SSHURL, p.HTTPURL} {
		if u == "" {
			continue
		}
		cu, err := ParseCloneURL(u) // cleaned by cleanProject, so it parses
		if err != nil || !allowsURL(cu, exclude) {
			return true
		}
	}
	return false
}

// cleanProject makes server data safe to show and to use as a folder
// below the root: every path segment must be a plain name (no "." or
// "..", no ".git", no leading "-", no control characters), URLs must be
// single-line.
// Descriptions are sanitized for the terminal and lose any credentials
// (RedactLine) before they are shown or cached; the URLs lose theirs
// (see StripCredentials): a server has no business handing them out,
// and they would be shown, cached and given to git. What is left must
// be a CloneURL (else that URL is dropped, and the project with it when
// none is left).
func cleanProject(p Project) (Project, bool) {
	if !safePath(p.Path) || (p.Name != "" && !safePath(p.Name)) {
		return p, false
	}
	for _, u := range []string{p.SSHURL, p.HTTPURL} {
		if safe.Line(u) != u || strings.HasPrefix(u, "-") {
			return p, false
		}
	}
	had := p.HTTPURL != "" || p.SSHURL != ""
	p.HTTPURL, p.SSHURL = serverURL(p.HTTPURL), serverURL(p.SSHURL)
	if had && p.HTTPURL == "" && p.SSHURL == "" {
		return p, false
	}
	p.Description = RedactLine(p.Description)
	return p, true
}

// serverURL is a clone URL from a server without its credentials, as a
// CloneURL would give it to git, or "" if it is none.
func serverURL(u string) string {
	if u == "" {
		return ""
	}
	cu, err := ParseCloneURL(StripCredentials(u))
	if err != nil {
		return ""
	}
	return cu.String()
}

var segment = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.+-]*$`)

// userName reports whether a user name a server sent is one plain
// segment: it is prefilled as the owner of new projects and compared
// with the typed one, so it must be what could be typed.
func userName(u string) bool { return !strings.Contains(u, "/") && safePath(u) }

// safePath reports whether p is a slash path of plain segments.
func safePath(p string) bool {
	if p == "" {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		// ".git" in any case would turn the folder above into a
		// repository whose config the server controls (case-insensitive
		// file systems make ".Git" the same folder).
		if !segment.MatchString(s) || s == "." || s == ".." || strings.EqualFold(s, ".git") {
			return false
		}
	}
	return true
}

var (
	_ Forge = GitLab{}
	_ Forge = GitHub{}
)
