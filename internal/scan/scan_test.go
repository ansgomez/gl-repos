package scan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFind(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("a/.git")
	mk("a/nested/.git") // inside a repo: not reported
	mk("group/b/.git")
	mk("group/deeper/c/.git")
	mk(".hidden/d/.git")
	mk("node_modules/e/.git")

	cases := []struct {
		depth int
		want  []string
	}{
		{0, nil},
		{1, []string{"a"}},
		{2, []string{"a", "group/b"}},
		{3, []string{"a", "group/b", "group/deeper/c"}},
	}
	for _, c := range cases {
		got, err := Find(root, c.depth)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(c.want) {
			t.Errorf("depth %d: got %v, want %v", c.depth, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != filepath.Join(root, c.want[i]) {
				t.Errorf("depth %d: got %v, want %v", c.depth, got, c.want)
				break
			}
		}
	}
}

func TestFindExtraSkip(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"keep/.git", "archive/old/.git"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Find(root, 3, "archive")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != filepath.Join(root, "keep") {
		t.Errorf("got %v, want only keep", got)
	}
}
