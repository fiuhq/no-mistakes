import { describe, expect, test } from "bun:test";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { canonical, checkoutOf, workspacesFor } from "./worktrees";

function git(cwd: string, ...args: string[]): void {
  execFileSync("git", ["-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main", ...args], { cwd, stdio: "ignore" });
}

function tmp(): string {
  return canonical(fs.mkdtempSync(path.join(os.tmpdir(), "nm-wt-")));
}

/** A standard repository on main with a linked worktree per extra branch, as `git worktree add` lays them out. */
function standardRepo(...branches: string[]): { root: string; worktree: (branch: string) => string } {
  const root = path.join(tmp(), "app");
  fs.mkdirSync(root);
  git(root, "init");
  git(root, "commit", "--allow-empty", "-m", "init");
  const dirs = new Map<string, string>();
  for (const branch of branches) {
    const dir = path.join(path.dirname(root), branch.replace("/", "-"));
    git(root, "worktree", "add", "-b", branch, dir);
    dirs.set(branch, dir);
  }
  return { root, worktree: (branch) => dirs.get(branch)! };
}

/** Every git process this module starts, counted by wrapping PATH's git. */
function countGitRuns<T>(fn: () => T): { result: T; runs: number } {
  const bin = tmp();
  const log = path.join(bin, "log");
  const real = execFileSync("sh", ["-c", "command -v git"], { encoding: "utf8" }).trim();
  fs.writeFileSync(path.join(bin, "git"), `#!/bin/sh\necho x >> ${log}\nexec ${real} "$@"\n`, { mode: 0o755 });
  const saved = process.env.PATH;
  process.env.PATH = `${bin}:${saved}`;
  try {
    const result = fn();
    return { result, runs: fs.existsSync(log) ? fs.readFileSync(log, "utf8").split("\n").filter(Boolean).length : 0 };
  } finally {
    process.env.PATH = saved;
  }
}

describe("workspacesFor", () => {
  test("a standard repository: only the worktree on the run's branch matches, not the main root", () => {
    const repo = standardRepo("feat/a", "feat/b");
    const dirs = new Map([
      [repo.root, ["main-ws"]],
      [repo.worktree("feat/a"), ["a-ws"]],
      [repo.worktree("feat/b"), ["b-ws"]],
    ]);
    expect(workspacesFor({ repoPath: repo.root, branch: "feat/a" }, dirs)).toEqual(["a-ws"]);
    expect(workspacesFor({ repoPath: repo.root, branch: "main" }, dirs)).toEqual(["main-ws"]);
  });

  test("a bare layout, where the run's checkout is the worktree itself", () => {
    const source = standardRepo();
    const root = tmp();
    const bare = path.join(root, ".bare");
    execFileSync("git", ["clone", "--bare", source.root, bare], { stdio: "ignore" });
    git(root, "--git-dir", bare, "worktree", "add", path.join(root, "main"), "main");
    git(root, "--git-dir", bare, "worktree", "add", "-b", "feat/b", path.join(root, "feat"));
    const dirs = new Map([
      [path.join(root, "main"), ["main-ws"]],
      [path.join(root, "feat"), ["feat-ws"]],
      [bare, ["bare-ws"]],
    ]);
    expect(workspacesFor({ repoPath: path.join(root, "feat"), branch: "feat/b" }, dirs)).toEqual(["feat-ws"]);
    expect(workspacesFor({ repoPath: path.join(root, "main"), branch: "main" }, dirs)).toEqual(["main-ws"]);
    expect(checkoutOf(bare)).toEqual({ repo: canonical(bare), branch: null });
  });

  test("the same branch in another repository does not match", () => {
    const one = standardRepo("feat/a");
    const two = standardRepo("feat/a");
    const dirs = new Map([[two.worktree("feat/a"), ["other-ws"]]]);
    expect(workspacesFor({ repoPath: one.root, branch: "feat/a" }, dirs)).toEqual([]);
  });

  test("a detached worktree has no branch, so no run matches it", () => {
    const repo = standardRepo();
    const detached = path.join(path.dirname(repo.root), "detached");
    git(repo.root, "worktree", "add", "--detach", detached);
    expect(checkoutOf(detached)?.branch).toBeNull();
    expect(workspacesFor({ repoPath: repo.root, branch: "HEAD" }, new Map([[detached, ["d-ws"]]]))).toEqual([]);
  });

  test("a deleted checkout, a leftover folder and a broken .git match nothing", () => {
    const gone = path.join(tmp(), "deleted");
    const leftover = tmp();
    fs.writeFileSync(path.join(leftover, "file"), "");
    const broken = tmp();
    fs.mkdirSync(path.join(broken, ".git"));
    for (const dir of [gone, leftover, broken]) {
      expect(checkoutOf(dir)).toBeNull();
      expect(workspacesFor({ repoPath: dir, branch: "main" }, new Map([[dir, ["ws"]]]))).toEqual([]);
    }
  });

  test("a workspace reached through a symlinked path matches the run at its real path", () => {
    const repo = standardRepo("feat/a");
    const alias = path.join(tmp(), "alias");
    fs.symlinkSync(repo.worktree("feat/a"), alias);
    expect(workspacesFor({ repoPath: alias, branch: "feat/a" }, new Map([[canonical(repo.worktree("feat/a")), ["ws"]]]))).toEqual(["ws"]);
  });

  test("matching runs no git, however many checkouts there are", () => {
    const repo = standardRepo("feat/a", "feat/b");
    const gone = path.join(tmp(), "deleted");
    const dirs = new Map([
      [repo.root, ["main-ws"]],
      [repo.worktree("feat/a"), ["a-ws"]],
      [repo.worktree("feat/b"), ["b-ws"]],
    ]);
    const { result, runs } = countGitRuns(() =>
      [repo.root, repo.worktree("feat/b"), gone].map((repoPath) => workspacesFor({ repoPath, branch: "feat/a" }, dirs)),
    );
    expect(result).toEqual([["a-ws"], ["a-ws"], []]);
    expect(runs).toBe(0);
  });
});
