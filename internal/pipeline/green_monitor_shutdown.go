package pipeline

import (
	"context"
	"errors"
)

// greenMonitorStoppedByShutdown reports a CI step that a daemon shutdown stopped
// after it recorded checks-passed readiness. Nothing failed: the pull request is
// open and green, only the monitoring ended. The run is left active, as a parked
// run is, so startup recovery records it as ci_monitor_interrupted.
func (e *Executor) greenMonitorStoppedByShutdown(ctx context.Context, runID string) bool {
	if !errors.Is(context.Cause(ctx), ErrDaemonShutdown) {
		return false
	}
	run, err := e.db.GetRun(runID)
	return err == nil && run != nil && run.CIReadyAt != nil
}
