package daemon

import (
	"errors"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestWaitingOnHumanMerge(t *testing.T) {
	t.Parallel()
	ready, url, empty := int64(1), "https://github.com/o/r/pull/1", ""
	ci := &db.StepResult{StepName: types.StepCI, Status: types.StepStatusRunning}
	review := &db.StepResult{StepName: types.StepReview, Status: types.StepStatusRunning}
	done := &db.StepResult{StepName: types.StepPR, Status: types.StepStatusCompleted}
	green := &db.Run{Status: types.RunRunning, CIReadyAt: &ready, PRURL: &url}
	for name, tc := range map[string]struct {
		run   *db.Run
		steps []*db.StepResult
		want  bool
	}{
		"green monitor on an open PR": {green, []*db.StepResult{done, ci}, true},
		"checks not green yet":        {&db.Run{Status: types.RunRunning, PRURL: &url}, []*db.StepResult{ci}, false},
		"no PR":                       {&db.Run{Status: types.RunRunning, CIReadyAt: &ready}, []*db.StepResult{ci}, false},
		"empty PR URL":                {&db.Run{Status: types.RunRunning, CIReadyAt: &ready, PRURL: &empty}, []*db.StepResult{ci}, false},
		"revalidating after a rebase": {green, []*db.StepResult{done, review}, false},
		"no step running":             {green, []*db.StepResult{done}, false},
		"finished run":                {&db.Run{Status: types.RunCompleted, CIReadyAt: &ready, PRURL: &url}, []*db.StepResult{ci}, false},
		"no run":                      {nil, nil, false},
	} {
		if got := waitingOnHumanMerge(tc.run, tc.steps); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

// A green PR waiting for someone to merge it must not hold the restart: the
// restart holds every new push, so this would freeze the gate until the merge.
func TestScheduledRestartDoesNotWaitForAGreenCIMonitor(t *testing.T) {
	p, d := newParkedRunEnv(t)
	repo, headSHA := setupTestGitRepo(t, p, d, "restart-green-monitor")
	step := &heldStep{name: types.StepCI, started: make(chan struct{}), released: make(chan struct{})}
	first := make(chan error, 1)
	go func() {
		first <- RunWithOptions(p, d, func() []pipeline.Step { return []pipeline.Step{step} })
	}()
	waitForDaemonReady(t, p)

	runID := submitBranchRun(t, p, p.RepoDir(repo.ID), "feature-a", headSHA)
	select {
	case <-step.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the CI step never started")
	}

	if err := d.UpdateRunPRURL(runID, "https://github.com/o/r/pull/1"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunCIReady(runID, true); err != nil {
		t.Fatal(err)
	}

	var scheduled ipc.RestartStatusResult
	if err := callDaemon(p, ipc.MethodScheduleRestart, &ipc.ScheduleRestartParams{}, &scheduled); err != nil {
		t.Fatalf("schedule restart: %v", err)
	}
	if scheduled.WaitingRuns != 0 {
		t.Fatalf("scheduled restart waits for %d runs, want 0: a green monitor is only waiting for a merge", scheduled.WaitingRuns)
	}
	select {
	case err := <-first:
		if !errors.Is(err, ErrRestartForUpdate) {
			t.Fatalf("daemon stopped with %v, want ErrRestartForUpdate", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a green CI monitor held the restart up")
	}
	if _, err := d.RecoverStaleRuns("daemon crashed during execution"); err != nil {
		t.Fatal(err)
	}
	run, err := d.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != types.RunCIMonitorInterrupted {
		t.Fatalf("green monitor after the restart = %s, want %s: nothing failed, the PR is open and green", run.Status, types.RunCIMonitorInterrupted)
	}
}

// pollingCIStep stops as the CI monitor does when a shutdown lands during a live
// poll: the read fails, the monitor withdraws readiness, the step returns.
type pollingCIStep struct {
	heldStep
}

func (s *pollingCIStep) Execute(sctx *pipeline.StepContext) (*pipeline.StepOutcome, error) {
	s.once.Do(func() { close(s.started) })
	<-sctx.Ctx.Done()
	if err := sctx.SetCIReadiness(false, false); err != nil {
		return nil, err
	}
	return nil, sctx.Ctx.Err()
}

func TestAShutdownDuringAPollKeepsTheGreenVerdict(t *testing.T) {
	p, d := newParkedRunEnv(t)
	repo, headSHA := setupTestGitRepo(t, p, d, "stop-during-poll")
	step := &pollingCIStep{heldStep{name: types.StepCI, started: make(chan struct{})}}
	first := make(chan error, 1)
	go func() {
		first <- RunWithOptions(p, d, func() []pipeline.Step { return []pipeline.Step{step} })
	}()
	waitForDaemonReady(t, p)
	runID := submitBranchRun(t, p, p.RepoDir(repo.ID), "feature-a", headSHA)
	<-step.started
	if err := d.UpdateRunPRURL(runID, "https://github.com/o/r/pull/1"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunCIReady(runID, true); err != nil {
		t.Fatal(err)
	}
	if err := Stop(p); err != nil {
		t.Fatalf("stop daemon: %v", err)
	}
	select {
	case <-first:
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon did not stop")
	}
	if _, err := d.RecoverStaleRuns("daemon crashed during execution"); err != nil {
		t.Fatal(err)
	}
	run, err := d.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != types.RunCIMonitorInterrupted {
		t.Fatalf("green monitor stopped during a poll = %s, want %s", run.Status, types.RunCIMonitorInterrupted)
	}
}

func TestACleanStopStillFailsACIStepWithNoVerdict(t *testing.T) {
	p, d := newParkedRunEnv(t)
	repo, headSHA := setupTestGitRepo(t, p, d, "stop-ci-pending")
	step := &heldStep{name: types.StepCI, started: make(chan struct{}), released: make(chan struct{})}
	first := make(chan error, 1)
	go func() {
		first <- RunWithOptions(p, d, func() []pipeline.Step { return []pipeline.Step{step} })
	}()
	waitForDaemonReady(t, p)
	runID := submitBranchRun(t, p, p.RepoDir(repo.ID), "feature-a", headSHA)
	<-step.started
	if err := Stop(p); err != nil {
		t.Fatalf("stop daemon: %v", err)
	}
	select {
	case <-first:
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon did not stop")
	}
	run, err := d.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != types.RunFailed {
		t.Fatalf("a CI step with no verdict after a clean stop = %s, want failed", run.Status)
	}
}

func TestScheduledRestartWaitsForACIStepWithNoVerdict(t *testing.T) {
	p, d := newParkedRunEnv(t)
	repo, headSHA := setupTestGitRepo(t, p, d, "restart-ci-pending")
	released := make(chan struct{})
	step := &heldStep{name: types.StepCI, started: make(chan struct{}), released: released}
	first := make(chan error, 1)
	go func() {
		first <- RunWithOptions(p, d, func() []pipeline.Step { return []pipeline.Step{step} })
	}()
	waitForDaemonReady(t, p)
	submitBranchRun(t, p, p.RepoDir(repo.ID), "feature-a", headSHA)
	<-step.started

	var scheduled ipc.RestartStatusResult
	if err := callDaemon(p, ipc.MethodScheduleRestart, &ipc.ScheduleRestartParams{}, &scheduled); err != nil {
		t.Fatalf("schedule restart: %v", err)
	}
	if scheduled.WaitingRuns != 1 {
		t.Fatalf("scheduled restart waits for %d runs, want the CI step still working", scheduled.WaitingRuns)
	}
	close(released)
	select {
	case err := <-first:
		if !errors.Is(err, ErrRestartForUpdate) {
			t.Fatalf("daemon stopped with %v, want ErrRestartForUpdate", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the restart never came after the CI step finished")
	}
}
