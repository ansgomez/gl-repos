package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const planJSON = `{"summary":"Commit the fix.","steps":[{"op":"stage_all"},{"op":"commit","message":"Fix typo in README"}]}`

// fakeOllama serves /api/ps, /api/tags and /api/generate (unload), and
// delegates /api/chat to chat.
type fakeOllama struct {
	loaded   []string
	sizes    map[string]uint64 // resident sizes /api/ps reports, by name
	size     uint64
	chat     http.HandlerFunc
	unloads  atomic.Int32
	lastChat chatRequest
}

func (f *fakeOllama) start(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ps":
			var ms []map[string]any
			for _, n := range f.loaded {
				ms = append(ms, map[string]any{"name": n, "size": f.sizes[n]})
			}
			json.NewEncoder(w).Encode(map[string]any{"models": ms})
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "m:latest", "size": f.size}}})
		case "/api/generate":
			f.unloads.Add(1)
		case "/api/chat":
			// Buffer the body so handlers can decode it, and so the server
			// notices client disconnects (it only watches the connection
			// once the body has been read).
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			f.chat(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// streamChunks writes content as NDJSON chunks.
func streamChunks(w http.ResponseWriter, content string, parts int) {
	fl := w.(http.Flusher)
	n := len(content) / parts
	for i := 0; i < parts; i++ {
		piece := content[i*n:]
		if i < parts-1 {
			piece = content[i*n : (i+1)*n]
		}
		json.NewEncoder(w).Encode(chatChunk{Message: chatMessage{Content: piece}, Done: i == parts-1})
		fl.Flush()
	}
}

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "gl-repos-test")
	sizeCachePath = func() string { return filepath.Join(dir, "sizes.json") }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func withMemory(t *testing.T, avail uint64) {
	old := availableMemory
	availableMemory = func() (uint64, bool) { return avail, true }
	t.Cleanup(func() { availableMemory = old })
}

func TestProposeStreams(t *testing.T) {
	f := &fakeOllama{size: 1 << 30}
	f.chat = func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&f.lastChat)
		streamChunks(w, "<think>hmm</think>"+planJSON, 5)
	}
	srv := f.start(t)
	withMemory(t, 8<<30)

	var progress int
	c := Client{URL: srv.URL, Model: "m", MemoryGuard: true}
	p, err := c.Propose(context.Background(), "Branch: main", "", Call{Progress: func(n int) { progress = n }})
	if err != nil {
		t.Fatal(err)
	}
	got := f.lastChat
	if got.Model != "m" || !got.Stream || got.Think == nil || *got.Think || got.Format == nil {
		t.Errorf("request = %+v", got)
	}
	if got.KeepAlive != defaultKeepAlive {
		t.Errorf("keep_alive = %q, want %q for a model we load", got.KeepAlive, defaultKeepAlive)
	}
	if !strings.Contains(got.Messages[1].Content, DefaultPrompt) {
		t.Errorf("empty request should use default prompt")
	}
	if len(p.Steps) != 2 || p.Steps[1].Message != "Fix typo in README" {
		t.Errorf("plan = %+v", p)
	}
	if progress != 5 {
		t.Errorf("progress = %d, want 5", progress)
	}
}

func TestProposeKeepsResidentModel(t *testing.T) {
	f := &fakeOllama{loaded: []string{"m:latest"}, size: 100 << 30}
	f.chat = func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&f.lastChat)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}
	srv := f.start(t)
	withMemory(t, 1<<30) // would fail the guard, but the model is already loaded

	_, err := Client{URL: srv.URL, Model: "m", MemoryGuard: true}.Propose(context.Background(), "", "x", Call{})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if f.lastChat.KeepAlive != "" {
		t.Errorf("must not change keep_alive of a resident model")
	}
	time.Sleep(200 * time.Millisecond) // an unload would be sent right away
	if f.unloads.Load() != 0 {
		t.Errorf("must not unload a model another tool loaded")
	}
}

func TestMemoryGuard(t *testing.T) {
	f := &fakeOllama{size: 9 << 30}
	f.chat = func(w http.ResponseWriter, r *http.Request) { streamChunks(w, planJSON, 1) }
	srv := f.start(t)
	withMemory(t, 6<<30)

	c := Client{URL: srv.URL, Model: "m", MemoryGuard: true}
	_, err := c.Propose(context.Background(), "", "x", Call{})
	var me *MemoryError
	if !errors.As(err, &me) {
		t.Fatalf("err = %v, want MemoryError", err)
	}
	if _, err := c.Propose(context.Background(), "", "x", Call{SkipMemoryCheck: true}); err != nil {
		t.Errorf("forced call failed: %v", err)
	}

	// Once the model has been seen resident at 3 GB, that size is used
	// instead of the 9 GB file size and the guard lets it through.
	rememberSize("m:latest", 3<<30)
	t.Cleanup(func() { os.Remove(sizeCachePath()) })
	if _, err := c.Propose(context.Background(), "", "x", Call{}); err != nil {
		t.Errorf("with remembered size: %v", err)
	}
}

// A size reported near the uint64 limit (a hostile server or cache) must
// not wrap the sum of weights and allowance to a small number that fits.
func TestMemoryGuardHugeSize(t *testing.T) {
	var huge uint64 = 16769767339247891550 // huge + huge/10 + 512<<20 wraps to 1
	if sum := huge + huge/10 + 512<<20; sum != 1 {
		t.Fatalf("setup: sum wraps to %d, want 1", sum)
	}
	f := &fakeOllama{size: huge}
	f.chat = func(w http.ResponseWriter, r *http.Request) { streamChunks(w, planJSON, 1) }
	srv := f.start(t)
	withMemory(t, 6<<30)

	_, err := Client{URL: srv.URL, Model: "m", MemoryGuard: true}.Propose(context.Background(), "", "x", Call{})
	var me *MemoryError
	if !errors.As(err, &me) || me.Need != huge {
		t.Fatalf("err = %v, want MemoryError needing %d", err, huge)
	}
}

func TestLoadTimeoutUnloads(t *testing.T) {
	f := &fakeOllama{size: 1 << 30}
	f.chat = func(w http.ResponseWriter, r *http.Request) {
		select { // simulate a model that never finishes loading
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	srv := f.start(t)

	start := time.Now()
	c := Client{URL: srv.URL, Model: "m", LoadTimeout: 200 * time.Millisecond}
	_, err := c.Propose(context.Background(), "", "x", Call{})
	if err == nil || !strings.Contains(err.Error(), "no response within") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s, watchdog did not fire promptly", time.Since(start))
	}
	for deadline := time.Now().Add(3 * time.Second); f.unloads.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond) // the unload is sent asynchronously
	}
	if f.unloads.Load() != 1 {
		t.Errorf("unloads = %d, want 1", f.unloads.Load())
	}
}

// An answer that is not a valid plan unloads a model gl-repos loaded,
// like any other failure.
func TestInvalidPlanUnloads(t *testing.T) {
	f := &fakeOllama{size: 1 << 30}
	f.chat = func(w http.ResponseWriter, r *http.Request) { streamChunks(w, "not a plan", 1) }
	srv := f.start(t)

	if _, err := (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", "x", Call{}); err == nil {
		t.Fatal("an invalid plan was accepted")
	}
	for deadline := time.Now().Add(3 * time.Second); f.unloads.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond) // the unload is sent asynchronously
	}
	if f.unloads.Load() != 1 {
		t.Errorf("unloads = %d, want 1", f.unloads.Load())
	}
}

func TestStallTimeout(t *testing.T) {
	f := &fakeOllama{size: 1 << 30}
	f.chat = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatChunk{Message: chatMessage{Content: `{"summ`}})
		w.(http.Flusher).Flush()
		<-r.Context().Done() // then nothing
	}
	srv := f.start(t)

	c := Client{URL: srv.URL, Model: "m", StallTimeout: 200 * time.Millisecond}
	_, err := c.Propose(context.Background(), "", "x", Call{})
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("err = %v", err)
	}
}

func TestUserCancel(t *testing.T) {
	f := &fakeOllama{size: 1 << 30}
	f.chat = func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }
	srv := f.start(t)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err := Client{URL: srv.URL, Model: "m"}.Propose(ctx, "", "x", Call{})
	if err == nil || err.Error() != "cancelled" {
		t.Fatalf("err = %v", err)
	}
}

func TestProposeRetriesWithoutThink(t *testing.T) {
	calls := 0
	f := &fakeOllama{size: 1 << 30}
	f.chat = func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if _, ok := req["think"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		streamChunks(w, `{"summary":"s","steps":[]}`, 1)
	}
	srv := f.start(t)

	if _, err := (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", "x", Call{}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestProposeModelError(t *testing.T) {
	f := &fakeOllama{size: 1 << 30}
	f.chat = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"model \"nope\" not found"}`))
	}
	srv := f.start(t)

	_, err := Client{URL: srv.URL, Model: "m"}.Propose(context.Background(), "", "x", Call{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

func TestNumCtx(t *testing.T) {
	for _, c := range []struct{ chars, limit, want int }{
		{100, 0, 8192},
		{30000, 0, 16384},
		{500000, 32768, 32768},
		{500000, 4096, 8192},
	} {
		if got := numCtx(c.chars, c.limit); got != c.want {
			t.Errorf("numCtx(%d, %d) = %d, want %d", c.chars, c.limit, got, c.want)
		}
	}
}

func TestProposeAsksAgainForMissingCommit(t *testing.T) {
	calls := 0
	f := &fakeOllama{size: 1 << 30}
	var second []chatMessage
	f.chat = func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if calls == 1 {
			streamChunks(w, `{"summary":"Stage calc.py and commit it.","steps":[{"op":"stage","paths":["calc.py"]}]}`, 1)
			return
		}
		second = req.Messages
		streamChunks(w, `{"summary":"Stage calc.py and commit it.","steps":[{"op":"stage","paths":["calc.py"]},{"op":"commit","message":"Add sub"}]}`, 1)
	}
	srv := f.start(t)
	p, err := (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", "commit just calc.py", Call{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(p.Steps) != 2 || p.Steps[1].Op != "commit" {
		t.Fatalf("calls %d, plan %+v; want the corrected plan from a second request", calls, p)
	}
	if n := len(second); n != 4 || second[2].Role != "assistant" || second[3].Content != missingCommitNote {
		t.Errorf("second request messages: %+v", second)
	}

	// a complete plan, a stash plan or one with no steps is never asked again
	for _, answer := range []string{planJSON,
		`{"summary":"Stash the changes so you can commit later.","steps":[{"op":"stash","message":"wip"}]}`,
		`{"summary":"Nothing to commit.","steps":[]}`} {
		calls = 0
		f.chat = func(w http.ResponseWriter, r *http.Request) { calls++; streamChunks(w, answer, 1) }
		if _, err := (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", "x", Call{}); err != nil || calls != 1 {
			t.Errorf("%s: calls %d err %v, want one request", answer, calls, err)
		}
	}
}

// The size cache is written through a temporary file of its own: a
// planted "<cache>.tmp" (here a symlink) is never written through.
func TestRememberSizeIgnoresPlantedTmp(t *testing.T) {
	dir := t.TempDir()
	defer func(f func() string) { sizeCachePath = f }(sizeCachePath)
	sizeCachePath = func() string { return filepath.Join(dir, "sizes.json") }
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, sizeCachePath()+".tmp"); err != nil {
		t.Fatal(err)
	}
	rememberSize("m:latest", 3<<30)
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("wrote through the planted .tmp: %q", b)
	}
	if loadSizes()["m:latest"] != 3<<30 {
		t.Errorf("size not remembered: %v", loadSizes())
	}
	if fi, err := os.Stat(sizeCachePath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("cache file: %v %v", fi, err)
	}
}

// A symlink planted at the size cache's own path is neither read nor
// written through (forge.ReadCacheFile, WriteCacheFile): it is replaced.
func TestSizeCacheSymlinkNotFollowed(t *testing.T) {
	dir := t.TempDir()
	defer func(f func() string) { sizeCachePath = f }(sizeCachePath)
	sizeCachePath = func() string { return filepath.Join(dir, "sizes.json") }
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte(`{"m:latest":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, sizeCachePath()); err != nil {
		t.Fatal(err)
	}
	if got := loadSizes(); len(got) != 0 {
		t.Errorf("read through the link: %v", got)
	}
	if err := os.Symlink(victim, sizeCachePath()); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	rememberSize("m:latest", 3<<30)
	if b, _ := os.ReadFile(victim); string(b) != `{"m:latest":1}` {
		t.Errorf("wrote through the link: %q", b)
	}
	if fi, err := os.Stat(victim); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("target mode %v (%v): chmod followed the link", fi.Mode().Perm(), err)
	}
	if fi, err := os.Lstat(sizeCachePath()); err != nil || !fi.Mode().IsRegular() || loadSizes()["m:latest"] != 3<<30 {
		t.Errorf("cache file: %v %v %v", fi, err, loadSizes())
	}
}

// A size cache whose directory is a symlink is neither read nor written
// (forge.openCacheDir): what the link points to stays as it is.
func TestSizeCacheDirSymlinkRefused(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "cache")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	defer func(f func() string) { sizeCachePath = f }(sizeCachePath)
	sizeCachePath = func() string { return filepath.Join(link, "sizes.json") }
	victim := filepath.Join(target, "sizes.json")
	if err := os.WriteFile(victim, []byte(`{"m:latest":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadSizes(); len(got) != 0 {
		t.Errorf("read through the linked directory: %v", got)
	}
	rememberSize("m:latest", 3<<30)
	if b, err := os.ReadFile(victim); err != nil || string(b) != `{"m:latest":1}` {
		t.Errorf("target changed (%v): %q", err, b)
	}
	if fi, err := os.Stat(victim); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("target mode %v (%v): chmod followed the link", fi.Mode().Perm(), err)
	}
	if left, _ := os.ReadDir(target); len(left) != 1 {
		t.Errorf("written where the link points: %v", left)
	}
}

// Only a plain model name Ollama reports (/api/ps) reaches the size
// cache (CheckModel), and one in an older cache is dropped on load.
func TestSizeCacheKeepsOnlyPlainModelNames(t *testing.T) {
	dir := t.TempDir()
	defer func(f func() string) { sizeCachePath = f }(sizeCachePath)
	sizeCachePath = func() string { return filepath.Join(dir, "sizes.json") }
	bad := []string{"oauth2:SECRET@h/p.git", "m;private_token=SECRET", "m?token=SECRET", "m\x1b[2J:latest", "a b", "-m", "m@sha256:ab"}
	f := &fakeOllama{loaded: append([]string{"m:latest", "hf.co/user/repo:Q4_K_M"}, bad...), sizes: map[string]uint64{}}
	for _, n := range f.loaded {
		f.sizes[n] = 3 << 30
	}
	srv := f.start(t)
	if ok, err := (Client{URL: srv.URL, Model: "m"}).loaded(context.Background()); !ok || err != nil {
		t.Fatal(ok, err)
	}
	data, err := os.ReadFile(sizeCachePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range bad {
		if strings.Contains(string(data), n) || strings.Contains(string(data), "SECRET") {
			t.Errorf("cache holds %q: %s", n, data)
		}
	}
	got := loadSizes()
	if len(got) != 2 || got["m:latest"] != 3<<30 || got["hf.co/user/repo:Q4_K_M"] != 3<<30 {
		t.Errorf("sizes %v: %s", got, data)
	}
	// an older cache with such a name: dropped on load, gone after a write
	if err := os.WriteFile(sizeCachePath(), []byte(`{"m:latest":1,"oauth2:SECRET@h/p.git":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadSizes(); len(got) != 1 || got["m:latest"] != 1 {
		t.Errorf("loaded %v", got)
	}
	rememberSize("n:latest", 2)
	if data, _ := os.ReadFile(sizeCachePath()); strings.Contains(string(data), "SECRET") {
		t.Errorf("cache keeps a bad name: %s", data)
	}
}

// Concurrent size writes (every loaded call of a background job) are
// serialized: none is lost.
func TestRememberSizeConcurrent(t *testing.T) {
	dir := t.TempDir()
	defer func(f func() string) { sizeCachePath = f }(sizeCachePath)
	sizeCachePath = func() string { return filepath.Join(dir, "sizes.json") }
	const n = 32
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rememberSize("m"+strconv.Itoa(i)+":latest", uint64(i+1)<<20)
		}()
	}
	wg.Wait()
	got := loadSizes()
	for i := range n {
		if got["m"+strconv.Itoa(i)+":latest"] != uint64(i+1)<<20 {
			t.Fatalf("size %d lost: %v", i, got)
		}
	}
}

func TestProposeAsksAgainForMissingPush(t *testing.T) {
	calls := 0
	f := &fakeOllama{size: 1 << 30}
	var second []chatMessage
	f.chat = func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if calls == 1 {
			streamChunks(w, `{"summary":"Stage all, commit and push to origin/main.","steps":[{"op":"stage_all"},{"op":"commit","message":"Add sub"}]}`, 1)
			return
		}
		second = req.Messages
		streamChunks(w, `{"summary":"Stage all, commit and push to origin/main.","steps":[{"op":"stage_all"},{"op":"commit","message":"Add sub"},{"op":"push"}]}`, 1)
	}
	srv := f.start(t)
	p, err := (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", "commit and push", Call{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(p.Steps) != 3 || p.Steps[2].Op != "push" {
		t.Fatalf("calls %d, plan %+v; want the corrected plan from a second request", calls, p)
	}
	if n := len(second); n != 4 || second[3].Content != missingPushNote {
		t.Errorf("second request messages: %+v", second)
	}
	// "nothing to push" with no steps is an answer, not a slip
	calls = 0
	f.chat = func(w http.ResponseWriter, r *http.Request) {
		calls++
		streamChunks(w, `{"summary":"Nothing to push.","steps":[]}`, 1)
	}
	if _, err := (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", "push", Call{}); err != nil || calls != 1 {
		t.Errorf("calls %d err %v, want one request", calls, err)
	}
}

// The default request (enter on an empty prompt) asks for a commit: a
// push, pull or fetch the model adds anyway is dropped and named.
func TestDefaultRequestStaysLocal(t *testing.T) {
	f := &fakeOllama{size: 1 << 30}
	answer := `{"summary":"Commit and push.","steps":[{"op":"stage_all"},{"op":"commit","message":"Add sub"},{"op":"push"},{"op":"fetch"}]}`
	f.chat = func(w http.ResponseWriter, r *http.Request) { streamChunks(w, answer, 1) }
	srv := f.start(t)
	p, err := (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", "  ", Call{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 2 || p.Steps[1].Op != "commit" || !strings.Contains(p.Summary, "Left out: push") {
		t.Errorf("default request plan %+v", p)
	}
	p, err = (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", "commit and push", Call{})
	if err != nil || len(p.Steps) != 4 {
		t.Errorf("an asked-for push was dropped: %+v %v", p, err)
	}
	// the default is told by the call, not by the request's text: the
	// same words typed are an ordinary request, and a caller that put in
	// DefaultPrompt itself says so with Default
	p, err = (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", DefaultPrompt, Call{})
	if err != nil || len(p.Steps) != 4 {
		t.Errorf("the default prompt typed was taken for the default request: %+v %v", p, err)
	}
	p, err = (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", DefaultPrompt, Call{Default: true})
	if err != nil || len(p.Steps) != 2 || !strings.Contains(p.Summary, "Left out: push") {
		t.Errorf("a default request by flag kept the push: %+v %v", p, err)
	}
}

// A clone the model answers to the default request would search and
// download: it is dropped like a push, so the default stays local.
func TestDefaultRequestNeverClones(t *testing.T) {
	f := &fakeOllama{size: 1 << 30}
	answer := `{"summary":"Clone the billing service.","clone":"billing service","steps":[]}`
	f.chat = func(w http.ResponseWriter, r *http.Request) { streamChunks(w, answer, 1) }
	srv := f.start(t)
	p, err := (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", "", Call{})
	if err != nil || p.Clone != "" || !strings.Contains(p.Summary, "Left out: a clone") {
		t.Errorf("default request plan %+v %v", p, err)
	}
	p, err = (Client{URL: srv.URL, Model: "m"}).Propose(context.Background(), "", "clone billing", Call{})
	if err != nil || p.Clone != "billing service" {
		t.Errorf("an asked-for clone was dropped: %+v %v", p, err)
	}
}

func TestSameModel(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"m", "m:latest", true},
		{"m:7b", "m:7b", true},
		{"m:7b", "m", false},
		{"host:11434/m", "host:11434/m:latest", true}, // the colon is the port, not a tag
		{"host:11434/m:7b", "host:11434/m", false},
	} {
		if got := sameModel(tc.a, tc.b); got != tc.same {
			t.Errorf("sameModel(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

// A local server is one on localhost, however spelled, or a loopback IP:
// only then is this machine's memory what the model loads into.
func TestLocalServer(t *testing.T) {
	for u, want := range map[string]bool{
		"http://localhost:11434": true, "http://LOCALHOST:11434": true, "http://localhost.:11434": true,
		"http://127.0.0.1:11434": true, "http://[::1]:11434": true,
		"http://ollama.example.com:11434": false, "http://localhost.example.com:11434": false, "http://10.0.0.5:11434": false,
	} {
		if got := (Client{URL: u}).localServer(); got != want {
			t.Errorf("localServer(%s) = %v, want %v", u, got, want)
		}
	}
}

// An error that quotes the model's or the server's words redacts them
// first and cuts them after: a credential longer than the cut would
// otherwise lose its "@" and show its start.
func TestErrorsRedactBeforeCut(t *testing.T) {
	cred := `{"summary":"https://oauth2:` + strings.Repeat("S", 300) + `@h/x"` // never closed: invalid
	check := func(name string, err error) {
		t.Helper()
		if err == nil || strings.Contains(err.Error(), "SSSS") || !strings.Contains(err.Error(), "***") {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := parsePlan(cred)
	check("invalid plan", err)

	f := &fakeOllama{size: 1 << 30}
	f.chat = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(cred))
	}
	srv := f.start(t)
	_, err = Client{URL: srv.URL, Model: "m"}.Propose(context.Background(), "", "x", Call{})
	check("status error", err)

	f.chat = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(cred + "\n")) }
	_, err = Client{URL: srv.URL, Model: "m"}.Propose(context.Background(), "", "x", Call{})
	check("bad stream chunk", err)
}

// A redirect is never followed: the request, with the repository's
// diff in it, would be re-sent wherever the server says.
func TestNoRedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		streamChunks(w, planJSON, 1)
	}))
	t.Cleanup(other.Close)
	f := &fakeOllama{loaded: []string{"m:latest"}}
	f.chat = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/chat", http.StatusTemporaryRedirect)
	}
	srv := f.start(t)
	withMemory(t, 1<<30)

	_, err := Client{URL: srv.URL, Model: "m", MemoryGuard: true}.Propose(context.Background(), "", "x", Call{})
	if err == nil || !strings.Contains(err.Error(), "redirect refused") {
		t.Fatalf("err = %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Errorf("the redirect was followed")
	}
}
