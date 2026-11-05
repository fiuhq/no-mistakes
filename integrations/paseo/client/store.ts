/**
 * One read loop per client, shared by every pill and view. Plugin RPCs are request/response, so the client
 * asks; it asks often only while a run is moving.
 */
import type { PluginClientContext } from "@getpaseo/plugin/client";
import { useSyncExternalStore } from "react";
import { isTerminal, type StateView } from "../shared/model";
import { getState } from "../shared/rpc";

const ACTIVE_MS = 3_000;
const IDLE_MS = 15_000;

export interface Snapshot {
  state: StateView | null;
  /** The RPC itself failed (the daemon half is down); a read problem arrives as `state.problem`. */
  error: string | null;
}

export interface RunsStore {
  start(): void;
  stop(): void;
  current(): Snapshot;
  subscribe(listener: () => void): () => void;
}

export function createRunsStore(client: PluginClientContext): RunsStore {
  let snapshot: Snapshot = { state: null, error: null };
  const listeners = new Set<() => void>();
  let timer: ReturnType<typeof setTimeout> | null = null;
  let stopped = false;

  async function read(): Promise<void> {
    try {
      snapshot = { state: await client.rpc(getState, {}), error: null };
    } catch (error) {
      snapshot = { state: snapshot.state, error: error instanceof Error ? error.message : String(error) };
    }
    for (const listener of listeners) listener();
  }

  function loop(): void {
    if (stopped) return;
    void read().finally(() => {
      if (stopped) return;
      const moving = snapshot.state?.runs.some((run) => !isTerminal(run.status)) ?? false;
      timer = setTimeout(loop, moving ? ACTIVE_MS : IDLE_MS);
    });
  }

  return {
    start: loop,
    stop() {
      stopped = true;
      if (timer) clearTimeout(timer);
    },
    current: () => snapshot,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
}

export function useRuns(store: RunsStore): Snapshot {
  return useSyncExternalStore(store.subscribe, store.current, store.current);
}
