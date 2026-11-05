import { defineRpc } from "@getpaseo/plugin";
import { z } from "zod";
import { StateViewSchema } from "./model";

/** Every recent run on this host, each tied to the Paseo workspaces of its checkout. Read-only. */
export const getState = defineRpc({
  name: "no-mistakes.state",
  input: z.object({}),
  output: StateViewSchema,
});
