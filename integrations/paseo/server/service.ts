/**
 * The daemon half: answers the state RPC, and writes a row into the chat of the agent working on a checkout
 * whenever one of its runs changes. The change signal is the daemon writing its state database (an fs watch on
 * NM_HOME), never a timer.
 */
import type { PaseoApi } from "@getpaseo/client";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { announce, ROW_KIND, ROW_VERSION, type RunView, type Seen, type StateView } from "../shared/model";
import { nmHome, readRuns, statePath, withState } from "./db";
import { canonical, workspacesFor, worktreesOf } from "./worktrees";

/** Finished runs older than this drop out of the views. */
const WINDOW_SECONDS = 7 * 24 * 60 * 60;
/** The daemon writes the database several times per step; one read per burst is enough. */
const SETTLE_MS = 1500;
/** How long one checkout's worktree list is reused. */
const WORKTREES_TTL_MS = 5000;

function log(message: string, error?: unknown): void {
  console.error(`[no-mistakes] ${message}${error === undefined ? "" : `: ${error instanceof Error ? error.message : String(error)}`}`);
}

/** Plugin state, kept per host like every Paseo plugin's. */
function seenFile(): string {
  return path.join(process.env.PASEO_HOME ?? path.join(os.homedir(), ".paseo"), "no-mistakes", "seen.json");
}

function readSeen(): Record<string, Seen> | null {
  try {
    return JSON.parse(fs.readFileSync(seenFile(), "utf8")) as Record<string, Seen>;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return null;
    throw error;
  }
}

function writeSeen(seen: Record<string, Seen>): void {
  const file = seenFile();
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(`${file}.tmp`, JSON.stringify(seen));
  fs.renameSync(`${file}.tmp`, file);
}

/** Paseo workspaces by the canonical checkout they run in. A worktree workspace has its own directory; a local one is its project root. */
async function workspacesByDir(paseo: PaseoApi): Promise<Map<string, string[]>> {
  const byDir = new Map<string, string[]>();
  let cursor: string | undefined;
  do {
    // Paseo pages at most 200 workspaces at a time.
    const { entries, pageInfo } = await paseo.workspaces.list({ page: { limit: 200, ...(cursor ? { cursor } : {}) } });
    for (const workspace of entries) {
      const dir = canonical(workspace.workspaceDirectory ?? workspace.projectRootPath);
      byDir.set(dir, [...(byDir.get(dir) ?? []), workspace.id]);
    }
    cursor = pageInfo.hasMore ? (pageInfo.nextCursor ?? undefined) : undefined;
  } while (cursor);
  return byDir;
}

// ponytail: each checkout's worktrees are up to 5s stale so the 3s client poll does not run git per read; a new worktree's run shows on its workspace after that.
const worktreeCache = new Map<string, { at: number; worktrees: Promise<Map<string, string>> }>();

function cachedWorktrees(repoPath: string, now: number): Promise<Map<string, string>> {
  const hit = worktreeCache.get(repoPath);
  if (hit && now - hit.at < WORKTREES_TTL_MS) return hit.worktrees;
  const worktrees = worktreesOf(repoPath);
  worktreeCache.set(repoPath, { at: now, worktrees });
  return worktrees;
}

function readWindow(now: number): Omit<RunView, "workspaceIds">[] {
  return withState(statePath(), (db) => readRuns(db, Math.floor(now / 1000) - WINDOW_SECONDS));
}

async function readState(paseo: PaseoApi | null): Promise<StateView> {
  const readAt = Date.now();
  try {
    const runs = readWindow(readAt);
    if (!paseo) return { runs: runs.map((run) => ({ ...run, workspaceIds: [] })), readAt, problem: null };
    const byDir = await workspacesByDir(paseo);
    const repoPaths = [...new Set(runs.map((run) => run.repoPath))];
    const worktrees = new Map(await Promise.all(repoPaths.map(async (repoPath) => [repoPath, await cachedWorktrees(repoPath, readAt)] as const)));
    return { runs: runs.map((run) => ({ ...run, workspaceIds: workspacesFor(run, worktrees.get(run.repoPath)!, byDir) })), readAt, problem: null };
  } catch (error) {
    return { runs: [], readAt, problem: `could not read ${statePath()}: ${error instanceof Error ? error.message : String(error)}` };
  }
}

/**
 * The agent that gets a run's rows: the most recently active one in a workspace on that checkout.
 * ponytail: "most recently active" stands in for "the agent that pushed"; record the pusher on the run
 * (no-mistakes has no caller field yet) if two agents in one worktree ever get each other's rows.
 */
async function agentFor(paseo: PaseoApi, workspaceIds: readonly string[]): Promise<string | null> {
  if (workspaceIds.length === 0) return null;
  const { entries } = await paseo.agents.list({ sort: [{ key: "updated_at", direction: "desc" }], page: { limit: 200 } });
  const agent = entries.map((entry) => entry.agent).find((a) => !a.archivedAt && a.workspaceId && workspaceIds.includes(a.workspaceId));
  return agent?.id ?? null;
}

class Service {
  private paseo: PaseoApi | null = null;
  private watcher: fs.FSWatcher | null = null;
  private timer: NodeJS.Timeout | null = null;
  private running: Promise<void> = Promise.resolve();

  start(): void {
    try {
      this.watcher = fs.watch(nmHome(), (_event, file) => {
        if (file?.startsWith("state.sqlite")) this.schedule();
      });
      this.watcher.unref();
    } catch (error) {
      log(`cannot watch ${nmHome()}; chat rows are off until the plugin reloads`, error);
    }
  }

  /** Hooks and handlers are the only places Paseo hands a plugin its API; keep the first one. */
  capture(paseo: PaseoApi): void {
    if (this.paseo) return;
    this.paseo = paseo;
    this.schedule();
  }

  state(paseo: PaseoApi): Promise<StateView> {
    this.capture(paseo);
    return readState(paseo);
  }

  /** At most one read per SETTLE_MS however long the daemon keeps writing; a later write rides the pending read. */
  private schedule(): void {
    if (this.timer) return;
    this.timer = setTimeout(() => {
      this.timer = null;
      this.running = this.running.then(() => this.publish()).catch((error: unknown) => log("could not write run rows", error));
    }, SETTLE_MS);
    this.timer.unref();
  }

  /**
   * Appends each changed run's rows to its agent's chat, then records what was seen. Only a run with rows
   * looks up worktrees, uncached, so a branch checked out a moment ago still finds its workspace.
   */
  private async publish(): Promise<void> {
    const paseo = this.paseo;
    if (!paseo) return; // nothing is recorded, so the change is announced once an API arrives
    const seen = readSeen();
    const next: Record<string, Seen> = {};
    const changed: { run: Omit<RunView, "workspaceIds">; rows: ReturnType<typeof announce>["rows"] }[] = [];
    for (const run of readWindow(Date.now())) {
      const { rows, seen: runSeen } = announce(seen?.[run.id], run);
      next[run.id] = runSeen;
      // First start on this host: everything already there is the baseline, not news.
      if (seen && rows.length > 0) changed.push({ run, rows });
    }
    const byDir = changed.length > 0 ? await workspacesByDir(paseo) : new Map<string, string[]>();
    const repoPaths = [...new Set(changed.map(({ run }) => run.repoPath))];
    const worktrees = new Map(await Promise.all(repoPaths.map(async (repoPath) => [repoPath, await worktreesOf(repoPath)] as const)));
    for (const { run, rows } of changed) {
      const agentId = await agentFor(paseo, workspacesFor(run, worktrees.get(run.repoPath)!, byDir));
      if (!agentId) continue;
      for (const { id, row } of rows) {
        try {
          await paseo.agents.ref(agentId).timeline.append({ type: "plugin", id, kind: ROW_KIND, version: ROW_VERSION, data: row });
        } catch (error) {
          log(`could not add a row to ${agentId}`, error);
        }
      }
    }
    writeSeen(next);
  }

  dispose(): void {
    this.watcher?.close();
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    this.paseo = null;
    if (instance === this) instance = null;
  }
}

let instance: Service | null = null;

export function service(): Service {
  if (!instance) {
    instance = new Service();
    instance.start();
  }
  return instance;
}

for (const signal of ["disconnect", "SIGTERM", "SIGINT"] as const) process.once(signal, () => instance?.dispose());
