import { describe, expect, test } from "bun:test";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { canonical, parseWorktrees, workspacesFor, worktreesOf } from "./worktrees";

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

describe("workspacesFor", () => {
  test("a standard repository: only the worktree on the run's branch matches, not the main root", async () => {
    const repo = standardRepo("feat/a", "feat/b");
    const dirs = new Map([
      [repo.root, ["main-ws"]],
      [repo.worktree("feat/a"), ["a-ws"]],
      [repo.worktree("feat/b"), ["b-ws"]],
    ]);
    const worktrees = await worktreesOf(repo.root);
    expect(workspacesFor({ branch: "feat/a" }, worktrees, dirs)).toEqual(["a-ws"]);
    expect(workspacesFor({ branch: "main" }, worktrees, dirs)).toEqual(["main-ws"]);
  });

  test("a bare layout, where the run's checkout is the worktree itself", async () => {
    const source = standardRepo();
    const root = tmp();
    execFileSync("git", ["clone", "--bare", source.root, path.join(root, ".bare")], { stdio: "ignore" });
    const bare = path.join(root, ".bare");
    git(root, "--git-dir", bare, "worktree", "add", path.join(root, "main"), "main");
    git(root, "--git-dir", bare, "worktree", "add", "-b", "feat/b", path.join(root, "feat"));
    const dirs = new Map([
      [path.join(root, "main"), ["main-ws"]],
      [path.join(root, "feat"), ["feat-ws"]],
    ]);
    const worktrees = await worktreesOf(path.join(root, "feat"));
    expect(workspacesFor({ branch: "feat/b" }, worktrees, dirs)).toEqual(["feat-ws"]);
    expect([...worktrees.keys()]).not.toContain(bare);
  });

  test("a detached worktree has no branch, so no run matches it", async () => {
    const repo = standardRepo();
    const detached = path.join(path.dirname(repo.root), "detached");
    git(repo.root, "worktree", "add", "--detach", detached);
    const worktrees = await worktreesOf(repo.root);
    expect(worktrees.has(detached)).toBe(false);
    expect(workspacesFor({ branch: "HEAD" }, worktrees, new Map([[detached, ["d-ws"]]]))).toEqual([]);
  });

  test("a checkout git cannot read matches no workspace", async () => {
    const gone = path.join(tmp(), "deleted");
    expect(await worktreesOf(gone)).toEqual(new Map());
  });

  test("a worktree listed through a symlinked path matches the workspace at its real path", () => {
    const real = tmp();
    const alias = path.join(tmp(), "alias");
    fs.symlinkSync(real, alias);
    const worktrees = parseWorktrees(`worktree ${alias}\nHEAD 0000000000000000000000000000000000000000\nbranch refs/heads/feat/a\n\n`);
    expect(workspacesFor({ branch: "feat/a" }, worktrees, new Map([[canonical(real), ["ws"]]]))).toEqual(["ws"]);
  });
});
