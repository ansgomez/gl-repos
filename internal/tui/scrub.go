package tui

import (
	"slices"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

// scrub hides URL credentials (forge.RedactURLs) in the text a
// background job hands to Update: git, CLI and model output, errors,
// server descriptions. It runs before anything is stored or wrapped
// (wrapping can split a URL), so a view never needs to find a secret
// again. Values that are executed later (plan steps, the AI's clone
// query, project URLs and paths, the user name) are left alone: those
// are checked and refused instead (a redacted copy would run).
func scrub(msg tea.Msg) tea.Msg {
	switch m := msg.(type) {
	case statusMsg:
		m.st = scrubStatus(m.st)
		return m
	case actionMsg:
		m.out, m.err = scrubOut(m.out), scrubErr(m.err)
		return m
	case execDoneMsg:
		m.err = scrubErr(m.err)
		return m
	case graphMsg:
		m.out, m.err = forge.RedactColored(m.out), scrubErr(m.err)
		return m
	case cloneJobDoneMsg:
		m.out, m.err = scrubOut(m.out), scrubErr(m.err)
		return m
	case cloneSearchMsg:
		m.results, m.err = scrubProjects(m.results), scrubErr(m.err)
		return m
	case projectsMsg:
		m.projects, m.groups, m.err = scrubProjects(m.projects), scrubProjects(m.groups), scrubErr(m.err)
		return m
	case groupProjectsMsg:
		m.projects, m.err = scrubProjects(m.projects), scrubErr(m.err)
		return m
	case groupPlanMsg:
		m.err = scrubErr(m.err)
		return m
	case createGroupsMsg:
		m.groups, m.err = scrubProjects(m.groups), scrubErr(m.err)
		return m
	case whoamiMsg:
		m.err = scrubErr(m.err)
		return m
	case groupCheckMsg:
		m.err = scrubErr(m.err)
		return m
	case publishInfoMsg:
		m.risky, m.err = shownRisks(m.risky), scrubErr(m.err)
		return m
	case createDoneMsg:
		m.out, m.failed, m.err = scrubOut(m.out), scrubOut(m.failed), scrubErr(m.err)
		return m
	case planMsg:
		m.plan.Summary, m.err = forge.RedactURLs(m.plan.Summary), scrubErr(m.err)
		return m
	case planValidMsg:
		errs := make([]error, len(m.errs))
		for i, err := range m.errs {
			errs[i] = scrubErr(err)
		}
		m.errs, m.risky = errs, shownRisks(m.risky)
		return m
	}
	return msg
}

// maxOutLine bounds a line of stored command output, in bytes: far wider
// than any terminal, so the details show the same, and a single line as
// long as forge.MaxOutput is not redacted and cleaned on every frame.
const maxOutLine = 4096

// scrubOut redacts command output and cuts each line to maxOutLine, at a
// rune boundary. Redaction comes first: a cut alone could leave part of
// a credential.
func scrubOut(out string) string {
	out = forge.RedactURLs(out)
	if len(out) <= maxOutLine {
		return out
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if len(l) > maxOutLine {
			n := maxOutLine
			for n > 0 && !utf8.RuneStart(l[n]) {
				n--
			}
			lines[i] = l[:n]
		}
	}
	return strings.Join(lines, "\n")
}

// scrubStatus redacts what a status shows: Origin is only shown and
// compared by host (which redaction keeps); the branch, upstream and
// default branch are only shown and compared with each other (a valid
// ref name may hold "#access_token=…" or ";private_token=…"; git never
// gets them from a Status, it pushes HEAD, and gitx refuses an argument
// shaped so); the risky keys are compared with fresh ones in the same
// redacted form (shownRisks). Any other use of a redacted field for a
// decision would be the scrub-vs-execution mismatch: keep such fields
// raw (Path, the step branches, project URLs) and refuse them instead.
func scrubStatus(st gitx.Status) gitx.Status {
	st.Origin = forge.RedactURLs(st.Origin)
	st.Branch, st.Upstream, st.DefaultBranch = forge.RedactLine(st.Branch), forge.RedactLine(st.Upstream), forge.RedactLine(st.DefaultBranch)
	st.Risky = shownRisks(st.Risky)
	st.Err = scrubErr(st.Err)
	return st
}

// scrubProjects redacts project descriptions (a copy: the slice may be
// shared with a cache).
func scrubProjects(ps []forge.Project) []forge.Project {
	if ps == nil {
		return nil
	}
	ps = slices.Clone(ps)
	for i := range ps {
		ps[i].Description = forge.RedactURLs(ps[i].Description)
	}
	return ps
}

func scrubAll(ss []string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = forge.RedactURLs(s)
	}
	return out
}

// scrubbed is an error whose text was redacted; errors.Is and As still
// see the original.
type scrubbed struct {
	msg string
	err error
}

func (e scrubbed) Error() string { return e.msg }
func (e scrubbed) Unwrap() error { return e.err }

// scrubErr redacts err's text, keeping err when there is nothing to hide.
func scrubErr(err error) error {
	if err == nil {
		return nil
	}
	if s := forge.RedactURLs(err.Error()); s != err.Error() {
		return scrubbed{s, err}
	}
	return err
}
