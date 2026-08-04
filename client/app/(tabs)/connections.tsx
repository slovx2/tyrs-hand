import { CameraView, useCameraPermissions } from "expo-camera";
import { useState } from "react";
import { Alert, Modal, Pressable, StyleSheet, Text, TextInput, View } from "react-native";

import { ClientApi } from "@/api/client";
import { Button, Card, EmptyState, Muted, Screen, StatusDot, Title } from "@/components/ui";
import { SegmentedControl } from "@/components/SegmentedControl";
import { removeConnection, renameConnection } from "@/db/connections";
import { connectPairingUri } from "@/features/connections/connectPairing";
import { isPreviewMode } from "@/preview/config";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import type { ThemeMode } from "@/theme/tokens";

export default function ConnectionsScreen() {
  const theme = useTheme();
  const connections = useAppStore((state) => state.connections);
  const active = useAppStore((state) => state.activeConnection);
  const switchConnection = useAppStore((state) => state.switchConnection);
  const reload = useAppStore((state) => state.reloadConnections);
  const mode = useAppStore((state) => state.themeMode);
  const setMode = useAppStore((state) => state.setThemeMode);
  const [permission, requestPermission] = useCameraPermissions();
  const [scanning, setScanning] = useState(false);
  const [claiming, setClaiming] = useState(false);
  const [renameId, setRenameId] = useState<string | null>(null);
  const [name, setName] = useState("");
  const scanned = async (value: string) => {
    if (claiming) return;
    setClaiming(true);
    try {
      Alert.alert("等待管理员确认", "请回到管理后台确认这台设备。确认完成后连接会自动出现。");
      const serverId = await connectPairingUri(value);
      await reload();
      await switchConnection(serverId);
      setScanning(false);
    } catch (error) {
      Alert.alert("连接失败", error instanceof Error ? error.message : "无法读取二维码");
    } finally { setClaiming(false); }
  };
  const openScanner = async () => {
    if (isPreviewMode) { setScanning(true); return; }
    if (!permission?.granted) {
      const next = await requestPermission();
      if (!next.granted) { Alert.alert("需要相机权限", "请在系统设置中允许相机权限。" ); return; }
    }
    setScanning(true);
  };
  const revoke = (serverId: string) => Alert.alert("清除连接？",
    "将移除这台设备上的连接和本地数据。移除后需要重新扫码添加。", [
      { text: "取消", style: "cancel" },
      { text: "清除", style: "destructive", onPress: () => void (async () => {
        const connection = connections.find((item) => item.serverId === serverId);
        if (connection) await new ClientApi(connection).deleteDevice().catch(() => undefined);
        await removeConnection(serverId); await reload();
        const remaining = useAppStore.getState().connections[0];
        if (remaining) await switchConnection(remaining.serverId);
      })() },
    ]);
  return <Screen>
    <View style={styles.header}><View style={styles.headerCopy}><Title>设备连接</Title><Muted>添加并切换可以使用的 Tyrs Hand 服务。</Muted></View>
      <Button testID="connection:add" title="扫码添加" onPress={() => void openScanner()} /></View>
    <View style={styles.theme}><Muted>外观</Muted><SegmentedControl testIDPrefix="theme" value={mode} options={[
      { value: "system", label: "跟随系统" }, { value: "light", label: "浅色" }, { value: "dark", label: "深色" },
    ] as const} onChange={(value: ThemeMode) => setMode(value)} /></View>
    <View style={styles.list}>
      {connections.length === 0 ? <EmptyState title="还没有连接" detail="在管理后台生成设备二维码，然后用这里的相机扫描。" /> :
        connections.map((connection) => <Pressable key={connection.serverId}
          testID={`connection:${encodeURIComponent(connection.serverId)}`}
          onPress={() => void switchConnection(connection.serverId)}>
          <Card style={styles.connection}>
            <View style={styles.row}><StatusDot status={active?.serverId === connection.serverId ? "success" : "muted"} />
              <View testID={active?.serverId === connection.serverId ? "connection:active" : "connection:inactive"}
                style={{ flex: 1 }}><Title>{connection.name}</Title><Muted numberOfLines={1}>{connection.baseUrl}</Muted></View>
              <Pressable testID={`connection:${encodeURIComponent(connection.serverId)}:rename`}
                onPress={() => { setRenameId(connection.serverId); setName(connection.name); }}>
                <Text style={{ color: theme.colors.textMuted, padding: 8 }}>改名</Text></Pressable>
              <Pressable testID={`connection:${encodeURIComponent(connection.serverId)}:remove`}
                onPress={() => revoke(connection.serverId)}>
                <Text style={{ color: theme.colors.danger, padding: 8 }}>清除</Text></Pressable>
            </View>
          </Card>
        </Pressable>)}
    </View>
    <Modal testID="connection:scanner" visible={scanning} animationType="slide" onRequestClose={() => setScanning(false)}>
      <View style={styles.scanner}>{isPreviewMode ? <View style={[StyleSheet.absoluteFill, styles.previewCamera,
        { backgroundColor: theme.colors.surfaceAlt }]}><View style={[styles.previewQr, { borderColor: theme.colors.textMuted }]} />
        <Muted>预览模式相机画面</Muted></View> : <CameraView style={StyleSheet.absoluteFill}
        barcodeScannerSettings={{ barcodeTypes: ["qr"] }} onBarcodeScanned={({ data }) => void scanned(data)} />}
        <View style={styles.scannerOverlay}><Title>扫描设备二维码</Title><Muted>请扫描管理后台生成的二维码。</Muted>
          <Button title={claiming ? "等待确认…" : "取消"} disabled={claiming} onPress={() => setScanning(false)} /></View>
      </View>
    </Modal>
    <Modal visible={renameId !== null} transparent animationType="fade" onRequestClose={() => setRenameId(null)}>
      <View style={[styles.modalBackdrop, { backgroundColor: theme.colors.overlay }]}><Card style={styles.renameCard}>
        <Title>连接名称</Title><TextInput testID="connection:rename:input" value={name} onChangeText={setName} autoFocus
          style={[styles.input, { color: theme.colors.text, borderColor: theme.colors.border }]} />
        <View style={styles.row}><Button title="取消" variant="secondary" onPress={() => setRenameId(null)} />
          <Button testID="connection:rename:save" title="保存" onPress={() => void (async () => { if (renameId && name.trim()) {
            await renameConnection(renameId, name); await reload(); setRenameId(null); } })()} /></View>
      </Card></View>
    </Modal>
  </Screen>;
}

const styles = StyleSheet.create({
  header: { padding: 16, flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: 12 },
  headerCopy: { flex: 1, minWidth: 0 },
  theme: { paddingHorizontal: 16, gap: 6 }, list: { padding: 16, gap: 8 }, connection: { padding: 13 },
  row: { flexDirection: "row", alignItems: "center", gap: 10 }, scanner: { flex: 1, justifyContent: "flex-end" },
  previewCamera: { alignItems: "center", justifyContent: "center", gap: 14 },
  previewQr: { width: 160, height: 160, borderWidth: 8, borderRadius: 12 },
  scannerOverlay: { margin: 20, padding: 18, borderRadius: 16, backgroundColor: "rgba(255,255,255,0.92)", gap: 8 },
  modalBackdrop: { flex: 1, justifyContent: "center", padding: 24 }, renameCard: { gap: 12 },
  input: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 8, minHeight: 44, paddingHorizontal: 12,
    fontFamily: "Inter_400Regular" },
});
