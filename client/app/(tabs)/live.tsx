import { router } from "expo-router";
import { useEffect, useRef, useState } from "react";
import { Modal, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { RTCPeerConnection, mediaDevices, type MediaStream } from "react-native-webrtc";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { closeLiveSession, createLiveConversation, createLiveSession, getLiveConversation, listLiveMessages, recoverLiveSession, type LiveConversation } from "@/api/live";
import { Screen } from "@/components/ui";
import { LiveMark } from "@/features/live/LiveMark";
import { initialLiveTranscriptState, reduceLiveTranscript, visibleLiveTranscript, type LiveTranscriptState } from "@/features/live/transcriptReducer";
import { loadLiveConversationId, saveLiveConversationId } from "@/db/settings";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

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
  const connection = useAppStore((state) => state.activeConnection);
  const peer = useRef<RTCPeerConnection | null>(null);
  const channel = useRef<ReturnType<RTCPeerConnection["createDataChannel"]> | null>(null);
  const stream = useRef<MediaStream | null>(null);
  const resetPending = useRef(false);
  const [conversation, setConversation] = useState<LiveConversation | null>(null);
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [state, setState] = useState<LiveTranscriptState>(initialLiveTranscriptState);
  const [status, setStatus] = useState("未连接");
  const [error, setError] = useState<string | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const link = connection?.controls[0] ?? null;
  const profileId = connection?.profileId;

  useEffect(() => () => stopPeer(), []);
  useEffect(() => {
    if (!link || !profileId) return;
    let cancelled = false;
    void (async () => {
      const id = await loadLiveConversationId(profileId);
      if (!id || cancelled) return;
      try {
        const current = await getLiveConversation(link, id);
        const history = await listLiveMessages(link, id);
        if (cancelled) return;
        setConversation(current);
        setState({
          items: [...history.items].reverse().map((item) => ({
            role: item.role === "user" ? "user" : "assistant",
            text: item.text,
          })),
          partial: {},
          seenEventIds: {},
          finalized: {},
        });
      } catch {
        if (!cancelled) await saveLiveConversationId(profileId, null);
      }
    })();
    return () => { cancelled = true; };
  }, [link, profileId]);

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
      if (event.type === "session.started") setStatus("已连接");
      if (event.type === "session.closed") {
        setStatus("未连接"); setSessionId(null);
      }
      setState((current) => reduceLiveTranscript(current, event));
    } catch { /* Provider event may be ignored when it is not JSON. */ }
  };

  const connect = async () => {
    if (!link || !profileId) { setError("请先在连接页授权一个 Control"); return; }
    setError(null); setStatus("连接中");
    try {
      const shouldRecover = Boolean(conversation) && !resetPending.current;
      let current = conversation;
      stopPeer();
      if (!current) {
        current = await createLiveConversation(link);
        setConversation(current);
        await saveLiveConversationId(profileId, current.id);
      }
      const pc = new RTCPeerConnection(liveIceConfiguration);
      peer.current = pc;
      const connectionState = pc as unknown as { connectionState?: string; onconnectionstatechange: (() => void) | null };
      connectionState.onconnectionstatechange = () => {
        if (connectionState.connectionState === "disconnected" || connectionState.connectionState === "failed") {
          setStatus("未连接");
        }
      };
      try {
        const local = await mediaDevices.getUserMedia({ audio: true, video: false });
        stream.current = local;
        local.getTracks().forEach((track) => pc.addTrack(track, local));
      } catch {
        setError("未获得麦克风权限");
      }
      if (stream.current === null) {
        try { pc.addTransceiver("audio", { direction: "recvonly" }); }
        catch { /* 预览构建可能不暴露 transceiver。 */ }
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
      const result = shouldRecover
        ? await recoverLiveSession(link, current.id, pc.localDescription.sdp)
        : await createLiveSession(link, current.id, pc.localDescription.sdp);
      resetPending.current = false;
      setSessionId(result.sessionId);
      await pc.setRemoteDescription({ type: "answer", sdp: result.transport.answerSdp });
      setStatus("连接中");
    } catch (reason) {
      stopPeer();
      setStatus("未连接");
      setError(reason instanceof Error ? reason.message : "Live 连接失败");
    }
  };

  const disconnect = async () => {
    if (link && sessionId) {
      try { await closeLiveSession(link, sessionId); }
      catch (reason) { setError(reason instanceof Error ? reason.message : "关闭失败"); }
    }
    setSessionId(null);
    setStatus("未连接");
    stopPeer();
  };

  const resetSession = async () => { await disconnect(); resetPending.current = true; };
  const clearCaptions = async () => {
    await disconnect();
    resetPending.current = false;
    setConversation(null);
    setState(initialLiveTranscriptState);
    if (profileId) await saveLiveConversationId(profileId, null);
  };
  const leave = () => { if (router.canGoBack()) router.back(); else router.replace("/(tabs)/sessions"); };
  const visible = visibleLiveTranscript(state);
  const connected = status === "已连接";
  const connecting = status === "连接中";
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
        disabled={connecting} onPress={() => void (connected ? disconnect() : connect())}
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
  transcript: { flexGrow: 1, paddingHorizontal: 20, paddingTop: 8, paddingBottom: 16, gap: 12 },
  line: { gap: 4 },
  dock: { alignItems: "center", gap: 10, borderTopWidth: StyleSheet.hairlineWidth, paddingTop: 18, paddingHorizontal: 24 },
  hint: { fontSize: 13 },
  action: { alignSelf: "stretch", minHeight: 48, borderRadius: 12, alignItems: "center", justifyContent: "center" },
  actionText: { fontFamily: "Inter_600SemiBold", fontSize: 16 },
  modalRoot: { flex: 1 },
  menu: { position: "absolute", right: 10, width: 180, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8, paddingVertical: 6 },
  menuItem: { minHeight: 48, paddingHorizontal: 16, justifyContent: "center" },
  menuText: { fontFamily: "Inter_500Medium", fontSize: 16 },
});
