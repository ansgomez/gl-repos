package tui

import (
	"strings"
	"testing"
)

// Every key of the list stays in its help down to 60 columns; it used to
// be cut after two lines, which hid "i ask AI" below ~130 columns.
func TestListHelpShowsEveryKey(t *testing.T) {
	for _, w := range []int{60, 80, 100, 160} {
		got := strings.Join(wrapHelpN(listHelp, w, listHelpLines), " · ")
		if got != listHelp {
			t.Errorf("width %d: help is\n%s", w, got)
		}
	}
}
