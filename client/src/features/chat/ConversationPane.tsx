import type { Model } from "@codex-app-server/v2/Model";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import { FlashList, type FlashListRef } from "@shopify/flash-list";
import { forwardRef, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { ActivityIndicator, Alert, AppState, KeyboardAvoidingView, Platform, Pressable,
  StyleSheet, Text, View, type ViewProps } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import type { LocalAttachment } from "@/app-server/attachments";
import { latestExecutablePlan, type TurnPreferences } from "@/app-server/officialClient";
import { defaultTurnPreferences, normalizeTurnPreferences, turnPreferencesSummary } from "@/app-server/preferences";
import { targetKey } from "@/app-server/types";
import { Button, EmptyState } from "@/components/ui";
import { createImageLoadGate, ImageLoadGateContext } from "@/features/images/ImageLoadGate";
import { useKeyboardVisible } from "@/hooks/useKeyboardVisible";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import { previewPerf } from "@/preview/perf";
import { keyboardAvoidance } from "@/utils/keyboardAvoidance";
import { ConversationComposer } from "./ConversationComposer";
import { PendingMessagePreviews } from "./PendingMessagePreviews";
import { createFollowState, latestTurnPhase, reduceFollowState,
  shouldFollowLatest, type FollowEvent } from "./conversationFollow";
import { beginUserScroll, updateUserScroll, type UserScrollGesture }
  from "./conversationUserScroll";
import { ConversationContentRow } from "./OfficialTurn";
import { markdownBlocks } from "./MarkdownContent";
import { TurnDetails, turnWithDetails } from "./turnDetails";
import { ParameterSheet } from "./ParameterSheet";
import { ServerRequestCard } from "./ServerRequestCard";
import { ACTIVITY_TOGGLE_SCROLL_SETTLE_MS, activityToggleAllowed } from "./activityDisclosure";
import { createConversationRowProjector, type ConversationRow } from "./conversationRows";
import { anchorViewOffset, conversationScrollState,
  resolveConversationPosition, saveConversationPosition, visibleRowTop } from "./conversationPosition";

const rowKey = (row: ConversationRow) => row.key;
const rowType = (row: ConversationRow) => row.kind !== "block" ? row.kind : row.sizeHint === undefined
  ? row.block.kind : `${row.block.kind}:markdown:${row.sizeHint >= 400 ? "long" : "short"}`;
const ConversationCell = forwardRef<View, ViewProps>(function ConversationCell(props, ref) {
  // Android 需要真实 Cell 容器维护绝对定位长列表的可访问性边界。
  return <View {...props} ref={ref} collapsable={false} />;
});

const EMPTY_MODELS: Model[] = [];
const EMPTY_REQUESTS: ServerRequest[] = [];
const LIST_POSITIONING = {
  startRenderingFromBottom: true,
} as const;
const LIST_DRAW_DISTANCE = 180;
const LIST_RECYCLE_POOL_SIZE = 12;
const OLDER_LOAD_SETTLE_MS = 120;
const INITIAL_IMAGE_LOAD_DELAY_MS = 300;

export function ConversationPane({ sessionId }: { sessionId: string }) {
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  const keyboardVisible = useKeyboardVisible();
  const imageLoadGate = useMemo(() => createImageLoadGate(true), []);
  const list = useRef<FlashListRef<ConversationRow>>(null);
  const connection = useAppStore((state) => state.activeConnection);
  const record = useAppStore((state) => state.threads.find((item) => item.thread.id === sessionId));
  const modelsByTarget = useAppStore((state) => state.modelsByTarget);
  const requests = useAppStore((state) => state.pendingRequests[sessionId] ?? EMPTY_REQUESTS);
  const loadThread = useAppStore((state) => state.loadThread);
  const loadOlderThread = useAppStore((state) => state.loadOlderThread);
  const loadTurnItems = useAppStore((state) => state.loadTurnItems);
  const submitMessage = useAppStore((state) => state.submitMessage);
  const setThreadVisible = useAppStore((state) => state.setThreadVisible);
  const pendingMessages = useAppStore((state) => state.pendingMessages);
  const confirmPendingMessage = useAppStore((state) => state.confirmPendingMessage);
  const executePlan = useAppStore((state) => state.executePlan);
  const interruptThread = useAppStore((state) => state.interruptThread);
  const answerRequest = useAppStore((state) => state.answerRequest);
  const profileId = connection?.profileId ?? null;
  const details = useMemo(() => new TurnDetails((turnId, cursor, direction) => {
    if (useAppStore.getState().activeConnection?.profileId !== profileId) {
      return Promise.reject(new Error("会话所属连接已切换"));
    }
    return loadTurnItems(sessionId, turnId, cursor, direction);
  }), [loadTurnItems, profileId, sessionId]);
  const detailState = useSyncExternalStore(details.subscribe, details.snapshot);
  const projectRows = useMemo(() => ({ sessionId, profileId, project: createConversationRowProjector() }),
    [profileId, sessionId]).project;
  useEffect(() => () => details.dispose(), [details]);
  const [expandedTools, setExpandedTools] = useState<ReadonlySet<string>>(new Set());
  const [visibleStartId, setVisibleStartId] = useState<string | null>(null);
  const visibleTurns = useMemo(() => {
    const turns = record?.thread.turns ?? [];
    const start = visibleStartId ? turns.findIndex((turn) => turn.id === visibleStartId) : -1;
    return turns.slice(start < 0 ? -5 : start);
  }, [record?.thread.turns, visibleStartId]);
  useEffect(() => {
    if (visibleStartId === null && visibleTurns[0]) setVisibleStartId(visibleTurns[0].id);
  }, [visibleStartId, visibleTurns]);
  const paginated = record?.thread.historyMode === "paginated";
  const turnsRef = useRef(record?.thread.turns ?? []);
  turnsRef.current = record?.thread.turns ?? [];
  useEffect(() => {
    if (!paginated) return;
    for (const turn of visibleTurns) {
      if (turn.status === "inProgress" && !detailState.has(turn.id)) void details.load(turn);
    }
  }, [detailState, details, paginated, visibleTurns]);
  const workspaceId = record?.workspaceId ?? null;
  const positionKey = profileId ? `${profileId}:${sessionId}` : null;
  const followFrame = useRef<number | null>(null);
  const followRequest = useRef({ animated: false, force: false });
  // 官方从会话列表进入详情时总是落到最新消息；阅读锚点只在当前详情实例内
  // 由 FlashList 维护，不跨离开/重进会话恢复。
  const [preferences, setPreferences] = useState<TurnPreferences | null>(null);
  const [beforeSheet, setBeforeSheet] = useState<TurnPreferences | null>(null);
  const [showParameters, setShowParameters] = useState(false);
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [olderError, setOlderError] = useState<string | null>(null);
  const [showScrollToLatest, setShowScrollToLatest] = useState(false);
  const [positionRestored, setPositionRestored] = useState(false);
  const rowsRef = useRef<ConversationRow[]>([]);
  const pinnedToLatest = useRef(true);
  const showScrollToLatestRef = useRef(false);
  const historyPagingReady = useRef(false);
  const olderLoadRequested = useRef(false);
  const momentumScrolling = useRef(false);
  const userDragging = useRef(false);
  const interactionBlocked = useRef(false);
  const interactionSettleTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const disclosureSettleTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const followState = useRef(createFollowState());
  const latestPhase = useRef<ReturnType<typeof latestTurnPhase>>("idle");
  const activityToggleBlockedUntil = useRef(0);
  const olderLoadTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const activeSessionId = useRef(sessionId);
  const scrollOffset = useRef(0);
  const touchStartY = useRef<number | null>(null);
  const userScrollGesture = useRef<UserScrollGesture | null>(null);
  activeSessionId.current = sessionId;
  const models = profileId
    ? modelsByTarget[targetKey(profileId, workspaceId)] ?? EMPTY_MODELS
    : EMPTY_MODELS;
  const fallbackPreferences = useMemo(() => defaultTurnPreferences(models), [models]);
  const runtimePreferences = record?.preferences;
  useEffect(() => {
    // 运行时模式和权限是权威状态，防止恢复后再次提交过期设置。
    if (runtimePreferences) setPreferences(current => current
      ? { ...current, collaborationMode: runtimePreferences.collaborationMode,
        permissions: runtimePreferences.permissions, runtimePermissions: runtimePreferences.runtimePermissions } : current);
  }, [runtimePreferences]);
  const resolvedPreferences = (() => {
    const current = preferences ?? record?.preferences ?? fallbackPreferences;
    return current ? normalizeTurnPreferences(current) : current;
  })();
  const activeTurnId = [...(record?.thread.turns ?? [])].reverse()
    .find((turn) => turn.status === "inProgress")?.id ?? null;
  const activeTurn = [...(record?.thread.turns ?? [])].reverse()
    .find((turn) => turn.status === "inProgress") ?? null;
  const setScrollToLatestVisible = useCallback((visible: boolean) => {
    if (showScrollToLatestRef.current === visible) return;
    showScrollToLatestRef.current = visible;
    setShowScrollToLatest(visible);
  }, []);
  const canToggleActivity = useCallback(() =>
    activityToggleAllowed(activityToggleBlockedUntil.current), []);
  const dispatchFollow = useCallback((event: FollowEvent) => {
    followState.current = reduceFollowState(followState.current, event);
  }, []);

  useEffect(() => {
    setVisibleStartId(null);
    setExpandedTools(new Set());
    let canceled = false;
    setLoading(true);
    setError(null);
    void loadThread(sessionId).catch((cause) => {
      if (!canceled) setError(cause instanceof Error ? cause.message : "无法读取官方会话历史");
    }).finally(() => { if (!canceled) setLoading(false); });
    return () => { canceled = true; };
  }, [loadThread, profileId, sessionId]);

  useEffect(() => {
    if (!record) return;
    const confirmed = new Set(record.thread.turns.flatMap((turn) => turn.items.flatMap((item) =>
      item.type === "userMessage" && item.clientId ? [item.clientId] : [])));
    for (const pending of pendingMessages) {
      if (pending.threadId === sessionId && confirmed.has(pending.clientMessageId)) {
        void confirmPendingMessage(pending.clientMessageId);
      }
    }
  }, [confirmPendingMessage, pendingMessages, record, sessionId]);

  const retryLoad = useCallback(() => {
    setLoading(true);
    setError(null);
    void loadThread(sessionId).catch((cause) => {
      setError(cause instanceof Error ? cause.message : "无法读取官方会话历史");
    }).finally(() => setLoading(false));
  }, [loadThread, sessionId]);

  useEffect(() => {
    const applyVisibility = (state: string) => setThreadVisible(sessionId, state === "active");
    applyVisibility(AppState.currentState);
    const subscription = AppState.addEventListener("change", applyVisibility);
    return () => {
      subscription.remove();
      setThreadVisible(sessionId, false);
    };
  }, [sessionId, setThreadVisible]);

  useEffect(() => {
    pinnedToLatest.current = true;
    historyPagingReady.current = false;
    olderLoadRequested.current = false;
    momentumScrolling.current = false;
    userDragging.current = false;
    interactionBlocked.current = false;
    followState.current = createFollowState();
    latestPhase.current = "idle";
    activityToggleBlockedUntil.current = 0;
    if (interactionSettleTimer.current) clearTimeout(interactionSettleTimer.current);
    interactionSettleTimer.current = null;
    if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
    olderLoadTimer.current = null;
    scrollOffset.current = 0;
    userScrollGesture.current = null;
    imageLoadGate.setBlocked(true);
    setScrollToLatestVisible(false);
    setPositionRestored(false);
    setLoadingOlder(false);
    setOlderError(null);
    let imageLoadTimer: ReturnType<typeof setTimeout>;
    const releaseImagesWhenIdle = () => {
      if (interactionBlocked.current) {
        imageLoadTimer = setTimeout(releaseImagesWhenIdle, OLDER_LOAD_SETTLE_MS);
        return;
      }
      imageLoadGate.setBlocked(false);
    };
    imageLoadTimer = setTimeout(releaseImagesWhenIdle, INITIAL_IMAGE_LOAD_DELAY_MS);
    return () => clearTimeout(imageLoadTimer);
  }, [imageLoadGate, positionKey, setScrollToLatestVisible]);

  useEffect(() => () => {
    if (followFrame.current !== null) cancelAnimationFrame(followFrame.current);
    if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
    if (interactionSettleTimer.current) clearTimeout(interactionSettleTimer.current);
    if (disclosureSettleTimer.current) clearTimeout(disclosureSettleTimer.current);
  }, []);

  const paintMeasurement = useMemo(() => ({ key: positionKey, readyAt: null as number | null }), [positionKey]);
  if (paintMeasurement.readyAt === null && visibleTurns.length > 0) paintMeasurement.readyAt = performance.now();
  const rows = useMemo(() => projectRows(visibleTurns, requests,
    { details: detailState, expandedTools, paginated, splitMarkdown: markdownBlocks }),
  [detailState, expandedTools, paginated, projectRows, requests, visibleTurns]);
  rowsRef.current = rows;
  const restorePosition = useMemo(() => resolveConversationPosition(null,
    rows.map((row) => row.key)), [rows]);
  const hasRestorableAnchor = restorePosition.kind === "anchor";
  const hasCachedHistory = (record?.thread.turns.length ?? 0) > visibleTurns.length;
  const hasMoreHistory = hasCachedHistory || record?.history.kind === "loaded" &&
    !record.history.hasLoadedOldest && record.history.olderCursor !== null;

  const scheduleFollowLatest = useCallback((animated = false, force = false) => {
    followRequest.current = { animated, force: followRequest.current.force || force };
    if (followFrame.current !== null) return;
    followFrame.current = requestAnimationFrame(() => {
      followFrame.current = null;
      const request = followRequest.current;
      followRequest.current = { animated: false, force: false };
      if ((!request.force && !positionRestored) || (!request.force && interactionBlocked.current) ||
        (!request.force && !shouldFollowLatest(followState.current))) return;
      list.current?.scrollToEnd({ animated: request.animated });
    });
  }, [positionRestored]);

  useEffect(() => {
    const nextPhase = latestTurnPhase(activeTurn);
    const previousPhase = latestPhase.current;
    latestPhase.current = nextPhase;
    if (previousPhase !== nextPhase) dispatchFollow({ type: "latest_turn_phase_changed",
      previousLatestTurnPhase: previousPhase, latestTurnPhase: nextPhase });
    if (shouldFollowLatest(followState.current) && !interactionBlocked.current) {
      scheduleFollowLatest();
    }
  }, [activeTurn, dispatchFollow, rows, scheduleFollowLatest]);

  const saveVisiblePosition = useCallback((offsetY?: number) => {
    if (!positionKey) return;
    if (pinnedToLatest.current) {
      saveConversationPosition(positionKey, { kind: "latest" });
      return;
    }
    const current = list.current;
    if (!current) return;
    const index = current.getFirstVisibleIndex();
    const row = rowsRef.current[index];
    const layout = current.getLayout(index);
    if (!row || !layout) return;
    const scrollOffset = offsetY ?? current.getAbsoluteLastScrollOffset();
    saveConversationPosition(positionKey,
      { kind: "anchor", rowKey: row.key,
        topOffset: visibleRowTop(layout.y, current.getFirstItemOffset(), scrollOffset) });
  }, [positionKey]);

  const followLatest = useCallback((animated: boolean) => {
    dispatchFollow({ type: "scroll_to_bottom", latestTurnPhase: latestPhase.current });
    pinnedToLatest.current = true;
    interactionBlocked.current = false;
    userDragging.current = false;
    momentumScrolling.current = false;
    setScrollToLatestVisible(false);
    if (positionKey) saveConversationPosition(positionKey, { kind: "latest" });
    scheduleFollowLatest(animated, true);
  }, [dispatchFollow, positionKey, scheduleFollowLatest, setScrollToLatestVisible]);

  const handleDisclosureChange = useCallback(() => {
    pinnedToLatest.current = false;
    dispatchFollow({ type: "scroll_distance_changed", distanceFromBottomPx: 100,
      latestTurnPhase: latestPhase.current });
    interactionBlocked.current = true;
    if (disclosureSettleTimer.current) clearTimeout(disclosureSettleTimer.current);
    disclosureSettleTimer.current = setTimeout(() => {
      disclosureSettleTimer.current = null;
      interactionBlocked.current = false;
      if (shouldFollowLatest(followState.current)) scheduleFollowLatest();
    }, ACTIVITY_TOGGLE_SCROLL_SETTLE_MS);
  }, [dispatchFollow, scheduleFollowLatest]);

  const finishUserInteraction = useCallback(() => {
    interactionBlocked.current = false;
    imageLoadGate.setBlocked(false);
    if (pinnedToLatest.current) {
      dispatchFollow({ type: "user_reached_bottom", latestTurnPhase: latestPhase.current });
    }
    if (shouldFollowLatest(followState.current)) {
      list.current?.scrollToEnd({ animated: false });
    }
    userScrollGesture.current = null;
  }, [dispatchFollow, imageLoadGate]);

  const settleUserInteraction = useCallback(() => {
    if (interactionSettleTimer.current) clearTimeout(interactionSettleTimer.current);
    interactionSettleTimer.current = setTimeout(() => {
      interactionSettleTimer.current = null;
      if (!momentumScrolling.current && !userDragging.current) finishUserInteraction();
    }, 120);
  }, [finishUserInteraction]);

  const loadOlder = useCallback(async () => {
    olderLoadRequested.current = false;
    if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
    olderLoadTimer.current = null;
    if (!hasMoreHistory || loadingOlder) return;
    historyPagingReady.current = false;
    setLoadingOlder(true);
    setOlderError(null);
    try {
      const first = visibleTurns[0]?.id;
      if (!hasCachedHistory) await loadOlderThread(sessionId);
      const turns = useAppStore.getState().threads.find((item) => item.thread.id === sessionId)?.thread.turns ?? [];
      const index = turns.findIndex((turn) => turn.id === first);
      if (activeSessionId.current === sessionId) setVisibleStartId(turns[Math.max(0, index - 5)]?.id ?? null);
    } catch (cause) {
      setOlderError(cause instanceof Error ? cause.message : "无法加载更早历史");
    } finally {
      setLoadingOlder(false);
    }
  }, [hasCachedHistory, hasMoreHistory, loadOlderThread, loadingOlder, sessionId, visibleTurns]);

  const requestOlderLoad = useCallback(() => {
    if (!historyPagingReady.current || !hasMoreHistory || loadingOlder) return;
    olderLoadRequested.current = true;
  }, [hasMoreHistory, loadingOlder]);

  const flushOlderLoad = useCallback(() => {
    if (!olderLoadRequested.current || momentumScrolling.current) return;
    olderLoadRequested.current = false;
    void loadOlder();
  }, [loadOlder]);

  const scheduleOlderLoad = useCallback(() => {
    if (!olderLoadRequested.current) return;
    if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
    olderLoadTimer.current = setTimeout(() => {
      olderLoadTimer.current = null;
      flushOlderLoad();
    }, OLDER_LOAD_SETTLE_MS);
  }, [flushOlderLoad]);

  const send = useCallback(async (text: string, attachments: LocalAttachment[]): Promise<boolean> => {
    if (!resolvedPreferences || (!text.trim() && attachments.length === 0)) return false;
    setSending(true);
    try {
      const sent = await submitMessage(sessionId, text, attachments, resolvedPreferences);
      if (!sent) throw new Error("服务器未确认发送");
      followLatest(false);
      return true;
    } catch (cause) {
      Alert.alert("发送失败", cause instanceof Error ? cause.message : "请检查连接后重试");
      return false;
    } finally {
      setSending(false);
    }
  }, [followLatest, resolvedPreferences, sessionId, submitMessage]);

  const stop = useCallback(async () => {
    if (!activeTurnId || stopping) return;
    setStopping(true);
    try {
      await interruptThread(sessionId);
    } catch (cause) {
      Alert.alert("停止失败", cause instanceof Error ? cause.message : "请重试");
    } finally {
      setStopping(false);
    }
  }, [activeTurnId, interruptThread, sessionId, stopping]);

  const toggleTurn = useCallback((turnId: string) => {
    if (!canToggleActivity()) return;
    const turn = turnsRef.current.find((item) => item.id === turnId);
    if (!turn) return;
    handleDisclosureChange();
    details.toggle(turn, paginated);
  }, [canToggleActivity, details, handleDisclosureChange, paginated]);
  const loadDetails = useCallback((turnId: string) => {
    const turn = turnsRef.current.find((item) => item.id === turnId);
    if (!turn) return;
    handleDisclosureChange();
    void details.load(turn);
  }, [details, handleDisclosureChange]);
  const toggleTool = useCallback((key: string) => {
    if (!canToggleActivity()) return;
    handleDisclosureChange();
    setExpandedTools((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  }, [canToggleActivity, handleDisclosureChange]);

  const renderRow = useCallback(({ item }: { item: ConversationRow }) => item.kind === "request"
      ? <ServerRequestCard request={item.request} onAnswer={(result) => {
        try {
          if (!answerRequest(sessionId, item.request.id, result)) {
            Alert.alert("请求已经处理", "这个请求已由其他连接回答，正在刷新官方状态。");
            void loadThread(sessionId);
          }
        } catch (cause) {
          Alert.alert("回答发送失败", cause instanceof Error ? cause.message : "请检查连接后重试");
        }
      }} />
      : <ConversationContentRow row={item} profileId={profileId ?? "unavailable"}
        threadId={sessionId} expandedTool={expandedTools.has(item.key)}
        onToggleTurn={toggleTurn} onLoadDetails={loadDetails} onToggleTool={toggleTool}
        onDisclosureChange={handleDisclosureChange} />,
    [answerRequest, expandedTools, handleDisclosureChange, loadDetails, loadThread, profileId,
      sessionId, toggleTool, toggleTurn]);

  if (!connection || !record) {
    return <EmptyState title="会话不可用" detail="它可能已被归档、移除，或属于其他连接。" />;
  }
  if (!loading && error && record.thread.turns.length === 0) {
    return <EmptyState title="无法加载会话" detail={error}
      action={<Button title="重试" onPress={retryLoad} />} />;
  }

  const plan = latestExecutablePlan({ ...record.thread,
    turns: visibleTurns.map((turn) => paginated
      ? turnWithDetails(turn, detailState.get(turn.id)) : turn) });
  return <ImageLoadGateContext.Provider value={imageLoadGate}>
    <KeyboardAvoidingView {...keyboardAvoidance(Platform.OS, insets.top, keyboardVisible)}
    style={styles.container}>
    <View style={styles.messageArea}>
      <FlashList key={positionKey} ref={list} testID="messages:list" data={rows}
        style={[styles.messageList, { opacity: hasRestorableAnchor && !positionRestored ? 0 : 1 }]}
        drawDistance={LIST_DRAW_DISTANCE}
        maxItemsInRecyclePool={LIST_RECYCLE_POOL_SIZE}
        initialScrollIndex={restorePosition.kind === "anchor" ? restorePosition.index : undefined}
        keyExtractor={rowKey}
        getItemType={rowType}
        keyboardShouldPersistTaps="handled"
        renderItem={renderRow}
        CellRendererComponent={ConversationCell}
        contentContainerStyle={styles.list}
        maintainVisibleContentPosition={LIST_POSITIONING}
        scrollEventThrottle={100}
        onLayout={() => {
          // Composer 完成首次布局或键盘改变可视高度时，内容高度可能不变。
          if (positionRestored && pinnedToLatest.current && !interactionBlocked.current) {
            scheduleFollowLatest();
          }
        }}
        onTouchStart={({ nativeEvent }) => { touchStartY.current = nativeEvent.pageY; }}
        onTouchEnd={({ nativeEvent }) => {
          // 短摘要不足一屏时原生 ScrollView 不产生拖动事件，也必须支持下拉旧页。
          if (touchStartY.current !== null && nativeEvent.pageY - touchStartY.current > 40 &&
            scrollOffset.current <= 8 && hasMoreHistory && !loadingOlder) {
            pinnedToLatest.current = false;
            dispatchFollow({ type: "scroll_distance_changed", distanceFromBottomPx: 100,
              latestTurnPhase: latestPhase.current });
            void loadOlder();
          }
          touchStartY.current = null;
        }}
        onContentSizeChange={() => {
          // 数据更新和披露展开会先触发 React 更新，随后 FlashList 才完成真实高度测量。
          // 跟随态在这个布局时点补滚到底，避免多行命令的最后一行卡在 composer 上沿。
          if (!positionRestored || interactionBlocked.current || !pinnedToLatest.current) return;
          scheduleFollowLatest(false, true);
        }}
        onScroll={({ nativeEvent }) => {
          const state = conversationScrollState(nativeEvent.contentSize.height,
            nativeEvent.layoutMeasurement.height, nativeEvent.contentOffset.y);
          scrollOffset.current = nativeEvent.contentOffset.y;
          setScrollToLatestVisible(state.showLatest);
          if (userDragging.current || momentumScrolling.current) {
            const gesture = userScrollGesture.current ??
              beginUserScroll(nativeEvent.contentOffset.y);
            const update = updateUserScroll(gesture, nativeEvent.contentOffset.y,
              state.distanceFromBottom);
            userScrollGesture.current = update.gesture;
            if (update.intent === "away") {
              pinnedToLatest.current = false;
              dispatchFollow({ type: "scroll_distance_changed",
                distanceFromBottomPx: state.distanceFromBottom,
                latestTurnPhase: latestPhase.current });
            } else if (update.intent === "bottom") {
              pinnedToLatest.current = true;
              dispatchFollow({ type: "user_reached_bottom",
                latestTurnPhase: latestPhase.current });
            }
          }
        }}
        onScrollBeginDrag={({ nativeEvent }) => {
          imageLoadGate.setBlocked(true);
          momentumScrolling.current = false;
          userDragging.current = true;
          interactionBlocked.current = true;
          userScrollGesture.current = beginUserScroll(nativeEvent.contentOffset.y);
          if (interactionSettleTimer.current) clearTimeout(interactionSettleTimer.current);
          interactionSettleTimer.current = null;
          activityToggleBlockedUntil.current = Number.POSITIVE_INFINITY;
          if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
          olderLoadTimer.current = null;
          historyPagingReady.current = true;
          if (scrollOffset.current <= 8) requestOlderLoad();
        }}
        onScrollEndDrag={() => {
          userDragging.current = false;
          activityToggleBlockedUntil.current = Date.now() + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS;
          saveVisiblePosition();
          scheduleOlderLoad();
          settleUserInteraction();
        }}
        onMomentumScrollBegin={() => {
          imageLoadGate.setBlocked(true);
          momentumScrolling.current = true;
          interactionBlocked.current = true;
          if (interactionSettleTimer.current) clearTimeout(interactionSettleTimer.current);
          interactionSettleTimer.current = null;
          activityToggleBlockedUntil.current = Number.POSITIVE_INFINITY;
          if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
          olderLoadTimer.current = null;
        }}
        onMomentumScrollEnd={() => {
          momentumScrolling.current = false;
          finishUserInteraction();
          activityToggleBlockedUntil.current = Date.now() + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS;
          saveVisiblePosition();
          flushOlderLoad();
        }}
        onStartReached={() => {
          requestOlderLoad();
        }}
        onStartReachedThreshold={0.2}
        onLoad={({ elapsedTimeInMs }) => {
          previewPerf("conversation.firstPaint", { sessionId, elapsedTimeInMs,
            readyToListLoadMs: paintMeasurement.readyAt === null ? null : performance.now() - paintMeasurement.readyAt,
            rows: rowsRef.current.length, turns: visibleTurns.length });
          if (activeSessionId.current !== sessionId) return;
          if (restorePosition.kind !== "anchor") {
            setPositionRestored(true);
            followLatest(false);
            return;
          }
          const restore = list.current?.scrollToIndex({ index: restorePosition.index, animated: false,
            viewPosition: 0, viewOffset: anchorViewOffset(restorePosition.topOffset) });
          if (!restore) {
            followLatest(false);
            setPositionRestored(true);
            return;
          }
          void restore.catch(() => followLatest(false)).finally(() => {
            if (activeSessionId.current === sessionId) setPositionRestored(true);
          });
        }}
        ListHeaderComponent={loadingOlder
          ? <View style={styles.historyStatus}><ActivityIndicator size="small"
            color={theme.colors.textMuted} /></View>
          : olderError ? <Pressable testID="history:retry" style={styles.historyStatus}
            onPress={() => void loadOlder()}>
            <Text style={{ color: theme.colors.danger }}>加载更早消息失败，点按重试</Text>
          </Pressable> : hasMoreHistory ? <Pressable testID="history:load-older"
            style={styles.historyStatus} onPress={() => void loadOlder()}>
            <Text style={{ color: theme.colors.textMuted }}>加载更早消息</Text>
          </Pressable> : null}
        ListFooterComponent={<>
          {plan && <View style={styles.planAction}>
            <Button testID="plan:execute" title="执行计划" loading={sending}
              disabled={!resolvedPreferences || sending}
              onPress={() => void (async () => {
                if (!resolvedPreferences) return;
                setSending(true);
                try {
                  await executePlan(sessionId, resolvedPreferences, plan);
                  followLatest(false);
                } catch (cause) { Alert.alert("无法执行计划",
                  cause instanceof Error ? cause.message : "请刷新后重试"); }
                finally { setSending(false); }
              })()} />
          </View>}
        </>} />
      {loading ? <View pointerEvents="none" testID="messages:syncing"
        style={[styles.syncStatus, { backgroundColor: theme.colors.surface,
          borderColor: theme.colors.border }]}>
        <ActivityIndicator size="small" color={theme.colors.textMuted} />
        <Text style={[styles.syncStatusText, { color: theme.colors.textMuted }]}>正在同步</Text>
      </View> : error ? <Pressable testID="messages:sync-retry" onPress={retryLoad}
        style={[styles.syncStatus, { backgroundColor: theme.colors.surface,
          borderColor: theme.colors.border }]}>
        <Text style={[styles.syncStatusText, { color: theme.colors.danger }]}>同步失败，点按重试</Text>
      </Pressable> : null}
      {showScrollToLatest && <Pressable testID="chat:scroll-to-latest"
        accessibilityRole="button" accessibilityLabel="回到最新消息"
        onPress={() => followLatest(true)} style={({ pressed }) => [styles.scrollToLatest, {
          backgroundColor: theme.colors.surface, borderColor: theme.colors.border,
          opacity: pressed ? 0.72 : 1,
        }, theme.shadow]}>
        <Text style={[styles.scrollToLatestIcon, { color: theme.colors.text }]}>↓</Text>
      </Pressable>}
    </View>
    <PendingMessagePreviews items={pendingMessages.filter((item) => item.threadId === sessionId)} />
    <ConversationComposer key={`${profileId}:${sessionId}`} profileId={profileId!}
      sessionId={sessionId} preferences={preferences} onDraftPreferences={setPreferences}
      onSendMessage={send} onParameters={() => {
        if (!resolvedPreferences) {
          Alert.alert("参数暂不可用", "Codex App Server 没有返回可用模型。");
          return;
        }
        setBeforeSheet(preferences); setPreferences(resolvedPreferences); setShowParameters(true);
      }} onStop={() => void stop()} active={activeTurnId !== null}
      sending={sending} stopping={stopping}
      parameterLabel={resolvedPreferences
        ? turnPreferencesSummary(resolvedPreferences)
        : "参数暂不可用"} disabled={sending} />
    {resolvedPreferences && <ParameterSheet visible={showParameters} models={models}
      value={resolvedPreferences} onChange={setPreferences} onClose={() => setShowParameters(false)}
      onCancel={() => { setPreferences(beforeSheet); setShowParameters(false); }} />}
    </KeyboardAvoidingView>
  </ImageLoadGateContext.Provider>;
}

const styles = StyleSheet.create({
  container: { flex: 1 },
  messageArea: { flex: 1, minHeight: 0 },
  messageList: { flex: 1 },
  list: { paddingVertical: 8 },
  historyStatus: { minHeight: 40, alignItems: "center", justifyContent: "center",
    paddingHorizontal: 16 },
  planAction: { paddingHorizontal: 16, paddingTop: 8 },
  syncStatus: { position: "absolute", top: 8, alignSelf: "center", zIndex: 2,
    minHeight: 30, borderWidth: StyleSheet.hairlineWidth, borderRadius: 15,
    flexDirection: "row", alignItems: "center", gap: 7, paddingHorizontal: 11 },
  syncStatusText: { fontFamily: "Inter_400Regular", fontSize: 12, lineHeight: 18 },
  outbox: { minHeight: 34, marginHorizontal: 12, marginBottom: 6, borderRadius: 8,
    paddingHorizontal: 10, alignItems: "center", justifyContent: "center" },
  scrollToLatest: { position: "absolute", right: 16, bottom: 12, width: 42, height: 42,
    borderRadius: 21, borderWidth: StyleSheet.hairlineWidth, alignItems: "center",
    justifyContent: "center", elevation: 5 },
  scrollToLatestIcon: { fontFamily: "Inter_500Medium", fontSize: 22, lineHeight: 26 },
});
