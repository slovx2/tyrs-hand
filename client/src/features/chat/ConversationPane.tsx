import type { Model } from "@codex-app-server/v2/Model";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import type { Turn } from "@codex-app-server/v2/Turn";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Alert, FlatList, KeyboardAvoidingView, Platform, Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import type { LocalAttachment } from "@/app-server/attachments";
import { latestCompletedPlan, type TurnPreferences } from "@/app-server/officialClient";
import { defaultTurnPreferences } from "@/app-server/preferences";
import { targetKey } from "@/app-server/types";
import { Button, EmptyState } from "@/components/ui";
import { clearDraft, loadDraft, saveDraft } from "@/db/drafts";
import { useKeyboardVisible } from "@/hooks/useKeyboardVisible";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import { keyboardAvoidance } from "@/utils/keyboardAvoidance";
import { ChatComposer } from "./ChatComposer";
import { OfficialThreadItem } from "./OfficialThreadItem";
import { ParameterSheet } from "./ParameterSheet";
import { ServerRequestCard } from "./ServerRequestCard";

type Row =
  | { kind: "item"; key: string; item: ThreadItem }
  | { kind: "error"; key: string; turn: Turn }
  | { kind: "request"; key: string; request: ServerRequest };

const EMPTY_MODELS: Model[] = [];
const EMPTY_REQUESTS: ServerRequest[] = [];

export function ConversationPane({ sessionId }: { sessionId: string }) {
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  const keyboardVisible = useKeyboardVisible();
  const list = useRef<FlatList<Row>>(null);
  const connection = useAppStore((state) => state.activeConnection);
  const record = useAppStore((state) => state.threads.find((item) => item.thread.id === sessionId));
  const modelsByTarget = useAppStore((state) => state.modelsByTarget);
  const requests = useAppStore((state) => state.pendingRequests[sessionId] ?? EMPTY_REQUESTS);
  const loadThread = useAppStore((state) => state.loadThread);
  const submitMessage = useAppStore((state) => state.submitMessage);
  const executePlan = useAppStore((state) => state.executePlan);
  const interruptThread = useAppStore((state) => state.interruptThread);
  const answerRequest = useAppStore((state) => state.answerRequest);
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const [preferences, setPreferences] = useState<TurnPreferences | null>(null);
  const [beforeSheet, setBeforeSheet] = useState<TurnPreferences | null>(null);
  const [showParameters, setShowParameters] = useState(false);
  const [draftReady, setDraftReady] = useState(false);
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const profileId = connection?.profileId ?? null;
  const workspaceId = record?.workspaceId ?? null;
  const models = profileId
    ? modelsByTarget[targetKey(profileId, workspaceId)] ?? EMPTY_MODELS
    : EMPTY_MODELS;
  const fallbackPreferences = useMemo(() => defaultTurnPreferences(models), [models]);
  const resolvedPreferences = preferences ?? fallbackPreferences;
  const draftScope = `thread:${sessionId}`;

  useEffect(() => {
    setLoading(true);
    setError(null);
    void loadThread(sessionId).catch((cause) => setError(
      cause instanceof Error ? cause.message : "无法读取官方会话历史"))
      .finally(() => setLoading(false));
  }, [loadThread, sessionId]);

  useEffect(() => {
    if (!profileId) return;
    let canceled = false;
    setDraftReady(false);
    setText("");
    setAttachments([]);
    setPreferences(null);
    void loadDraft(profileId, draftScope).then((draft) => {
      if (canceled) return;
      if (draft) {
        setText(draft.text);
        setAttachments(draft.attachments);
        setPreferences(draft.settings);
      }
      setDraftReady(true);
    });
    return () => { canceled = true; };
  }, [draftScope, profileId]);

  useEffect(() => {
    if (!profileId || !draftReady) return;
    const timer = setTimeout(() => void saveDraft(profileId, draftScope,
      { text, attachments, settings: preferences }), 150);
    return () => clearTimeout(timer);
  }, [attachments, draftReady, draftScope, preferences, profileId, text]);

  const rows = useMemo<Row[]>(() => {
    const result: Row[] = [];
    for (const turn of record?.thread.turns ?? []) {
      for (const item of turn.items) {
        result.push({ kind: "item", key: `${turn.id}:${item.id}`, item });
      }
      if (turn.status === "failed" && turn.error) {
        result.push({ kind: "error", key: `${turn.id}:error`, turn });
      }
    }
    for (const request of requests) {
      result.push({ kind: "request", key: `request:${String(request.id)}`, request });
    }
    return result;
  }, [record?.thread.turns, requests]);

  const send = useCallback(async () => {
    if (!resolvedPreferences || (!text.trim() && attachments.length === 0)) return;
    setSending(true);
    try {
      await submitMessage(sessionId, text, attachments, resolvedPreferences);
      if (profileId) await clearDraft(profileId, draftScope);
      setText("");
      setAttachments([]);
    } catch (cause) {
      Alert.alert("发送状态未确认", cause instanceof Error ? cause.message : "请刷新后重试");
    } finally {
      setSending(false);
    }
  }, [attachments, draftScope, profileId, resolvedPreferences, sessionId, submitMessage, text]);

  if (!connection || !record) {
    return <EmptyState title="会话不可用" detail="它可能已被归档、移除，或属于其他连接。" />;
  }
  if (loading && record.thread.turns.length === 0) {
    return <EmptyState title="正在读取会话" detail="正在从 Codex App Server 加载官方历史。" />;
  }
  if (error && record.thread.turns.length === 0) {
    return <EmptyState title="无法加载会话" detail={error}
      action={<Button title="重试" onPress={() => void loadThread(sessionId)} />} />;
  }

  const activeTurn = [...record.thread.turns].reverse().find((turn) => turn.status === "inProgress");
  const plan = latestCompletedPlan(record.thread);
  return <KeyboardAvoidingView {...keyboardAvoidance(Platform.OS, insets.top, keyboardVisible)}
    style={styles.container}>
    <FlatList ref={list} testID="messages:list" data={rows} keyExtractor={(item) => item.key}
      keyboardShouldPersistTaps="handled"
      renderItem={({ item }) => item.kind === "item"
        ? <OfficialThreadItem item={item.item} />
        : item.kind === "request"
          ? <ServerRequestCard request={item.request} onAnswer={(result) => {
            if (!answerRequest(sessionId, item.request.id, result)) {
              Alert.alert("请求已经处理", "这个请求已由其他连接回答，正在刷新官方状态。");
              void loadThread(sessionId);
            }
          }} />
          : <View testID="turn:error" style={styles.error}>
            <Text selectable style={{ color: theme.colors.danger }}>
            {item.turn.error?.message ?? "本轮执行失败"}
          </Text></View>}
      contentContainerStyle={styles.list}
      onContentSizeChange={() => list.current?.scrollToEnd({ animated: false })}
      ListFooterComponent={<>
        {plan && <View style={styles.planAction}>
          <Button testID="plan:execute" title="执行计划" loading={sending}
            disabled={!resolvedPreferences || sending}
            onPress={() => void (async () => {
              if (!resolvedPreferences) return;
              setSending(true);
              try { await executePlan(sessionId, resolvedPreferences); }
              catch (cause) { Alert.alert("无法执行计划",
                cause instanceof Error ? cause.message : "请刷新后重试"); }
              finally { setSending(false); }
            })()} />
        </View>}
        {activeTurn && <Pressable testID="session:stop" style={styles.stop}
          onPress={() => void interruptThread(sessionId).catch((cause) => Alert.alert("停止失败",
            cause instanceof Error ? cause.message : "请重试"))}>
          <Text style={{ color: theme.colors.danger }}>停止当前 Turn</Text>
        </Pressable>}
      </>} />
    <ChatComposer value={text} onChange={setText} attachments={attachments}
      onAttachmentsChange={setAttachments} onParameters={() => {
        if (!resolvedPreferences) {
          Alert.alert("参数暂不可用", "Codex App Server 没有返回可用模型。");
          return;
        }
        setBeforeSheet(preferences); setPreferences(resolvedPreferences); setShowParameters(true);
      }} onSend={() => void send()} sending={sending}
      parameterLabel={resolvedPreferences
        ? `${resolvedPreferences.model} · ${resolvedPreferences.effort ?? "默认"} · ${resolvedPreferences.collaborationMode === "plan" ? "先做计划" : "直接执行"}`
        : "参数暂不可用"} />
    {resolvedPreferences && <ParameterSheet visible={showParameters} models={models}
      value={resolvedPreferences} onChange={setPreferences} onClose={() => setShowParameters(false)}
      onCancel={() => { setPreferences(beforeSheet); setShowParameters(false); }} />}
  </KeyboardAvoidingView>;
}

const styles = StyleSheet.create({
  container: { flex: 1 },
  list: { paddingVertical: 8 },
  error: { marginHorizontal: 16, paddingVertical: 10 },
  planAction: { paddingHorizontal: 16, paddingTop: 8 },
  stop: { alignSelf: "center", minHeight: 44, justifyContent: "center", paddingHorizontal: 16 },
});
