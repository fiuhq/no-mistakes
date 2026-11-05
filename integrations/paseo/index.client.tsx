import type { PluginClientContext } from "@getpaseo/plugin/client";
import { PANEL_ID, contributePills } from "./client/pills";
import { RunRow } from "./client/run-row";
import { panel, surface } from "./client/runs-view";
import { createRunsStore } from "./client/store";
import { ROW_KIND, ROW_VERSION, RowSchema } from "./shared/model";

export default function contribute(client: PluginClientContext) {
  const store = createRunsStore(client);
  store.start();
  const cleanups = [
    client.addSurface("runs", surface(store)),
    client.addSidebarItem({ id: "runs", title: "no-mistakes", icon: "ShieldCheck", surface: "runs" }),
    client.addWorkspacePanel({ id: PANEL_ID, title: "no-mistakes", icon: "ShieldCheck", locations: ["workspace", "explorer"], Component: panel(store) }),
    client.addCommandCenterItem({
      id: "open-run",
      title: "Open no-mistakes run",
      icon: "ShieldCheck",
      keywords: ["no-mistakes", "gate", "pipeline", "run", "review", "pr"],
      context: "workspace",
      onSelect: (context) => context.openPanel(PANEL_ID),
    }),
    client.addTimelineRenderer({ kind: ROW_KIND, version: ROW_VERSION, schema: RowSchema, Component: RunRow }),
    contributePills(client, store),
  ];
  return async () => {
    for (const cleanup of cleanups.reverse()) await cleanup();
    store.stop();
  };
}
