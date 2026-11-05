/**
 * Which Paseo workspaces a run belongs to: the ones sitting in a worktree of the run's checkout that has the
 * run's branch checked out. A standard repository records its main root as the run's checkout; a bare layout
 * records the worktree itself. `git worktree list` answers both the same way.
 */
import { execFile } from "node:child_process";
import fs from "node:fs";
import { promisify } from "node:util";

const execFileP = promisify(execFile);

/** One spelling per directory, so /var and /private/var (or any symlinked path) compare equal. */
export function canonical(dir: string): string {
  try {
    return fs.realpathSync(dir);
  } catch {
    return dir;
  }
}

/** Canonical worktree directory -> the branch checked out there. Bare and detached entries have no branch. */
export function parseWorktrees(porcelain: string): Map<string, string> {
  const worktrees = new Map<string, string>();
  for (const entry of porcelain.split(/\n\s*\n/)) {
    const lines = entry.split("\n");
    const dir = lines.find((line) => line.startsWith("worktree "))?.slice("worktree ".length);
    const branch = lines.find((line) => line.startsWith("branch refs/heads/"))?.slice("branch refs/heads/".length);
    if (dir && branch) worktrees.set(canonical(dir), branch);
  }
  return worktrees;
}

/** The worktrees of a checkout; none when git cannot read it (a deleted checkout matches no workspace). */
export async function worktreesOf(repoPath: string): Promise<Map<string, string>> {
  try {
    const { stdout } = await execFileP("git", ["-C", repoPath, "worktree", "list", "--porcelain"]);
    return parseWorktrees(stdout);
  } catch {
    return new Map();
  }
}

/** `workspaceDirs` is keyed by canonical directory. */
export function workspacesFor(run: { branch: string }, worktrees: Map<string, string>, workspaceDirs: Map<string, string[]>): string[] {
  const ids: string[] = [];
  for (const [dir, workspaceIds] of workspaceDirs) if (worktrees.get(dir) === run.branch) ids.push(...workspaceIds);
  return ids;
}
