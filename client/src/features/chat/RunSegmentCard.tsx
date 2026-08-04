import { FlashList, type FlashListRef, useRecyclingState } from "@shopify/flash-list";
import { memo, useCallback, useEffect, useMemo, useRef } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";

import { ClientApi } from "@/api/client";
import { loadCachedRunActivities, saveRunActivities } from "@/db/cache";
import { previewPerf } from "@/preview/perf";
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

const Operations = memo(function Operations({ part }: { part: OperationsPart }) {
  const theme = useTheme();
  const [expanded, setExpanded] = useRecyclingState(false, [part.id]);
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
});

const RunActivityItem = memo(function RunActivityItem({ part }: { part: RunActivityPart }) {
  return part.kind === "commentary" ?
    <View style={styles.commentary}><MarkdownContent compact>{part.text}</MarkdownContent></View> :
    <Operations part={part} />;
});

function renderRunActivity({ item }: { item: RunActivityPart }) {
  return <RunActivityItem part={item} />;
}

const minimumActivityHeight = 96;
const innerPositioning = {
  disabled: false,
  animateAutoScrollToBottom: false,
  startRenderingFromBottom: true,
} as const;

function runActivityPartType(part: RunActivityPart): string {
  return part.kind;
}

export const RunSegmentCard = memo(function RunSegmentCard({ run, segment, continued, active, maxHeight, liveVersion,
  onInteractionStart, onInteractionEnd, onFollowLatest, onFinalDraft }: {
  run: TurnRun; segment: RunSegment; continued: boolean; active: boolean; maxHeight: number;
  liveVersion: number; onInteractionStart: () => void; onInteractionEnd: () => void;
  onFollowLatest: () => void; onFinalDraft: (runId: string, text: string) => void;
}) {
  const renderStartedAt = performance.now();
  const theme = useTheme();
  const connection = useAppStore((state) => state.activeConnection);
  const terminal = ["completed", "failed", "canceled"].includes(run.status);
  const activityMaxHeight = Math.max(minimumActivityHeight, maxHeight - 84);
  const initialActivityHeight = segment.activityCount > 4 ? activityMaxHeight : minimumActivityHeight;
  const [expanded, setExpanded] = useRecyclingState(active, [segment.id]);
  const [activities, setActivities] = useRecyclingState<RunActivity[]>([], [segment.id]);
  const [ready, setReady] = useRecyclingState(false, [segment.id]);
  const [loadFailed, setLoadFailed] = useRecyclingState(false, [segment.id]);
  const [pendingExpand, setPendingExpand] = useRecyclingState(false, [segment.id]);
  const [hasMore, setHasMore] = useRecyclingState(false, [segment.id]);
  const [loadingOlder, setLoadingOlder] = useRecyclingState(false, [segment.id]);
  const [hasNew, setHasNew] = useRecyclingState(false, [segment.id]);
  const [activityViewportHeight, setActivityViewportHeight] = useRecyclingState(
    initialActivityHeight, [segment.id]);
  const nearBottom = useRef(true);
  const wasActive = useRef(active);
  const scroll = useRef<FlashListRef<RunActivityPart>>(null);
  const followNextActivityCommit = useRef(false);
  const revealNewOnNextActivityCommit = useRef(false);
  const watermark = useRef(0);
  const requestedVersion = useRef(0);
  const lastCardHeight = useRef(0);
  const segmentIdentity = useRef(segment.id);
  const innerHistoryReady = useRef(false);
  const dragging = useRef(false);
  const momentum = useRef(false);
  const unlockTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const loadLatestPromise = useRef<{ segmentId: string; promise: Promise<void> } | null>(null);
  if (segmentIdentity.current !== segment.id) {
    segmentIdentity.current = segment.id;
    nearBottom.current = true;
    wasActive.current = active;
    watermark.current = 0;
    requestedVersion.current = 0;
    lastCardHeight.current = 0;
    followNextActivityCommit.current = false;
    revealNewOnNextActivityCommit.current = false;
    innerHistoryReady.current = false;
    dragging.current = false;
    momentum.current = false;
    if (unlockTimer.current) clearTimeout(unlockTimer.current);
    unlockTimer.current = null;
    loadLatestPromise.current = null;
  }
  const parts = useMemo(() => buildProjectedRunActivity(activities), [activities]);
  const latestActivitySequence = activities.at(-1)?.lastEventSequence ?? 0;
  const presentation = continued ? { label: "已继续", color: theme.colors.textMuted } :
    run.status === "failed" ? { label: "失败", color: theme.colors.danger } :
    run.status === "canceled" ? { label: "已停止", color: theme.colors.textMuted } :
    terminal ? { label: "已完成", color: theme.colors.success } :
    run.status === "waiting_for_user" ? { label: "等待回答", color: theme.colors.warning } :
    { label: "正在处理", color: theme.colors.accent };

  const applyPage = useCallback((page: Awaited<ReturnType<ClientApi["listRunActivities"]>>,
    preserveHistory = false, targetSegmentId = segment.id) => {
    if (segmentIdentity.current !== targetSegmentId) return false;
    setActivities((current) => mergeActivities(current, page.activities));
    if (!preserveHistory) setHasMore(page.hasMoreBefore);
    watermark.current = page.persistedThroughEventSeq;
    if (connection) {
      void saveRunActivities(connection.serverId, targetSegmentId, page.activities).catch(() => undefined);
    }
    const text = page.finalAnswerDraft?.payload.text ?? "";
    if (run.actualSettings.collaborationMode !== "plan") onFinalDraft(run.id, text);
    return true;
  }, [connection, onFinalDraft, run.actualSettings.collaborationMode, run.id, segment.id,
    setActivities, setHasMore]);

  const loadLatest = useCallback((): Promise<void> => {
    if (!connection) return Promise.resolve();
    const targetSegmentId = segment.id;
    const existing = loadLatestPromise.current;
    if (existing?.segmentId === targetSegmentId) return existing.promise;
    const startedAt = performance.now();
    const promise = (async () => {
      setLoadFailed(false);
      previewPerf("segment:load:start", { segmentId: targetSegmentId });
      try {
        const cached = await loadCachedRunActivities(connection.serverId, targetSegmentId);
        if (segmentIdentity.current !== targetSegmentId) return;
        previewPerf("segment:cache:received", { segmentId: targetSegmentId, activities: cached.length,
          elapsed: (performance.now() - startedAt).toFixed(1) });
        if (cached.length) {
          setActivities(cached);
          if (cached.length > 4) setActivityViewportHeight(activityMaxHeight);
        }
        const page = await new ClientApi(connection).listRunActivities(run.id, targetSegmentId);
        if (segmentIdentity.current !== targetSegmentId) return;
        previewPerf("segment:page:received", { segmentId: targetSegmentId,
          activities: page.activities.length, elapsed: (performance.now() - startedAt).toFixed(1) });
        if (page.activities.length > 4) setActivityViewportHeight(activityMaxHeight);
        applyPage(page, false, targetSegmentId);
      } catch (error) {
        if (segmentIdentity.current === targetSegmentId) setLoadFailed(true);
        throw error;
      } finally {
        if (segmentIdentity.current === targetSegmentId) {
          setReady(true);
          previewPerf("segment:load:state-queued", { segmentId: targetSegmentId,
            elapsed: (performance.now() - startedAt).toFixed(1) });
        }
      }
    })().finally(() => {
      if (loadLatestPromise.current?.segmentId === targetSegmentId) loadLatestPromise.current = null;
    });
    loadLatestPromise.current = { segmentId: targetSegmentId, promise };
    return promise;
  }, [activityMaxHeight, applyPage, connection, run.id, segment.id, setActivities,
    setActivityViewportHeight, setLoadFailed, setReady]);

  useEffect(() => {
    setActivityViewportHeight((value) => segment.activityCount > 4 ? activityMaxHeight :
      Math.min(value, activityMaxHeight));
  }, [activityMaxHeight, segment.activityCount, setActivityViewportHeight]);
  useEffect(() => {
    if (wasActive.current !== active) setExpanded(active);
    wasActive.current = active;
  }, [active, setExpanded]);
  useEffect(() => () => {
    if (unlockTimer.current) clearTimeout(unlockTimer.current);
    onInteractionEnd();
  }, [onInteractionEnd, segment.id]);
  useEffect(() => { if (!expanded) onInteractionEnd(); }, [expanded, onInteractionEnd]);
  useEffect(() => {
    if (expanded && !ready) void loadLatest().catch(() => undefined);
  }, [expanded, loadLatest, ready]);
  useEffect(() => {
    previewPerf("segment:commit", { segmentId: segment.id, expanded, ready,
      activities: activities.length, parts: parts.length, viewport: activityViewportHeight,
      renderElapsed: (performance.now() - renderStartedAt).toFixed(1) });
  });
  useEffect(() => {
    if (!expanded || parts.length === 0 ||
      (!followNextActivityCommit.current && !revealNewOnNextActivityCommit.current)) return;
    const targetSegmentId = segment.id;
    const shouldFollow = followNextActivityCommit.current || nearBottom.current;
    followNextActivityCommit.current = false;
    revealNewOnNextActivityCommit.current = false;
    if (shouldFollow) {
      setHasNew(false);
      requestAnimationFrame(() => {
        if (segmentIdentity.current !== targetSegmentId) return;
        previewPerf("segment:auto-scroll", { segmentId: targetSegmentId, source: "activity-commit" });
        scroll.current?.scrollToEnd({ animated: true });
        onFollowLatest();
      });
      return;
    }
    setHasNew(true);
    previewPerf("segment:new-indicator", { segmentId: targetSegmentId, visible: true });
  }, [expanded, latestActivitySequence, onFollowLatest, parts.length, segment.id, setHasNew]);
  useEffect(() => {
    if (!active || !expanded || !ready || !connection || liveVersion <= requestedVersion.current) return;
    requestedVersion.current = liveVersion;
    let canceled = false;
    const targetSegmentId = segment.id;
    const timer = setTimeout(() => void (async () => {
      let cursor = watermark.current;
      for (;;) {
        const page = await new ClientApi(connection).listRunActivities(run.id, targetSegmentId,
          { afterEventSeq: cursor });
        if (canceled || segmentIdentity.current !== targetSegmentId) return;
        if (page.activities.length > 0) {
          if (nearBottom.current) {
            followNextActivityCommit.current = true;
            setHasNew(false);
            previewPerf("segment:live-follow", { segmentId: targetSegmentId, mode: "bottom" });
          } else {
            revealNewOnNextActivityCommit.current = true;
            previewPerf("segment:live-follow", { segmentId: targetSegmentId, mode: "away" });
          }
        }
        applyPage(page, true, targetSegmentId);
        cursor = page.persistedThroughEventSeq;
        if (!page.hasMoreAfter) break;
      }
    })().catch(() => undefined), 120);
    return () => { canceled = true; clearTimeout(timer); };
  }, [active, applyPage, connection, expanded, liveVersion, ready, run.id, segment.id, setHasNew]);

  const loadOlder = async () => {
    if (!connection || !innerHistoryReady.current || loadingOlder || !hasMore || !activities[0]) return;
    const targetSegmentId = segment.id;
    setLoadingOlder(true);
    try {
      const page = await new ClientApi(connection).listRunActivities(run.id, targetSegmentId,
        { beforeActivitySeq: activities[0].firstEventSequence });
      if (segmentIdentity.current !== targetSegmentId) return;
      applyPage(page, false, targetSegmentId);
    } finally {
      if (segmentIdentity.current === targetSegmentId) setLoadingOlder(false);
    }
  };

  const toggleExpanded = () => {
    if (expanded) {
      previewPerf("segment:toggle", { segmentId: segment.id, expanded: false });
      setExpanded(false);
      return;
    }
    if (pendingExpand) return;
    const targetSegmentId = segment.id;
    if (ready && !loadFailed) {
      previewPerf("segment:toggle", { segmentId: targetSegmentId, expanded: true });
      setExpanded(true);
      return;
    }
    previewPerf("segment:prepare-expand", { segmentId: targetSegmentId });
    setPendingExpand(true);
    void loadLatest().catch(() => undefined).finally(() => {
      if (segmentIdentity.current !== targetSegmentId) return;
      setPendingExpand(false);
      setExpanded(true);
      previewPerf("segment:toggle", { segmentId: targetSegmentId, expanded: true, source: "loaded" });
    });
  };
  const clearUnlockTimer = () => {
    if (!unlockTimer.current) return;
    clearTimeout(unlockTimer.current);
    unlockTimer.current = null;
  };
  const releaseOuterScroll = () => {
    clearUnlockTimer();
    dragging.current = false;
    momentum.current = false;
    onInteractionEnd();
  };
  const scheduleOuterScrollRelease = () => {
    clearUnlockTimer();
    unlockTimer.current = setTimeout(() => {
      unlockTimer.current = null;
      if (!momentum.current) onInteractionEnd();
    }, 120);
  };

  return <View testID={`run:${run.id}:segment:${segment.sequence}`}
    onLayout={({ nativeEvent }) => {
      const next = nativeEvent.layout.height;
      if (Math.abs(next - lastCardHeight.current) < 0.5) return;
      previewPerf("segment:layout", { segmentId: segment.id, previous: lastCardHeight.current,
        next: next.toFixed(1), expanded });
      lastCardHeight.current = next;
    }}
    style={[styles.card, { borderColor: theme.colors.border, backgroundColor: theme.colors.surface,
      maxHeight }]}>
    <View style={[styles.rail, { backgroundColor: presentation.color }]} />
    <Pressable accessibilityRole="button" accessibilityState={{ expanded }}
      onPress={toggleExpanded} style={styles.header}>
      <View style={[styles.dot, { backgroundColor: presentation.color }]} />
      <Text style={[styles.title, { color: theme.colors.text }]}>{presentation.label}</Text>
      {run.attempt > 1 && <Text style={{ color: theme.colors.textMuted }}>第 {run.attempt} 次尝试</Text>}
      <Text style={{ color: theme.colors.textMuted }}>{pendingExpand ? "…" : expanded ? "⌃" : "⌄"}</Text>
    </Pressable>
    {expanded && <>
      <View style={[styles.meta, { borderColor: theme.colors.border }]}>
        <Text style={{ color: theme.colors.textMuted }}>
          {duration(run)} · {Math.max(segment.activityCount, activities.length)} 项动态
        </Text>
        <Text numberOfLines={1} style={styles.model}>{run.actualSettings.model ?? "默认模型"}</Text>
      </View>
      <View style={[styles.activityViewport, { height: activityViewportHeight,
        maxHeight: activityMaxHeight }]}>
        <FlashList key={segment.id} ref={scroll} data={parts} nestedScrollEnabled
          bounces={false} overScrollMode="never"
          drawDistance={120}
          getItemType={runActivityPartType}
          keyExtractor={(part) => part.id}
          renderItem={renderRunActivity}
          maintainVisibleContentPosition={innerPositioning} style={styles.activityList}
          contentContainerStyle={styles.content}
          onContentSizeChange={(_width, contentHeight) => {
            const longContent = segment.activityCount > 4 || activities.length > 4;
            const next = longContent ? activityMaxHeight : Math.max(minimumActivityHeight,
              Math.min(activityMaxHeight, Math.ceil(contentHeight)));
            previewPerf("segment:content-size", { segmentId: segment.id,
              contentHeight: contentHeight.toFixed(1), previousViewport: activityViewportHeight,
              nextViewport: next });
            setActivityViewportHeight((current) => current === next ? current : next);
          }}
          onTouchStart={() => {
            clearUnlockTimer();
            dragging.current = false;
            momentum.current = false;
            onInteractionStart();
          }}
          onTouchEnd={() => { if (!dragging.current && !momentum.current) releaseOuterScroll(); }}
          onTouchCancel={releaseOuterScroll}
          onScrollBeginDrag={() => {
            clearUnlockTimer();
            dragging.current = true;
            innerHistoryReady.current = ready;
            onInteractionStart();
          }}
          onScrollEndDrag={() => {
            dragging.current = false;
            scheduleOuterScrollRelease();
          }}
          onMomentumScrollBegin={() => {
            clearUnlockTimer();
            momentum.current = true;
            onInteractionStart();
          }}
          onMomentumScrollEnd={releaseOuterScroll}
          scrollEventThrottle={80} onScroll={({ nativeEvent }) => {
            const distance = nativeEvent.contentSize.height - nativeEvent.layoutMeasurement.height -
              nativeEvent.contentOffset.y;
            nearBottom.current = distance < 80;
            if (distance <= 8) setHasNew(false);
          }}
          onStartReached={() => {
            if (innerHistoryReady.current) void loadOlder();
          }} onStartReachedThreshold={0.15}
          ListHeaderComponent={loadingOlder ?
            <Text style={{ color: theme.colors.textMuted }}>正在加载更早动态…</Text> :
            !ready ? <Text style={{ color: theme.colors.textMuted }}>正在加载动态…</Text> :
              loadFailed ? <Text style={{ color: theme.colors.danger }}>动态加载失败，请收起后重试</Text> : null}
          ListFooterComponent={run.errorMessage && !continued ?
            <Text style={{ color: theme.colors.danger }}>{run.errorMessage}</Text> : null} />
      </View>
      {hasNew && <Pressable testID="run:activity:new" style={[styles.newActivity, {
        backgroundColor: theme.colors.surfaceAlt, borderColor: theme.colors.border,
      }, theme.shadow]} onPress={() => {
        followNextActivityCommit.current = false;
        revealNewOnNextActivityCommit.current = false;
        setHasNew(false); nearBottom.current = true;
        scroll.current?.scrollToEnd({ animated: true });
        onFollowLatest();
      }}><Text style={{ color: theme.colors.accent }}>有新消息 ↓</Text></Pressable>}
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
  model: { flex: 1, textAlign: "right" },
  activityViewport: { minHeight: 0 },
  activityList: { flex: 1 },
  content: { padding: 12, paddingBottom: 44, gap: 8 },
  commentary: { paddingVertical: 4 }, operation: { gap: 5 },
  operationHeader: { flexDirection: "row", alignItems: "center", gap: 7, minHeight: 32 },
  operationSummary: { flex: 1 }, operationRow: { flexDirection: "row", gap: 7, paddingLeft: 18 },
  operationText: { flex: 1, fontSize: 13 },
  newActivity: { position: "absolute", left: 48, right: 48, bottom: 10, alignItems: "center",
    paddingHorizontal: 16, paddingVertical: 7, borderRadius: 18,
    borderWidth: StyleSheet.hairlineWidth, elevation: 4 },
});
