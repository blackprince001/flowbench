package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Workspace is a set of named run stores served together — one per project, or
// per service, or per branch. Runs never move between them: a store is a
// directory the user owns (ADR 0007), and a workspace only decides which ones
// are on screen at once.
type Workspace struct {
	projects []Project
}

// Project is one named store in a workspace. Slug is what appears in URLs.
type Project struct {
	Name  string
	Slug  string
	Store *Store
}

// NewWorkspace opens each spec as a project. A spec is `name=path` or bare
// `path`, in which case the directory's own base name is used — so
// `--store ./runs` reads as "runs" without ceremony.
func NewWorkspace(specs []string) (*Workspace, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("no run store given")
	}

	w := &Workspace{}
	seen := map[string]bool{}
	for _, spec := range specs {
		name, path := splitSpec(spec)
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("run store %s: %w", path, err)
		}
		st, err := Open(path)
		if err != nil {
			return nil, err
		}

		slug := slugify(name)
		// Two projects resolving to the same slug would make one unreachable,
		// so disambiguate rather than silently shadow.
		base := slug
		for i := 2; seen[slug]; i++ {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		seen[slug] = true

		w.projects = append(w.projects, Project{Name: name, Slug: slug, Store: st})
	}
	return w, nil
}

// WorkspaceOf wraps one already-open store, for callers that never asked for
// projects.
func WorkspaceOf(st *Store) *Workspace {
	name := filepath.Base(st.Root())
	return &Workspace{projects: []Project{{Name: name, Slug: slugify(name), Store: st}}}
}

// Projects returns the projects in the order they were given, which is the
// order the operator chose and therefore the order to show them in.
func (w *Workspace) Projects() []Project { return w.projects }

// Single reports the only project when a workspace has exactly one, so the UI
// can drop project chrome nobody needs.
func (w *Workspace) Single() (Project, bool) {
	if len(w.projects) == 1 {
		return w.projects[0], true
	}
	return Project{}, false
}

// Project looks a project up by slug.
func (w *Workspace) Project(slug string) (Project, bool) {
	for _, p := range w.projects {
		if p.Slug == slug {
			return p, true
		}
	}
	return Project{}, false
}

// Runs is one project's index, newest first.
func (p Project) Runs() ([]Meta, error) { return p.Store.List() }

// Group is a set of runs sharing a scenario — the natural section within a
// project, since re-running one scenario is the common case and comparing those
// runs to each other is the reason to look.
type Group struct {
	Scenario string
	Runs     []Meta
}

// GroupByScenario buckets an index by scenario, ordering groups by their most
// recent run so what was just run is at the top.
func GroupByScenario(index []Meta) []Group {
	order := []string{}
	byScenario := map[string][]Meta{}
	for _, m := range index {
		if _, ok := byScenario[m.Scenario]; !ok {
			order = append(order, m.Scenario)
		}
		byScenario[m.Scenario] = append(byScenario[m.Scenario], m)
	}

	out := make([]Group, 0, len(order))
	for _, s := range order {
		out = append(out, Group{Scenario: s, Runs: byScenario[s]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Runs[0].StartedAt.After(out[j].Runs[0].StartedAt)
	})
	return out
}

func splitSpec(spec string) (name, path string) {
	// A Windows drive letter or an `=` inside a path would both confuse a naive
	// split, so only treat the first `=` as a separator when what precedes it
	// looks like a label rather than a path.
	if i := strings.Index(spec, "="); i > 0 && !strings.ContainsAny(spec[:i], `/\.`) {
		return spec[:i], spec[i+1:]
	}
	clean := filepath.Clean(spec)
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) {
		if abs, err := filepath.Abs(clean); err == nil {
			base = filepath.Base(abs)
		}
	}
	return base, spec
}

// slugify makes a URL-safe segment, keeping it recognizable as the name.
func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "store"
	}
	return s
}
