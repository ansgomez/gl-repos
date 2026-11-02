package tui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gl-repos/internal/ai"
	"gl-repos/internal/gitx"
)

func TestRenderStepKeepsConfirmedArgs(t *testing.T) {
	msg := "Add parser\n\nHandles empty input."
	m := Model{width: 80, rows: []*row{{}}, plan: &planState{
		plan: ai.Plan{Steps: []gitx.Step{{Op: "commit", Message: msg}}},
		args: [][]string{{"commit", "-m", msg}},
	}}
	out := strings.Join(m.renderStep(0, m.plan.plan.Steps[0], nil), "\n")
	if !slices.Equal(m.plan.args[0], []string{"commit", "-m", msg}) {
		t.Fatalf("rendering changed the confirmed args: %q", m.plan.args[0])
	}
	if want := "git commit -m '<message>'"; !strings.Contains(out, want) {
		t.Errorf("render = %q, want %q", out, want)
	}
}

func TestPlanValidatedInCommand(t *testing.T) {
	m := New(Options{Root: "/w", Repos: []string{"/nonexistent/w/a"}})
	m.width, m.height = 80, 24
	m.planSeq = 3
	m.plan = &planState{idx: 0, loading: true}
	m.mode = modePlan
	steps := []gitx.Step{{Op: "commit", Message: "Add parser"}}
	cmd := m.handlePlanMsg(planMsg{seq: 3, plan: ai.Plan{Summary: "commit", Steps: steps}})
	if cmd == nil || !m.plan.validating || m.plan.runnable() {
		t.Fatalf("the plan must be checked in a command first (validating %v)", m.plan.validating)
	}
	if out := m.viewPlan(); !strings.Contains(out, "checking the steps") || strings.Contains(out, "y run") {
		t.Errorf("view while checking:\n%s", out)
	}
	next, run := m.handlePlanKey(keyY)
	if run != nil || next.(Model).plan == nil {
		t.Fatal("y ran a plan that was not checked yet")
	}
	first := cmd().(planValidMsg)

	// an edit while the check runs makes the first answer stale
	m.plan.plan.Steps[0].Message = "Add a parser"
	cmd2 := m.validatePlan()
	m.handlePlanValid(first)
	if !m.plan.validating || m.plan.checked != nil {
		t.Fatal("a stale check was used")
	}
	// the answer for the edited steps counts; y then uses exactly them
	m.handlePlanValid(planValidMsg{planSeq: 3, valSeq: m.plan.valSeq, steps: slices.Clone(m.plan.plan.Steps),
		args: [][]string{{"commit", "-m", "Add a parser"}}, errs: []error{nil}})
	if m.plan.validating || !m.plan.runnable() || m.plan.checked[0].Message != "Add a parser" {
		t.Errorf("after the check: validating %v runnable %v checked %+v", m.plan.validating, m.plan.runnable(), m.plan.checked)
	}
	if msg, ok := cmd2().(planValidMsg); !ok || msg.steps[0].Message != "Add a parser" {
		t.Errorf("second check validated %+v", msg.steps)
	}
	m.life.cancel()
}

// Checking a plan runs git, so it waits for a slot of -jobs like every
// other git operation, and gives up (not runnable) when none comes.
func TestPlanCheckTakesGitSlot(t *testing.T) {
	m := New(Options{Root: "/w", Repos: []string{"/nonexistent/w/a"}, Jobs: 1})
	defer m.life.cancel()
	m.planSeq = 1
	m.plan = &planState{idx: 0, plan: ai.Plan{Steps: []gitx.Step{{Op: "fetch"}}}}
	m.sem <- struct{}{} // all slots busy
	done := make(chan tea.Msg, 1)
	cmd := m.validatePlan()
	go func() { done <- cmd() }()
	select {
	case <-done:
		t.Fatal("the check ran git without a slot")
	case <-time.After(100 * time.Millisecond):
	}
	<-m.sem
	select {
	case msg := <-done:
		if v := msg.(planValidMsg); len(v.errs) != 1 {
			t.Errorf("check: %+v", v)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the check did not run once a slot was free")
	}
	if len(m.sem) != 0 {
		t.Error("the slot was not given back")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.sem <- struct{}{}
	ran := false
	if err := withSlot(ctx, m.sem, func() { ran = true }); err == nil || ran {
		t.Errorf("withSlot ran without a slot: %v", err)
	}
}

// r (and f) ask again with the request as it was typed: an empty one is
// still the default request, so a push the model adds is still left out.
func TestRetryKeepsDefaultRequest(t *testing.T) {
	answer := `{"summary":"Commit and push.","steps":[{"op":"stage_all"},{"op":"commit","message":"Add x"},{"op":"push"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			w.Write([]byte(`{"models":[]}`))
			return
		}
		w.Write([]byte(`{"message":{"role":"assistant","content":` + strconv.Quote(answer) + `},"done":true}` + "\n"))
	}))
	defer srv.Close()
	m := New(Options{Root: "/w", Repos: []string{"/nonexistent/w/a"}, Jobs: 1,
		AI: ai.Client{URL: srv.URL, Model: "test:1b"}})
	defer m.life.cancel()
	ask := func(cmd tea.Cmd) {
		msg := cmd().(planMsg)
		if msg.err != nil {
			t.Fatal(msg.err)
		}
		m.handlePlanMsg(msg)
		for _, s := range m.plan.plan.Steps {
			if s.Op == "push" {
				t.Errorf("the default request pushed: %q", m.plan.request)
			}
		}
	}
	ask(m.startPlan(0, "", false))
	for _, key := range []string{"r", "f"} {
		if key == "f" {
			m.plan.err = &ai.MemoryError{}
		}
		next, cmd := m.handlePlanKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m = next.(Model)
		if m.plan.request != ai.DefaultPrompt {
			t.Errorf("%s: request shown as %q", key, m.plan.request)
		}
		ask(cmd)
	}
}

// A clone the model answers to the default request (also on r) does not
// open the clone flow, which would search the forge and download.
func TestDefaultRequestNeverClones(t *testing.T) {
	answer := `{"summary":"Clone the billing service.","clone":"billing service","steps":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			w.Write([]byte(`{"models":[]}`))
			return
		}
		w.Write([]byte(`{"message":{"role":"assistant","content":` + strconv.Quote(answer) + `},"done":true}` + "\n"))
	}))
	defer srv.Close()
	m := New(Options{Root: "/w", Repos: []string{"/nonexistent/w/a"}, Jobs: 1,
		AI: ai.Client{URL: srv.URL, Model: "test:1b"}})
	defer m.life.cancel()
	ask := func(cmd tea.Cmd) {
		msg := cmd().(planMsg)
		if msg.err != nil {
			t.Fatal(msg.err)
		}
		m.handlePlanMsg(msg)
		if m.mode != modePlan || m.clone != nil || m.plan == nil || m.plan.plan.Clone != "" {
			t.Fatalf("the default request opened the clone flow (mode %v)", m.mode)
		}
	}
	ask(m.startPlan(0, "", false))
	next, cmd := m.handlePlanKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = next.(Model)
	ask(cmd)
}

// Asking for a plan reads the repository under a git slot, gives the
// slot back before the model is asked (a slow model holds none), and
// gives up when cancelled while waiting for one.
func TestPlanRequestTakesGitSlot(t *testing.T) {
	asked := make(chan int, 16) // free slots' fill level when the model is asked
	release := make(chan struct{})
	var sem chan struct{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- len(sem)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		http.Error(w, "no model here", http.StatusInternalServerError)
	}))
	defer srv.Close()
	defer close(release)
	m := New(Options{Root: "/w", Repos: []string{"/nonexistent/w/a"}, Jobs: 1,
		AI: ai.Client{URL: srv.URL, Model: "test:1b"}})
	defer m.life.cancel()
	sem = m.sem

	m.sem <- struct{}{} // all slots busy
	done := make(chan tea.Msg, 1)
	cmd := m.startPlan(0, "", true)
	go func() { done <- cmd() }()
	select {
	case <-asked:
		t.Fatal("the model was asked before the repository was read")
	case <-done:
		t.Fatal("the request ended without a slot")
	case <-time.After(100 * time.Millisecond):
	}
	<-m.sem
	select {
	case n := <-asked:
		if n != 0 {
			t.Errorf("%d slot(s) held while the model is asked", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the model was never asked once a slot was free")
	}
	release <- struct{}{}
	select {
	case msg := <-done:
		if p := msg.(planMsg); p.err == nil {
			t.Errorf("plan from a failing server: %+v", p)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the request did not end")
	}

	// esc while waiting for a slot: cancelled, the model never asked
	m.sem <- struct{}{}
	cmd = m.startPlan(0, "", true)
	go func() { done <- cmd() }()
	m.plan.cancel()
	select {
	case msg := <-done:
		if p := msg.(planMsg); !errors.Is(p.err, context.Canceled) {
			t.Errorf("cancelled request: %v", p.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled request kept waiting for a slot")
	}
	select {
	case <-asked:
		t.Error("the model was asked after the request was cancelled")
	default:
	}
	<-m.sem

	// a deadline while waiting gives up the same way
	m.sem <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := withSlot(ctx, m.sem, func() { t.Error("ran without a slot") }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("withSlot after the deadline: %v", err)
	}
}

func TestPlanStepWithCredentialsCannotRun(t *testing.T) {
	m := New(Options{Root: "/w", Repos: []string{"/nonexistent/w/a"}, Jobs: 1})
	defer m.life.cancel()
	m.planSeq = 1
	for _, sec := range secretShapes {
		m.plan = &planState{idx: 0, plan: ai.Plan{Steps: []gitx.Step{
			{Op: "commit", Message: "Pin " + sec}, {Op: "stash", Message: sec}, {Op: "fetch"}}}}
		v := m.validatePlan()().(planValidMsg)
		if len(v.errs) != 3 || !errors.Is(v.errs[0], errStepCredentials) || !errors.Is(v.errs[1], errStepCredentials) ||
			errors.Is(v.errs[2], errStepCredentials) {
			t.Errorf("%q: errs %v", sec, v.errs)
		}
	}
}

// The commit graph (l) reads git under a slot (-jobs) like a status
// read, so l on row after row never runs more git than asked for.
func TestGraphTakesGitSlot(t *testing.T) {
	m := New(Options{Root: "/w", Repos: []string{"/nonexistent/w/a"}, Jobs: 1})
	defer m.life.cancel()
	m.width, m.height = 80, 24
	m.sem <- struct{}{} // all slots busy
	done := make(chan tea.Msg, 1)
	cmd := m.openGraph()
	go func() { done <- cmd() }()
	select {
	case <-done:
		t.Fatal("the graph ran git without a slot")
	case <-time.After(100 * time.Millisecond):
	}
	<-m.sem
	select {
	case msg := <-done:
		if g := msg.(graphMsg); g.idx != 0 || g.err == nil {
			t.Errorf("graph of a missing repository: %+v", g)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the graph did not run once a slot was free")
	}
	if len(m.sem) != 0 {
		t.Error("the slot was not given back")
	}
}

// An empty prompt is the default request and says so to the model
// client; the default's own words typed are an ordinary request.
func TestDefaultRequestByFlagNotText(t *testing.T) {
	for _, tc := range []struct {
		typed, request string
		isDefault      bool
	}{
		{"", ai.DefaultPrompt, true},
		{"  ", ai.DefaultPrompt, true},
		{ai.DefaultPrompt, ai.DefaultPrompt, false},
		{"commit and push", "commit and push", false},
	} {
		if request, def := defaultRequest(tc.typed); request != tc.request || def != tc.isDefault {
			t.Errorf("defaultRequest(%q) = %q, %v; want %q, %v", tc.typed, request, def, tc.request, tc.isDefault)
		}
	}
}
