package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// ListAll fetches every project the user is a member of, most recently
// active first, without projects in excluded groups. With hundreds of projects this takes seconds, so callers
// cache the result (see LoadCache/SaveCache).
func (g GitLab) ListAll(ctx context.Context) ([]Project, error) {
	if g.Host == "" {
		return nil, errors.New("no GitLab host")
	}
	call := g.API
	if call == nil {
		call = glabAPI
	}
	out, err := call(ctx, g.Host,
		"projects?membership=true&simple=true&order_by=last_activity_at&per_page=100", "--paginate")
	if err != nil {
		return nil, err
	}
	all, err := decodePages[Project](out)
	if err != nil {
		return nil, err
	}
	// Drop excluded groups here, before anything is shown or cached.
	return g.Keep(all), nil
}

type projectCache struct {
	Fetched  time.Time `json:"fetched"`
	Projects []Project `json:"projects"`
}

// EnvCacheDir overrides the OS cache directory (used by tests).
const EnvCacheDir = "GL_REPOS_CACHE_DIR"

// CacheDir holds the project lists and the model sizes:
// $GL_REPOS_CACHE_DIR, else gl-repos in the OS cache directory; "" if
// none can be found.
func CacheDir() string {
	if d := os.Getenv(EnvCacheDir); d != "" {
		return d
	}
	return defaultCacheDir()
}

// defaultCacheDir is gl-repos's own folder in the OS cache directory,
// "" if there is none.
func defaultCacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "gl-repos")
}

// cachePath is "<kind>-<profile>@<host>.json". Lists are cached per
// profile: each is saved without that profile's excluded groups, so two
// profiles on one host must not share a file.
func cachePath(kind, profile, host string) string {
	d := CacheDir()
	if d == "" || host == "" || strings.ContainsAny(host, `/\`) {
		return ""
	}
	return filepath.Join(d, kind+"-"+url.QueryEscape(profile)+"@"+host+".json")
}

// LoadCache returns the project list saved for profile on host and when
// it was fetched, without the projects f excludes (f.Keep); a zero time
// means there is none.
func LoadCache(profile, host string, f Forge) ([]Project, time.Time) {
	return loadCache("projects", profile, host, f.Keep)
}

// LoadGroupCache is LoadCache for the group list.
func LoadGroupCache(profile, host string, f Forge) ([]Project, time.Time) {
	gs, at := loadCache("groups", profile, host, func(gs []Project) []Project { return KeepGroups(f, gs) })
	for i := range gs {
		gs[i].Group = true
	}
	return gs, at
}

// loadCache reads the cache file of kind, cleaned (keepProjects) and
// without what keep drops. A file holding more, written before URLs were
// cleaned (credentials, or URLs that are no CloneURL) or before a group
// was excluded, is rewritten without it (same fetch time), or removed if
// that fails: an excluded group is not kept on disk either.
func loadCache(kind, profile, host string, keep func([]Project) []Project) ([]Project, time.Time) {
	p := cachePath(kind, profile, host)
	if p == "" {
		return nil, time.Time{}
	}
	data, err := ReadCacheFile(p) // never through a planted link; private, or removed
	if err != nil {
		return nil, time.Time{}
	}
	var c projectCache
	if json.Unmarshal(data, &c) != nil {
		return nil, time.Time{}
	}
	if clean := keep(keepProjects(c.Projects, nil)); !slices.Equal(clean, c.Projects) {
		c.Projects = clean
		if writeCache(p, c) != nil {
			_ = RemoveCacheFile(p) // relative to the checked directory, like the read
		}
	}
	return c.Projects, c.Fetched
}

// SaveCache stores the project list of profile on host (already without
// its excluded groups). It holds only project metadata already visible to
// the user, never credentials: every list is cleaned (cleanProject) as
// it is written, whatever the caller did.
func SaveCache(profile, host string, ps []Project) error {
	return saveCache("projects", profile, host, ps)
}

// SaveGroupCache is SaveCache for the group list.
func SaveGroupCache(profile, host string, gs []Project) error {
	return saveCache("groups", profile, host, gs)
}

func saveCache(kind, profile, host string, ps []Project) error {
	p := cachePath(kind, profile, host)
	if p == "" {
		return errors.New("no cache directory")
	}
	return writeCache(p, projectCache{Fetched: time.Now(), Projects: keepProjects(ps, nil)})
}

// writeCache replaces the cache file p with c in one step, private to
// this user (WriteCacheFile: project names can be private).
func writeCache(p string, c projectCache) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return WriteCacheFile(p, data)
}

// Match ranks projects for an as-you-type query. Every word must occur
// in the project path; hits in the project's own name rank above hits in
// its namespace, segment starts above mid-word hits, shorter paths above
// longer ones, and otherwise the input order (most recent first) holds.
// If no word-wise match exists, a single word is tried as a subsequence
// ("bilv3" finds billing-v3).
func Match(ps []Project, query string, limit int) []Project {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return ps[:min(limit, len(ps))]
	}
	type hit struct {
		p     Project
		score int
		order int
	}
	var hits []hit
	for i, p := range ps {
		if s, ok := wordScore(strings.ToLower(p.Path), strings.ToLower(p.Name), words); ok {
			hits = append(hits, hit{p, s - len(p.Path)/8, i})
		}
	}
	if len(hits) == 0 && len(words) == 1 {
		for i, p := range ps {
			if subsequence(strings.ToLower(p.Name), words[0]) {
				hits = append(hits, hit{p, 10 - len(p.Path)/8, i})
			} else if subsequence(strings.ToLower(p.Path), words[0]) {
				hits = append(hits, hit{p, -len(p.Path) / 8, i})
			}
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return hits[a].order < hits[b].order
	})
	out := make([]Project, 0, min(limit, len(hits)))
	for _, h := range hits[:min(limit, len(hits))] {
		out = append(out, h.p)
	}
	return out
}

func wordScore(path, name string, words []string) (int, bool) {
	score := 0
	for _, w := range words {
		i := strings.Index(path, w)
		if i < 0 {
			return 0, false
		}
		switch {
		case name == w || path == w:
			score += 100
		case strings.HasPrefix(name, w):
			score += 60
		case strings.Contains(name, w):
			score += 40
		case i == 0 || path[i-1] == '/':
			score += 20
		default:
			score += 5
		}
	}
	return score, true
}

func subsequence(s, sub string) bool {
	j := 0
	for i := 0; i < len(s) && j < len(sub); i++ {
		if s[i] == sub[j] {
			j++
		}
	}
	return j == len(sub)
}
