/** A run's change as one line in the chat of the agent working on that checkout. */
import type { PluginTimelineItemProps } from "@getpaseo/plugin/client";
import { ExternalLink, Icon } from "@getpaseo/plugin/client/react-native";
import { Text, View } from "react-native";
import { describeRow, type Row } from "../shared/model";

function icon(row: Row): string {
  if (row.event === "parked") return "ShieldAlert";
  if (row.status === "failed") return "ShieldX";
  if (row.event === "finished" && row.status !== "cancelled") return "ShieldCheck";
  if (row.event === "pr") return "GitPullRequest";
  return "Shield";
}

export function RunRow({ item, theme }: PluginTimelineItemProps<Row>) {
  const row = item.data;
  const color = row.status === "failed" ? theme.colors.statusDanger : row.event === "parked" ? theme.colors.statusWarning : theme.colors.foregroundMuted;
  return (
    <View style={{ flexDirection: "row", alignItems: "center", gap: 8, paddingVertical: 4 }} testID={`no-mistakes-row-${row.event}`}>
      <Icon name={icon(row)} size={14} color={color} />
      <Text style={{ flex: 1, minWidth: 0, color: theme.colors.foreground, fontSize: 13 }} selectable>
        {describeRow(row)}
      </Text>
      {/* The text already names the PR number; the link is the way to it. */}
      {row.prUrl && (row.event === "pr" || row.event === "finished") ? <ExternalLink href={row.prUrl}>Open</ExternalLink> : null}
    </View>
  );
}
