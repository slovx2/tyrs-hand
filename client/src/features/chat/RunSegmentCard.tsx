import { FlashList, type FlashListRef } from "@shopify/flash-list";
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";

import { ClientApi } from "@/api/client";
import { loadCachedRunActivities, saveRunActivities } from "@/db/cache";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import type { RunActivity, RunSegment, TurnRun } from "@/types/protocol";
import { MarkdownContent } from "./MarkdownContent";
import { buildProjectedRunActivity, type OperationsPart, type RunActivityPart } from "./runActivity";

function mergeActivities(current: RunActivity[], incoming: RunActivity[]): RunActivity[] {
  const values = new Map(current.map((item) => [item.id, item]));
  for (const item of incoming) values.set(item.id, item);
  return [...values.values()].sort((left, right) => left.firstEventSequence - right.firstEventSequence);
}

function duration(run: TurnRun): string {
  const elapsed = Math.max(0, new Date(run.finishedAt ?? Date.now()).getTime() -
    new Date(run.startedAt).getTime());
  const seconds = Math.floor(elapsed / 1000);
  return seconds >= 60 ? `${Math.floor(seconds / 60)}m ${seconds % 60}s` : `${seconds}s`;
}

function Operations({ part }: { part: OperationsPart }) {
  const theme = useTheme();
  const [expanded, setExpanded] = useState(false);
  const summary = part.operations.length === 1 ? part.operations[0]!.label :
    `${part.operations[0]!.label}等 ${part.operations.length} 项操作`;
  return <View style={styles.operation}>
    <Pressable onPress={() => setExpanded((value) => !value)} style={styles.operationHeader}>
      <Text style={{ color: theme.colors.textMuted }}>↳</Text>
      <Text numberOfLines={1} style={[styles.operationSummary, { color: theme.colors.textMuted }]}>
        {summary}
      </Text>
      <Text style={{ color: theme.colors.textMuted }}>{expanded ? "⌃" : "⌄"}</Text>
    </Pressable>
    {expanded && part.operations.map((item) => <View key={item.id} style={styles.operationRow}>
      <Text style={{ color: item.status === "failed" ? theme.colors.danger : theme.colors.textMuted }}>
        {item.status === "running" ? "○" : item.status === "failed" ? "!" : "✓"}
      </Text>
      <Text style={[styles.operationText, { color: theme.colors.text }]}>{item.label}</Text>
    </View>)}
  </View>;
}

const minimumActivityHeight = 96;

export const RunSegmentCard = memo(function RunSegmentCard({ run, segment, continued, active, maxHeight, liveVersion,
  onInteractionStart, onInteractionEnd, onFinalDraft }: {
  run: TurnRun; segment: RunSegment; continued: boolean; active: boolean; maxHeight: number;
  liveVersion: number; onInteractionStart: () => void; onInteractionEnd: () => void;
  onFinalDraft: (runId: string, text: string) => void;
}) {
  const theme = useTheme();
  const connection = useAppStore((state) => state.activeConnection);
  const terminal = ["completed", "failed", "canceled"].includes(run.status);
  const [expanded, setExpanded] = useState(active);
  const [activities, setActivities] = useState<RunActivity[]>([]);
  const [ready, setReady] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [hasNew, setHasNew] = useState(false);
  const [activityViewportHeight, setActivityViewportHeight] = useState(minimumActivityHeight);
  const nearBottom = useRef(true);
  const wasActive = useRef(active);
  const scroll = useRef<FlashListRef<RunActivityPart>>(null);
  const watermark = useRef(0);
  const requestedVersion = useRef(0);
  const parts = useMemo(() => buildProjectedRunActivity(activities), [activities]);
  const activityMaxHeight = Math.max(minimumActivityHeight, maxHeight - 92);
  const presentation = continued ? { label: "已继续", color: theme.colors.textMuted } :
    run.status === "failed" ? { label: "失败", color: theme.colors.danger } :
    run.status === "canceled" ? { label: "已停止", color: theme.colors.textMuted } :
    terminal ? { label: "已完成", color: theme.colors.success } :
    run.status === "waiting_for_user" ? { label: "等待回答", color: theme.colors.warning } :
    { label: "正在处理", color: theme.colors.accent };

  const applyPage = useCallback(async (page: Awaited<ReturnType<ClientApi["listRunActivities"]>>,
    preserveHistory = false) => {
    setActivities((current) => mergeActivities(current, page.activities));
    if (!preserveHistory) setHasMore(page.hasMoreBefore);
    watermark.current = page.persistedThroughEventSeq;
    if (connection) await saveRunActivities(connection.serverId, segment.id, page.activities);
    const text = page.finalAnswerDraft?.payload.text ?? "";
    if (run.actualSettings.collaborationMode !== "plan") onFinalDraft(run.id, text);
  }, [connection, onFinalDraft, run.actualSettings.collaborationMode, run.id, segment.id]);

  const loadLatest = useCallback(async () => {
    if (!connection) return;
    const cached = await loadCachedRunActivities(connection.serverId, segment.id);
    if (cached.length) setActivities(cached);
    const page = await new ClientApi(connection).listRunActivities(run.id, segment.id);
    await applyPage(page);
    setReady(true);
    requestAnimationFrame(() => scroll.current?.scrollToEnd({ animated: false }));
  }, [applyPage, connection, run.id, segment.id]);

  useEffect(() => {
    setActivityViewportHeight((height) => Math.min(height, activityMaxHeight));
  }, [activityMaxHeight]);
  useEffect(() => {
    if (wasActive.current !== active) setExpanded(active);
    wasActive.current = active;
  }, [active]);
  useEffect(() => () => onInteractionEnd(), [onInteractionEnd]);
  useEffect(() => { if (!expanded) onInteractionEnd(); }, [expanded, onInteractionEnd]);
  useEffect(() => { if (expanded && !ready) void loadLatest(); }, [expanded, loadLatest, ready]);
  useEffect(() => {
    if (!active || !expanded || !ready || !connection || liveVersion <= requestedVersion.current) return;
    requestedVersion.current = liveVersion;
    let canceled = false;
    const timer = setTimeout(() => void (async () => {
      let cursor = watermark.current;
      for (;;) {
        const page = await new ClientApi(connection).listRunActivities(run.id, segment.id,
          { afterEventSeq: cursor });
        if (canceled) return;
        await applyPage(page, true);
        cursor = page.persistedThroughEventSeq;
        if (!page.hasMoreAfter) break;
      }
      if (canceled) return;
      if (nearBottom.current) requestAnimationFrame(() => scroll.current?.scrollToEnd({ animated: true }));
      else setHasNew(true);
    })(), 120);
    return () => { canceled = true; clearTimeout(timer); };
  }, [active, applyPage, connection, expanded, liveVersion, ready, run.id, segment.id]);

  const loadOlder = async () => {
    if (!connection || loadingOlder || !hasMore || !activities[0]) return;
    setLoadingOlder(true);
    try {
      const page = await new ClientApi(connection).listRunActivities(run.id, segment.id,
        { beforeActivitySeq: activities[0].firstEventSequence });
      await applyPage(page);
    } finally { setLoadingOlder(false); }
  };

  return <View testID={`run:${run.id}:segment:${segment.sequence}`}
    style={[styles.card, { borderColor: theme.colors.border, backgroundColor: theme.colors.surface,
      maxHeight }]}>
    <View style={[styles.rail, { backgroundColor: presentation.color }]} />
    <Pressable accessibilityRole="button" accessibilityState={{ expanded }}
      onPress={() => setExpanded((value) => !value)} style={styles.header}>
      <View style={[styles.dot, { backgroundColor: presentation.color }]} />
      <Text style={[styles.title, { color: theme.colors.text }]}>{presentation.label}</Text>
      {run.attempt > 1 && <Text style={{ color: theme.colors.textMuted }}>第 {run.attempt} 次尝试</Text>}
      <Text style={{ color: theme.colors.textMuted }}>{expanded ? "⌃" : "⌄"}</Text>
    </Pressable>
    {expanded && <>
      <View style={[styles.meta, { borderColor: theme.colors.border }]}>
        <Text style={{ color: theme.colors.textMuted }}>
          {duration(run)} · {Math.max(segment.activityCount, activities.length)} 项动态
        </Text>
        <Text numberOfLines={1} style={styles.model}>{run.actualSettings.model ?? "默认模型"}</Text>
      </View>
      <View style={{ height: activityViewportHeight, maxHeight: activityMaxHeight }}>
        <FlashList ref={scroll} data={parts} nestedScrollEnabled bounces={false} overScrollMode="never"
          keyExtractor={(part) => part.id}
          renderItem={({ item: part }) => part.kind === "commentary" ?
            <View style={styles.commentary}><MarkdownContent compact>{part.text}</MarkdownContent></View> :
            <Operations part={part} />}
          maintainVisibleContentPosition={{ disabled: false }} style={styles.activityList}
          contentContainerStyle={styles.content}
          onContentSizeChange={(_width, height) => {
            setActivityViewportHeight(Math.max(minimumActivityHeight,
              Math.min(activityMaxHeight, Math.ceil(height))));
          }}
          onTouchStart={onInteractionStart} onTouchEnd={onInteractionEnd}
          onTouchCancel={onInteractionEnd} onScrollBeginDrag={onInteractionStart}
          onScrollEndDrag={onInteractionEnd} onMomentumScrollBegin={onInteractionStart}
          onMomentumScrollEnd={onInteractionEnd}
          scrollEventThrottle={80} onScroll={({ nativeEvent }) => {
            nearBottom.current = nativeEvent.contentSize.height - nativeEvent.layoutMeasurement.height -
              nativeEvent.contentOffset.y < 80;
            if (nearBottom.current) setHasNew(false);
          }}
          onStartReached={() => void loadOlder()} onStartReachedThreshold={0.15}
          ListHeaderComponent={loadingOlder ?
            <Text style={{ color: theme.colors.textMuted }}>正在加载更早动态…</Text> :
            !ready ? <Text style={{ color: theme.colors.textMuted }}>正在加载动态…</Text> : null}
          ListFooterComponent={run.errorMessage && !continued ?
            <Text style={{ color: theme.colors.danger }}>{run.errorMessage}</Text> : null} />
      </View>
      {hasNew && <Pressable style={styles.newActivity} onPress={() => {
        setHasNew(false); nearBottom.current = true; scroll.current?.scrollToEnd({ animated: true });
      }}><Text style={{ color: theme.colors.accent }}>有新动态 ↓</Text></Pressable>}
    </>}
  </View>;
});

const styles = StyleSheet.create({
  card: { marginHorizontal: 12, marginVertical: 6, borderWidth: StyleSheet.hairlineWidth,
    borderRadius: 12, overflow: "hidden" },
  rail: { position: "absolute", left: 0, top: 0, bottom: 0, width: 3 },
  header: { minHeight: 48, paddingHorizontal: 14, flexDirection: "row", alignItems: "center", gap: 9 },
  dot: { width: 14, height: 14, borderRadius: 7 },
  title: { flex: 1, fontFamily: "Inter_600SemiBold", fontSize: 15 },
  meta: { borderTopWidth: StyleSheet.hairlineWidth, borderBottomWidth: StyleSheet.hairlineWidth,
    paddingHorizontal: 14, paddingVertical: 7, flexDirection: "row", gap: 8 },
  model: { flex: 1, textAlign: "right" }, activityList: { flex: 1 },
  content: { padding: 12, gap: 8 },
  commentary: { paddingVertical: 4 }, operation: { gap: 5 },
  operationHeader: { flexDirection: "row", alignItems: "center", gap: 7, minHeight: 32 },
  operationSummary: { flex: 1 }, operationRow: { flexDirection: "row", gap: 7, paddingLeft: 18 },
  operationText: { flex: 1, fontSize: 13 },
  newActivity: { alignItems: "center", paddingVertical: 7 },
});
