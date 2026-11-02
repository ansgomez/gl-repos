package forge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func groupFake() func(context.Context, string, string, ...string) ([]byte, error) {
	return func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
		switch {
		case strings.HasPrefix(ep, "groups?"):
			return []byte(`[{"full_path":"acme","path":"acme"},{"full_path":"acme/tools","path":"tools"}]` +
				`[{"full_path":"acme/legal","path":"legal"},{"full_path":"acme/legal/contracts","path":"contracts"}]`), nil
		case ep == "groups/acme%2Ftools":
			return []byte(`{"full_path":"acme/tools","path":"tools","description":"Tooling"}`), nil
		case ep == "groups/acme%2Flegal":
			return []byte(`{"full_path":"acme/legal","path":"legal"}`), nil
		case strings.HasPrefix(ep, "groups/acme%2Ftools/projects?"), strings.HasPrefix(ep, "groups/Acme%2FTools/projects?"):
			if !strings.Contains(ep, "include_subgroups=true") || !strings.Contains(ep, "with_shared=false") {
				return nil, errors.New("bad endpoint " + ep)
			}
			return []byte(`[{"path_with_namespace":"acme/tools/cli/deploykit","path":"deploykit"},` +
				`{"path_with_namespace":"acme/tools/old","path":"old","archived":true}]` +
				`[{"path_with_namespace":"other/shared","path":"shared"},` +
				`{"path_with_namespace":"acme/tools/legal/notes","path":"notes"}]`), nil
		}
		return nil, errors.New("404 Not Found")
	}
}

func TestGroups(t *testing.T) {
	g := GitLab{Host: "h", Exclude: []string{"legal"}, API: groupFake()}
	gs, err := g.ListGroups(context.Background())
	if err != nil || names(gs) != "acme,acme/tools" || !gs[0].Group {
		t.Fatalf("ListGroups = %s %+v, %v", names(gs), gs, err)
	}
	ps, err := g.GroupProjects(context.Background(), "acme/tools")
	if err != nil {
		t.Fatal(err)
	}
	if names(ps) != "acme/tools/cli/deploykit,acme/tools/old" || !ps[1].Archived {
		t.Errorf("GroupProjects = %+v (shared and excluded must be dropped)", ps)
	}
	if ps, err := g.Search(context.Background(), "acme/tools"); err != nil || len(ps) != 1 || !ps[0].Group || ps[0].Description != "Tooling" {
		t.Errorf("Search(group) = %+v, %v", ps, err)
	}
	if _, err := g.Search(context.Background(), "acme/legal"); !errors.Is(err, ErrNotFound) {
		t.Errorf("excluded group found: %v", err)
	}
	if _, err := g.GroupProjects(context.Background(), "acme/legal"); err == nil {
		t.Errorf("excluded group listed")
	}
	// the server takes the group in any case and lists canonical paths
	if ps, err := g.GroupProjects(context.Background(), "Acme/Tools"); err != nil || names(ps) != "acme/tools/cli/deploykit,acme/tools/old" {
		t.Errorf("GroupProjects(Acme/Tools) = %s, %v", names(ps), err)
	}
	for _, tc := range []struct {
		path, group string
		want        bool
	}{
		{"acme/tools/x", "acme/tools", true},
		{"Acme/Tools/x", "acme/tools", true},
		{"acme/tools", "acme/tools", false},
		{"acme/toolsmith/x", "acme/tools", false},
		{"acme/tool", "acme/tools", false},
	} {
		if got := inGroup(tc.path, tc.group); got != tc.want {
			t.Errorf("inGroup(%q, %q) = %t", tc.path, tc.group, got)
		}
	}
}

func TestGroupCache(t *testing.T) {
	t.Setenv("GL_REPOS_CACHE_DIR", t.TempDir())
	SaveGroupCache("lab", "h", []Project{{Path: "acme/tools", Name: "tools"}, {Path: "acme/legal", Name: "legal"}})
	gs, at := LoadGroupCache("lab", "h", GitLab{Exclude: []string{"legal"}})
	if at.IsZero() || names(gs) != "acme/tools" || !gs[0].Group {
		t.Errorf("group cache = %+v", gs)
	}
	if ps, _ := LoadCache("lab", "h", GitLab{}); ps != nil {
		t.Errorf("group and project caches must be separate")
	}
}

func TestSearchIncludesGroups(t *testing.T) {
	g := GitLab{Host: "h", API: func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
		switch {
		case strings.HasPrefix(ep, "projects?"):
			if !strings.Contains(ep, "search=tools&") && !strings.HasSuffix(ep, "search=tools") {
				return nil, errors.New("unexpected " + ep)
			}
			return []byte(`[{"path_with_namespace":"acme/tools/cli","path":"cli"}]`), nil
		case strings.HasPrefix(ep, "groups?"):
			return []byte(`[{"full_path":"acme/tools","path":"tools"}]`), nil
		}
		return nil, errors.New("404")
	}}
	ps, err := g.Search(context.Background(), "tools")
	if err != nil || names(ps) != "acme/tools/cli,acme/tools" || !ps[1].Group {
		t.Errorf("Search = %s, %v", names(ps), err)
	}
	ps, err = g.Search(context.Background(), "tools group")
	if err != nil || names(ps) != "acme/tools,acme/tools/cli" {
		t.Errorf("Search(… group) = %s, %v", names(ps), err)
	}
}

func TestMissingGroups(t *testing.T) {
	g := GitLab{Host: "h", API: func(_ context.Context, _, ep string, _ ...string) ([]byte, error) {
		switch ep {
		case "groups/acme":
			return []byte(`{"id":7,"full_path":"acme","path":"acme"}`), nil
		case "groups/acme%2Ftools":
			return []byte(`{"id":9,"full_path":"acme/tools","path":"tools","visibility":"internal"}`), nil
		case "groups/odd":
			return []byte(`{"id":5,"full_path":"odd","path":"odd","visibility":"public?private_token=SECRET"}`), nil
		case "groups/down":
			return nil, errors.New("glab (h): connection refused")
		}
		return nil, errors.New(`glab (h): {"message":"404 Group Not Found"}`)
	}}
	ctx := context.Background()
	sub, err := g.MissingGroups(ctx, "acme/tools")
	if err != nil || sub.Parent != "acme/tools" || sub.ParentID != 9 || sub.ParentVis != "internal" || len(sub.Missing) != 0 {
		t.Errorf("existing: %+v %v", sub, err)
	}
	sub, err = g.MissingGroups(ctx, "acme/tools/new/deeper")
	if err != nil || sub.Parent != "acme/tools" || sub.ParentID != 9 || strings.Join(sub.Missing, ",") != "acme/tools/new,acme/tools/new/deeper" {
		t.Errorf("two missing levels: %+v %v", sub, err)
	}
	// a visibility the server made up is neither shown nor sent back
	if sub, err = g.MissingGroups(ctx, "odd/sub"); err != nil || sub.ParentVis != "" || sub.SubgroupVisibility("internal") != "private" {
		t.Errorf("unknown visibility: %+v %v", sub, err)
	}
	if _, err = g.MissingGroups(ctx, "nope/sub"); err == nil || !strings.Contains(err.Error(), "nope is not a group you can see on h (user namespaces can't have subgroups)") {
		t.Errorf("missing top-level group: %v", err)
	}
	if _, err = g.MissingGroups(ctx, "down/sub"); err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("API failure must not count as missing: %v", err)
	}
	want := "GITLAB_HOST=h glab api --hostname h groups -X POST -f name=new -f path=new -f parent_id=9 -f visibility=private"
	if c := g.CreateSubgroupCmd("9", "acme/tools/new", "private").String(); c != want {
		t.Errorf("CreateSubgroupCmd = %s\nwant %s", c, want)
	}
}
