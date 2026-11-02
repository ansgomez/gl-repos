package forge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Groups is implemented by forges whose projects live in nested groups
// that can be cloned as a whole (GitLab).
type Groups interface {
	ListGroups(ctx context.Context) ([]Project, error)
	GroupProjects(ctx context.Context, group string) ([]Project, error)
}

var _ Groups = GitLab{}

var errExcludedGroup = errors.New("excluded group")

type glGroup struct {
	ID          int64  `json:"id"`
	FullPath    string `json:"full_path"`
	Path        string `json:"path"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
}

func (g glGroup) project() Project {
	return Project{Path: g.FullPath, Name: g.Path, Description: RedactLine(g.Description), Group: true}
}

// groupExcluded reports whether group (a full path) is or lies in an
// excluded group: every segment of a group path is a group.
func groupExcluded(group string, exclude []string) bool {
	return Excluded(strings.Trim(group, "/")+"/-", exclude)
}

// ListGroups fetches every group the user is a member of (directly or
// through a parent group), without excluded ones.
func (g GitLab) ListGroups(ctx context.Context) ([]Project, error) {
	call := g.API
	if call == nil {
		call = glabAPI
	}
	if g.Host == "" {
		return nil, errors.New("no GitLab host")
	}
	out, err := call(ctx, g.Host, "groups?min_access_level=10&per_page=100&order_by=path", "--paginate")
	if err != nil {
		return nil, err
	}
	gs, err := decodePages[glGroup](out)
	if err != nil {
		return nil, err
	}
	var ps []Project
	for _, gr := range gs {
		if safePath(gr.FullPath) && !groupExcluded(gr.FullPath, g.Exclude) {
			ps = append(ps, gr.project())
		}
	}
	return ps, nil
}

// LookupGroup finds a group by full path.
func (g GitLab) LookupGroup(ctx context.Context, path string) (Project, error) {
	path = strings.Trim(path, "/")
	if !safePath(path) {
		return Project{}, ErrNotFound
	}
	var gr glGroup
	if err := g.api(ctx, "groups/"+url.PathEscape(path), &gr); err != nil {
		return Project{}, err
	}
	if !safePath(gr.FullPath) {
		return Project{}, ErrNotFound
	}
	if groupExcluded(gr.FullPath, g.Exclude) {
		return Project{}, errExcludedGroup
	}
	return gr.project(), nil
}

// GroupProjects lists every project in group and its subgroups (archived
// ones included, marked), sorted by path. Projects only shared into the
// group are left out: they live elsewhere.
func (g GitLab) GroupProjects(ctx context.Context, group string) ([]Project, error) {
	call := g.API
	if call == nil {
		call = glabAPI
	}
	if g.Host == "" {
		return nil, errors.New("no GitLab host")
	}
	group = strings.Trim(group, "/")
	if !safePath(group) || groupExcluded(group, g.Exclude) {
		return nil, ErrNotFound
	}
	out, err := call(ctx, g.Host, "groups/"+url.PathEscape(group)+
		"/projects?include_subgroups=true&with_shared=false&order_by=path&sort=asc&per_page=100", "--paginate")
	if err != nil {
		return nil, err
	}
	all, err := decodePages[Project](out)
	if err != nil {
		return nil, err
	}
	var ps []Project
	for _, p := range g.Keep(all) {
		if inGroup(p.Path, group) {
			ps = append(ps, p)
		}
	}
	return ps, nil
}

// inGroup reports whether path is below group: the server resolves a
// group path in any case ("Acme/Tools" lists acme/tools) and returns the
// canonical one, so the match ignores case, at a segment boundary.
func inGroup(path, group string) bool {
	return len(path) > len(group) && path[len(group)] == '/' && strings.EqualFold(path[:len(group)], group)
}

// KeepGroups drops groups that f's exclusions cover.
func KeepGroups(f Forge, gs []Project) []Project {
	out := gs[:0:0]
	for _, g := range gs {
		g.Description = RedactLine(g.Description)
		if safePath(g.Path) && len(f.Keep([]Project{{Path: g.Path + "/x"}})) == 1 {
			out = append(out, g)
		}
	}
	return out
}

// GroupID returns the id of the group at path, or ErrNotFound if there is
// no such group (or the user cannot see it). An excluded group is an
// error, as in LookupGroup.
func (g GitLab) GroupID(ctx context.Context, path string) (int64, error) {
	gr, err := g.group(ctx, path)
	return gr.ID, err
}

func (g GitLab) group(ctx context.Context, path string) (glGroup, error) {
	path = strings.Trim(path, "/")
	if !safePath(path) {
		return glGroup{}, ErrNotFound
	}
	if groupExcluded(path, g.Exclude) {
		return glGroup{}, fmt.Errorf("group %s: %w (exclude in the config)", path, errExcludedGroup)
	}
	var gr glGroup
	if err := g.api(ctx, "groups/"+url.PathEscape(path), &gr); err != nil {
		if code404.MatchString(err.Error()) {
			return glGroup{}, ErrNotFound
		}
		return glGroup{}, err
	}
	if gr.ID == 0 || !strings.EqualFold(gr.FullPath, path) {
		return glGroup{}, ErrNotFound
	}
	if !knownVisibility(gr.Visibility) {
		gr.Visibility = "" // server text that is no visibility is never shown or sent back
	}
	return gr, nil
}

// CreateSubgroupCmd creates the subgroup named after the last segment of
// path inside the group with id parent ("glab api groups -X POST …").
// parent is a number, or a placeholder when only shown.
func (g GitLab) CreateSubgroupCmd(parent, path, visibility string) Cmd {
	name := path[strings.LastIndex(path, "/")+1:]
	return Cmd{Env: []string{"GITLAB_HOST=" + g.Host}, Name: "glab", Args: []string{"api", "--hostname", g.Host,
		"groups", "-X", "POST", "-f", "name=" + name, "-f", "path=" + name,
		"-f", "parent_id=" + parent, "-f", "visibility=" + visibility}}
}

// Subgroups is what MissingGroups found for an owner.
type Subgroups struct {
	Parent    string   // deepest existing group
	ParentID  int64    // its id
	ParentVis string   // its visibility; "" if the server did not say
	Missing   []string // full paths below Parent, outermost first
}

// knownVisibility reports whether v is a GitLab visibility.
func knownVisibility(v string) bool { return v == "private" || v == "internal" || v == "public" }

// visRank orders GitLab visibilities; unknown ones rank as private.
func visRank(v string) int {
	switch v {
	case "internal":
		return 1
	case "public":
		return 2
	}
	return 0
}

// SubgroupVisibility is the visibility for new subgroups that hold a
// project of visibility vis: never wider than the parent group's, which
// GitLab refuses (a parent of unknown visibility counts as private).
func (s Subgroups) SubgroupVisibility(vis string) string {
	if visRank(vis) > visRank(s.ParentVis) {
		if s.ParentVis == "" {
			return "private"
		}
		return s.ParentVis
	}
	return vis
}

// MissingGroups splits owner into its deepest existing group (with id
// and visibility) and the subgroups below it that do not exist yet,
// outermost first. A missing top-level group is an error: gl-repos never
// creates those. So is an owner in an excluded group.
func (g GitLab) MissingGroups(ctx context.Context, owner string) (Subgroups, error) {
	owner = strings.Trim(owner, "/")
	if owner == "" {
		return Subgroups{}, errors.New("no group given")
	}
	if !safePath(owner) { // only a plain path is ever quoted below
		return Subgroups{}, errors.New("want a group path like acme/tools (letters, digits, . _ -; not starting with -)")
	}
	if groupExcluded(owner, g.Exclude) {
		return Subgroups{}, fmt.Errorf("group %s: %w (exclude in the config): pick another owner", owner, errExcludedGroup)
	}
	var missing []string
	for p := owner; p != ""; {
		gr, err := g.group(ctx, p)
		switch {
		case err == nil:
			return Subgroups{Parent: p, ParentID: gr.ID, ParentVis: gr.Visibility, Missing: missing}, nil
		case !errors.Is(err, ErrNotFound):
			return Subgroups{}, err
		}
		missing = append([]string{p}, missing...)
		i := strings.LastIndex(p, "/")
		if i < 0 {
			break
		}
		p = p[:i]
	}
	// The top level may also be a user namespace (someone else's, or
	// the user's own while their name is unknown): it has no subgroups.
	return Subgroups{}, fmt.Errorf("%s is not a group you can see on %s (user namespaces can't have subgroups), "+
		"and gl-repos only creates subgroups of existing groups", missing[0], g.Host)
}
