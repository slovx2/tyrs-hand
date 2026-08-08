import { router } from "expo-router";
import { FlatList, Pressable, StyleSheet, Text, View } from "react-native";

import { Card, EmptyState, Muted, Screen, StatusDot, Title } from "@/components/ui";
import { ConnectionErrorBanner } from "@/components/ConnectionErrorBanner";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

export default function ProjectsScreen() {
  const theme = useTheme();
  const connection = useAppStore((state) => state.activeConnection);
  const projects = useAppStore((state) => state.projects);
  const selectProject = useAppStore((state) => state.setSelectedProject);

  if (!connection) {
    return <Screen><EmptyState title="还没有可用的连接"
      detail="请先在连接页扫描管理后台中的设备二维码。" /></Screen>;
  }

  const openProject = (id: string) => {
    selectProject(id);
    router.push({ pathname: "/project/[id]", params: { id } });
  };

  return <Screen><ConnectionErrorBanner /><FlatList testID="projects:list" data={projects}
    keyExtractor={(item) => item.id} contentContainerStyle={styles.list}
    ListEmptyComponent={<EmptyState title="没有项目" detail="当前连接中暂时没有可用项目。" />}
    renderItem={({ item, index }) => <Pressable testID={`project:row:${index}`}
      accessibilityRole="button" accessibilityLabel={`打开项目 ${item.name}`}
      onPress={() => openProject(item.id)}>
      <Card style={styles.project} testID={`project:${encodeURIComponent(item.id)}`}>
        <View style={styles.row}><StatusDot status={item.availabilityStatus === "available" ? "success" : "warning"} />
          <View style={styles.copy}><Title>{item.name}</Title></View>
          <Text style={[styles.chevron, { color: theme.colors.textMuted }]}>›</Text></View>
        <Muted numberOfLines={1}>{item.relativePath}{item.branch ? ` · ${item.branch}` : ""}
          {item.dirty ? " · 有修改" : ""}</Muted>
      </Card>
    </Pressable>} /></Screen>;
}

const styles = StyleSheet.create({
  list: { padding: 12, gap: 8 },
  project: { gap: 6 },
  row: { flexDirection: "row", gap: 9, alignItems: "center" },
  copy: { flex: 1, minWidth: 0 },
  chevron: { fontFamily: "Inter_400Regular", fontSize: 28, lineHeight: 30 },
});
