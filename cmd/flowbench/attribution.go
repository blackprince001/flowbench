package main

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/store"
	"github.com/blackprince001/flowbench/internal/target"
)

// saveRun writes a completed load/stress/soak run to the store with attribution
// so a run answers who ran what, against which target, at which revision.
func saveRun(storeRoot, scenarioPath string, sc *ir.Scenario, tgt *target.Target, startedAt time.Time, res *executor.Result, outcomes []collector.Outcome) (string, error) {
	st, err := store.Open(storeRoot)
	if err != nil {
		return "", err
	}
	commit, dirty := gitInfo(filepath.Dir(scenarioPath), scenarioPath)
	return st.Save(store.RunInfo{
		Scenario:  filepath.Base(scenarioPath),
		Mode:      string(sc.Profile.Mode),
		Initiator: initiator(),
		Target:    tgt.Config().Name,
		Commit:    commit,
		Dirty:     dirty,
		StartedAt: startedAt,
	}, res, outcomes)
}

// initiator is the OS user who launched the run.
func initiator() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "unknown"
}

// gitInfo returns the HEAD commit of the flow file's repository and whether the
// flow file has uncommitted changes. Both are empty/false outside a git repo.
func gitInfo(dir, file string) (commit string, dirty bool) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false
	}
	commit = strings.TrimSpace(string(out))
	if status, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--", file).Output(); err == nil {
		dirty = strings.TrimSpace(string(status)) != ""
	}
	return commit, dirty
}
