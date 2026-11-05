/**
 * The daemon half: answers the state RPC, and writes a row into the chat of the agent working on a checkout
 * whenever one of its runs changes. The change signal is the daemon writing its state database (an fs watch on
 * NM_HOME), never a timer.
 */
import type { PaseoApi } from "@getpaseo/client";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { ROW_KIND, ROW_VERSION, rowId, rowsFor, seenOf, type RunView, type Seen, type StateView } from "../shared/model";
import { nmHome, readRuns, statePath, withState } from "./db";

/** Finished runs older than this drop out of the views. */
const WINDOW_SECONDS = 7 * 24 * 60 * 60;
/** The daemon writes the database several times per step; one read per burst is enough. */
const SETTLE_MS = 750;

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

/** Paseo workspaces by the checkout they run in. A worktree workspace has its own directory; a local one is its project root. */
async function workspacesByDir(paseo: PaseoApi): Promise<Map<string, string[]>> {
  const byDir = new Map<string, string[]>();
  const { entries } = await paseo.workspaces.list({ page: { limit: 500 } });
  for (const workspace of entries) {
    const dir = workspace.workspaceDirectory ?? workspace.projectRootPath;
    byDir.set(dir, [...(byDir.get(dir) ?? []), workspace.id]);
  }
  return byDir;
}

async function readState(paseo: PaseoApi | null): Promise<StateView> {
  const readAt = Date.now();
  try {
    const runs = withState(statePath(), (db) => readRuns(db, Math.floor(readAt / 1000) - WINDOW_SECONDS));
    const byDir = paseo ? await workspacesByDir(paseo) : new Map<string, string[]>();
    return { runs: runs.map((run) => ({ ...run, workspaceIds: byDir.get(run.repoPath) ?? [] })), readAt, problem: null };
  } catch (error) {
    return { runs: [], readAt, problem: `could not read ${statePath()}: ${error instanceof Error ? error.message : String(error)}` };
  }
}

/**
 * The agent that gets a run's rows: the most recently active one in a workspace on that checkout.
 * ponytail: "most recently active" stands in for "the agent that pushed"; record the pusher on the run
 * (no-mistakes has no caller field yet) if two agents in one worktree ever get each other's rows.
 */
async function agentFor(paseo: PaseoApi, run: RunView): Promise<string | null> {
  if (run.workspaceIds.length === 0) return null;
  const { entries } = await paseo.agents.list({ sort: [{ key: "updated_at", direction: "desc" }], page: { limit: 200 } });
  const agent = entries.map((entry) => entry.agent).find((a) => !a.archivedAt && a.workspaceId && run.workspaceIds.includes(a.workspaceId));
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

  private schedule(): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => {
      this.running = this.running.then(() => this.announce()).catch((error: unknown) => log("could not write run rows", error));
    }, SETTLE_MS);
    this.timer.unref();
  }

  /** Appends each changed run's rows to its agent's chat, then records what was seen. */
  private async announce(): Promise<void> {
    const paseo = this.paseo;
    if (!paseo) return; // nothing is recorded, so the change is announced once an API arrives
    const state = await readState(paseo);
    if (state.problem) return log(state.problem);
    const seen = readSeen();
    const next: Record<string, Seen> = {};
    for (const run of state.runs) {
      next[run.id] = seenOf(run);
      // First start on this host: everything already there is the baseline, not news.
      if (!seen) continue;
      const rows = rowsFor(seen[run.id], run);
      if (rows.length === 0) continue;
      const agentId = await agentFor(paseo, run);
      if (!agentId) continue;
      for (const row of rows) {
        try {
          await paseo.agents.ref(agentId).timeline.append({ type: "plugin", id: rowId(row), kind: ROW_KIND, version: ROW_VERSION, data: row });
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
  }
}

let instance: Service | null = null;

export function service(): Service {
  if (!instance) {
    instance = new Service();
    instance.start();
    for (const signal of ["disconnect", "SIGTERM", "SIGINT"] as const) process.once(signal, () => instance?.dispose());
  }
  return instance;
}
