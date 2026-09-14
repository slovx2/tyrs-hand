import { router, Tabs } from "expo-router";
import { Ionicons } from "@expo/vector-icons";
import { useEffect, useMemo, useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { threadTitle } from "@/app-server/types";
import { listLiveWorkerProjects } from "@/api/live";
import { ConnectionErrorBanner } from "@/components/ConnectionErrorBanner";
import { Dropdown } from "@/components/Dropdown";
import { EmptyState, Screen } from "@/components/ui";
import { ConversationPane } from "@/features/chat/ConversationPane";
import { SessionActionsMenu } from "@/features/chat/SessionActionsMenu";
import { SessionListPane } from "@/features/session-list/SessionListPane";
import { resolveMachineControlBinding } from "@/features/connections/machineControlBinding";
import { resolveLiveProjectForSSHProject } from "@/features/live/liveProjectMapping";
import { openLive } from "@/features/live/openLive";
import { useTablet } from "@/hooks/useTablet";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

export default function SessionsScreen() {
  const theme = useTheme();
  const tablet = useTablet();
  const insets = useSafeAreaInsets();
  const connections = useAppStore((state) => state.connections);
  const connection = useAppStore((state) => state.activeConnection);
  const projects = useAppStore((state) => state.projects);
  const allSessions = useAppStore((state) => state.threads);
  const selectedProjectId = useAppStore((state) => state.selectedProjectId);
  const switchConnection = useAppStore((state) => state.switchConnection);
  const setSelectedProject = useAppStore((state) => state.setSelectedProject);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showArchived, setShowArchived] = useState(false);
  const [liveProjectStatus, setLiveProjectStatus] = useState<
    "idle" | "loading" | "matched" | "unmatched" | "ambiguous"
  >("idle");
  const [liveProjectMessage, setLiveProjectMessage] = useState("");
  const sshConnections = useMemo(() => connections.filter((item) => item.kind === "ssh"), [connections]);
  const selectedProject = projects.find((item) => item.id === selectedProjectId) ?? null;
  const machineBinding = useMemo(() => resolveMachineControlBinding(connection), [connection]);
  const sessions = useMemo(() => selectedProjectId
    ? allSessions.filter((item) => item.projectId === selectedProjectId) : [],
  [allSessions, selectedProjectId]);
  const visibleSessions = useMemo(() => sessions.filter((item) =>
    showArchived ? item.archived : !item.archived), [sessions, showArchived]);

  useEffect(() => {
    if (selectedId && !visibleSessions.some((item) => item.thread.id === selectedId)) setSelectedId(null);
  }, [selectedId, visibleSessions]);

  const selectSession = (id: string) => {
    if (tablet) setSelectedId(id);
    else router.push({ pathname: "/session/[id]", params: { id } });
  };
  const selectMachine = (profileId: string) => {
    setSelectedId(null);
    void switchConnection(profileId);
  };
  const openLivePage = () => {
    if (!selectedProject || machineBinding.status !== "bound") return;
    openLive();
  };
  useEffect(() => {
    if (machineBinding.status !== "bound" || !selectedProject) {
      setLiveProjectStatus("idle");
      setLiveProjectMessage(!selectedProject ? "请先选择 Codex 项目" : machineBinding.message ?? "");
      return;
    }
    let cancelled = false;
    setLiveProjectStatus("loading");
    setLiveProjectMessage("");
    void listLiveWorkerProjects(machineBinding.link, machineBinding.workerId)
      .then(({ projects: controlProjects }) => {
        if (cancelled) return;
        const resolution = resolveLiveProjectForSSHProject(selectedProject, controlProjects);
        setLiveProjectStatus(resolution.status);
        setLiveProjectMessage(resolution.status === "matched" ? "" : resolution.message);
      })
      .catch((reason: unknown) => {
        if (cancelled) return;
        setLiveProjectStatus("unmatched");
        setLiveProjectMessage(reason instanceof Error ? reason.message : "无法读取 Control 项目");
    });
    return () => { cancelled = true; };
  }, [machineBinding, selectedProject]);
  const navigation = <Tabs.Screen options={{
    title: selectedId ? (() => {
      const record = allSessions.find((item) => item.thread.id === selectedId);
      return record ? threadTitle(record.thread) : "会话";
    })() : "会话",
    headerRight: () => <SessionActionsMenu sessionId={tablet ? selectedId : null}
      archiveView={showArchived} onArchiveViewChange={(value) => {
        setShowArchived(value); setSelectedId(null);
      }} />,
  }} />;
  const selectors = <View style={styles.selectors}>
    <View style={styles.selectorItem}><Dropdown label="机器" value={connection?.profileId ?? null}
      options={sshConnections.map((item) => ({ value: item.profileId, label: item.name,
        detail: `${item.user}@${item.host}:${item.port}` }))}
      emptyLabel="无机器" onChange={selectMachine} testID="session:machine" /></View>
    <View style={styles.selectorItem}><Dropdown label="项目" value={selectedProjectId}
      options={projects.map((item) => ({ value: item.id, label: item.name, detail: item.relativePath }))}
      emptyLabel="无项目" onChange={(value) => { setSelectedId(null); setSelectedProject(value); }}
      testID="session:project" /></View>
  </View>;

  if (sshConnections.length === 0) {
    return <Screen>{navigation}{selectors}<EmptyState title="无机器"
      detail="请先在连接页添加 SSH 机器；定时任务授权不能用于会话。" /></Screen>;
  }
  const list = <View style={[styles.master, tablet && { borderRightColor: theme.colors.border }]}>
    <ConnectionErrorBanner />
    {selectedProject ? <SessionListPane sessions={sessions} selectedId={selectedId} onSelect={selectSession}
      positionKey={`${connection?.profileId ?? "none"}:project:${selectedProject.id}:sessions`}
      emptyDetail={showArchived ? "这个项目还没有已归档会话。" : "这个项目还没有会话，点击右下角加号创建第一个任务。"}
      hideFilter archivedOnly={showArchived} />
      : <EmptyState title="无项目" detail="请在连接页为当前机器添加项目目录。" />}
  </View>;
  const canOpenLive = Boolean(selectedProject && machineBinding.status === "bound");
  const actions = selectedProject ? <View style={[styles.actions,
    { bottom: Math.max(insets.bottom, 16) }]}>
    <Pressable testID="session:new-task:add" accessibilityRole="button"
      accessibilityLabel="新建会话" onPress={() => router.push({ pathname: "/project/[id]/new",
        params: { id: selectedProject.id } })} style={({ pressed }) => [styles.fab,
          { backgroundColor: theme.colors.accent, opacity: pressed ? 0.78 : 1 }, theme.shadow]}>
      <Ionicons name="add" size={30} color={theme.colors.accentForeground} />
    </Pressable>
    <Pressable testID="session:live:add" accessibilityRole="button" accessibilityLabel="Live"
      accessibilityState={{ disabled: !canOpenLive }}
      disabled={!canOpenLive} onPress={openLivePage}
      style={({ pressed }) => [styles.fab, { backgroundColor: theme.colors.accent,
        opacity: canOpenLive ? (pressed ? 0.78 : 1) : 0.4 }, theme.shadow]}>
      <Ionicons name="mic-outline" size={27} color={theme.colors.accentForeground} />
    </Pressable>
  </View> : null;
  const liveStatus = selectedProject && (machineBinding.status !== "bound" || liveProjectStatus !== "matched")
    ? <Text style={[styles.liveStatus, { color: theme.colors.textMuted }]}>{machineBinding.status !== "bound"
      ? machineBinding.message : liveProjectStatus === "loading"
      ? "正在检查 Control 项目…" : liveProjectMessage}</Text> : null;

  if (!tablet) return <Screen>{navigation}{selectors}{liveStatus}
    <View style={styles.mobileList}>{list}</View>{actions}</Screen>;
  return <Screen style={styles.horizontal}>{navigation}<View style={styles.tabletContent}>
    {selectors}{liveStatus}<View style={styles.split}><View style={styles.master}>{list}</View>
      <View style={styles.detail}>{selectedId ? <ConversationPane sessionId={selectedId} /> :
        <EmptyState title="选择一个会话" detail="消息、处理进度、计划和交互问答会显示在这里。" />}</View></View>
  </View>{actions}</Screen>;
}

const styles = StyleSheet.create({
  selectors: { padding: 12, gap: 8, flexDirection: "row" },
  selectorItem: { flex: 1, minWidth: 0 },
  liveStatus: { paddingHorizontal: 16, paddingBottom: 4 },
  mobileList: { flex: 1, minHeight: 0 },
  tabletContent: { flex: 1, minHeight: 0 },
  split: { flex: 1, flexDirection: "row", minHeight: 0 },
  horizontal: { flexDirection: "column" },
  master: { flex: 1, minWidth: 0 },
  detail: { flex: 1.65, minWidth: 0 },
  actions: { position: "absolute", right: 20, gap: 12, flexDirection: "row", zIndex: 10 },
  fab: { width: 56, height: 56, borderRadius: 28,
    alignItems: "center", justifyContent: "center", zIndex: 10, elevation: 6 },
});
