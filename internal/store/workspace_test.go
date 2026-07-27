package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/store"
)

func projectDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWorkspaceNamesProjects(t *testing.T) {
	a := projectDir(t, "checkout-api")
	b := projectDir(t, "runs")

	w, err := store.NewWorkspace([]string{a, "Billing Service=" + b})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	ps := w.Projects()
	if len(ps) != 2 {
		t.Fatalf("want 2 projects, got %d", len(ps))
	}
	// A bare path takes the directory's own name; an explicit label wins.
	if ps[0].Name != "checkout-api" || ps[0].Slug != "checkout-api" {
		t.Errorf("bare path should name itself: %+v", ps[0])
	}
	if ps[1].Name != "Billing Service" || ps[1].Slug != "billing-service" {
		t.Errorf("labelled store wrong: %+v", ps[1])
	}
	// Order is the operator's, not alphabetical.
	if got, ok := w.Project("billing-service"); !ok || got.Name != "Billing Service" {
		t.Errorf("lookup by slug failed: %+v", got)
	}
	if _, ok := w.Project("nope"); ok {
		t.Error("unknown slug should not resolve")
	}
	if _, ok := w.Single(); ok {
		t.Error("two projects is not a single-project workspace")
	}
}

// Two stores whose names collapse to the same slug must both stay reachable.
func TestWorkspaceDisambiguatesSlugs(t *testing.T) {
	w, err := store.NewWorkspace([]string{
		"Check Out=" + projectDir(t, "one"),
		"check-out=" + projectDir(t, "two"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ps := w.Projects()
	if ps[0].Slug == ps[1].Slug {
		t.Fatalf("slugs collided: %q and %q", ps[0].Slug, ps[1].Slug)
	}
	for _, p := range ps {
		if _, ok := w.Project(p.Slug); !ok {
			t.Errorf("project %q unreachable at slug %q", p.Name, p.Slug)
		}
	}
}

func TestWorkspaceRejectsMissingStore(t *testing.T) {
	if _, err := store.NewWorkspace([]string{filepath.Join(t.TempDir(), "absent")}); err == nil {
		t.Error("a missing store directory should be an error, not an empty project")
	}
	if _, err := store.NewWorkspace(nil); err == nil {
		t.Error("a workspace with no stores should be an error")
	}
}

func TestWorkspaceSingleProject(t *testing.T) {
	w, err := store.NewWorkspace([]string{projectDir(t, "runs")})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := w.Single()
	if !ok || p.Name != "runs" {
		t.Errorf("one store should be a single-project workspace, got %+v %v", p, ok)
	}
}

// Groups order by their most recent run, and runs inside a group keep the
// index's newest-first order.
func TestGroupByScenario(t *testing.T) {
	at := func(min int) time.Time { return time.Date(2026, 7, 24, 12, min, 0, 0, time.UTC) }
	index := []store.Meta{
		{ID: "d", Scenario: "stress.yaml", StartedAt: at(40)},
		{ID: "c", Scenario: "load.yaml", StartedAt: at(30)},
		{ID: "b", Scenario: "stress.yaml", StartedAt: at(20)},
		{ID: "a", Scenario: "login.yaml", StartedAt: at(10)},
	}

	groups := store.GroupByScenario(index)
	if len(groups) != 3 {
		t.Fatalf("want 3 scenarios, got %d", len(groups))
	}
	if groups[0].Scenario != "stress.yaml" || groups[1].Scenario != "load.yaml" || groups[2].Scenario != "login.yaml" {
		t.Errorf("groups should lead with the most recent run: %v %v %v",
			groups[0].Scenario, groups[1].Scenario, groups[2].Scenario)
	}
	if len(groups[0].Runs) != 2 || groups[0].Runs[0].ID != "d" {
		t.Errorf("runs within a group should stay newest-first: %+v", groups[0].Runs)
	}
	if got := store.GroupByScenario(nil); len(got) != 0 {
		t.Errorf("no runs should make no groups, got %+v", got)
	}
}

// makeStore creates a directory that is a run store — which is to say, one with
// an index.json. A run's own directory never has one, which is what keeps the
// discovery below from reporting individual runs as projects.
func makeStore(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Organizing runs into a directory per project is the obvious thing to do the
// moment there is more than one thing under test, and pointing at the parent
// should then show all of them rather than nothing.
func TestWorkspaceDiscoversStoresInsideAParent(t *testing.T) {
	root := t.TempDir()
	makeStore(t, root, "ws-local")
	makeStore(t, root, "grpc-local")
	makeStore(t, root, "auth-local")
	// A directory that is not a store is not a project.
	if err := os.MkdirAll(filepath.Join(root, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}

	w, err := store.NewWorkspace([]string{root})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	ps := w.Projects()
	if len(ps) != 3 {
		t.Fatalf("want 3 discovered projects, got %d", len(ps))
	}
	// Sorted, because ReadDir order is not something to expose in a URL space.
	want := []string{"auth-local", "grpc-local", "ws-local"}
	for i, p := range ps {
		if p.Name != want[i] {
			t.Errorf("project %d = %q, want %q", i, p.Name, want[i])
		}
	}
}

// A directory that is a store in its own right is one project, even when it
// happens to contain subdirectories — which every store does, since each run is
// one.
func TestWorkspacePrefersTheStoreItWasPointedAt(t *testing.T) {
	root := t.TempDir()
	dir := makeStore(t, root, "runs")
	makeStore(t, dir, "20260101T000000.000000000Z") // a run that looks store-ish

	w, err := store.NewWorkspace([]string{dir})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if ps := w.Projects(); len(ps) != 1 || ps[0].Name != "runs" {
		t.Fatalf("want the store itself as one project, got %+v", ps)
	}
}

// Naming a project is a statement that it is one project, so an explicit
// `name=path` is taken literally and never expanded.
func TestWorkspaceDoesNotExpandANamedSpec(t *testing.T) {
	root := t.TempDir()
	makeStore(t, root, "a")
	makeStore(t, root, "b")

	w, err := store.NewWorkspace([]string{"everything=" + root})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	ps := w.Projects()
	if len(ps) != 1 || ps[0].Name != "everything" {
		t.Fatalf("a named spec should stay one project, got %+v", ps)
	}
}

// A fresh store, before anything has been run into it, has no index.json and no
// children — and must still open as itself rather than as nothing.
func TestWorkspaceOpensAnEmptyStore(t *testing.T) {
	dir := projectDir(t, "runs")
	w, err := store.NewWorkspace([]string{dir})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if ps := w.Projects(); len(ps) != 1 || ps[0].Name != "runs" {
		t.Fatalf("want one empty project, got %+v", ps)
	}
}
