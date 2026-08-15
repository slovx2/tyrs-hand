import { router } from "expo-router";
import { Alert, FlatList, Pressable, StyleSheet, Text, View } from "react-native";

import { Card, EmptyState, Muted, Screen, StatusDot, Title } from "@/components/ui";
import { ConnectionErrorBanner } from "@/components/ConnectionErrorBanner";
import { removeSSHProject } from "@/db/sshProjects";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

export default function ProjectsScreen() {
  const theme = useTheme();
  const connection = useAppStore((state) => state.activeConnection);
  const projects = useAppStore((state) => state.projects);
  const selectProject = useAppStore((state) => state.setSelectedProject);
  const refresh = useAppStore((state) => state.refresh);

  if (!connection) {
    return <Screen><EmptyState title="还没有可用的连接"
      detail="项目需要先在连接页添加 SSH；扫码授权只用于查看定时任务。" /></Screen>;
  }

  const emptyDetail = connection.kind === "ssh"
    ? "请先在连接页为这个 SSH profile 添加项目。"
    : "当前机器尚未配置 SSH；扫码授权只用于查看定时任务。";

  const openProject = (id: string) => {
    selectProject(id);
    router.push({ pathname: "/project/[id]", params: { id } });
  };
  const confirmRemove = (id: string, name: string) => {
    if (connection.kind !== "ssh") return;
    const profileId = connection.profileId;
    Alert.alert("移除项目？", `只移除 ${name} 的本地入口，不会删除远端目录或 Codex 历史。`, [
      { text: "取消", style: "cancel" },
      { text: "移除", style: "destructive", onPress: () => void (async () => {
        try {
          await removeSSHProject(profileId, id);
          await refresh();
        } catch (error) {
          Alert.alert("移除项目失败", error instanceof Error ? error.message : "请重试");
        }
      })() },
    ]);
  };

  return <Screen><ConnectionErrorBanner /><FlatList testID="projects:list" data={projects}
    keyExtractor={(item) => item.id} contentContainerStyle={styles.list}
    ListEmptyComponent={<EmptyState title="没有项目" detail={emptyDetail} />}
    renderItem={({ item, index }) => <Card style={styles.project}
      testID={`project:${encodeURIComponent(item.id)}`}><View style={styles.row}>
      <Pressable testID={`project:row:${index}`} style={styles.projectOpen}
        accessibilityRole="button" accessibilityLabel={`打开项目 ${item.name}`}
        onPress={() => openProject(item.id)}>
        <StatusDot status={item.availabilityStatus === "available" ? "success" : "warning"} />
        <View style={styles.copy}><Title>{item.name}</Title>
          <Muted numberOfLines={1}>{item.relativePath}{item.branch ? ` · ${item.branch}` : ""}
            {item.dirty ? " · 有修改" : ""}</Muted></View>
      </Pressable>
      {connection.kind === "ssh" ? <Pressable testID={`project:remove:${index}`}
        accessibilityRole="button" accessibilityLabel={`移除项目 ${item.name}`}
        onPress={() => confirmRemove(item.id, item.name)}>
        <Text style={[styles.remove, { color: theme.colors.danger }]}>移除</Text>
      </Pressable> : null}
      <Text style={[styles.chevron, { color: theme.colors.textMuted }]}>›</Text>
    </View></Card>} /></Screen>;
}

const styles = StyleSheet.create({
  list: { padding: 12, gap: 8 },
  project: { gap: 6 },
  row: { flexDirection: "row", gap: 9, alignItems: "center" },
  projectOpen: { flex: 1, minWidth: 0, flexDirection: "row", gap: 9, alignItems: "center" },
  copy: { flex: 1, minWidth: 0 },
  remove: { paddingVertical: 10, paddingHorizontal: 3, fontFamily: "Inter_500Medium" },
  chevron: { fontFamily: "Inter_400Regular", fontSize: 28, lineHeight: 30 },
});
