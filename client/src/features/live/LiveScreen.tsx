import * as Linking from "expo-linking";
import { useEffect, useMemo, useRef, useState } from "react";
import { Modal, PermissionsAndroid, Platform, Pressable, ScrollView, StyleSheet,
  Switch, Text, View } from "react-native";
import { RTCPeerConnection, mediaDevices, type MediaStream } from "react-native-webrtc";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import audioRoute, { type LiveAudioRoute, type LiveAudioRouteKind } from "tyrs-audio-route";

import { clearLiveConversationMessages, closeLiveSession, createLiveConversation, createLiveSession, getLiveConversation, listLiveMessages, listLiveWorkerProjects, recoverLiveSession, resetLiveConversationHistory, updateLiveConversation, type LiveConversation } from "@/api/live";
import { Screen } from "@/components/ui";
import { resolveMachineControlBinding } from "@/features/connections/machineControlBinding";
import { LiveMark } from "@/features/live/LiveMark";
import { LiveVoicePicker } from "@/features/live/LiveVoicePicker";
import { selectableLiveAudioDevices } from "@/features/live/liveAudioDevices";
import { resolveLiveProjectForSSHProject, type LiveProjectResolution } from "@/features/live/liveProjectMapping";
import { initialLiveTranscriptState, reduceLiveTranscript, visibleLiveTranscript, type LiveTranscriptState } from "@/features/live/transcriptReducer";
import { defaultLiveVoice, findLiveVoice, type LiveVoice } from "@/features/live/voices";
import { clearLegacyLiveConversationId, loadLegacyLiveConversationId, loadLiveConnectionSoundsEnabled,
  loadLiveConversationId, saveLiveConversationId, saveLiveConnectionSoundsEnabled } from "@/db/settings";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import { playLiveConnectionSound } from "@/features/live/liveSounds";
import { isLiveWakeParam, shouldAutoConnectOnOpen, shouldPlaySessionStartedSound } from "@/features/live/liveWake";
import { addLiveWakeListener, takeLiveWake } from "@/native/voiceWake";

const liveIceConfiguration = {
  iceServers: [{ urls: "stun:stun.l.google.com:19302" }],
};

type Transcript = { role: "user" | "assistant"; text: string };
type LiveAudioSelection = {
  kind: "auto" | LiveAudioRouteKind;
  deviceId?: number;
};

const bluetoothConnectPermission = "android.permission.BLUETOOTH_CONNECT";

async function requestBluetoothConnectPermission(): Promise<void> {
  if (Platform.OS !== "android" || Platform.Version < 31) return;
  try {
    if (await PermissionsAndroid.check(bluetoothConnectPermission)) return;
    await PermissionsAndroid.request(bluetoothConnectPermission);
  } catch {
    // 没有蓝牙权限时仍允许 Live 使用手机或有线设备。
  }
}

function audioRouteLabel(route: LiveAudioRoute | null): string {
  if (!route) return "自动";
  const name = route.activeDevice?.name;
  const inputKind = route.activeInputDevice?.kind;
  const inputLabel = inputKind === "bluetooth" ? "蓝牙麦克风"
    : inputKind === "wired" ? "有线麦克风"
      : inputKind ? "手机麦克风" : "输入待确认";
  const inputOutputLabel = inputKind === route.kind ? "输入/输出" : `输出 · ${inputLabel}`;
  if (route.kind === "bluetooth") {
    return name ? `蓝牙耳机（${inputOutputLabel}） · ${name}` : `蓝牙耳机（${inputOutputLabel}）`;
  }
  if (route.kind === "wired") {
    return name ? `有线耳机（${inputOutputLabel}） · ${name}` : `有线耳机（${inputOutputLabel}）`;
  }
  if (route.kind === "speaker") return "手机扬声器 · 手机麦克风";
  if (route.kind === "earpiece") return "手机听筒 · 手机麦克风";
  return "系统默认";
}

function audioSelectionLabel(selection: LiveAudioSelection, route: LiveAudioRoute | null): string {
  if (selection.kind === "auto") return `自动（${audioRouteLabel(route)}）`;
  return selection.kind === "bluetooth" ? "蓝牙耳机"
    : selection.kind === "wired" ? "有线耳机"
      : selection.kind === "speaker" ? "扬声器"
        : selection.kind === "earpiece" ? "听筒" : "自动";
}

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

export function LiveScreen({ initialWake = false, onLeave }: {
  initialWake?: boolean;
  onLeave: () => void;
}) {
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  const ready = useAppStore((state) => state.ready);
  const connection = useAppStore((state) => state.activeConnection);
  const projects = useAppStore((state) => state.projects);
  const selectedProjectId = useAppStore((state) => state.selectedProjectId);
  const peer = useRef<RTCPeerConnection | null>(null);
  const channel = useRef<ReturnType<RTCPeerConnection["createDataChannel"]> | null>(null);
  const stream = useRef<MediaStream | null>(null);
  const [conversation, setConversation] = useState<LiveConversation | null>(null);
  const [conversationLoaded, setConversationLoaded] = useState(false);
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [state, setState] = useState<LiveTranscriptState>(initialLiveTranscriptState);
  const [status, setStatus] = useState("未连接");
  const [error, setError] = useState<string | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [audioRouteMenuOpen, setAudioRouteMenuOpen] = useState(false);
  const [activeAudioRoute, setActiveAudioRoute] = useState<LiveAudioRoute | null>(null);
  const [audioSelection, setAudioSelection] = useState<LiveAudioSelection>({ kind: "auto" });
  const [soundsEnabled, setSoundsEnabled] = useState(true);
  const [wakeRequestCount, setWakeRequestCount] = useState(0);
  const [selectedVoice, setSelectedVoice] = useState<LiveVoice>(defaultLiveVoice);
  const [activeSessionVoice, setActiveSessionVoice] = useState<string>();
  const [controlProjectId, setControlProjectId] = useState<string | null | undefined>(undefined);
  const [controlProjectError, setControlProjectError] = useState<string | null>(null);
  const voiceSaveQueue = useRef(Promise.resolve());
  const voiceSaveRevision = useRef(0);
  const requestedVoice = useRef<{ conversationId: string; voice: LiveVoice } | undefined>(undefined);
  const selectedVoiceRef = useRef(selectedVoice);
  const connectingRef = useRef(false);
  const sessionIdRef = useRef<string | null>(null);
  const sessionStartedSoundPlayed = useRef(false);
  const consumedWakeRequestCount = useRef(0);
  const loadedConversationTarget = useRef<string | undefined>(undefined);
  const connectRef = useRef<() => Promise<void>>(async () => undefined);
  const disconnectRef = useRef<() => Promise<void>>(async () => undefined);
  const soundsEnabledRef = useRef(true);
  const mountedRef = useRef(true);
  const machineBinding = useMemo(() => resolveMachineControlBinding(connection), [connection]);
  const workerId = machineBinding.workerId ?? "";
  const link = machineBinding.link;
  const linkRef = useRef(link);
  linkRef.current = link;
  const profileId = connection?.profileId;
  const selectedProject = projects.find((item) => item.id === selectedProjectId) ?? null;

  useEffect(() => {
    void loadLiveConnectionSoundsEnabled()
      .then((value) => {
        soundsEnabledRef.current = value;
        setSoundsEnabled(value);
      })
      .catch(() => undefined);
  }, []);
  useEffect(() => {
    let cancelled = false;
    const subscription = audioRoute.addLiveAudioRouteListener((route) => {
      if (!cancelled) {
        setActiveAudioRoute(route);
        if (route.automatic) setAudioSelection({ kind: "auto" });
      }
    });
    void audioRoute.getLiveAudioRoute()
      .then((route) => {
        if (!cancelled) {
          setActiveAudioRoute(route);
          if (route.automatic) setAudioSelection({ kind: "auto" });
        }
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
      subscription?.remove();
    };
  }, []);
  useEffect(() => {
    if (shouldAutoConnectOnOpen(initialWake)) setWakeRequestCount((current) => current + 1);
    void takeLiveWake().then((wake) => {
      if (wake && mountedRef.current) setWakeRequestCount((current) => current + 1);
    }).catch(() => undefined);
    const nativeWake = addLiveWakeListener(() => {
      setWakeRequestCount((current) => current + 1);
    });
    const subscription = Linking.addEventListener("url", ({ url }) => {
      const parsed = Linking.parse(url);
      if (parsed.path !== "live" && parsed.hostname !== "live") return;
      if (!isLiveWakeParam(parsed.queryParams?.wake as string | string[] | undefined)) return;
      setWakeRequestCount((current) => current + 1);
    });
    return () => {
      nativeWake?.remove();
      subscription.remove();
    };
  }, [initialWake]);
  useEffect(() => () => {
    mountedRef.current = false;
    void disconnectRef.current();
  }, []);
  useEffect(() => {
    if (!link || !workerId || !selectedProject) {
      setControlProjectId(null);
      setControlProjectError(machineBinding.status !== "bound" ? machineBinding.message
        : !selectedProject ? "请先在会话页选择 Codex 项目" : null);
      return;
    }
    let cancelled = false;
    setControlProjectId(undefined);
    setControlProjectError(null);
    void listLiveWorkerProjects(link, workerId)
      .then(({ projects: controlProjects }) => {
        if (cancelled) return;
        const resolution: LiveProjectResolution = resolveLiveProjectForSSHProject(
          selectedProject, controlProjects);
        setControlProjectId(resolution.status === "matched" ? resolution.project.id : null);
        setControlProjectError(resolution.status === "matched" ? null : resolution.message);
      })
      .catch((reason: unknown) => {
        if (cancelled) return;
        setControlProjectId(null);
        setControlProjectError(reason instanceof Error ? reason.message : "无法读取 Control 项目");
      });
    return () => { cancelled = true; };
  }, [link, machineBinding, selectedProject, workerId]);
  useEffect(() => {
    if (!ready) return;
    setConversationLoaded(false);
    if (!profileId) {
      setConversationLoaded(true);
      return;
    }
    if (!link) {
      setConversationLoaded(true);
      return;
    }
    const targetKey = `${profileId}:${workerId}`;
    if (loadedConversationTarget.current !== targetKey) {
      loadedConversationTarget.current = targetKey;
      setConversation(null);
      setState(initialLiveTranscriptState);
    }
    let cancelled = false;
    void (async () => {
      try {
        const id = await loadLiveConversationId(profileId, workerId);
        const legacyId = id ? null : await loadLegacyLiveConversationId(profileId);
        const savedId = id ?? legacyId;
        if (!savedId || cancelled) return;
        const current = await getLiveConversation(link, savedId);
        if (current.workerId !== workerId) {
          if (id) await saveLiveConversationId(profileId, workerId, null);
          return;
        }
        const history = await listLiveMessages(link, savedId);
        if (!id) {
          await saveLiveConversationId(profileId, workerId, current.id);
          await clearLegacyLiveConversationId(profileId);
        }
        if (cancelled) return;
        setConversation(current);
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
          await saveLiveConversationId(profileId, workerId, null);
          setError(reason instanceof Error ? reason.message : "无法读取已保存的 Live 配置");
        }
      } finally {
        if (!cancelled) setConversationLoaded(true);
      }
    })();
    return () => { cancelled = true; };
  }, [link, profileId, ready, workerId]);

  const stopPeer = () => {
    channel.current?.close();
    channel.current = null;
    stream.current?.getTracks().forEach((track) => track.stop());
    stream.current = null;
    peer.current?.close();
    peer.current = null;
  };

  const restoreAudioRoute = async () => {
    if (mountedRef.current) {
      setAudioSelection({ kind: "auto" });
      setActiveAudioRoute(null);
    }
    await audioRoute.restoreLiveAudioRoute().catch(() => undefined);
  };

  const onEvent = (raw: string) => {
    try {
      const event = JSON.parse(raw) as { type?: string };
      if (event.type === "session.started") {
        setStatus("已连接");
        if (shouldPlaySessionStartedSound(event.type, sessionStartedSoundPlayed.current)) {
          sessionStartedSoundPlayed.current = true;
          void playLiveConnectionSound("connected", soundsEnabledRef.current).catch(() => undefined);
        }
      }
      if (event.type === "session.closed") {
        stopPeer();
        void restoreAudioRoute();
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
    setError(null);
    setStatus("连接中");
    try {
      const shouldRecover = Boolean(conversation);
      let current = conversation;
      stopPeer();
      if (!current) {
        if (!workerId) throw new Error(machineBinding.message ??
          "当前机器尚未关联 Control Worker");
        if (!controlProjectId) {
          throw new Error(controlProjectError ?? "当前 SSH 项目尚未同步到 Control");
        }
        current = await createLiveConversation(link, {
          workerId, projectId: controlProjectId, voice: selectedVoice,
        });
        setConversation(current);
        await saveLiveConversationId(profileId, workerId, current.id);
      }
      await requestBluetoothConnectPermission();
      let route: LiveAudioRoute | null = null;
      try {
        route = await audioRoute.prepareLiveAudioRoute();
        if (audioSelection.kind !== "auto") {
          route = await audioRoute.setLiveAudioRoute(audioSelection.kind, audioSelection.deviceId);
        }
        setActiveAudioRoute(route);
      } catch {
        // 路由选择失败时继续使用系统默认设备，不阻断 Live 建连。
      }
      void playLiveConnectionSound("connecting", soundsEnabledRef.current).catch(() => undefined);
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
          void restoreAudioRoute();
          if (failedSessionId) {
            void closeLiveSession(link, failedSessionId).catch(() => undefined);
          }
        }
      };
      try {
        const local = await mediaDevices.getUserMedia({ audio: true, video: false });
        stream.current = local;
        local.getTracks().forEach((track) => pc.addTrack(track, local));
        void audioRoute.getLiveAudioRoute()
          .then(setActiveAudioRoute)
          .catch(() => undefined);
        setTimeout(() => {
          if (peer.current !== pc) return;
          void audioRoute.getLiveAudioRoute()
            .then(setActiveAudioRoute)
            .catch(() => undefined);
        }, 300);
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
      await restoreAudioRoute();
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
    if (!ready || !conversationLoaded || controlProjectId === undefined) return;
    if (wakeRequestCount <= consumedWakeRequestCount.current) return;
    consumedWakeRequestCount.current = wakeRequestCount;
    void connectRef.current();
  }, [conversationLoaded, controlProjectId, ready, wakeRequestCount]);

  const toggleSounds = (value: boolean) => {
    soundsEnabledRef.current = value;
    setSoundsEnabled(value);
    void saveLiveConnectionSoundsEnabled(value).catch(() => {
      setError("连接提示音设置保存失败");
    });
  };

  const disconnect = async () => {
    const activeSessionId = sessionIdRef.current;
    const activeLink = linkRef.current;
    sessionIdRef.current = null;
    sessionStartedSoundPlayed.current = false;
    connectingRef.current = false;
    if (mountedRef.current) {
      setSessionId(null);
      setStatus("未连接");
      setActiveSessionVoice(undefined);
    }
    stopPeer();
    await restoreAudioRoute();
    if (activeLink && activeSessionId) {
      try { await closeLiveSession(activeLink, activeSessionId); }
      catch (reason) {
        if (mountedRef.current) {
          setError(reason instanceof Error ? reason.message : "关闭失败");
        }
      }
    }
  };
  disconnectRef.current = disconnect;

  const resetSession = async () => {
    const reconnect = Boolean(sessionId);
    setError(null);
    await voiceSaveQueue.current;
    if (link && conversation) {
      try {
        const updated = await resetLiveConversationHistory(link, conversation.id);
        setConversation(updated);
      }
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
      try {
        const updated = await clearLiveConversationMessages(link, conversation.id);
        setConversation(updated);
      }
      catch (reason) { setError(reason instanceof Error ? reason.message : "清空字幕失败"); return; }
    }
    await disconnect();
    setState(initialLiveTranscriptState);
    if (reconnect) await connect();
  };
  const leave = () => { void disconnect().finally(() => onLeave()); };
  const visible = visibleLiveTranscript(state);
  const connected = status === "已连接";
  const connecting = status === "连接中";
  const voiceNeedsReset = activeSessionVoice !== undefined && selectedVoice !== activeSessionVoice;
  const audioDevices = selectableLiveAudioDevices(activeAudioRoute);
  const selectAudioRoute = async (selection: LiveAudioSelection) => {
    setAudioRouteMenuOpen(false);
    try {
      const route = await audioRoute.setLiveAudioRoute(selection.kind, selection.deviceId);
      setAudioSelection(selection);
      setActiveAudioRoute(route);
      setError(null);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "音频设备切换失败");
    }
  };
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
      <Text style={[styles.routeHint, { color: theme.colors.textMuted }]}>
        音频输出 · {audioRouteLabel(activeAudioRoute)}
      </Text>
      <Pressable testID={connected ? "live:disconnect" : "live:connect"} accessibilityRole="button"
        disabled={connecting || (!conversation && (!workerId || !controlProjectId))}
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
          <View style={styles.menuVoice}>
            <LiveVoicePicker value={selectedVoice} onChange={handleVoiceChange}
              onOpen={() => setMenuOpen(false)} />
            {voiceNeedsReset ? <Text style={[styles.voicePending, { color: theme.colors.warning }]}>
              已选择 {findLiveVoice(selectedVoice).name}，重置会话或清空字幕后生效
            </Text> : null}
          </View>
          <Pressable testID="live:reset" style={styles.menuItem} onPress={() => { setMenuOpen(false); void resetSession(); }}>
            <Text style={[styles.menuText, { color: theme.colors.text }]}>重置会话</Text>
          </Pressable>
          <Pressable testID="live:clear" style={styles.menuItem} onPress={() => { setMenuOpen(false); void clearCaptions(); }}>
            <Text style={[styles.menuText, { color: theme.colors.text }]}>清空字幕</Text>
          </Pressable>
          <Pressable testID="live:audio-route" style={styles.menuItem}
            disabled={!connected && !connecting}
            onPress={() => { setMenuOpen(false); setAudioRouteMenuOpen(true); }}>
            <Text style={[styles.menuText, { color: theme.colors.text }]}>音频输出</Text>
            <Text style={[styles.menuSubtext, { color: theme.colors.textMuted }]}>
              {audioSelectionLabel(audioSelection, activeAudioRoute)}
            </Text>
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
    <Modal visible={audioRouteMenuOpen} transparent animationType="fade"
      onRequestClose={() => setAudioRouteMenuOpen(false)}>
      <View style={styles.modalRoot}>
        <Pressable accessibilityRole="button" accessibilityLabel="关闭音频输出选择"
          style={StyleSheet.absoluteFill} onPress={() => setAudioRouteMenuOpen(false)} />
        <View style={[styles.routeMenu, { backgroundColor: theme.colors.surface, borderColor: theme.colors.border }, theme.shadow]}>
          <Text style={[styles.routeTitle, { color: theme.colors.text }]}>音频输出</Text>
          <Pressable testID="live:audio-route-auto" style={styles.routeItem}
            onPress={() => void selectAudioRoute({ kind: "auto" })}>
            <Text style={[styles.menuText, { color: theme.colors.text }]}>自动</Text>
            <Text style={[styles.menuSubtext, { color: theme.colors.textMuted }]}>
              {audioRouteLabel(activeAudioRoute)}
            </Text>
          </Pressable>
          {audioDevices.map((device) => (
            <Pressable key={device.id} testID={`live:audio-route-${device.id}`} style={styles.routeItem}
              onPress={() => void selectAudioRoute({ kind: device.kind, deviceId: device.id })}>
              <Text style={[styles.menuText, { color: theme.colors.text }]}>
                {device.kind === "bluetooth" ? "蓝牙耳机"
                  : device.kind === "wired" ? "有线耳机" : "扬声器"}
              </Text>
              <Text style={[styles.menuSubtext, { color: theme.colors.textMuted }]}>{device.name}</Text>
            </Pressable>
          ))}
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
  menuVoice: { padding: 8, gap: 4 },
  voicePending: { fontSize: 12, paddingHorizontal: 4 },
  transcript: { flexGrow: 1, paddingHorizontal: 20, paddingTop: 8, paddingBottom: 16, gap: 12 },
  line: { gap: 4 },
  dock: { alignItems: "center", gap: 10, borderTopWidth: StyleSheet.hairlineWidth, paddingTop: 18, paddingHorizontal: 24 },
  hint: { fontSize: 13 },
  routeHint: { fontSize: 12, textAlign: "center" },
  action: { alignSelf: "stretch", minHeight: 48, borderRadius: 12, alignItems: "center", justifyContent: "center" },
  actionText: { fontFamily: "Inter_600SemiBold", fontSize: 16 },
  modalRoot: { flex: 1 },
  menu: { position: "absolute", right: 10, width: 240, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8, paddingVertical: 6 },
  menuItem: { minHeight: 48, paddingHorizontal: 16, justifyContent: "center" },
  menuSubtext: { fontSize: 12, marginTop: 2 },
  menuSetting: { minHeight: 48, paddingHorizontal: 12, flexDirection: "row",
    alignItems: "center", justifyContent: "space-between", gap: 8 },
  menuText: { fontFamily: "Inter_500Medium", fontSize: 16 },
  routeMenu: { position: "absolute", left: 24, right: 24, top: "28%", borderWidth: StyleSheet.hairlineWidth, borderRadius: 8, paddingVertical: 6 },
  routeTitle: { fontFamily: "Inter_600SemiBold", fontSize: 17, paddingHorizontal: 16, paddingVertical: 12 },
  routeItem: { minHeight: 54, paddingHorizontal: 16, justifyContent: "center" },
});
