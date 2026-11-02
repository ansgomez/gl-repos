package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gl-repos/internal/gitx"
)

// A repository whose own git config names programs (gitx.Status.Risky)
// is listed with a red "!" and the keys in the details. Reads by gl-repos
// never run them, but fetch, pull, push, AI steps, publish and opening a
// tool, agent or shell there would: those ask for y first, naming the
// keys, and re-check right before they run.

const riskMark = "!"

// riskText describes the keys of a risky repository.
func riskText(keys []string) string {
	return "its own git config runs programs (" + strings.Join(keys, ", ") + ")"
}

// shownRisks is the one form a list of risky keys has in the TUI, the
// shown one: redacted, since a key may hold a credential
// ("credential.https://oauth2:<token>@host.helper"). scrub stores every
// list in this form (a status, publishInfoMsg, planValidMsg), and the
// confirmed keys are such a list, so checkRisks puts the fresh keys in
// the same form before it compares: what the user confirmed is what
// was shown, and a key whose only difference is a hidden secret is the
// same confirmation. A comparison with raw keys would refuse every
// confirmation of such a key.
func shownRisks(keys []string) []string { return scrubAll(keys) }

// checkRisks reads dir's config again just before something runs there:
// it fails if the config names programs that were not confirmed (ack,
// a shownRisks list).
func checkRisks(ctx context.Context, dir string, ack []string) error {
	risky := shownRisks(gitx.ConfigRisks(ctx, dir))
	for _, k := range risky {
		if !slices.Contains(ack, k) {
			return fmt.Errorf("not run: %s; check it, then confirm with y", riskText(risky))
		}
	}
	return nil
}

// riskyRows returns the names of the rows among idx whose config is known
// to run programs.
func (m Model) riskyRows(idx []int) []string {
	var names []string
	for _, i := range idx {
		if r := m.rows[i]; len(r.st.Risky) > 0 {
			names = append(names, r.rel)
		}
	}
	return names
}

// ackRisks snapshots the risky keys of rows idx as the question shows
// them: a y covers those, not a key a status read lists while the
// question is on screen (checkRisks refuses it, as any key added later).
func (m Model) ackRisks(idx []int) map[int][]string {
	ack := make(map[int][]string, len(idx))
	for _, i := range idx {
		ack[i] = slices.Clone(m.rows[i].st.Risky)
	}
	return ack
}

// confirmText is the question of the list's confirm line.
func (m Model) confirmText() string {
	p := m.confirm
	q := fmt.Sprintf("%s %d repo(s)? [y/N]", capitalize(p.act.String()), len(p.targets))
	if p.exec != nil {
		q = fmt.Sprintf("Open %s in %s? [y/N]", p.exec[0], m.rows[p.targets[0]].rel)
	}
	if len(p.risky) == 0 {
		return q
	}
	keys := ""
	if len(p.targets) == 1 {
		keys = " (" + strings.Join(m.rows[p.targets[0]].st.Risky, ", ") + ")"
	}
	return q + " " + riskMark + " own git config runs programs" + keys + ": " + strings.Join(p.risky, ", ")
}
