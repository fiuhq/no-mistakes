/**
 * Which Paseo workspaces a run belongs to: the ones sitting in a worktree of the run's checkout that has the
 * run's branch checked out. A standard repository records its main root as the run's checkout; a bare layout
 * records the worktree itself. `git worktree list` answers both the same way.
 */
import { execFile } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
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

/**
 * The repository a checkout belongs to: its common git directory, read from disk without running git, so every
 * worktree of one repository shares one `git worktree list`. A directory with no `.git` (a bare repository) is its
 * own key. Null when the checkout is gone: git could not read it either, so it matches no workspace.
 */
export function repoOf(dir: string): string | null {
  const dotGit = path.join(dir, ".git");
  let stat: fs.Stats;
  try {
    stat = fs.statSync(dotGit);
  } catch {
    return fs.existsSync(dir) ? canonical(dir) : null;
  }
  if (stat.isDirectory()) return canonical(dotGit);
  try {
    const gitDir = path.resolve(dir, fs.readFileSync(dotGit, "utf8").replace(/^gitdir:\s*/, "").trim());
    const common = path.join(gitDir, "commondir");
    return canonical(fs.existsSync(common) ? path.resolve(gitDir, fs.readFileSync(common, "utf8").trim()) : gitDir);
  } catch {
    return canonical(dir);
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
