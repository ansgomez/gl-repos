package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gl-repos/internal/ai"
	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

// errStepCredentials refuses a plan step whose command holds a URL with
// credentials: the screen hides them, so the step could not be confirmed
// as it would run.
var errStepCredentials = errors.New("contains a URL with credentials; gl-repos does not run it")

// planState tracks one AI request from prompt to confirmation.
type planState struct {
	idx      int
	typed    string // as entered: "" is the default request, also on r and f
	request  string // as asked and shown
	loading  bool
	started  time.Time
	chunks   *atomic.Int64 // streamed chunks so far, written by the request goroutine
	cancel   context.CancelFunc
	plan     ai.Plan
	stepErrs []error    // validation result per step
	args     [][]string // exact git arguments per step, as confirmed
	err      error
	editStep int

	// The plan is checked against a fresh status in a command; y works
	// only once the answer for the current steps is in.
	validating bool
	valSeq     int
	checked    []gitx.Step // the steps args and stepErrs belong to
	risky      []string    // config keys that run programs, as checked

	// The plan scrolls when it does not fit; y needs every line to have
	// been on screen (seen counts lines from the top).
	scroll int
	seen   int
	notice string
}

// planValidMsg is the result of checking a plan's steps.
type planValidMsg struct {
	planSeq, valSeq int
	steps           []gitx.Step
	args            [][]string
	errs            []error
	risky           []string
}

type planMsg struct {
	seq  int
	plan ai.Plan
	err  error
}

func (p *planState) runnable() bool {
	if p.loading || p.validating || p.err != nil || len(p.plan.Steps) == 0 ||
		len(p.checked) != len(p.plan.Steps) || len(p.args) != len(p.checked) {
		return false
	}
	for _, err := range p.stepErrs {
		if err != nil {
			return false
		}
	}
	return true
}

func (p *planState) memoryRefused() bool {
	var me *ai.MemoryError
	return errors.As(p.err, &me)
}

func (m Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		return m, nil
	case "enter":
		return m, m.startPlan(m.currentIdx(), m.input.Value(), false)
	}
	m.input.Update(msg)
	return m, nil
}

// defaultRequest is the request the model is asked and shown: an empty
// prompt means the default one, which the call says (ai.Call.Default,
// the plan stays local) rather than its text, so the same words typed
// are an ordinary request.
func defaultRequest(typed string) (request string, isDefault bool) {
	if strings.TrimSpace(typed) == "" {
		return ai.DefaultPrompt, true
	}
	return typed, false
}

// startPlan asks the model for a plan for row i. force skips the memory
// guard.
func (m *Model) startPlan(i int, typed string, force bool) tea.Cmd {
	if i < 0 {
		m.mode = modeList
		return nil
	}
	request, def := defaultRequest(typed)
	ctx, cancel := context.WithTimeout(context.Background(), aiTimeout)
	m.planSeq++
	seq := m.planSeq
	chunks := new(atomic.Int64)
	m.plan = &planState{idx: i, typed: typed, request: request, loading: true, cancel: cancel, started: time.Now(), chunks: chunks}
	m.mode = modePlan

	path, client, maxDiff, sem := m.rows[i].st.Path, m.opts.AI, m.opts.MaxDiff, m.sem
	call := ai.Call{
		Progress:        func(n int) { chunks.Store(int64(n)) },
		SkipMemoryCheck: force,
		Default:         def,
	}
	return func() tea.Msg {
		defer cancel()
		// git takes a slot (-jobs); the model call after it holds none,
		// so other git work goes on while the model thinks
		var desc string
		if err := withSlot(ctx, sem, func() {
			desc = gitx.Describe(ctx, gitx.ReadStatus(ctx, path), maxDiff)
		}); err != nil {
			return planMsg{seq: seq, err: err}
		}
		p, err := client.Propose(ctx, desc, request, call)
		return planMsg{seq: seq, plan: p, err: err}
	}
}

func (m *Model) handlePlanMsg(msg planMsg) tea.Cmd {
	p := m.plan
	if p == nil || msg.seq != m.planSeq {
		return nil // cancelled or superseded
	}
	p.loading = false
	p.plan, p.err = msg.plan, msg.err
	if p.err == nil && strings.TrimSpace(p.plan.Clone) != "" {
		// The request was to clone something: continue in the clone flow,
		// which searches, lets the user pick and confirms the command.
		query := p.plan.Clone
		m.plan = nil
		return m.openClone(query)
	}
	if p.err != nil {
		return nil
	}
	return m.validatePlan()
}

// validatePlan checks the plan's current steps against a fresh status in
// a command; until the answer is in, the plan cannot run.
func (m *Model) validatePlan() tea.Cmd {
	p := m.plan
	p.validating, p.args, p.stepErrs, p.checked = true, nil, nil, nil
	p.seen, p.notice = 0, ""
	p.valSeq++
	planSeq, valSeq := m.planSeq, p.valSeq
	st, sem := m.rows[p.idx].st, m.sem
	steps := slices.Clone(p.plan.Steps) // edits must not race the check
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
		defer cancel()
		var args [][]string
		var errs []error
		if err := withSlot(ctx, sem, func() {
			// fresh status: the commands shown must be the ones that will run now
			if fresh := gitx.ReadStatus(ctx, st.Path); fresh.Err == nil {
				st = fresh
			}
			args, errs = gitx.PlanArgs(ctx, st.Path, st, steps)
		}); err != nil {
			// no slot in time: every step unchecked, so the plan cannot run
			args, errs = make([][]string, len(steps)), make([]error, len(steps))
			for i := range errs {
				errs[i] = fmt.Errorf("not checked: %w", err)
			}
		}
		// The screen redacts URLs, so a step carrying credentials would run
		// something other than what was shown: it cannot run at all.
		for i, a := range args {
			if errs[i] == nil && forge.HasCredentials(strings.Join(a, "\n")) {
				errs[i] = errStepCredentials
			}
		}
		return planValidMsg{planSeq: planSeq, valSeq: valSeq, steps: steps, args: args, errs: errs, risky: st.Risky}
	}
}

// withSlot runs fn once it has a slot of sem (-jobs), like every other
// git operation, and gives the slot back right after; it returns
// ctx.Err() if ctx ends while waiting. fn must not wait for another
// slot. A nil sem (tests) runs fn at once.
func withSlot(ctx context.Context, sem chan struct{}, fn func()) error {
	if sem != nil {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	fn()
	return nil
}

func (m *Model) handlePlanValid(msg planValidMsg) {
	p := m.plan
	if p == nil || msg.planSeq != m.planSeq || msg.valSeq != p.valSeq {
		return // closed, regenerated or edited meanwhile
	}
	p.validating = false
	p.checked, p.args, p.stepErrs, p.risky = msg.steps, msg.args, msg.errs, msg.risky
}

func (m *Model) closePlan() {
	if m.plan != nil && m.plan.cancel != nil {
		m.plan.cancel()
	}
	m.plan = nil
	m.mode = modeList
}

func (m Model) handlePlanKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.plan
	switch lowerKey(msg) {
	case "esc", "n", "q":
		m.closePlan()
		m.flash = "plan discarded"
		return m, nil
	}
	if p.loading {
		return m, nil
	}

	switch lowerKey(msg) {
	case "up", "k", "down", "j", "pgup", "pgdown", "home":
		m.scrollPlan(lowerKey(msg))
		return m, nil
	}
	p.notice = ""
	switch lowerKey(msg) {
	case "r":
		return m, m.startPlan(p.idx, p.typed, false)
	case "f":
		if p.memoryRefused() {
			return m, m.startPlan(p.idx, p.typed, true)
		}
	case "e":
		for i, s := range p.plan.Steps {
			if s.Op == "commit" || s.Op == "stash" {
				subject, _, _ := strings.Cut(s.Message, "\n")
				p.editStep = i
				m.input.Prompt = "message ▸ "
				m.input.SetValue(subject)
				m.input.CursorEnd()
				m.mode = modeEditMsg
				return m, nil
			}
		}
	case "y": // only y: enter is too easy to press by accident
		if !p.runnable() {
			return m, nil
		}
		if !m.planSeen() {
			p.notice = "scroll down (↓/pgdn) through the whole plan before y"
			return m, nil
		}
		// exactly the steps that were checked, with their confirmed args
		steps, confirmed, ack := p.checked, p.args, p.risky
		cmd := m.startJob(p.idx, "ai", actionTimeout, func(ctx context.Context, st gitx.Status) (string, error) {
			// the plan view showed these config risks above y
			if err := checkRisks(ctx, st.Path, ack); err != nil {
				return "", err
			}
			return gitx.RunSteps(ctx, st.Path, steps, confirmed)
		})
		if cmd == nil {
			m.flash = "repository is busy, try again"
			return m, nil
		}
		m.closePlan()
		return m, cmd
	}
	return m, nil
}

// handleEditKey edits the subject line of a commit/stash message, keeping
// any body the model wrote.
func (m Model) handleEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.input.Prompt = "ask ▸ "
		m.mode = modePlan
		return m, nil
	case "enter":
		p := m.plan
		step := &p.plan.Steps[p.editStep]
		_, body, hasBody := strings.Cut(step.Message, "\n")
		step.Message = strings.TrimSpace(m.input.Value())
		if hasBody {
			step.Message += "\n" + body
		}
		m.input.Prompt = "ask ▸ "
		m.mode = modePlan
		return m, m.validatePlan()
	}
	m.input.Update(msg)
	return m, nil
}

// scrollPlan moves the plan by a line or a page. There is no jump to the
// end: every line passes the screen on the way down.
func (m *Model) scrollPlan(key string) {
	p := m.plan
	m.planSeen() // what was on screen before this key
	lines, help := m.planLines()
	room, off := m.planWindow(len(lines), help)
	switch key {
	case "up", "k":
		off--
	case "down", "j":
		off++
	case "pgup":
		off -= room
	case "pgdown":
		off += room
	case "home":
		off = 0
	}
	p.scroll = min(max(off, 0), max(len(lines)-room, 0))
	m.planSeen() // records what is on screen now
}

// planSeen reports whether every line of the plan has been on screen,
// counting what is shown now.
func (m *Model) planSeen() bool {
	p := m.plan
	lines, help := m.planLines()
	room, off := m.planWindow(len(lines), help)
	if off <= p.seen || off == 0 {
		p.seen = max(p.seen, off+room)
	}
	return p.seen >= len(lines)
}
