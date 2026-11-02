// Package ai asks a local Ollama model to turn a free-form request into a
// plan of allow-listed git steps. It never executes anything itself.
//
// Requests are guarded so a struggling machine recovers quickly: a memory
// check before loading a model, a watchdog that aborts when the model
// stops producing output, a hard deadline, and unloading the model after
// a failed request it loaded.
package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gl-repos/internal/safe"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"gl-repos/internal/forge"
	"gl-repos/internal/gitx"
)

// DefaultPrompt is used when the user submits an empty request.
const DefaultPrompt = "Commit all current changes with a good commit message."

// Client talks to an Ollama server. Zero durations use the defaults below.
type Client struct {
	URL    string // e.g. http://localhost:11434
	Model  string
	MaxCtx int // upper bound for num_ctx; 0 means 32768

	Timeout      time.Duration // whole request
	LoadTimeout  time.Duration // until the first token (covers model load)
	StallTimeout time.Duration // max gap between tokens once streaming
	KeepAlive    string        // how long Ollama keeps a model we loaded, e.g. "2m"
	MemoryGuard  bool          // refuse to load a model that won't fit in free RAM

	HTTP *http.Client
}

const (
	minCtx     = 8192
	maxPredict = 1024 // a plan is short; cap runaway generations

	defaultTimeout      = 2 * time.Minute
	defaultLoadTimeout  = 90 * time.Second
	defaultStallTimeout = 20 * time.Second
	defaultKeepAlive    = "2m"
)

// Call carries per-request options.
type Call struct {
	// Progress, if set, is called with the number of chunks received so
	// far. It runs on the request goroutine and must be cheap.
	Progress func(chunks int)
	// SkipMemoryCheck bypasses the memory guard ("force").
	SkipMemoryCheck bool
	// Default says the request is the default one (an empty prompt, for
	// which the caller may already have put in DefaultPrompt): the plan
	// then stays local (localOnly). A typed request is never taken for
	// it by its text, so typing the default's words asks for exactly
	// that, pushes included.
	Default bool
}

// numCtx sizes the context window to the prompt (≈3.5 chars per token,
// plus room for the reply), rounded up to a power of two and clamped.
// A fixed large window wastes memory: Ollama allocates the KV cache for
// the full num_ctx up front.
func numCtx(chars, limit int) int {
	if limit <= 0 {
		limit = 32768
	}
	need := int(float64(chars)/3.5) + maxPredict
	n := minCtx
	for n < need {
		n <<= 1
	}
	return min(n, max(limit, minCtx))
}

// Plan is the model's proposal.
type Plan struct {
	Summary string `json:"summary"`
	// Clone is set instead of steps when the user wants a repository
	// cloned: the project name, path or URL to look up.
	Clone string      `json:"clone"`
	Steps []gitx.Step `json:"steps"`
}

func systemPrompt() string {
	var ops strings.Builder
	for _, o := range gitx.StepOps {
		fmt.Fprintf(&ops, "- %s: %s\n", o.Op, o.Doc)
	}
	return `You are a git assistant inside a terminal repository dashboard.
Given the state of one repository and the user's request, reply with a JSON
plan. First write "summary" (what the plan does), then "clone", then
"steps" that carry it out completely, in order.

"clone" is only for requests to clone, download or check out a repository
from GitLab. Then set it to what identifies the project, as the user said
it (a URL, a group/project path, or name words; leave out filler like
"the", "project", "repo", "from gitlab"), and return no steps. For every
other request clone is "".

Every step has all five fields. Use "", [] or false for fields that do not
apply to that op:
- op: one of the operations below
- branch: branch name, required for create_branch and switch
- paths: file paths, required for stage and unstage
- include_untracked: true to also stash untracked files (stash only)
- message: commit message (commit) or stash description (stash)

Operations:
` + ops.String() + `
Rules:
- The plan must do everything the summary says. A request to commit must end with a commit step.
- A commit only includes staged changes: stage first (stage_all, or stage with paths) unless the user wants only what is already staged.
- Commit messages: imperative subject line of at most 72 characters that says what changed and why it matters, matching the style of recent commits; add a short body after a blank line only when the reason is not obvious from the subject. Base it on the actual diff.
- Never push, pull, fetch or switch branches unless the request asks for it in words (push, send, upload, publish; pull, update; fetch; switch, check out). A request to commit or stash is not a request to push: it never gets a push step.
- If the working tree is clean there is nothing to commit or stash: a request to commit or stash then gets no steps, and the summary says so.
- Push, pull and fetch do not depend on the working tree. When the user asks to push, add a push step if Sync says commits are not pushed yet or the branch has no upstream; after a commit step in the same plan, the new commit is pushed too.
- Destructive operations (reset, clean, force push, deleting branches, discarding changes) do not exist. If asked for one, return no steps and say so in summary.

Examples:

Request: ` + DefaultPrompt + `
{"summary":"Stage all changes and commit them.","clone":"","steps":[` +
		`{"op":"stage_all","branch":"","paths":[],"include_untracked":false,"message":""},` +
		`{"op":"commit","branch":"","paths":[],"include_untracked":false,"message":"Add input validation to the parser"}]}

Request: push my commits
{"summary":"Push the 2 local commits to origin/main.","clone":"","steps":[` +
		`{"op":"push","branch":"","paths":[],"include_untracked":false,"message":""}]}

Request: stash all my changes including new files as experiment
{"summary":"Stash tracked and untracked changes as 'experiment'.","clone":"","steps":[` +
		`{"op":"stash","branch":"","paths":[],"include_untracked":true,"message":"experiment"}]}

Request: make a branch fix/login and commit only auth.go there
{"summary":"Create fix/login, stage auth.go and commit it there.","clone":"","steps":[` +
		`{"op":"create_branch","branch":"fix/login","paths":[],"include_untracked":false,"message":""},` +
		`{"op":"stage","branch":"","paths":["auth.go"],"include_untracked":false,"message":""},` +
		`{"op":"commit","branch":"","paths":[],"include_untracked":false,"message":"Fix session check in login handler"}]}

Request: throw away my changes and reset to origin
{"summary":"Discarding changes and resetting are destructive and not supported; nothing will be done.","clone":"","steps":[]}

Request: please clone the billing service repo from gitlab
{"summary":"Clone the billing service project.","clone":"billing service","steps":[]}`
}

// planSchema constrains Ollama's structured output. It is built as raw
// JSON because property order matters to the model: the summary comes
// first so it plans before it acts, and every step field is required so
// none gets silently dropped.
func planSchema() json.RawMessage {
	ops := make([]string, len(gitx.StepOps))
	for i, o := range gitx.StepOps {
		ops[i] = o.Op
	}
	enum, _ := json.Marshal(ops)
	return json.RawMessage(`{"type":"object","properties":{` +
		`"summary":{"type":"string"},` +
		`"clone":{"type":"string"},` +
		`"steps":{"type":"array","items":{"type":"object","properties":{` +
		`"op":{"type":"string","enum":` + string(enum) + `},` +
		`"branch":{"type":"string"},` +
		`"paths":{"type":"array","items":{"type":"string"}},` +
		`"include_untracked":{"type":"boolean"},` +
		`"message":{"type":"string"}},` +
		`"required":["op","branch","paths","include_untracked","message"]}}},` +
		`"required":["summary","clone","steps"]}`)
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string          `json:"model"`
	Messages  []chatMessage   `json:"messages"`
	Stream    bool            `json:"stream"`
	Think     *bool           `json:"think,omitempty"`
	Format    json.RawMessage `json:"format"`
	Options   map[string]any  `json:"options,omitempty"`
	KeepAlive string          `json:"keep_alive,omitempty"`
}

type chatChunk struct {
	Message chatMessage `json:"message"`
	Done    bool        `json:"done"`
	Error   string      `json:"error"`
}

var thinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

// errRetryWithoutThink signals a 400, which older Ollama returns for the
// unknown "think" field.
var errRetryWithoutThink = errors.New("retry without think")

// Propose asks the model for a plan. repo is the output of gitx.Describe.
// An empty request is the default one (DefaultPrompt, call.Default).
func (c Client) Propose(ctx context.Context, repo, request string, call Call) (Plan, error) {
	if strings.TrimSpace(request) == "" {
		request, call.Default = DefaultPrompt, true
	}
	p, err := c.propose(ctx, repo, request, call)
	if err == nil && call.Default {
		p = localOnly(p)
	}
	return p, err
}

// localOnly drops push, pull and fetch steps and a clone: the default
// request (enter on an empty prompt) asks to commit, never to touch the
// remote, and small models sometimes add a push anyway.
func localOnly(p Plan) Plan {
	steps := p.Steps[:0:0]
	for _, s := range p.Steps {
		if s.Op != "push" && s.Op != "pull" && s.Op != "fetch" {
			steps = append(steps, s)
		}
	}
	if len(steps) < len(p.Steps) {
		p.Summary = strings.TrimSpace(p.Summary + " (Left out: push, pull and fetch were not asked for.)")
	}
	if p.Clone != "" {
		p.Summary = strings.TrimSpace(p.Summary + " (Left out: a clone was not asked for.)")
	}
	p.Steps, p.Clone = steps, ""
	return p
}

func (c Client) propose(ctx context.Context, repo, request string, call Call) (Plan, error) {
	timeout := or(c.Timeout, defaultTimeout)
	ctx, cancel := context.WithTimeoutCause(ctx, timeout,
		fmt.Errorf("gave up after %s (ai.timeout_seconds)", timeout))
	defer cancel()

	// Was the model already resident (e.g. used by another tool)? Then we
	// neither change its keep_alive nor unload it on failure.
	loaded, err := c.loaded(ctx)
	if err != nil {
		return Plan{}, err
	}
	if !loaded && c.MemoryGuard && !call.SkipMemoryCheck {
		if err := c.checkMemory(ctx); err != nil {
			return Plan{}, err
		}
	}

	msgs := []chatMessage{
		{Role: "system", Content: systemPrompt()},
		{Role: "user", Content: "Repository state:\n" + repo + "\n\nRequest: " + request},
	}
	no := false
	cr := chatRequest{
		Model:    c.Model,
		Messages: msgs,
		Stream:   true,
		// Thinking models (gemma4, qwen3) would otherwise spend the budget
		// on hidden reasoning and may return empty content.
		Think:  &no,
		Format: planSchema(),
		Options: map[string]any{
			"temperature": 0.2,
			"num_ctx":     numCtx(len(msgs[0].Content)+len(msgs[1].Content), c.MaxCtx),
			"num_predict": maxPredict,
		},
	}
	if !loaded {
		cr.KeepAlive = c.KeepAlive
		if cr.KeepAlive == "" {
			cr.KeepAlive = defaultKeepAlive
		}
	}

	content, err := c.chat(ctx, cr, call.Progress)
	if errors.Is(err, errRetryWithoutThink) {
		cr.Think = nil
		content, err = c.chat(ctx, cr, call.Progress)
	}
	if err != nil {
		if !loaded {
			c.unloadAsync()
		}
		return Plan{}, err
	}

	if !loaded {
		_, _ = c.loaded(ctx) // record the resident size for future memory checks
	}

	p, err := parsePlan(content)
	if err != nil {
		if !loaded {
			c.unloadAsync()
		}
		return Plan{}, err
	}
	if op, note := missingStep(p); op != "" {
		// Small models sometimes end the steps early ("Stage x and
		// commit it." with only the stage step, or "… and push" without
		// the push). Ask once more with the mistake pointed out; keep
		// the first answer if that fails too.
		cr.Messages = slices.Concat(msgs, []chatMessage{
			{Role: "assistant", Content: content},
			{Role: "user", Content: note}})
		cr.Options["num_ctx"] = numCtx(len(msgs[0].Content)+len(msgs[1].Content)+len(content)+len(note), c.MaxCtx)
		if again, err := c.chat(ctx, cr, call.Progress); err == nil {
			if q, err := parsePlan(again); err == nil && hasOp(q, op) {
				if next, _ := missingStep(q); next == "" {
					p = q
				}
			}
		}
	}
	return p, nil
}

const (
	missingCommitNote = "Your summary says the changes are committed, but the steps have no commit step. " +
		"Return the complete plan again, ending with the commit step."
	missingPushNote = "Your summary says the work is pushed, but the steps have no push step. " +
		"Return the complete plan again, ending with the push step."
)

// missingStep reports a plan whose summary promises a commit or a push
// that its steps leave out: the op to ask for again and the note saying
// so, or "" when the plan is complete.
func missingStep(p Plan) (op, note string) {
	switch {
	case missingCommit(p):
		return "commit", missingCommitNote
	case len(p.Steps) > 0 && !hasOp(p, "push") && pushWord.MatchString(p.Summary):
		return "push", missingPushNote
	}
	return "", ""
}

// parsePlan decodes the model's answer. The model reads diffs and commit
// messages anyone could have written: its words are untrusted text. Step
// fields are validated (and refused if unsafe) by gitx.Step.Args.
func parsePlan(content string) (Plan, error) {
	content = strings.TrimSpace(thinkBlock.ReplaceAllString(content, ""))
	var p Plan
	if err := json.Unmarshal([]byte(content), &p); err != nil {
		return Plan{}, fmt.Errorf("model returned invalid plan: %w: %s", err, quoted(content))
	}
	p.Summary, p.Clone = safe.Text(p.Summary), safe.Line(p.Clone)
	for i := range p.Steps {
		p.Steps[i].Op = safe.Line(p.Steps[i].Op)
	}
	return p, nil
}

// quoted is the start of untrusted text (a model answer, a server reply)
// for an error message: redacted first, then cut. A cut alone could
// leave part of a credential, which the display would not find.
func quoted(s string) string { return fmt.Sprintf("%.200s", forge.RedactURLs(s)) }

var (
	commitWord = regexp.MustCompile(`(?i)\bcommit(s|ted|ting)?\b`)
	pushWord   = regexp.MustCompile(`(?i)\bpush(es|ed|ing)?\b`)
)

// missingCommit reports a plan that stages changes and says it commits
// them, but has no commit step (and is not a stash plan).
func missingCommit(p Plan) bool {
	return (hasOp(p, "stage") || hasOp(p, "stage_all")) && !hasCommit(p) && !hasOp(p, "stash") &&
		commitWord.MatchString(p.Summary)
}

func hasCommit(p Plan) bool { return hasOp(p, "commit") }

func hasOp(p Plan, op string) bool {
	for _, s := range p.Steps {
		if s.Op == op {
			return true
		}
	}
	return false
}

// chat streams one request under a watchdog: the first chunk must arrive
// within LoadTimeout and later chunks within StallTimeout of each other.
// Cancelling closes the connection, which makes Ollama abort the request.
// maxAnswer caps a streamed answer; plans are a few hundred bytes.
const maxAnswer = 256 << 10

func (c Client) chat(ctx context.Context, cr chatRequest, progress func(int)) (string, error) {
	body, err := json.Marshal(cr)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	load, stall := or(c.LoadTimeout, defaultLoadTimeout), or(c.StallTimeout, defaultStallTimeout)
	var streaming atomic.Bool
	watchdog := time.AfterFunc(load, func() {
		if streaming.Load() {
			cancel(fmt.Errorf("model stalled: no output for %s (ai.stall_timeout_seconds)", stall))
			return
		}
		cancel(fmt.Errorf("no response within %s: model load or prompt processing too slow "+
			"(ai.load_timeout_seconds; try a smaller model)", load))
	})
	defer watchdog.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/api/chat"), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return "", c.wrapErr(ctx, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest && cr.Think != nil {
		return "", errRetryWithoutThink
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var ch chatChunk
		if json.Unmarshal(raw, &ch) == nil && ch.Error != "" {
			return "", fmt.Errorf("ollama: %s", ch.Error)
		}
		return "", fmt.Errorf("ollama: %s: %s", resp.Status, quoted(string(raw)))
	}

	var out strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	chunks := 0
	for sc.Scan() {
		var ch chatChunk
		if err := json.Unmarshal(sc.Bytes(), &ch); err != nil {
			return "", fmt.Errorf("ollama: bad stream chunk: %s", quoted(string(sc.Bytes())))
		}
		if ch.Error != "" {
			return "", fmt.Errorf("ollama: %s", ch.Error)
		}
		out.WriteString(ch.Message.Content)
		if out.Len() > maxAnswer {
			return "", fmt.Errorf("ollama: answer longer than %d bytes, aborted", maxAnswer)
		}
		chunks++
		// From the first chunk on, the watchdog measures gaps between chunks.
		streaming.Store(true)
		watchdog.Reset(stall)
		if progress != nil {
			progress(chunks)
		}
		if ch.Done {
			return out.String(), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", c.wrapErr(ctx, err)
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	return "", errors.New("ollama: stream ended before the response was complete")
}

// wrapErr prefers the watchdog/deadline reason over the transport error.
func (c Client) wrapErr(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return errors.New("cancelled")
		}
		return cause
	}
	return fmt.Errorf("ollama unreachable at %s (is it running?): %w", c.URL, err)
}

func (c Client) endpoint(path string) string {
	return strings.TrimRight(c.URL, "/") + path
}

// httpClient is gl-repos' own client for Ollama, so nothing else in the
// process that changes http.DefaultClient (or its transport) affects
// it. It has no Timeout: every request is bounded by its context (the
// load, stall and total timeouts above, and esc).
var httpClient = newHTTPClient()

// newHTTPClient clones the default transport; if something replaced it
// with another RoundTripper, it falls back to a fresh Transport rather
// than panic. It follows no redirect: a 307/308 would re-send the
// request, the repository's diff and commit subjects in it, wherever
// the server says.
func newHTTPClient() *http.Client {
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		c.Transport = t.Clone()
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errRedirect }
	return c
}

var errRedirect = errors.New("redirect refused: ai.url must be the Ollama server itself")

func (c Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return httpClient
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}
