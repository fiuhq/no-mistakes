package daemon

import (
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// waitingOnHumanMerge reports a run whose only remaining work is a person
// merging its pull request: it sits in the CI step of an open PR with
// checks-passed readiness recorded, the state `axi handoff` accepts. A monitor
// like that can last days, so counting it as executing would hold a scheduled
// restart, and with it every new push, until someone merges. A restart here is
// safe: the executor leaves the run active at shutdown, recovery records it as
// ci_monitor_interrupted, and the PR stays open. A CI step with no readiness, or
// a run back in any other step, is still working.
func waitingOnHumanMerge(run *db.Run, steps []*db.StepResult) bool {
	if run == nil || run.Status != types.RunRunning || run.CIReadyAt == nil || run.PRURL == nil || *run.PRURL == "" {
		return false
	}
	active := 0
	inCI := false
	for _, step := range steps {
		if step.Status == types.StepStatusRunning {
			active++
			inCI = step.StepName == types.StepCI
		}
	}
	return active == 1 && inCI
}

func (m *RunManager) runWaitingOnHumanMerge(runID string) bool {
	run, err := m.db.GetRun(runID)
	if err != nil {
		return false
	}
	steps, err := m.db.GetStepsByRun(runID)
	if err != nil {
		return false
	}
	return waitingOnHumanMerge(run, steps)
}
