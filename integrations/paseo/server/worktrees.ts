/**
 * Which Paseo workspaces a run belongs to: the ones sitting in a worktree of the run's repository that has the
 * run's branch checked out. A standard repository records its main root as the run's checkout; a bare layout
 * records the worktree itself. Both are answered from the checkout's own `.git` files, never by running git:
 * the client polls every few seconds and a week of runs spans dozens of checkouts.
 */
import fs from "node:fs";
import path from "node:path";

/** One spelling per directory, so /var and /private/var (or any symlinked path) compare equal. */
export function canonical(dir: string): string {
  try {
    return fs.realpathSync(dir);
  } catch {
    return dir;
  }
}

function read(file: string): string | null {
  try {
    return fs.readFileSync(file, "utf8").trim();
  } catch {
    return null;
  }
}

/** The git directory of a checkout or of a bare repository; null when the directory is not one. */
function gitDirOf(dir: string): string | null {
  const dotGit = path.join(dir, ".git");
  const pointer = read(dotGit);
  if (pointer !== null) return pointer.startsWith("gitdir:") ? path.resolve(dir, pointer.slice("gitdir:".length).trim()) : null;
  if (fs.existsSync(path.join(dotGit, "HEAD"))) return dotGit;
  return fs.existsSync(path.join(dir, "HEAD")) && fs.existsSync(path.join(dir, "objects")) ? dir : null;
}

/**
 * The checkout at a directory: its repository (the common git directory every worktree of it shares) and the
 * branch checked out there (none when detached or bare). Null when the directory is gone or not a checkout.
 */
export function checkoutOf(dir: string): { repo: string; branch: string | null } | null {
  const gitDir = gitDirOf(dir);
  if (gitDir === null) return null;
  const common = read(path.join(gitDir, "commondir"));
  const repo = canonical(common === null ? gitDir : path.resolve(gitDir, common));
  const bare = gitDir === dir;
  // ponytail: reads the files git's default backend writes; a reftable repository shows no branch until this reads its tables.
  const head = bare ? null : read(path.join(gitDir, "HEAD"));
  return { repo, branch: head?.startsWith("ref: refs/heads/") ? head.slice("ref: refs/heads/".length) : null };
}

/** `workspaceDirs` is keyed by canonical directory. */
export function workspacesFor(run: { repoPath: string; branch: string }, workspaceDirs: Map<string, string[]>): string[] {
  const repo = checkoutOf(run.repoPath)?.repo;
  if (repo === undefined) return [];
  const ids: string[] = [];
  for (const [dir, workspaceIds] of workspaceDirs) {
    const checkout = checkoutOf(dir);
    if (checkout?.repo === repo && checkout.branch === run.branch) ids.push(...workspaceIds);
  }
  return ids;
}
