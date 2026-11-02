package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"gl-repos/internal/forge"
)

// MemoryError reports that loading the model would likely exhaust RAM.
type MemoryError struct {
	Model       string
	Need, Avail uint64 // bytes
}

// Error describes the shortfall and how to get past it.
func (e *MemoryError) Error() string {
	return fmt.Sprintf("not loading %s: needs ~%.1f GB but only ~%.1f GB is free "+
		"(free memory, use a smaller model, or force)", e.Model, gb(e.Need), gb(e.Avail))
}

func gb(b uint64) float64 { return float64(b) / (1 << 30) }

// modelName is an Ollama model name: "gemma4:e2b", "llama3.1:8b-q4_K_M",
// "hf.co/user/repo:Q4_K_M", "registry/ns/model:tag". Nothing else
// (no "@", "?", "#", ";", "%", a space or a control character): a name
// is sent in every request, shown in messages and kept in the size
// cache, and that set holds no credential shape.
var modelName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._/:-]{0,198}[A-Za-z0-9])?$`)

// errBadModel refuses a model name that is none; the name is not quoted
// (a config value or a server's reply may hold a token).
var errBadModel = errors.New("use a plain Ollama model name like gemma4:e2b (letters, digits and . _ - / :)")

// CheckModel reports whether name is a plain Ollama model name
// (modelName), also by the credential shapes gl-repos refuses
// everywhere (forge.HasCredentials). It is the one check for ai.model
// and for the names Ollama reports (/api/ps), before one is kept in the
// size cache (rememberSize, loadSizes).
func CheckModel(name string) error {
	if !modelName.MatchString(name) || forge.HasCredentials(name) {
		return errBadModel
	}
	return nil
}

// sameModel compares Ollama model names, treating "x" as "x:latest". The
// tag is a colon after the last slash: "host:port/x" has none.
func sameModel(a, b string) bool {
	norm := func(s string) string {
		if !strings.Contains(s[strings.LastIndex(s, "/")+1:], ":") {
			return s + ":latest"
		}
		return s
	}
	return norm(a) == norm(b)
}

// loaded reports whether the model is currently resident in Ollama. It
// doubles as a fast reachability check. Resident sizes of all loaded
// models are remembered for the memory guard.
func (c Client) loaded(ctx context.Context) (bool, error) {
	var ps struct {
		Models []struct {
			Name string `json:"name"`
			Size uint64 `json:"size"`
		} `json:"models"`
	}
	if err := c.getJSON(ctx, "/api/ps", &ps); err != nil {
		return false, err
	}
	found := false
	for _, m := range ps.Models {
		rememberSize(m.Name, m.Size)
		if sameModel(m.Name, c.Model) {
			found = true
		}
	}
	return found, nil
}

// checkMemory refuses to load a model whose weights plus a KV-cache
// allowance exceed the memory the OS reports as available.
func (c Client) checkMemory(ctx context.Context) error {
	if !c.localServer() {
		return nil // the model runs on another machine: our RAM is irrelevant
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
			Size uint64 `json:"size"`
		} `json:"models"`
	}
	if err := c.getJSON(ctx, "/api/tags", &tags); err != nil {
		return err
	}
	var size uint64
	for _, m := range tags.Models {
		if sameModel(m.Name, c.Model) {
			size = m.Size
		}
	}
	if size == 0 {
		return fmt.Errorf("model %s is not installed (ollama pull %s)", c.Model, c.Model)
	}
	// The file size overstates multimodal models (vision/audio encoders
	// that a text request never loads); prefer the size seen when loaded.
	if seen := rememberedSize(c.Model); seen > 0 {
		size = seen
	}
	avail, ok := availableMemory()
	if !ok {
		return nil // unknown platform: don't block
	}
	if size > avail { // the weights alone do not fit (and a size near the uint64 limit would wrap the sum below)
		return &MemoryError{Model: c.Model, Need: size, Avail: avail}
	}
	need := size + size/10 + 512<<20 // weights + KV cache/runtime allowance
	if need > avail {
		return &MemoryError{Model: c.Model, Need: need, Avail: avail}
	}
	return nil
}

// sizeCachePath stores resident model sizes observed via /api/ps.
// Overridable for tests.
var sizeCachePath = func() string {
	dir := forge.CacheDir() // the project lists' directory
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "model-sizes.json")
}

// loadSizes reads the size cache; a name that is no plain model name
// (CheckModel: an older cache, or one edited by hand) is dropped.
func loadSizes() map[string]uint64 {
	sizes := map[string]uint64{}
	if p := sizeCachePath(); p != "" {
		// as the project lists: never through a planted link
		if data, err := forge.ReadCacheFile(p); err == nil {
			_ = json.Unmarshal(data, &sizes) // a broken cache is just empty
		}
	}
	for name := range sizes {
		if CheckModel(name) != nil {
			delete(sizes, name)
		}
	}
	return sizes
}

func rememberedSize(model string) uint64 {
	for name, size := range loadSizes() {
		if sameModel(name, model) {
			return size
		}
	}
	return 0
}

// sizeMu serializes the read-modify-write of the size cache within this
// process (loaded runs in background jobs). Across processes the last
// writer wins; each write is still one whole file (WriteCacheFile).
var sizeMu sync.Mutex

// rememberSize keeps the resident size of model, a name Ollama reported:
// only a plain model name is written (CheckModel; the cache is a file
// of names, and a server's reply is not trusted to hold one).
func rememberSize(model string, size uint64) {
	p := sizeCachePath()
	if p == "" || size == 0 || CheckModel(model) != nil {
		return
	}
	sizeMu.Lock()
	defer sizeMu.Unlock()
	sizes := loadSizes()
	if sizes[model] == size {
		return
	}
	sizes[model] = size
	data, _ := json.Marshal(sizes)
	// private, in one step, through a temporary file of its own, like the
	// project lists (nothing planted at p or p.tmp is written through)
	_ = forge.WriteCacheFile(p, data) // a cache that cannot be written is just none
}

// unloadAsync asks Ollama to drop the model right away (keep_alive 0) so
// a failed or abandoned request releases its memory immediately.
func (c Client) unloadAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		body, _ := json.Marshal(map[string]any{"model": c.Model, "keep_alive": 0})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/api/generate"), bytes.NewReader(body))
		if err != nil {
			return
		}
		if resp, err := c.client().Do(req); err == nil {
			resp.Body.Close()
		}
	}()
}

func (c Client) getJSON(ctx context.Context, path string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(path), nil)
	if err != nil {
		return err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("ollama unreachable at %s (is it running?): %w", c.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama %s: %s", path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

var freePercent = regexp.MustCompile(`free percentage:\s*(\d+)%`)

// availableMemory estimates RAM that can be used without heavy swapping.
// Overridable for tests.
var availableMemory = func() (uint64, bool) {
	switch runtime.GOOS {
	case "darwin":
		// memory_pressure counts free, inactive and purgeable pages, which
		// is what the kernel can hand out without swapping.
		total, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0, false
		}
		t, err := strconv.ParseUint(strings.TrimSpace(string(total)), 10, 64)
		if err != nil {
			return 0, false
		}
		out, err := exec.Command("memory_pressure", "-Q").Output()
		if err != nil {
			return 0, false
		}
		m := freePercent.FindSubmatch(out)
		if m == nil {
			return 0, false
		}
		pct, _ := strconv.ParseUint(string(m[1]), 10, 64)
		return t / 100 * pct, true
	case "linux":
		data, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, false
		}
		for _, line := range strings.Split(string(data), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemAvailable:" {
				kb, err := strconv.ParseUint(f[1], 10, 64)
				return kb * 1024, err == nil
			}
		}
	}
	return 0, false
}

// localServer reports whether Ollama runs on this machine.
func (c Client) localServer() bool {
	u, err := url.Parse(c.URL)
	if err != nil {
		return true
	}
	h := strings.TrimSuffix(u.Hostname(), ".") // "LOCALHOST." is localhost too
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Installed reports whether the configured model is installed in Ollama
// (GET /api/tags). It only reads: nothing is loaded or pulled. An error
// means Ollama is unreachable.
func (c Client) Installed(ctx context.Context) (bool, error) {
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := c.getJSON(ctx, "/api/tags", &tags); err != nil {
		return false, err
	}
	for _, m := range tags.Models {
		if sameModel(m.Name, c.Model) {
			return true, nil
		}
	}
	return false, nil
}
