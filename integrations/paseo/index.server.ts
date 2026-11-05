import type { PluginServerContext } from "@getpaseo/plugin/server";
import { service } from "./server/service";
import { getState } from "./shared/rpc";

export default function contribute(server: PluginServerContext) {
  const nm = service();
  // Any agent activity hands over the API the row writer needs; it never changes the agent.
  server.on("agent.turn_started", (_event, { paseo }) => nm.capture(paseo));
  server.on("agent.turn_ended", (_event, { paseo }) => nm.capture(paseo));
  server.handle(getState, (_input, { paseo }) => nm.state(paseo));
  return () => nm.dispose();
}
