package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"gl-repos/internal/ai"
	"gl-repos/internal/gitx"
)

// planModel shows a checked, runnable plan of steps at width × height.
func planModel(t *testing.T, width, height int, steps ...gitx.Step) Model {
	t.Helper()
	m := New(Options{Root: "/w", Repos: []string{"/nonexistent/w/a"}})
	t.Cleanup(m.life.cancel)
	m.width, m.height = width, height
	m.rows[0].loaded = true
	m.mode = modePlan
	p := &planState{idx: 0, request: "tidy up", plan: ai.Plan{Summary: "Stage and commit the docs", Steps: steps},
		checked: steps}
	for _, st := range steps {
		args, err := st.Args(gitx.Status{Branch: "main"})
		if err != nil {
			t.Fatal(err)
		}
		p.args = append(p.args, args)
		p.stepErrs = append(p.stepErrs, nil)
	}
	m.plan = p
	return m
}

// screen renders the plan as the terminal shows it, failing on any line
// wider than the screen or a screen taller than it.
func screen(t *testing.T, m Model) []string {
	t.Helper()
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) > m.height {
		t.Fatalf("%d lines on a %d-line screen: the top would be cut:\n%s", len(lines), m.height, strings.Join(lines, "\n"))
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > m.width {
			t.Fatalf("line wider than %d: %q", m.width, l)
		}
	}
	return lines
}

func longStage() gitx.Step {
	var paths []string
	for i := range 12 {
		paths = append(paths, fmt.Sprintf("docs/chapter-%c.md", 'a'+i))
	}
	return gitx.Step{Op: "stage", Paths: append(paths, ".env")}
}

// unwrap joins the plan's step lines back into commands: continuation
// lines are indented by four spaces.
func unwrap(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		if strings.HasPrefix(l, "    ") && !strings.HasPrefix(l, "      │") {
			b.WriteString(strings.TrimPrefix(l, "    "))
		} else {
			b.WriteString("\n" + l)
		}
	}
	return b.String()
}

func TestPlanStepsWrapNotCut(t *testing.T) {
	m := planModel(t, 80, 24, longStage(), gitx.Step{Op: "commit",
		Message: "Document every chapter of the guide including the appendix and the glossary at the end\n\n" +
			"A body line that is much longer than eighty columns and therefore has to wrap onto the next line."})
	out := unwrap(screen(t, m))
	args, _ := longStage().Args(gitx.Status{})
	if want := gitx.CommandLine(args); !strings.Contains(out, want) {
		t.Errorf("stage command not shown in full (%s):\n%s", want, out)
	}
	body := strings.ReplaceAll(ansi.Strip(m.View()), "\n        ", "") // message continuation lines
	if !strings.Contains(out, ".env") || !strings.Contains(body, "appendix and the glossary at the end") ||
		!strings.Contains(body, "has to wrap onto the next line.") {
		t.Errorf("message not shown in full:\n%s", ansi.Strip(m.View()))
	}
	if next, cmd := m.handlePlanKey(keyY); cmd == nil || next.(Model).plan != nil {
		t.Error("y did not run a plan that fits on the screen")
	}
}

func TestPlanScrollsAndRunsOnlyWhenSeen(t *testing.T) {
	steps := []gitx.Step{longStage()}
	for i := range 8 {
		steps = append(steps, gitx.Step{Op: "stage", Paths: []string{fmt.Sprintf("file-%d.txt", i)}})
	}
	steps = append(steps, gitx.Step{Op: "commit", Message: "Last step"})
	for _, size := range [][2]int{{80, 8}, {80, 12}, {50, 10}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := planModel(t, size[0], size[1], steps...)
			seen := map[string]bool{}
			look := func() {
				for _, l := range screen(t, m) {
					seen[l] = true
				}
			}
			look()
			next, cmd := m.handlePlanKey(keyY)
			m = next.(Model)
			if cmd != nil || m.plan == nil || !strings.Contains(ansi.Strip(m.View()), "scroll down") {
				t.Fatalf("y ran a plan whose end was never on screen:\n%s", ansi.Strip(m.View()))
			}
			for range 40 {
				next, _ = m.handlePlanKey(tea.KeyMsg{Type: tea.KeyDown})
				m = next.(Model)
				look()
			}
			var all []string
			for l := range seen {
				all = append(all, l)
			}
			for _, want := range []string{"git commit -m 'Last step'", "file-7.txt", ".env"} {
				if !strings.Contains(strings.Join(all, "\n"), want) {
					t.Errorf("%q never shown while scrolling", want)
				}
			}
			if _, cmd := m.handlePlanKey(keyY); cmd == nil {
				t.Errorf("y after scrolling to the end did not run:\n%s", ansi.Strip(m.View()))
			}
		})
	}
}

func TestPlanPageDownCannotSkipLines(t *testing.T) {
	steps := []gitx.Step{}
	for i := range 30 {
		steps = append(steps, gitx.Step{Op: "stage", Paths: []string{fmt.Sprintf("f%02d", i)}})
	}
	m := planModel(t, 80, 10, steps...)
	shown := map[string]bool{}
	for range 20 {
		for _, l := range screen(t, m) {
			shown[strings.TrimSpace(l)] = true
		}
		next, _ := m.handlePlanKey(tea.KeyMsg{Type: tea.KeyPgDown})
		m = next.(Model)
	}
	for i := range 30 {
		if want := fmt.Sprintf("%2d. git add -- f%02d", i+1, i); !shown[strings.TrimSpace(want)] {
			t.Errorf("step %d never on screen", i+1)
		}
	}
	if _, cmd := m.handlePlanKey(keyY); cmd == nil {
		t.Error("y after paging through did not run")
	}
}

func TestPlanShowsConfigRisk(t *testing.T) {
	m := planModel(t, 80, 24, gitx.Step{Op: "fetch"})
	m.plan.risky = []string{"filter.x.clean", "core.sshcommand"}
	out := strings.ReplaceAll(ansi.Strip(m.View()), "\n", "")
	if !strings.Contains(out, "its own git config runs programs (filter.x.clean, core.sshcommand)") {
		t.Errorf("risk not shown:\n%s", out)
	}
}
