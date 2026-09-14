import * as Linking from "expo-linking";
import { router, useLocalSearchParams } from "expo-router";
import { useEffect, useRef, useState } from "react";
import { Modal, Pressable, ScrollView, StyleSheet, Switch, Text, View } from "react-native";
import { RTCPeerConnection, mediaDevices, type MediaStream } from "react-native-webrtc";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { clearLiveConversationMessages, closeLiveSession, createLiveConversation, createLiveSession, getLiveConversation, listLiveMessages, listLiveWorkerProjects, listLiveWorkerSessions, recoverLiveSession, resetLiveConversationHistory, updateLiveConversation, type LiveConversation } from "@/api/live";
import { Dropdown } from "@/components/Dropdown";
import { Screen } from "@/components/ui";
import { LiveMark } from "@/features/live/LiveMark";
import { LiveVoicePicker } from "@/features/live/LiveVoicePicker";
import { initialLiveTranscriptState, reduceLiveTranscript, visibleLiveTranscript, type LiveTranscriptState } from "@/features/live/transcriptReducer";
import { defaultLiveVoice, findLiveVoice, type LiveVoice } from "@/features/live/voices";
import { loadLiveConnectionSoundsEnabled, loadLiveConversationId, saveLiveConversationId,
  saveLiveConnectionSoundsEnabled } from "@/db/settings";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import { playLiveConnectionSound } from "@/features/live/liveSounds";
import { isLiveWakeParam, shouldPlaySessionStartedSound } from "@/features/live/liveWake";

const liveIceConfiguration = {
  iceServers: [{ urls: "stun:stun.l.google.com:19302" }],
};

type Transcript = { role: "user" | "assistant"; text: string };

async function waitForIceGathering(connection: RTCPeerConnection): Promise<void> {
  if (connection.iceGatheringState === "complete") return;
  const eventConnection = connection as unknown as { onicegatheringstatechange: (() => void) | null };
  await new Promise<void>((resolve) => {
    const timeout = setTimeout(() => { eventConnection.onicegatheringstatechange = null; resolve(); }, 5000);
    eventConnection.onicegatheringstatechange = () => {
      if (connection.iceGatheringState === "complete") {
        clearTimeout(timeout);
        eventConnection.onicegatheringstatechange = null;
        resolve();
      }
    };
  });
}

export default function LiveScreen() {
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  const params = useLocalSearchParams<Record<string, string | string[]>>();
  const ready = useAppStore((state) => state.ready);
  const connection = useAppStore((state) => state.activeConnection);
  const peer = useRef<RTCPeerConnection | null>(null);
  const channel = useRef<ReturnType<RTCPeerConnection["createDataChannel"]> | null>(null);
  const stream = useRef<MediaStream | null>(null);
  const [conversation, setConversation] = useState<LiveConversation | null>(null);
  const [conversationLoaded, setConversationLoaded] = useState(false);
  const [conversationLoadError, setConversationLoadError] = useState<string | null>(null);
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [state, setState] = useState<LiveTranscriptState>(initialLiveTranscriptState);
  const [status, setStatus] = useState("未连接");
  const [error, setError] = useState<string | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [soundsEnabled, setSoundsEnabled] = useState(true);
  const [wakeRequestCount, setWakeRequestCount] = useState(0);
  const [workerId, setWorkerId] = useState("");
  const [mode, setMode] = useState<"bind" | "new">("bind");
  const [bindSessionId, setBindSessionId] = useState("");
  const [projectId, setProjectId] = useState("");
  const [selectedVoice, setSelectedVoice] = useState<LiveVoice>(defaultLiveVoice);
  const [activeSessionVoice, setActiveSessionVoice] = useState<string>();
  const [sessions, setSessions] = useState<Array<{ id: string; title: string }>>([]);
  const [projects, setProjects] = useState<Array<{ id: string; name: string }>>([]);
  const voiceSaveQueue = useRef(Promise.resolve());
  const voiceSaveRevision = useRef(0);
  const requestedVoice = useRef<{ conversationId: string; voice: LiveVoice } | undefined>(undefined);
  const selectedVoiceRef = useRef(selectedVoice);
  const connectingRef = useRef(false);
  const sessionIdRef = useRef<string | null>(null);
  const sessionStartedSoundPlayed = useRef(false);
  const consumedWakeRequestCount = useRef(0);
  const loadedConversationProfile = useRef<string | undefined>(undefined);
  const connectRef = useRef<() => Promise<void>>(async () => undefined);
  const soundsEnabledRef = useRef(true);
  const workers = connection?.controls ?? [];
  const link = workers.find((item) => item.workerId === workerId) ?? workers[0] ?? null;
  const profileId = connection?.profileId;

  useEffect(() => {
    void loadLiveConnectionSoundsEnabled()
      .then((value) => {
        soundsEnabledRef.current = value;
        setSoundsEnabled(value);
      })
      .catch(() => undefined);
  }, []);
  useEffect(() => {
    if (!isLiveWakeParam(params.wake)) return;
    setWakeRequestCount((current) => current + 1);
    router.setParams({ wake: undefined } as never);
  }, [params.wake]);
  useEffect(() => {
    const subscription = Linking.addEventListener("url", ({ url }) => {
      const parsed = Linking.parse(url);
      if (parsed.path !== "live" ||
        !isLiveWakeParam(parsed.queryParams?.wake as string | string[] | undefined)) return;
      setWakeRequestCount((current) => current + 1);
    });
    return () => subscription.remove();
  }, []);
  useEffect(() => () => stopPeer(), []);
  useEffect(() => {
    const only = connection?.controls.length === 1 ? connection.controls[0] : undefined;
    if (workerId || !only) return;
    setWorkerId(only.workerId);
  }, [workerId, connection]);
  useEffect(() => {
    if (!link || !workerId) {
      setSessions([]);
      setProjects([]);
      return;
    }
    let cancelled = false;
    void (async () => {
      try {
        const [sessionResult, projectResult] = await Promise.all([
          listLiveWorkerSessions(link, workerId),
          listLiveWorkerProjects(link, workerId),
        ]);
        if (cancelled) return;
        setSessions(sessionResult.sessions ?? []);
        setProjects(projectResult.projects ?? []);
      } catch {
        if (!cancelled) {
          setSessions([]);
          setProjects([]);
        }
      }
    })();
    return () => { cancelled = true; };
  }, [link, workerId]);
  useEffect(() => {
    if (!ready) return;
    setConversationLoaded(false);
    setConversationLoadError(null);
    if (!profileId) {
      setConversationLoaded(true);
      return;
    }
    if (!link) {
      setConversationLoaded(true);
      return;
    }
    if (loadedConversationProfile.current !== profileId) {
      loadedConversationProfile.current = profileId;
      setConversation(null);
    }
    let cancelled = false;
    void (async () => {
      try {
        const id = await loadLiveConversationId(profileId);
        if (!id || cancelled) return;
        const current = await getLiveConversation(link, id);
        const history = await listLiveMessages(link, id);
        if (cancelled) return;
        setConversation(current);
        if (current.workerId) setWorkerId(current.workerId);
        if (current.workspaceSessionId) setBindSessionId(current.workspaceSessionId);
        if (current.projectId) setProjectId(current.projectId);
        setSelectedVoice(findLiveVoice(current.voice).slug);
        selectedVoiceRef.current = findLiveVoice(current.voice).slug;
        setState({
          items: [...history.items].reverse().map((item) => ({
            role: item.role === "user" ? "user" : "assistant",
            text: item.text,
          })),
          partial: {},
          seenEventIds: {},
          finalized: {},
        });
      } catch (reason: unknown) {
        if (!cancelled) {
          await saveLiveConversationId(profileId, null);
          setConversationLoadError(reason instanceof Error ? reason.message : "无法读取已保存的 Live 配置");
        }
      } finally {
        if (!cancelled) setConversationLoaded(true);
      }
    })();
    return () => { cancelled = true; };
  }, [link, profileId, ready]);

  const stopPeer = () => {
    channel.current?.close();
    channel.current = null;
    stream.current?.getTracks().forEach((track) => track.stop());
    stream.current = null;
    peer.current?.close();
    peer.current = null;
  };

  const onEvent = (raw: string) => {
    try {
      const event = JSON.parse(raw) as { type?: string };
      if (event.type === "session.started") {
        setStatus("已连接");
        if (shouldPlaySessionStartedSound(event.type, sessionStartedSoundPlayed.current)) {
          sessionStartedSoundPlayed.current = true;
          void playLiveConnectionSound("connected", soundsEnabledRef.current);
        }
      }
      if (event.type === "session.closed") {
        stopPeer();
        setStatus("未连接");
        sessionIdRef.current = null;
        setSessionId(null);
        setActiveSessionVoice(undefined);
      }
      setState((current) => reduceLiveTranscript(current, event));
    } catch { /* Provider event may be ignored when it is not JSON. */ }
  };

  const handleVoiceChange = (voice: LiveVoice) => {
    setError(null);
    setSelectedVoice(voice);
    selectedVoiceRef.current = voice;
    const hasPendingVoice = conversation !== null &&
      requestedVoice.current?.conversationId === conversation.id;
    if (!conversation || (conversation.voice === voice && !hasPendingVoice)) return;
    if (!link) {
      setError("当前连接不可用，音色将在重新连接后尝试保存");
      return;
    }
    const revision = ++voiceSaveRevision.current;
    const conversationId = conversation.id;
    requestedVoice.current = { conversationId, voice };
    voiceSaveQueue.current = voiceSaveQueue.current
      .catch(() => undefined)
      .then(async () => {
        if (revision !== voiceSaveRevision.current) return;
        const updated = await updateLiveConversation(link!, conversationId, voice);
        if (revision !== voiceSaveRevision.current) return;
        requestedVoice.current = undefined;
        setConversation((current) => current?.id === updated.id ? updated : current);
      })
      .catch((reason: unknown) => {
        if (revision !== voiceSaveRevision.current) return;
        setError(reason instanceof Error ? reason.message : "音色保存失败");
      });
  };

  const connect = async () => {
    if (connectingRef.current || sessionIdRef.current || peer.current) return;
    if (!link || !profileId) { setError("请先在连接页授权一个 Control"); return; }
    connectingRef.current = true;
    sessionStartedSoundPlayed.current = false;
    void playLiveConnectionSound("connecting", soundsEnabledRef.current);
    setError(null);
    setStatus("连接中");
    try {
      const shouldRecover = Boolean(conversation);
      let current = conversation;
      stopPeer();
      if (!current) {
        if (!workerId) throw new Error("请先选择 Worker");
        if (mode === "bind") {
          if (!bindSessionId) throw new Error("请选择要绑定的 Session");
          current = await createLiveConversation(link, { workerId, sessionId: bindSessionId, voice: selectedVoice });
        } else {
          if (!projectId) throw new Error("请选择项目以新开语音");
          current = await createLiveConversation(link, { workerId, projectId, voice: selectedVoice });
        }
        setConversation(current);
        await saveLiveConversationId(profileId, current.id);
      }
      await voiceSaveQueue.current;
      const pc = new RTCPeerConnection(liveIceConfiguration);
      peer.current = pc;
      const connectionState = pc as unknown as { connectionState?: string; onconnectionstatechange: (() => void) | null };
      connectionState.onconnectionstatechange = () => {
        if (peer.current !== pc) return;
        if (connectionState.connectionState === "disconnected" || connectionState.connectionState === "failed") {
          const failedSessionId = sessionIdRef.current;
          stopPeer();
          sessionIdRef.current = null;
          setSessionId(null);
          setActiveSessionVoice(undefined);
          setStatus("未连接");
          setError("Live 连接已断开");
          if (failedSessionId) {
            void closeLiveSession(link, failedSessionId).catch(() => undefined);
          }
        }
      };
      try {
        const local = await mediaDevices.getUserMedia({ audio: true, video: false });
        stream.current = local;
        local.getTracks().forEach((track) => pc.addTrack(track, local));
      } catch {
        throw new Error("未获得麦克风权限");
      }
      const remoteTrack = (event: { track?: { enabled?: boolean } }) => {
        if (event.track) event.track.enabled = true;
      };
      (pc as unknown as { ontrack: typeof remoteTrack }).ontrack = remoteTrack;
      const events = pc.createDataChannel("oai-events");
      channel.current = events;
      const eventChannel = events as unknown as { onmessage: (event: { data: unknown }) => void; onopen: () => void };
      eventChannel.onmessage = (event) => onEvent(String(event.data));
      eventChannel.onopen = () => setStatus("连接中");
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      await waitForIceGathering(pc);
      if (!pc.localDescription?.sdp) throw new Error("无法生成 SDP offer");
      const sessionVoice = selectedVoiceRef.current;
      const result = shouldRecover
        ? await recoverLiveSession(link, current.id, pc.localDescription.sdp)
        : await createLiveSession(link, current.id, pc.localDescription.sdp);
      sessionIdRef.current = result.sessionId;
      setSessionId(result.sessionId);
      setActiveSessionVoice(sessionVoice);
      await pc.setRemoteDescription({ type: "answer", sdp: result.transport.answerSdp });
      setStatus("连接中");
    } catch (reason) {
      const failedSessionId = sessionIdRef.current;
      stopPeer();
      sessionIdRef.current = null;
      setSessionId(null);
      setActiveSessionVoice(undefined);
      setStatus("未连接");
      setError(reason instanceof Error ? reason.message : "Live 连接失败");
      if (failedSessionId && link) {
        void closeLiveSession(link, failedSessionId).catch(() => undefined);
      }
    } finally {
      connectingRef.current = false;
    }
  };

  connectRef.current = connect;
  useEffect(() => {
    if (!ready || !conversationLoaded ||
      wakeRequestCount <= consumedWakeRequestCount.current) return;
    consumedWakeRequestCount.current = wakeRequestCount;
    if (!profileId) {
      setError("请先在设置页连接一个 Control");
      return;
    }
    if (!link) {
      setError("当前连接不可用，请先在设置页连接 Control");
      return;
    }
    if (!conversation) {
      setError(conversationLoadError ?? "请先手动完成一次 Live 配置");
      return;
    }
    void connectRef.current();
  }, [conversation, conversationLoadError, conversationLoaded, link, profileId, ready, wakeRequestCount]);

  const toggleSounds = (value: boolean) => {
    soundsEnabledRef.current = value;
    setSoundsEnabled(value);
    void saveLiveConnectionSoundsEnabled(value).catch(() => {
      setError("连接提示音设置保存失败");
    });
  };

  const disconnect = async () => {
    const activeSessionId = sessionIdRef.current ?? sessionId;
    if (link && activeSessionId) {
      try { await closeLiveSession(link, activeSessionId); }
      catch (reason) { setError(reason instanceof Error ? reason.message : "关闭失败"); }
    }
    sessionIdRef.current = null;
    sessionStartedSoundPlayed.current = false;
    setSessionId(null);
    setStatus("未连接");
    setActiveSessionVoice(undefined);
    stopPeer();
  };

  const resetSession = async () => {
    const reconnect = Boolean(sessionId);
    setError(null);
    await voiceSaveQueue.current;
    if (link && conversation) {
      try { await resetLiveConversationHistory(link, conversation.id); }
      catch (reason) { setError(reason instanceof Error ? reason.message : "重置会话失败"); return; }
    }
    await disconnect();
    if (reconnect) await connect();
  };
  const clearCaptions = async () => {
    const reconnect = Boolean(sessionId);
    setError(null);
    await voiceSaveQueue.current;
    if (link && conversation) {
      try { await clearLiveConversationMessages(link, conversation.id); }
      catch (reason) { setError(reason instanceof Error ? reason.message : "清空字幕失败"); return; }
    }
    await disconnect();
    setState(initialLiveTranscriptState);
    if (reconnect) await connect();
  };
  const leave = () => { if (router.canGoBack()) router.back(); else router.replace("/(tabs)/sessions"); };
  const visible = visibleLiveTranscript(state);
  const connected = status === "已连接";
  const connecting = status === "连接中";
  const voiceNeedsReset = activeSessionVoice !== undefined && selectedVoice !== activeSessionVoice;
  return <Screen style={styles.screen}>
    <View style={[styles.top, { paddingTop: insets.top + 4 }]}>
      <Pressable testID="live:exit" accessibilityRole="button" accessibilityLabel="退出" hitSlop={8}
        onPress={leave} style={styles.topButton}>
        <Text style={[styles.exit, { color: theme.colors.accent }]}>‹ 退出</Text>
      </Pressable>
      <Pressable testID="live:more" accessibilityRole="button" accessibilityLabel="更多" hitSlop={8}
        onPress={() => setMenuOpen(true)} style={styles.topButton}>
        <Text style={[styles.more, { color: theme.colors.text }]}>⋯</Text>
      </Pressable>
    </View>
    {error ? <Text style={[styles.error, { color: theme.colors.danger }]}>{error}</Text> : null}
    <View style={styles.pickers}>
      <View style={styles.voiceField}>
        <LiveVoicePicker value={selectedVoice} onChange={handleVoiceChange} />
        {voiceNeedsReset ? <Text style={[styles.voicePending, { color: theme.colors.warning }]}>
          已选择 {findLiveVoice(selectedVoice).name}，重置会话或清空字幕后生效
        </Text> : null}
      </View>
      <Dropdown testID="live:worker" label="Worker" value={workerId || null}
        placeholder="选择 Worker" emptyLabel="没有可用 Worker"
        disabled={Boolean(conversation)}
        options={workers.map((item) => ({ value: item.workerId, label: item.workerName }))}
        onChange={(value) => { setWorkerId(value); setBindSessionId(""); setProjectId(""); }} />
      <Dropdown testID="live:mode" label="入口" value={mode}
        disabled={Boolean(conversation)}
        options={[{ value: "bind", label: "绑定已有 Session" }, { value: "new", label: "新开语音" }]}
        onChange={(value) => setMode(value as "bind" | "new")} />
      {mode === "bind" ? (
        <Dropdown testID="live:session" label="Session" value={bindSessionId || null}
          placeholder="选择 Session" emptyLabel="没有可绑定的 Session"
          disabled={Boolean(conversation)}
          options={sessions.map((item) => ({ value: item.id, label: item.title || item.id }))}
          onChange={setBindSessionId} />
      ) : (
        <Dropdown testID="live:project" label="项目" value={projectId || null}
          placeholder="选择项目" emptyLabel="没有可用项目"
          disabled={Boolean(conversation)}
          options={projects.map((item) => ({ value: item.id, label: item.name }))}
          onChange={setProjectId} />
      )}
    </View>
    <ScrollView contentContainerStyle={styles.transcript} testID="live:transcript">
      {visible.length === 0
        ? <Text style={{ color: theme.colors.textMuted }}>连接后开始说话</Text>
        : visible.map((item: Transcript, index) => (
          <View key={`${index}-${item.text}`} style={styles.line}>
            <Text style={{ color: item.role === "assistant" ? theme.colors.accent : theme.colors.textMuted, fontSize: 12, fontFamily: "Inter_600SemiBold" }}>
              {item.role === "user" ? "你" : "Live"}
            </Text>
            <Text style={{ color: theme.colors.text, fontSize: 17, lineHeight: 24 }}>{item.text}</Text>
          </View>
        ))}
    </ScrollView>
    <View style={[styles.dock, { borderColor: theme.colors.border, backgroundColor: theme.colors.surface, paddingBottom: Math.max(insets.bottom, 16) }]}>
      <LiveMark active={connected} color={theme.colors.accent} />
      <Text style={[styles.hint, { color: theme.colors.textMuted }]}>
        {connected ? "已连接 · 正在听" : connecting ? "连接中" : "未连接"}
      </Text>
      <Pressable testID={connected ? "live:disconnect" : "live:connect"} accessibilityRole="button"
        disabled={connecting || (!conversation && (!workerId || (mode === "bind" ? !bindSessionId : !projectId)))}
        onPress={() => void (connected ? disconnect() : connect())}
        style={[styles.action, { backgroundColor: theme.colors.accent, opacity: connecting ? 0.5 : 1 }]}>
        <Text style={[styles.actionText, { color: theme.colors.accentForeground }]}>{connected ? "断开" : "连接"}</Text>
      </Pressable>
    </View>
    <Modal visible={menuOpen} transparent animationType="fade" onRequestClose={() => setMenuOpen(false)}>
      <View style={styles.modalRoot}>
        <Pressable accessibilityRole="button" accessibilityLabel="关闭菜单" style={StyleSheet.absoluteFill}
          onPress={() => setMenuOpen(false)} />
        <View style={[styles.menu, { top: insets.top + 48, backgroundColor: theme.colors.surface, borderColor: theme.colors.border }, theme.shadow]}>
          <Pressable testID="live:reset" style={styles.menuItem} onPress={() => { setMenuOpen(false); void resetSession(); }}>
            <Text style={[styles.menuText, { color: theme.colors.text }]}>重置会话</Text>
          </Pressable>
          <Pressable testID="live:clear" style={styles.menuItem} onPress={() => { setMenuOpen(false); void clearCaptions(); }}>
            <Text style={[styles.menuText, { color: theme.colors.text }]}>清空字幕</Text>
          </Pressable>
          <View style={styles.menuSetting}>
            <Text style={[styles.menuText, { color: theme.colors.text }]}>连接提示音</Text>
            <Switch testID="live:connection-sounds" value={soundsEnabled}
              onValueChange={toggleSounds} trackColor={{ false: theme.colors.border,
                true: theme.colors.accent }} thumbColor={theme.colors.surface} />
          </View>
        </View>
      </View>
    </Modal>
  </Screen>;
}

const styles = StyleSheet.create({
  screen: { flex: 1 },
  top: { minHeight: 52, paddingHorizontal: 8, flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
  topButton: { minHeight: 44, minWidth: 44, paddingHorizontal: 8, justifyContent: "center" },
  exit: { fontFamily: "Inter_500Medium", fontSize: 16 },
  more: { fontFamily: "Inter_600SemiBold", fontSize: 22, letterSpacing: 1, textAlign: "right" },
  error: { paddingHorizontal: 20, marginBottom: 8 },
  pickers: { paddingHorizontal: 20, gap: 8, marginBottom: 8 },
  voiceField: { gap: 4 },
  voicePending: { fontSize: 12, paddingHorizontal: 4 },
  transcript: { flexGrow: 1, paddingHorizontal: 20, paddingTop: 8, paddingBottom: 16, gap: 12 },
  line: { gap: 4 },
  dock: { alignItems: "center", gap: 10, borderTopWidth: StyleSheet.hairlineWidth, paddingTop: 18, paddingHorizontal: 24 },
  hint: { fontSize: 13 },
  action: { alignSelf: "stretch", minHeight: 48, borderRadius: 12, alignItems: "center", justifyContent: "center" },
  actionText: { fontFamily: "Inter_600SemiBold", fontSize: 16 },
  modalRoot: { flex: 1 },
  menu: { position: "absolute", right: 10, width: 180, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8, paddingVertical: 6 },
  menuItem: { minHeight: 48, paddingHorizontal: 16, justifyContent: "center" },
  menuSetting: { minHeight: 48, paddingHorizontal: 12, flexDirection: "row",
    alignItems: "center", justifyContent: "space-between", gap: 8 },
  menuText: { fontFamily: "Inter_500Medium", fontSize: 16 },
});
