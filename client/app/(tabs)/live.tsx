import { useEffect, useRef, useState } from "react";
import { Button, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { RTCPeerConnection, mediaDevices, type MediaStream } from "react-native-webrtc";

import { Screen } from "@/components/ui";
import { createLiveConversation, createLiveSession, closeLiveSession, recoverLiveSession, listLiveMessages, type LiveConversation } from "@/api/live";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import { initialLiveTranscriptState, reduceLiveTranscript, visibleLiveTranscript, type LiveTranscriptState } from "@/features/live/transcriptReducer";

const liveIceConfiguration = {
  // STUN only discovers candidates; audio still uses the direct WebRTC path.
  iceServers: [{ urls: "stun:stun.l.google.com:19302" }],
};

type Transcript = { role: "user" | "assistant"; text: string };

async function waitForIceGathering(connection: RTCPeerConnection): Promise<void> {
  if (connection.iceGatheringState === "complete") return;
  const eventConnection = connection as unknown as { onicegatheringstatechange: (() => void) | null };
  await new Promise<void>((resolve) => {
    const timeout = setTimeout(() => { eventConnection.onicegatheringstatechange = null; resolve(); }, 5000);
    eventConnection.onicegatheringstatechange = () => {
      if (connection.iceGatheringState === "complete") { clearTimeout(timeout); eventConnection.onicegatheringstatechange = null; resolve(); }
    };
  });
}
export default function LiveScreen() {
  const theme = useTheme();
  const connection = useAppStore((state) => state.activeConnection);
  const peer = useRef<RTCPeerConnection | null>(null);
  const channel = useRef<ReturnType<RTCPeerConnection["createDataChannel"]> | null>(null);
  const stream = useRef<MediaStream | null>(null);
  const [conversation, setConversation] = useState<LiveConversation | null>(null);
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [sessionStatus, setSessionStatus] = useState('');
  const [state, setState] = useState<LiveTranscriptState>(initialLiveTranscriptState);
  const [text, setText] = useState("");
  const [status, setStatus] = useState("未连接");
  const [error, setError] = useState<string | null>(null);
  const link = connection?.controls[0] ?? null;
  useEffect(() => () => stopPeer(), []);
  const stopPeer = () => { channel.current?.close(); channel.current = null; stream.current?.getTracks().forEach((track) => track.stop()); stream.current = null; peer.current?.close(); peer.current = null; };
  const onEvent = (raw: string) => {
    try {
      const event = JSON.parse(raw) as { type?: string; id?: string; event_id?: string; eventId?: string; item_id?: string; itemId?: string; response_id?: string; responseId?: string; delta?: string; text?: string; transcript?: string };
      if (event.type === "session.started") { setStatus("已连接"); setSessionStatus("active"); }
      if (event.type === "session.closed") { setStatus("已关闭"); setSessionStatus("closed"); }
      setState((current) => reduceLiveTranscript(current, event));
    } catch { /* Provider event may be ignored when it is not JSON. */ }
  };
  const connect = async (recover: boolean) => {
    if (!link) { setError("请先在连接页授权一个 Control"); return; }
    setError(null); setStatus("连接中");
    try {
      const current = conversation ?? await createLiveConversation(link);
      const shouldRecover = recover || (Boolean(sessionId) && sessionStatus !== "closed" && sessionStatus !== "failed");
      setConversation(current); stopPeer();
      const pc = new RTCPeerConnection(liveIceConfiguration); peer.current = pc;
      const connectionState = pc as unknown as { connectionState?: string; onconnectionstatechange: (() => void) | null };
      connectionState.onconnectionstatechange = () => {
        if (connectionState.connectionState === "disconnected" || connectionState.connectionState === "failed") {
          setStatus("已断开，可恢复");
          setSessionStatus("sideband_disconnected");
        }
      };
      try {
        const local = await mediaDevices.getUserMedia({ audio: true, video: false }); stream.current = local;
        local.getTracks().forEach((track) => pc.addTrack(track, local));
      } catch {
        setError("未获得麦克风权限，将继续使用文本测试通道");
      }
      if (stream.current === null) { try { pc.addTransceiver("audio", { direction: "recvonly" }); } catch { /* 预览构建可能不暴露 transceiver。 */ } }
      const remoteTrack = (event: { track?: { enabled?: boolean } }) => {
        if (event.track) event.track.enabled = true;
      };
      (pc as unknown as { ontrack: typeof remoteTrack }).ontrack = remoteTrack;
      const events = pc.createDataChannel("oai-events"); channel.current = events; const eventChannel = events as unknown as { onmessage: (event: { data: unknown }) => void; onopen: () => void }; eventChannel.onmessage = (event) => onEvent(String(event.data)); eventChannel.onopen = () => setStatus("数据通道已连接");
      const offer = await pc.createOffer(); await pc.setLocalDescription(offer); await waitForIceGathering(pc);
      if (!pc.localDescription?.sdp) throw new Error("无法生成 SDP offer");
      if (!/^a=candidate:/m.test(pc.localDescription.sdp)) throw new Error("无法生成包含 ICE candidate 的 SDP offer");
      const result = shouldRecover ? await recoverLiveSession(link, current.id, pc.localDescription.sdp) : await createLiveSession(link, current.id, pc.localDescription.sdp);
      setSessionId(result.sessionId); setSessionStatus(result.session.status); await pc.setRemoteDescription({ type: "answer", sdp: result.transport.answerSdp }); setStatus("等待 Live session");
    } catch (reason) { stopPeer(); setStatus("连接失败"); setError(reason instanceof Error ? reason.message : "Live 连接失败"); }
  };
  const sendText = () => {
    const value = text.trim();
    if (!value || channel.current?.readyState !== "open") return;
    const eventId = `typed-live-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    channel.current.send(JSON.stringify({
      type: "session.context.append",
      event_id: eventId,
      channel: "speakable",
      content: [{ type: "input_text", text: value }],
    }));
    setText("");
  };
  const close = async () => { if (!link || !sessionId) return; try { await closeLiveSession(link, sessionId); setStatus("已关闭"); setSessionStatus("closed"); stopPeer(); } catch (reason) { setError(reason instanceof Error ? reason.message : "关闭失败"); } };
  const loadHistory = async () => { if (!link || !conversation) return; try { const result = await listLiveMessages(link, conversation.id); setState({ items: result.items.reverse().map((item) => ({ role: item.role === "user" ? "user" : "assistant", text: item.text })), partial: {}, seenEventIds: {}, finalized: {} }); } catch (reason) { setError(reason instanceof Error ? reason.message : "历史加载失败"); } };
  const visible = visibleLiveTranscript(state);
  return <Screen style={styles.screen}><ScrollView contentContainerStyle={styles.content}><Text style={[styles.title, { color: theme.colors.text }]}>Live Voice</Text><Text style={{ color: theme.colors.textMuted }}>Control 统一协议，WebRTC 音频直连 Live。</Text>{error ? <Text style={[styles.error, { color: theme.colors.danger }]}>{error}</Text> : null}<Text style={{ color: theme.colors.textMuted, marginTop: 16 }}>状态：{status}</Text><View style={styles.buttons}><Button title={conversation ? "重新连接" : "新建会话"} onPress={() => void connect(false)} disabled={status === "连接中"} /><Button title="恢复" onPress={() => void connect(true)} disabled={!conversation} /><Button title="关闭" onPress={() => void close()} disabled={!sessionId} /><Button title="历史" onPress={() => void loadHistory()} disabled={!conversation} /></View><View style={[styles.transcript, { borderColor: theme.colors.border }]}>{visible.length === 0 ? <Text style={{ color: theme.colors.textMuted }}>暂无文本消息，可用输入框测试。</Text> : visible.map((item: Transcript, index) => <View key={`${index}-${item.text}`} style={styles.line}><Text style={{ color: theme.colors.textMuted, width: 56 }}>{item.role === "user" ? "你" : "Live"}</Text><Text style={{ color: theme.colors.text, flex: 1 }}>{item.text}</Text></View>)}</View><View style={styles.composer}><TextInput value={text} onChangeText={setText} placeholder="输入文本测试" placeholderTextColor={theme.colors.textMuted} style={[styles.input, { borderColor: theme.colors.border, color: theme.colors.text }]} /><Pressable onPress={sendText} style={[styles.send, { backgroundColor: theme.colors.accent }]}><Text style={{ color: theme.colors.accentForeground }}>发送</Text></Pressable></View></ScrollView></Screen>;
}
const styles = StyleSheet.create({ screen: { flex: 1 }, content: { padding: 16, gap: 8 }, title: { fontSize: 25, fontFamily: "Inter_600SemiBold", marginBottom: 2 }, error: { marginTop: 12 }, buttons: { flexDirection: "row", flexWrap: "wrap", gap: 8, marginVertical: 12 }, transcript: { minHeight: 260, borderWidth: 1, borderRadius: 8, padding: 12, gap: 10 }, line: { flexDirection: "row", gap: 8 }, composer: { flexDirection: "row", gap: 8, marginTop: 12 }, input: { flex: 1, minHeight: 44, borderWidth: 1, borderRadius: 8, paddingHorizontal: 10 }, send: { minHeight: 44, paddingHorizontal: 14, borderRadius: 8, justifyContent: "center" } });
