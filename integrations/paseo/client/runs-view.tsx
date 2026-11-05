/** The run list: every recent run in the sidebar page, one checkout's runs in the workspace panel. */
import type { PluginSurfaceProps, PluginWorkspacePanelProps } from "@getpaseo/plugin/client";
import { ExternalLink, Icon, Loading, ScrollView } from "@getpaseo/plugin/client/react-native";
import { useMemo } from "react";
import { Pressable, Text, View } from "react-native";
import { currentStep, prNumber, VERDICT_ICON, verdict, type RunView, type StepView, type Verdict } from "../shared/model";
import { useRuns, type RunsStore } from "./store";

type Theme = PluginSurfaceProps["theme"];
type Navigation = PluginSurfaceProps["navigation"];

const STEP_MARK: Record<string, string> = {
  completed: "✓",
  skipped: "–",
  failed: "✗",
  awaiting_approval: "⏸",
  running: "●",
  fixing: "●",
  fix_review: "●",
  pending: "○",
};

function tone(theme: Theme, v: Verdict): string {
  const c = theme.colors;
  if (v === "passed") return c.statusSuccess;
  if (v === "failed") return c.statusDanger;
  if (v === "needs-you") return c.statusWarning;
  return c.foregroundMuted;
}

function age(epochSeconds: number, now: number): string {
  const minutes = Math.max(0, Math.round((now - epochSeconds * 1000) / 60_000));
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  return hours < 48 ? `${hours}h ago` : `${Math.round(hours / 24)}d ago`;
}

function stepLine(step: StepView): string | null {
  if (step.status === "failed") return `${step.name} failed${step.error ? `: ${step.error}` : ""}`;
  if (!step.findings) return null;
  return `${step.name}: ${step.findings} finding${step.findings === 1 ? "" : "s"}${step.summary ? ` — ${step.summary}` : ""}`;
}

function RunCard({ run, theme, now, navigation, showRepo }: { run: RunView; theme: Theme; now: number; navigation: Navigation; showRepo: boolean }) {
  const styles = useMemo(() => makeStyles(theme), [theme]);
  const v = verdict(run);
  const step = currentStep(run);
  const workspaceId = run.workspaceIds[0];
  return (
    <View style={styles.card} testID={`no-mistakes-run-${run.id}`}>
      <View style={styles.row}>
        <Icon name={VERDICT_ICON[v]} size={16} color={tone(theme, v)} />
        <Text style={styles.title} numberOfLines={1}>{run.branch}</Text>
        <Text style={[styles.meta, { color: tone(theme, v) }]}>{step && v !== "passed" ? `${run.status} · ${step.name}` : run.status}</Text>
      </View>
      <Text style={styles.meta} numberOfLines={1} selectable>
        {showRepo ? `${run.repoPath} · ` : ""}started {age(run.createdAt, now)} · {run.id}
      </Text>
      <Text style={styles.steps} testID={`no-mistakes-steps-${run.id}`}>
        {run.steps.map((s) => `${STEP_MARK[s.status] ?? "?"} ${s.name}`).join("   ")}
      </Text>
      {run.steps.map((s) => {
        const line = stepLine(s);
        return line ? <Text key={s.name} style={styles.detail} selectable>{line}</Text> : null;
      })}
      {run.error ? <Text style={[styles.detail, { color: theme.colors.statusDanger }]} selectable>{run.error}</Text> : null}
      <View style={styles.row}>
        {run.prUrl ? <ExternalLink href={run.prUrl} testID={`no-mistakes-pr-${run.id}`}>{`Pull request ${prNumber(run.prUrl) ?? ""}`.trim()}</ExternalLink> : null}
        {/* The sidebar page lists every checkout; the panel is already inside its workspace. */}
        {showRepo && navigation && workspaceId ? (
          <Pressable accessibilityRole="button" testID={`no-mistakes-open-${run.id}`} onPress={() => navigation.openWorkspace({ workspaceId })}>
            <Text style={styles.link}>Open workspace</Text>
          </Pressable>
        ) : null}
      </View>
    </View>
  );
}

function RunList({ store, theme, navigation, workspaceId }: { store: RunsStore; theme: Theme; navigation: Navigation; workspaceId?: string }) {
  const styles = useMemo(() => makeStyles(theme), [theme]);
  const { state, error } = useRuns(store);
  if (!state && !error) return <Loading />;
  const runs = (state?.runs ?? []).filter((run) => !workspaceId || run.workspaceIds.includes(workspaceId));
  const problem = error ?? state?.problem ?? null;
  return (
    <ScrollView contentContainerStyle={styles.page} testID="no-mistakes-runs">
      {problem ? <Text style={[styles.detail, { color: theme.colors.statusDanger }]} selectable>{problem}</Text> : null}
      {runs.length === 0 && !problem ? (
        <Text style={styles.meta}>{workspaceId ? "No no-mistakes run on this checkout in the last 7 days." : "No no-mistakes run in the last 7 days."}</Text>
      ) : null}
      {runs.map((run) => (
        <RunCard key={run.id} run={run} theme={theme} now={state?.readAt ?? Date.now()} navigation={navigation} showRepo={!workspaceId} />
      ))}
      {state ? <Text style={styles.meta}>Read {new Date(state.readAt).toLocaleTimeString()} from this host's no-mistakes state.</Text> : null}
    </ScrollView>
  );
}

export function surface(store: RunsStore) {
  return function RunsSurface(props: PluginSurfaceProps) {
    return <RunList store={store} theme={props.theme} navigation={props.navigation} />;
  };
}

export function panel(store: RunsStore) {
  return function RunsPanel(props: PluginWorkspacePanelProps) {
    return <RunList store={store} theme={props.theme} navigation={props.navigation} workspaceId={props.workspaceId} />;
  };
}

function makeStyles(theme: Theme) {
  const c = theme.colors;
  return {
    page: { padding: 16, gap: 12 },
    card: { gap: 6, padding: 12, borderRadius: 8, borderWidth: 1, borderColor: c.border, backgroundColor: c.surface1 },
    row: { flexDirection: "row" as const, alignItems: "center" as const, gap: 8, flexWrap: "wrap" as const },
    title: { flex: 1, minWidth: 0, color: c.foreground, fontSize: 14, fontWeight: "600" as const },
    meta: { color: c.foregroundMuted, fontSize: 12 },
    steps: { color: c.foreground, fontSize: 12, fontFamily: "monospace" },
    detail: { color: c.foreground, fontSize: 12, lineHeight: 18 },
    link: { color: c.accent, fontSize: 12 },
  };
}
