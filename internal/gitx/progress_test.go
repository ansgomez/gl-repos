package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gl-repos/internal/forge"
)

func TestProgressPct(t *testing.T) {
	for line, want := range map[string]int{
		"remote: Counting objects: 100% (5/5), done.":   6,
		"Receiving objects:   0% (0/100)":               10,
		"Receiving objects:  50% (50/100), 1.2 MiB | 3": 47,
		"Resolving deltas: 100% (3/3), done.":           97,
		"Updating files: 100% (12/12), done.":           100,
	} {
		_, got, ok := progressPct(line)
		if !ok || got != want {
			t.Errorf("%q = %d, %v; want %d", line, got, ok, want)
		}
	}
	if _, _, ok := progressPct("Cloning into 'x'..."); ok {
		t.Errorf("non-progress line parsed")
	}
	var lines []string
	parseProgress(strings.NewReader("a: 1%\ra: 2%\rdone\nlast"), func(l string) { lines = append(lines, l) })
	if strings.Join(lines, "|") != "a: 1%|a: 2%|done|last" {
		t.Errorf("lines = %q", lines)
	}
}

func TestCloneProgress(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", src},
		{"-C", src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	var last int
	// file:// forces the transport that reports progress
	out, err := CloneProgress(context.Background(), dir, mustURL(t, "file://"+src), filepath.Join(dir, "dst"), 10*time.Second,
		func(_ string, p int) {
			if p >= 0 {
				last = p
			}
		})
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "dst", ".git")); err != nil {
		t.Errorf("not cloned")
	}
	if last == 0 {
		t.Errorf("no progress reported")
	}

	// a server that never answers: stall timeout
	slow := filepath.Join(dir, "slow.sh")
	os.WriteFile(slow, []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	t.Setenv("GIT_SSH_COMMAND", slow)
	start := time.Now()
	_, err = CloneProgress(context.Background(), dir, mustURL(t, "ssh://example.invalid/x.git"), filepath.Join(dir, "never"), 300*time.Millisecond, nil)
	if !errors.Is(err, ErrStalled) || time.Since(start) > 15*time.Second {
		t.Errorf("err = %v after %s", err, time.Since(start))
	}
}

func mustURL(t *testing.T, s string) forge.CloneURL {
	t.Helper()
	u, err := forge.ParseCloneURL(s)
	if err != nil {
		t.Fatalf("ParseCloneURL(%q): %v", s, err)
	}
	return u
}

// Only a CloneURL reaches git clone: the zero value is refused.
func TestCloneNeedsURL(t *testing.T) {
	if _, err := CloneProgress(context.Background(), t.TempDir(), forge.CloneURL{}, "x", time.Second, nil); !errors.Is(err, forge.ErrBadCloneURL) {
		t.Errorf("CloneProgress: %v", err)
	}
	if _, err := Clone(context.Background(), t.TempDir(), forge.CloneURL{}, "x"); !errors.Is(err, forge.ErrBadCloneURL) {
		t.Errorf("Clone: %v", err)
	}
}

// A clone keeps only the last maxCloneLog of git's message lines.
func TestKeepTail(t *testing.T) {
	var log []string
	for i := range maxCloneLog + 50 {
		log = keepTail(log, strconv.Itoa(i))
	}
	if len(log) != maxCloneLog || log[0] != "50" || log[len(log)-1] != strconv.Itoa(maxCloneLog+49) {
		t.Errorf("kept %d lines, %q..%q", len(log), log[0], log[len(log)-1])
	}
}

// A line within maxProgressRead is read whole and redacted before it is
// cut to maxProgressLine: a credential longer than the cut would
// otherwise lose its "@" and show its start. A longer line is dropped
// whole (passed on empty), never in part, and the lines after it are
// still read.
func TestKeepTailRedactsBeforeCut(t *testing.T) {
	long := "fatal: unable to access 'https://oauth2:" + strings.Repeat("S", 2*maxProgressLine) + "@h/x.git/'"
	var lines []string
	parseProgress(strings.NewReader(long+"\rReceiving objects:  50%\n"+long), func(l string) { lines = append(lines, l) })
	if len(lines) != 3 || lines[0] != long || lines[1] != "Receiving objects:  50%" || lines[2] != long {
		t.Errorf("got %d lines of %d, %d, %d bytes", len(lines), len(lines[0]), len(lines[1]), len(lines[2]))
	}
	if log := keepTail(nil, long); len(log) != 1 || log[0] != "fatal: unable to access 'https://***@h/x.git/'" {
		t.Errorf("kept %d lines, the first of %d bytes, redacted: %v", len(log), len(log[0]), strings.Contains(log[0], "***"))
	}
	if log := keepTail(nil, "x"+strings.Repeat("é", maxProgressLine)); len(log[0]) != maxProgressLine-1 { // not mid-rune
		t.Errorf("cut to %d bytes", len(log[0]))
	}

	huge := "https://oauth2:" + strings.Repeat("S", maxProgressRead) + "@h/x.git/"
	lines = nil
	parseProgress(strings.NewReader(huge+"\nlast\n"+huge), func(l string) { lines = append(lines, l) })
	if len(lines) != 3 || lines[0] != "" || lines[1] != "last" || lines[2] != "" {
		t.Errorf("over the bound: %d lines of %d, %d, %d bytes", len(lines), len(lines[0]), len(lines[1]), len(lines[2]))
	}
}
