/**
 * Reads runs straight from the daemon's SQLite state, read-only. The plugin lives in this repository so these
 * queries are checked against `internal/db/schema.go` by `db.test.ts`: a schema change that breaks them fails
 * that test on the next upstream sync instead of blanking the panel.
 */
import os from "node:os";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
import type { RunView, StepView } from "../shared/model";

/** `NM_HOME` as the CLI resolves it (internal/paths). */
export function nmHome(): string {
  return process.env.NM_HOME?.trim() || path.join(os.homedir(), ".no-mistakes");
}

export function statePath(home = nmHome()): string {
  return path.join(home, "state.sqlite");
}

/** Every run that is not finished, plus every run touched in the window; a limit cuts only the oldest finished runs. */
export const RUNS_SQL = `
SELECT r.id, r.branch, r.status, r.pr_url, r.error, r.created_at, r.updated_at, p.working_path
FROM runs r JOIN repos p ON p.id = r.repo_id
WHERE r.status IN ('pending', 'running') OR r.updated_at >= ?
ORDER BY (r.status IN ('pending', 'running')) DESC, r.updated_at DESC
LIMIT ?`;

export const STEPS_SQL = `
SELECT step_name, status, duration_ms, findings_json, error
FROM step_results WHERE run_id = ? ORDER BY step_order`;

interface RunRow {
  id: string;
  branch: string;
  status: string;
  pr_url: string | null;
  error: string | null;
  created_at: number;
  updated_at: number;
  working_path: string;
}

interface StepRow {
  step_name: string;
  status: string;
  duration_ms: number | null;
  findings_json: string | null;
  error: string | null;
}

/** `{"findings": [...], "summary": "..."}`; anything else counts as no report. */
export function parseFindings(json: string | null): { findings: number | null; summary: string | null } {
  if (!json) return { findings: null, summary: null };
  try {
    const parsed = JSON.parse(json) as { findings?: unknown; summary?: unknown };
    return {
      findings: Array.isArray(parsed.findings) ? parsed.findings.length : null,
      summary: typeof parsed.summary === "string" && parsed.summary.trim() ? parsed.summary.trim() : null,
    };
  } catch {
    return { findings: null, summary: null };
  }
}

export function readRuns(db: DatabaseSync, sinceEpochSeconds: number, limit = 200): Omit<RunView, "workspaceIds">[] {
  const steps = db.prepare(STEPS_SQL);
  return (db.prepare(RUNS_SQL).all(sinceEpochSeconds, limit) as unknown as RunRow[]).map((run) => ({
    id: run.id,
    repoPath: run.working_path,
    branch: run.branch,
    status: run.status,
    prUrl: run.pr_url || null,
    error: run.error || null,
    createdAt: run.created_at,
    updatedAt: run.updated_at,
    steps: (steps.all(run.id) as unknown as StepRow[]).map(
      (step): StepView => ({
        name: step.step_name,
        status: step.status,
        ...parseFindings(step.findings_json),
        durationMs: step.duration_ms,
        error: step.error || null,
      }),
    ),
  }));
}

/** Opens the state read-only for one read; the daemon keeps writing it in WAL mode meanwhile. */
export function withState<T>(file: string, read: (db: DatabaseSync) => T): T {
  const db = new DatabaseSync(file, { readOnly: true });
  try {
    return read(db);
  } finally {
    db.close();
  }
}
