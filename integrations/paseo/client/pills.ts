/**
 * A pill in the composer of every agent whose workspace checkout has a no-mistakes run: the run's state at a
 * glance, and a menu to its panel and its pull request.
 */
import type { PluginButton, PluginButtonRegistration, PluginClientContext } from "@getpaseo/plugin/client";
import { Linking } from "react-native";
import { pillLabel, runForWorkspace, VERDICT_ICON, verdict, type RunView } from "../shared/model";
import type { RunsStore } from "./store";

export const PANEL_ID = "runs";

function buttonFor(client: PluginClientContext, run: RunView, workspaceId: string): PluginButton {
  const prUrl = run.prUrl;
  return {
    title: `no-mistakes: ${run.branch} is ${run.status}`,
    label: `nm · ${pillLabel(run)}`,
    icon: VERDICT_ICON[verdict(run)],
    behavior: {
      kind: "menu",
      items: [
        { kind: "item", id: "open-run", title: "Open run", icon: "ListChecks", behavior: { kind: "action", onPress: () => client.openPanel(PANEL_ID, { workspaceId }) } },
        {
          kind: "item",
          id: "open-pr",
          title: "Open pull request",
          icon: "GitPullRequest",
          visible: prUrl !== null,
          behavior: { kind: "action", onPress: () => void (prUrl && Linking.openURL(prUrl)) },
        },
      ],
    },
  };
}

export function contributePills(client: PluginClientContext, store: RunsStore): () => void {
  const agents = new Map<string, string>(); // agentId → workspaceId
  const pills = new Map<string, { registration: PluginButtonRegistration; key: string }>();
  const lifetime = new AbortController();

  function sync(): void {
    const runs = store.current().state?.runs ?? [];
    for (const [agentId, pill] of pills) {
      if (agents.has(agentId)) continue;
      pill.registration.remove();
      pills.delete(agentId);
    }
    for (const [agentId, workspaceId] of agents) {
      const run = runForWorkspace(runs, workspaceId);
      const existing = pills.get(agentId);
      if (!run) {
        existing?.registration.remove();
        pills.delete(agentId);
        continue;
      }
      const button = buttonFor(client, run, workspaceId);
      const key = JSON.stringify([button.label, button.icon, run.prUrl]);
      if (existing?.key === key) continue;
      if (existing) {
        existing.registration.update(button);
        existing.key = key;
        continue;
      }
      try {
        pills.set(agentId, { registration: client.addComposerPill({ id: "run", workspaceId, agentId, button }), key });
      } catch (error) {
        console.warn("[no-mistakes] could not add a pill", error);
      }
    }
  }

  const track = (agent: { id: string; workspaceId?: string | null; archivedAt?: string | null }) => {
    if (agent.workspaceId && !agent.archivedAt) agents.set(agent.id, agent.workspaceId);
    else agents.delete(agent.id);
  };

  const unsubscribe = store.subscribe(sync);
  // ponytail: only the newest page of agents gets pills; page through older ones if a long-idle agent needs one.
  void client.paseo.agents
    .list({ subscribe: {}, signal: lifetime.signal, sort: [{ key: "updated_at", direction: "desc" }], page: { limit: 200 } })
    .then(({ subscription }) => {
      subscription.subscribe({
        snapshot: ({ entries }) => {
          agents.clear();
          for (const { agent } of entries) track(agent);
          sync();
        },
        update: (message) => {
          if (message.type !== "agent_update") return;
          if (message.payload.kind === "remove") agents.delete(message.payload.agentId);
          else track(message.payload.agent);
          sync();
        },
      });
    })
    .catch((error: unknown) => {
      if (!lifetime.signal.aborted) console.error("[no-mistakes] could not watch agents", error);
    });

  return () => {
    lifetime.abort();
    unsubscribe();
    for (const pill of pills.values()) pill.registration.remove();
    pills.clear();
  };
}
