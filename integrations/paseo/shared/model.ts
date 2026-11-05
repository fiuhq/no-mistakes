/** What the plugin shows about no-mistakes runs, and the pure decisions made from it. No IO here. */
import { z } from "zod";

export const StepViewSchema = z.object({
  name: z.string(),
  status: z.string(),
  /** How many findings the step reported; null when it reported none at all (not "zero"). */
  findings: z.number().nullable(),
  summary: z.string().nullable(),
  durationMs: z.number().nullable(),
  error: z.string().nullable(),
});
export type StepView = z.infer<typeof StepViewSchema>;

export const RunViewSchema = z.object({
  id: z.string(),
  /** The checkout the gate belongs to, as no-mistakes recorded it. */
  repoPath: z.string(),
  branch: z.string(),
  status: z.string(),
  prUrl: z.string().nullable(),
  error: z.string().nullable(),
  /** Epoch seconds, as no-mistakes stores them. */
  createdAt: z.number(),
  updatedAt: z.number(),
  steps: z.array(StepViewSchema),
  /** The Paseo workspaces sitting in a worktree of this checkout that has the run's branch checked out. */
  workspaceIds: z.array(z.string()),
});
export type RunView = z.infer<typeof RunViewSchema>;

export const StateViewSchema = z.object({
  runs: z.array(RunViewSchema),
  /** Epoch ms of the read the runs come from. */
  readAt: z.number(),
  /** Why the runs could not be read, shown instead of an empty list that would read as "nothing running". */
  problem: z.string().nullable(),
});
export type StateView = z.infer<typeof StateViewSchema>;

/** Run statuses after which nothing more happens (internal/types/types.go RunStatus). */
const TERMINAL = new Set(["completed", "failed", "cancelled", "checks_passed", "handed_off", "ci_monitor_interrupted"]);
const PASSED = new Set(["completed", "checks_passed", "handed_off", "ci_monitor_interrupted"]);
/** Step statuses in which the step waits on a human: the first park, and the park after a fix round (internal/pipeline/executor.go). */
export const PARKED = new Set(["awaiting_approval", "fix_review"]);
/** Step statuses in which the step is the one the run is on now. */
const ACTIVE_STEP = new Set(["running", "awaiting_approval", "fixing", "fix_review"]);

export type Verdict = "running" | "needs-you" | "passed" | "failed" | "cancelled";

export function isTerminal(status: string): boolean {
  return TERMINAL.has(status);
}

export function currentStep(run: RunView): StepView | null {
  return run.steps.find((step) => ACTIVE_STEP.has(step.status)) ?? null;
}

export function verdict(run: RunView): Verdict {
  if (run.status === "cancelled") return "cancelled";
  if (run.status === "failed") return "failed";
  if (PASSED.has(run.status)) return "passed";
  return run.steps.some((step) => PARKED.has(step.status)) ? "needs-you" : "running";
}

export function prNumber(url: string | null): string | null {
  const match = url?.match(/\/(?:pull|merge_requests|pulls)\/(\d+)/);
  return match?.[1] ? `#${match[1]}` : null;
}

export const VERDICT_ICON: Record<Verdict, string> = {
  running: "ShieldEllipsis",
  "needs-you": "ShieldAlert",
  passed: "ShieldCheck",
  failed: "ShieldX",
  cancelled: "ShieldOff",
};

/** The composer pill's text: short, and the state first. */
export function pillLabel(run: RunView): string {
  const pr = prNumber(run.prUrl);
  const step = currentStep(run);
  switch (verdict(run)) {
    case "needs-you":
      return `needs you · ${step?.name ?? "gate"}`;
    case "running": {
      if (!step) return "starting";
      const done = run.steps.filter((s) => s.status === "completed" || s.status === "skipped").length;
      return `${step.name} ${done + 1}/${run.steps.length}`;
    }
    case "passed":
      return pr ? `passed · ${pr}` : "passed";
    case "failed": {
      const failed = run.steps.find((s) => s.status === "failed");
      return `failed · ${failed?.name ?? "run"}`;
    }
    case "cancelled":
      return "cancelled";
  }
}

/** The newest run on a workspace's checkout, which is what that workspace's pill shows. */
export function runForWorkspace(runs: readonly RunView[], workspaceId: string): RunView | null {
  let newest: RunView | null = null;
  for (const run of runs) {
    if (run.workspaceIds.includes(workspaceId) && (!newest || run.createdAt > newest.createdAt)) newest = run;
  }
  return newest;
}

// ---- Timeline rows: what changed in a run since it was last seen ----

export const ROW_KIND = "no-mistakes-run";
export const ROW_VERSION = 1;

export const RowSchema = z.object({
  runId: z.string(),
  branch: z.string(),
  event: z.enum(["started", "parked", "step", "pr", "finished"]),
  step: z.string().nullable(),
  status: z.string(),
  findings: z.number().nullable(),
  summary: z.string().nullable(),
  prUrl: z.string().nullable(),
  error: z.string().nullable(),
});
export type Row = z.infer<typeof RowSchema>;

/** The part of a run whose change is worth a row. */
export interface Seen {
  status: string;
  steps: Record<string, string>;
  prUrl: string | null;
  /** How many rows the run has earned so far; absent in a seen.json written before rows were counted. */
  rows?: number;
}

export function seenOf(run: Omit<RunView, "workspaceIds">): Seen {
  return { status: run.status, steps: Object.fromEntries(run.steps.map((s) => [s.name, s.status])), prUrl: run.prUrl };
}

/**
 * The rows a run earns between two reads. A clean run earns three (started, PR opened, passed); a step adds
 * one when it parks on a decision, fails, or completes with findings. `prev` undefined means the run is new.
 */
export function rowsFor(prev: Seen | undefined, run: Omit<RunView, "workspaceIds">): Row[] {
  const base = { runId: run.id, branch: run.branch, step: null, findings: null, summary: null, prUrl: run.prUrl, error: null };
  const rows: Row[] = [];
  if (!prev) rows.push({ ...base, event: "started", status: run.status });
  for (const step of run.steps) {
    const before = prev?.steps[step.name];
    if (before === step.status) continue;
    const stepRow = { ...base, step: step.name, status: step.status, findings: step.findings, summary: step.summary, error: step.error };
    if (PARKED.has(step.status)) rows.push({ ...stepRow, event: "parked" });
    else if (step.status === "failed") rows.push({ ...stepRow, event: "step" });
    else if (step.status === "completed" && (step.findings ?? 0) > 0) rows.push({ ...stepRow, event: "step" });
  }
  if (run.prUrl && run.prUrl !== prev?.prUrl) rows.push({ ...base, event: "pr", status: run.status });
  if (isTerminal(run.status) && prev?.status !== run.status) rows.push({ ...base, event: "finished", status: run.status, error: run.error });
  return rows;
}

/**
 * The rows a run earned since `prev`, each with its id, and what to remember next. A row's id is its sequence
 * number within the run, so a step that parks again gets a new row while re-announcing the same change (seen
 * not yet recorded) reuses the same ids.
 */
export function announce(prev: Seen | undefined, run: Omit<RunView, "workspaceIds">): { rows: { id: string; row: Row }[]; seen: Seen } {
  const before = prev?.rows ?? 0;
  const rows = rowsFor(prev, run).map((row, i) => ({ id: `nm-${run.id}-${before + i}`, row }));
  return { rows, seen: { ...seenOf(run), rows: before + rows.length } };
}

export function describeRow(row: Row): string {
  const findings = row.findings === null ? "" : `${row.findings} finding${row.findings === 1 ? "" : "s"}`;
  switch (row.event) {
    case "started":
      return `no-mistakes started on ${row.branch}`;
    case "parked":
      return `no-mistakes is waiting for a decision at ${row.step}${findings ? ` (${findings})` : ""}`;
    case "step":
      return row.status === "failed"
        ? `no-mistakes ${row.step} failed${row.error ? `: ${row.error}` : ""}`
        : `no-mistakes ${row.step}: ${findings}${row.summary ? ` — ${row.summary}` : ""}`;
    case "pr":
      return `no-mistakes opened ${prNumber(row.prUrl) ?? "the pull request"}`;
    case "finished": {
      if (row.status === "failed") return `no-mistakes failed${row.error ? `: ${row.error}` : ""}`;
      if (row.status === "cancelled") return "no-mistakes run cancelled";
      return `no-mistakes passed${row.prUrl ? ` · ${prNumber(row.prUrl) ?? row.prUrl}` : ""}`;
    }
  }
}
