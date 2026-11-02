package gitx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"gl-repos/internal/forge"
)

// Progress receives clone progress: the current phase ("receiving
// objects") and the overall percentage, which never decreases.
type Progress func(phase string, pct int)

// phases maps git's progress phases to slices of the overall bar.
var phases = map[string][2]int{
	"enumerating objects": {0, 3},
	"counting objects":    {3, 6},
	"compressing objects": {6, 10},
	"receiving objects":   {10, 85},
	"resolving deltas":    {85, 97},
	"updating files":      {97, 100},
	"checking out files":  {97, 100},
}

var progressLine = regexp.MustCompile(`^(?:remote: )?([A-Za-z][A-Za-z ]*?):\s+(\d+)%`)

// ErrStalled means a clone produced no output for too long.
var ErrStalled = fmt.Errorf("stalled")

// CloneProgress clones like Clone but reports progress while it runs and
// aborts if git prints nothing for stall (a dead connection). It returns
// git's messages without the progress lines. Cancelling ctx stops git
// with SIGTERM, which lets it remove the partial clone.
func CloneProgress(ctx context.Context, root string, url forge.CloneURL, dest string, stall time.Duration, report Progress) (string, error) {
	if url.IsZero() {
		return "", forge.ErrBadCloneURL
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := command(ctx, root, true, "clone", "--progress", "--", url.String(), dest)
	if err != nil {
		return "", err
	}
	sshEnv(ctx, root, cmd)
	// Own process group, so cancelling also stops ssh (which may hang on a
	// dead connection); git removes the partial clone on SIGTERM.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw

	var stalled bool
	var mu sync.Mutex
	timer := time.AfterFunc(stall, func() {
		mu.Lock()
		stalled = true
		mu.Unlock()
		cancel()
	})
	defer timer.Stop()

	if err := cmd.Start(); err != nil {
		return "", err
	}
	var log []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		parseProgress(pr, func(line string) {
			timer.Reset(stall)
			if phase, pct, ok := progressPct(line); ok {
				if report != nil {
					report(phase, pct)
				}
				return
			}
			if line = strings.TrimSpace(line); line != "" {
				log = keepTail(log, line)
			}
		})
	}()
	err = cmd.Wait()
	pw.Close()
	<-done
	out := strings.Join(log, "\n")
	mu.Lock()
	defer mu.Unlock()
	if stalled && err != nil {
		return out, fmt.Errorf("%w: no progress for %s (network down or VPN needed?)", ErrStalled, stall)
	}
	return out, err
}

// maxCloneLog is how many of git's message lines a clone keeps, so a
// server streaming messages cannot grow it without bound.
const maxCloneLog = 200

// keepTail appends line to log, redacted and cut to maxProgressLine,
// dropping the oldest line once log holds maxCloneLog lines: only the
// tail is ever shown. Redaction comes first: a cut alone could leave
// part of a credential, which the display would not find.
func keepTail(log []string, line string) []string {
	if len(log) >= maxCloneLog {
		log = append(log[:0], log[len(log)-maxCloneLog+1:]...)
	}
	return append(log, cutLine(forge.RedactURLs(line)))
}

// maxProgressLine bounds one kept line of git's messages, in bytes,
// once it is redacted whole (keepTail).
const maxProgressLine = 4096

// maxProgressRead bounds one line of git's output while it is read, in
// bytes: a server streaming a line that never ends cannot grow it. A
// longer line is dropped whole when it ends, never kept in part (which
// could be part of a credential), and passed on empty so that it still
// counts as output.
const maxProgressRead = 64 << 10

// cutLine cuts line to maxProgressLine bytes, at a rune boundary.
func cutLine(line string) string {
	if len(line) <= maxProgressLine {
		return line
	}
	n := maxProgressLine
	for n > 0 && !utf8.RuneStart(line[n]) {
		n--
	}
	return line[:n]
}

// parseProgress splits git's output on \r and \n: progress lines are
// rewritten in place with \r.
func parseProgress(r io.Reader, line func(string)) {
	buf := make([]byte, 4096)
	var cur bytes.Buffer
	over := false // the line is longer than maxProgressRead: dropped
	end := func() {
		if over {
			line("")
		} else {
			line(cur.String())
		}
		cur.Reset()
		over = false
	}
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			switch {
			case b == '\r' || b == '\n':
				end()
			case cur.Len() < maxProgressRead:
				cur.WriteByte(b)
			default:
				over = true
				cur.Reset()
			}
		}
		if err != nil {
			if cur.Len() > 0 || over {
				end()
			}
			return
		}
	}
}

// progressPct turns a git progress line into a phase and an overall
// percentage.
func progressPct(line string) (string, int, bool) {
	m := progressLine.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "", 0, false
	}
	phase := strings.ToLower(m[1])
	p, _ := strconv.Atoi(m[2])
	span, ok := phases[phase]
	if !ok {
		return phase, -1, true // unknown phase: keep the bar where it is
	}
	return phase, span[0] + (span[1]-span[0])*min(p, 100)/100, true
}
