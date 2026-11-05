import { describe, expect, test } from "bun:test";
import { describeRow, pillLabel, rowId, rowsFor, runForWorkspace, seenOf, verdict, type RunView, type StepView } from "./model";

const NAMES = ["intent", "rebase", "review", "test", "document", "lint", "push", "pr", "ci"];

function step(name: string, status: string, extra: Partial<StepView> = {}): StepView {
  return { name, status, findings: null, summary: null, durationMs: null, error: null, ...extra };
}

function run(statuses: string[], extra: Partial<RunView> = {}): RunView {
  return {
    id: "r1",
    repoPath: "/w/app",
    branch: "feat/x",
    status: "running",
    prUrl: null,
    error: null,
    createdAt: 100,
    updatedAt: 100,
    steps: NAMES.map((name, i) => step(name, statuses[i] ?? "pending")),
    workspaceIds: ["w1"],
    ...extra,
  };
}

describe("pill", () => {
  test("names the step a running run is on and how far along it is", () => {
    expect(pillLabel(run(["completed", "completed", "running"]))).toBe("review 3/9");
  });
  test("puts a parked run's decision first", () => {
    const parked = run(["completed", "completed", "awaiting_approval"]);
    expect(verdict(parked)).toBe("needs-you");
    expect(pillLabel(parked)).toBe("needs you · review");
  });
  test("a passed run shows its pull request; a failed one the step that failed", () => {
    expect(pillLabel(run([], { status: "checks_passed", prUrl: "https://github.com/o/r/pull/12" }))).toBe("passed · #12");
    expect(pillLabel(run(["completed", "completed", "completed", "failed"], { status: "failed" }))).toBe("failed · test");
  });
});

describe("runForWorkspace", () => {
  test("is the newest run on that workspace's checkout", () => {
    const older = run([], { id: "old", createdAt: 1 });
    const newer = run([], { id: "new", createdAt: 2 });
    const elsewhere = run([], { id: "other", createdAt: 3, workspaceIds: ["w2"] });
    expect(runForWorkspace([older, newer, elsewhere], "w1")?.id).toBe("new");
    expect(runForWorkspace([older], "w9")).toBeNull();
  });
});

describe("rowsFor", () => {
  test("a clean run earns started, pull request and passed, and nothing for quiet steps", () => {
    const started = run(["running"]);
    const first = rowsFor(undefined, started);
    expect(first.map((r) => r.event)).toEqual(["started"]);

    const quiet = run(["completed", "completed", "completed", "completed", "running"]);
    expect(rowsFor(seenOf(started), quiet)).toEqual([]);

    const done = run(NAMES.map(() => "completed"), { status: "checks_passed", prUrl: "https://github.com/o/r/pull/12" });
    const last = rowsFor(seenOf(quiet), done);
    expect(last.map((r) => r.event)).toEqual(["pr", "finished"]);
    expect(describeRow(last[1]!)).toBe("no-mistakes passed · #12");
  });

  test("a parked step, a step with findings and a failure each earn a row", () => {
    const before = run(["completed", "completed", "running"]);
    const parked = run(["completed", "completed", "awaiting_approval"]);
    parked.steps[2] = step("review", "awaiting_approval", { findings: 2, summary: "two" });
    expect(rowsFor(seenOf(before), parked).map((r) => [r.event, r.step])).toEqual([["parked", "review"]]);

    const fixed = run(["completed", "completed", "completed", "running"]);
    fixed.steps[2] = step("review", "completed", { findings: 2, summary: "two" });
    const [findingRow] = rowsFor(seenOf(parked), fixed);
    expect(describeRow(findingRow!)).toBe("no-mistakes review: 2 findings — two");

    const failed = run(["completed", "completed", "completed", "failed"], { status: "failed", error: "tests failed" });
    failed.steps[3] = step("test", "failed", { error: "3 tests failed" });
    expect(rowsFor(seenOf(fixed), failed).map(describeRow)).toEqual(["no-mistakes test failed: 3 tests failed", "no-mistakes failed: tests failed"]);
  });

  test("the same change never earns a second row, and its id is stable", () => {
    const parked = run(["completed", "completed", "awaiting_approval"]);
    expect(rowsFor(seenOf(parked), parked)).toEqual([]);
    const [row] = rowsFor(seenOf(run(["completed", "completed", "running"])), parked);
    expect(rowId(row!)).toBe("nm-r1-parked-review-awaiting_approval");
  });
});
