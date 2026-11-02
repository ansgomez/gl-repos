package forge

import "strings"

// Excluded reports whether a project path falls under one of the
// excluded groups. An entry with a slash ("org/legal") matches that
// group path and everything below it; a bare name ("legal") matches a
// group of that name at any level. Matching ignores case.
func Excluded(path string, exclude []string) bool {
	path = strings.ToLower(strings.Trim(path, "/"))
	if path == "" {
		return false
	}
	segs := strings.Split(path, "/")
	groups := segs[:len(segs)-1] // namespace only; the project name itself doesn't count
	for _, e := range exclude {
		e = strings.ToLower(strings.Trim(strings.TrimSpace(e), "/"))
		switch {
		case e == "":
		case strings.Contains(e, "/"):
			if path == e || strings.HasPrefix(path, e+"/") {
				return true
			}
		default:
			for _, g := range groups {
				if g == e {
					return true
				}
			}
		}
	}
	return false
}

// Keep drops projects in excluded groups or with unsafe paths or URLs.
func (g GitLab) Keep(ps []Project) []Project { return keepProjects(ps, g.Exclude) }

// AllowsURL reports whether a pasted clone URL is outside the excluded
// groups.
func (g GitLab) AllowsURL(u CloneURL) bool { return allowsURL(u, g.Exclude) }

func allowsURL(u CloneURL, exclude []string) bool { return !Excluded(u.Path(), exclude) }
