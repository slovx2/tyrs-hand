import { useState } from "react";
import { Alert, Modal, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { threadTitle } from "@/app-server/types";
import { Button, Title } from "@/components/ui";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

export function SessionActionsMenu({ sessionId, onArchiveAccepted }: {
  sessionId: string;
  onArchiveAccepted?: () => void;
}) {
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  const record = useAppStore((state) => state.threads.find((item) => item.thread.id === sessionId));
  const interrupt = useAppStore((state) => state.interruptThread);
  const setArchived = useAppStore((state) => state.setThreadArchived);
  const rename = useAppStore((state) => state.renameThread);
  const [visible, setVisible] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [title, setTitle] = useState("");
  const [loading, setLoading] = useState(false);
  if (!record) return null;
  const running = record.thread.status.type === "active";

  const run = async (operation: () => Promise<void>, errorTitle: string) => {
    setLoading(true);
    try { await operation(); setVisible(false); }
    catch (cause) { Alert.alert(errorTitle, cause instanceof Error ? cause.message : "请重试"); }
    finally { setLoading(false); }
  };

  return <>
    <Pressable testID="session:more" accessibilityRole="button" accessibilityLabel="更多会话操作"
      hitSlop={8} onPress={() => setVisible(true)} style={({ pressed }) => [styles.more,
        { opacity: pressed ? 0.55 : 1 }]}>
      <Text style={[styles.moreText, { color: theme.colors.text }]}>•••</Text>
    </Pressable>
    <Modal visible={visible} transparent animationType="fade" onRequestClose={() => setVisible(false)}>
      <View style={styles.modalRoot}>
        <Pressable accessibilityRole="button" accessibilityLabel="关闭菜单"
          style={StyleSheet.absoluteFill} onPress={() => setVisible(false)} />
        <View style={[styles.menu, { top: insets.top + 48, backgroundColor: theme.colors.surface,
          borderColor: theme.colors.border }, theme.shadow]}>
          {running && <Pressable testID="session:stop" disabled={loading}
            onPress={() => void run(() => interrupt(sessionId), "停止失败")} style={styles.menuItem}>
            <Text style={[styles.menuText, { color: theme.colors.danger }]}>停止</Text>
          </Pressable>}
          <Pressable testID="session:rename" disabled={loading} onPress={() => {
            setVisible(false); setTitle(threadTitle(record.thread)); setRenaming(true);
          }} style={styles.menuItem}>
            <Text style={[styles.menuText, { color: theme.colors.text }]}>改名</Text>
          </Pressable>
          <View style={[styles.divider, { backgroundColor: theme.colors.border }]} />
          <Pressable testID={record.archived ? "session:restore" : "session:archive"}
            disabled={loading} onPress={() => void run(async () => {
              await setArchived(sessionId, !record.archived);
              if (!record.archived) onArchiveAccepted?.();
            }, record.archived ? "恢复失败" : "归档失败")} style={styles.menuItem}>
            <Text style={[styles.menuText,
              { color: record.archived ? theme.colors.text : theme.colors.danger }]}>
              {record.archived ? "恢复" : "归档"}
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
            maxLength={120} style={[styles.titleInput,
              { color: theme.colors.text, borderColor: theme.colors.border }]} />
          <View style={styles.renameActions}>
            <Button title="取消" variant="secondary" disabled={loading}
              onPress={() => setRenaming(false)} />
            <Button testID="session:rename:save" title="保存" loading={loading}
              disabled={!title.trim()} onPress={() => void run(async () => {
                await rename(sessionId, title.trim()); setRenaming(false);
              }, "改名失败")} />
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
  menu: { position: "absolute", right: 10, width: 180,
    borderWidth: StyleSheet.hairlineWidth, borderRadius: 8, paddingVertical: 6, overflow: "hidden" },
  menuItem: { minHeight: 48, paddingHorizontal: 16, justifyContent: "center" },
  menuText: { fontFamily: "Inter_500Medium", fontSize: 16 },
  divider: { height: StyleSheet.hairlineWidth, marginVertical: 4 },
  renameBackdrop: { flex: 1, justifyContent: "center", padding: 24 },
  renameDialog: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 8, padding: 16, gap: 12 },
  titleInput: { minHeight: 44, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8,
    paddingHorizontal: 12, fontFamily: "Inter_400Regular" },
  renameActions: { flexDirection: "row", gap: 8, justifyContent: "flex-end" },
});
