import { describe, expect, test } from "bun:test";
import fs from "node:fs";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
import { parseFindings, readRuns } from "./db";

/** The daemon's real schema, built the way internal/db/schema.go builds it: the base SQL, then each migration. */
function daemonSchema(): DatabaseSync {
  const source = fs.readFileSync(path.join(import.meta.dir, "../../../internal/db/schema.go"), "utf8");
  const base = source.match(/const schemaSQL = `([\s\S]*?)`/)?.[1];
  const migrations = source.match(/var migrationStatements = \[\]string\{([\s\S]*?)\n\}/)?.[1];
  if (!base || !migrations) throw new Error("internal/db/schema.go no longer has schemaSQL and migrationStatements");
  const db = new DatabaseSync(":memory:");
  db.exec(base);
  // Comments quote names in backticks too, so drop them before taking each backtick string.
  for (const [, statement] of migrations.replace(/^\s*\/\/.*$/gm, "").matchAll(/`([^`]+)`/g)) {
    try {
      db.exec(statement!);
    } catch (error) {
      // schema.go tolerates exactly this error: the column is already in the base SQL.
      if (!String(error).includes("duplicate column")) throw error;
    }
  }
  return db;
}

describe("readRuns against internal/db/schema.go", () => {
  test("reads a run, its steps and its findings with the daemon's own columns", () => {
    const db = daemonSchema();
    db.prepare("INSERT INTO repos (id, working_path, upstream_url, created_at) VALUES (?, ?, ?, ?)").run("r1", "/w/app", "https://x/app.git", 1);
    db.prepare("INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, pr_url, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)").run(
      "run1", "r1", "feat/x", "h", "b", "running", "https://github.com/o/r/pull/7", 100, 200,
    );
    const step = db.prepare("INSERT INTO step_results (id, run_id, step_name, step_order, status, findings_json) VALUES (?, ?, ?, ?, ?, ?)");
    step.run("s2", "run1", "review", 2, "awaiting_approval", JSON.stringify({ findings: [{ id: "a" }, { id: "b" }], summary: "two things" }));
    step.run("s1", "run1", "rebase", 1, "completed", null);

    const [run] = readRuns(db, 150);
    expect(run).toEqual({
      id: "run1",
      repoPath: "/w/app",
      branch: "feat/x",
      status: "running",
      prUrl: "https://github.com/o/r/pull/7",
      error: null,
      createdAt: 100,
      updatedAt: 200,
      steps: [
        { name: "rebase", status: "completed", findings: null, summary: null, durationMs: null, error: null },
        { name: "review", status: "awaiting_approval", findings: 2, summary: "two things", durationMs: null, error: null },
      ],
    });
  });

  test("keeps unfinished runs of any age and drops finished ones outside the window", () => {
    const db = daemonSchema();
    db.prepare("INSERT INTO repos (id, working_path, upstream_url, created_at) VALUES ('r', '/w', 'u', 1)").run();
    const run = db.prepare("INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, created_at, updated_at) VALUES (?, 'r', 'b', 'h', 'b', ?, 1, ?)");
    run.run("old-running", "running", 10);
    run.run("old-done", "completed", 10);
    run.run("new-done", "completed", 500);
    expect(readRuns(db, 400).map((r) => r.id).sort()).toEqual(["new-done", "old-running"]);
  });
});

describe("parseFindings", () => {
  test("an empty list is zero findings; no report is null", () => {
    expect(parseFindings(JSON.stringify({ findings: [], summary: " ok " }))).toEqual({ findings: 0, summary: "ok" });
    expect(parseFindings(JSON.stringify({ findings: null }))).toEqual({ findings: null, summary: null });
    expect(parseFindings("not json")).toEqual({ findings: null, summary: null });
    expect(parseFindings(null)).toEqual({ findings: null, summary: null });
  });
});
