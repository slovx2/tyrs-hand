import { useCallback, useState } from "react";
import { Alert, Modal, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { ClientApi } from "@/api/client";
import { Button, Title } from "@/components/ui";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

const activeRunStatuses = new Set(["starting", "running", "waiting_for_user", "reconciling"]);

interface SessionActionsMenuProps {
  sessionId: string;
  onArchiveAccepted?: () => void;
}

export function SessionActionsMenu({ sessionId, onArchiveAccepted }: SessionActionsMenuProps) {
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  const connection = useAppStore((state) => state.activeConnection);
  const session = useAppStore((state) => state.sessions.find((item) => item.id === sessionId));
  const refreshSessions = useAppStore((state) => state.refresh);
  const upsertSession = useAppStore((state) => state.upsertSession);
  const [visible, setVisible] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [title, setTitle] = useState("");
  const [running, setRunning] = useState(false);
  const [loading, setLoading] = useState(false);

  const refreshRunStatus = useCallback(async () => {
    if (!connection) return;
    try {
      const detail = await new ClientApi(connection).getSession(sessionId);
      setRunning(Boolean(detail.currentRun && activeRunStatuses.has(detail.currentRun.status)));
    } catch {
      setRunning(false);
    }
  }, [connection, sessionId]);

  if (!connection || !session) return null;

  const action = async (value: "stop" | "archive" | "restore") => {
    setLoading(true);
    try {
      await new ClientApi(connection).action(sessionId, value);
      setVisible(false);
      if (value === "stop") setRunning(false);
      if (value === "archive") onArchiveAccepted?.();
      try {
        await refreshSessions();
      } catch (error) {
        if (value !== "archive") throw error;
      }
    } catch (error) {
      Alert.alert("操作失败", error instanceof Error ? error.message : "请重试");
    } finally {
      setLoading(false);
    }
  };

  const saveTitle = async () => {
    const value = title.trim();
    if (!value) return;
    setLoading(true);
    try {
      const updated = await new ClientApi(connection).patchSession(sessionId, { title: value });
      upsertSession(updated);
      setRenaming(false);
      await refreshSessions();
    } catch (error) {
      Alert.alert("改名失败", error instanceof Error ? error.message : "请重试");
    } finally {
      setLoading(false);
    }
  };

  const lifecyclePending = session.lifecycleState === "archive_pending" ||
    session.lifecycleState === "unarchive_pending";
  const archived = session.lifecycleState === "archived" || session.lifecycleState === "unarchive_pending";
  const lifecycleLabel = session.lifecycleState === "archive_pending" ? "归档中…" :
    session.lifecycleState === "unarchive_pending" ? "恢复中…" : archived ? "恢复" : "归档";

  return <>
    <Pressable testID="session:more" accessibilityRole="button" accessibilityLabel="更多会话操作"
      hitSlop={8} onPress={() => {
        setVisible(true);
        void refreshRunStatus();
      }} style={({ pressed }) => [styles.more, { opacity: pressed ? 0.55 : 1 }]}>
      <Text style={[styles.moreText, { color: theme.colors.text }]}>•••</Text>
    </Pressable>
    <Modal visible={visible} transparent animationType="fade" onRequestClose={() => setVisible(false)}>
      <View style={styles.modalRoot}>
        <Pressable accessibilityRole="button" accessibilityLabel="关闭菜单" style={StyleSheet.absoluteFill}
          onPress={() => setVisible(false)} />
        <View style={[styles.menu, { top: insets.top + 48, backgroundColor: theme.colors.surface,
          borderColor: theme.colors.border }, theme.shadow]}>
          {running && <Pressable testID="session:stop" disabled={loading} onPress={() => void action("stop")}
            style={({ pressed }) => [styles.menuItem, { opacity: loading ? 0.45 : pressed ? 0.6 : 1 }]}>
            <Text style={[styles.menuText, { color: theme.colors.danger }]}>停止</Text>
          </Pressable>}
          <Pressable testID="session:rename" disabled={loading} onPress={() => {
            setVisible(false);
            setTitle(session.title);
            setRenaming(true);
          }} style={({ pressed }) => [styles.menuItem, { opacity: loading ? 0.45 : pressed ? 0.6 : 1 }]}>
            <Text style={[styles.menuText, { color: theme.colors.text }]}>改名</Text>
          </Pressable>
          <View style={[styles.divider, { backgroundColor: theme.colors.border }]} />
          <Pressable testID={archived ? "session:restore" : "session:archive"}
            disabled={loading || lifecyclePending}
            onPress={() => void action(archived ? "restore" : "archive")}
            style={({ pressed }) => [styles.menuItem,
              { opacity: loading || lifecyclePending ? 0.45 : pressed ? 0.6 : 1 }]}>
            <Text style={[styles.menuText, { color: archived ? theme.colors.text : theme.colors.danger }]}>
              {lifecycleLabel}
            </Text>
          </Pressable>
        </View>
      </View>
    </Modal>
    <Modal visible={renaming} transparent animationType="fade" onRequestClose={() => setRenaming(false)}>
      <View style={[styles.renameBackdrop, { backgroundColor: theme.colors.overlay }]}>
        <View style={[styles.renameDialog, { backgroundColor: theme.colors.surface,
          borderColor: theme.colors.border }]}>
          <Title>修改会话名称</Title>
          <TextInput testID="session:rename:input" autoFocus value={title} onChangeText={setTitle}
            maxLength={120} style={[styles.titleInput, { color: theme.colors.text,
              borderColor: theme.colors.border }]} />
          <View style={styles.renameActions}>
            <Button title="取消" variant="secondary" disabled={loading} onPress={() => setRenaming(false)} />
            <Button testID="session:rename:save" title="保存" loading={loading} disabled={!title.trim()}
              onPress={() => void saveTitle()} />
          </View>
        </View>
      </View>
    </Modal>
  </>;
}

const styles = StyleSheet.create({
  more: { width: 44, height: 44, alignItems: "center", justifyContent: "center" },
  moreText: { fontFamily: "Inter_600SemiBold", fontSize: 18, letterSpacing: 1, marginTop: -7 },
  modalRoot: { flex: 1 },
  menu: { position: "absolute", right: 10, width: 180, borderWidth: StyleSheet.hairlineWidth,
    borderRadius: 12, paddingVertical: 6, overflow: "hidden" },
  menuItem: { minHeight: 48, paddingHorizontal: 16, justifyContent: "center" },
  menuText: { fontFamily: "Inter_500Medium", fontSize: 16 },
  divider: { height: StyleSheet.hairlineWidth, marginVertical: 4 },
  renameBackdrop: { flex: 1, justifyContent: "center", padding: 24 },
  renameDialog: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 12, padding: 16, gap: 12 },
  titleInput: { minHeight: 44, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8,
    paddingHorizontal: 12, fontFamily: "Inter_400Regular" },
  renameActions: { flexDirection: "row", gap: 8, justifyContent: "flex-end" },
});
