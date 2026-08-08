import { File } from "expo-file-system";
import * as DocumentPicker from "expo-document-picker";
import * as Crypto from "expo-crypto";
import { CameraView, useCameraPermissions } from "expo-camera";
import { useState } from "react";
import { Alert, Modal, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";

import { ControlApi } from "@/api/control";
import { ConnectionErrorBanner } from "@/components/ConnectionErrorBanner";
import { Button, Card, EmptyState, Muted, Screen, StatusDot, Title } from "@/components/ui";
import { SegmentedControl } from "@/components/SegmentedControl";
import { removeConnection, renameConnection, saveSSHConnection,
  updateSSHHostFingerprint } from "@/db/connections";
import { connectPairingUri } from "@/features/connections/connectPairing";
import { listSSHDirectoryAddress, probeSSHHost, probeSSHHostAddress,
  sshTransport } from "@/native/sshTransport";
import { isPreviewMode } from "@/preview/config";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import type { ThemeMode } from "@/theme/tokens";

export default function ConnectionsScreen() {
  const theme = useTheme();
  const connections = useAppStore((state) => state.connections);
  const active = useAppStore((state) => state.activeConnection);
  const connectionError = useAppStore((state) => state.error);
  const switchConnection = useAppStore((state) => state.switchConnection);
  const reload = useAppStore((state) => state.reloadConnections);
  const mode = useAppStore((state) => state.themeMode);
  const setMode = useAppStore((state) => state.setThemeMode);
  const [permission, requestPermission] = useCameraPermissions();
  const [scanning, setScanning] = useState(false);
  const [claiming, setClaiming] = useState(false);
  const [renameId, setRenameId] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [sshVisible, setSSHVisible] = useState(false);
  const [savingSSH, setSavingSSH] = useState(false);
  const [browsingSSH, setBrowsingSSH] = useState(false);
  const [confirmedFingerprint, setConfirmedFingerprint] = useState<string | null>(null);
  const [directoryBrowser, setDirectoryBrowser] = useState<{
    path: string;
    entries: { name: string; path: string; directory: boolean }[];
  } | null>(null);
  const [ssh, setSSH] = useState({ name: "", host: "", port: "22", user: "",
    root: "", privateKey: "", passphrase: "", publicKey: "" });

  const scanned = async (value: string) => {
    if (claiming) return;
    setClaiming(true);
    try {
      Alert.alert("等待管理员确认", "请回到管理后台确认这台设备。确认完成后连接会自动出现。");
      const profileId = await connectPairingUri(value);
      await reload(); await switchConnection(profileId); setScanning(false);
    } catch (error) {
      Alert.alert("连接失败", error instanceof Error ? error.message : "无法读取二维码");
    } finally { setClaiming(false); }
  };
  const openScanner = async () => {
    if (isPreviewMode) { setScanning(true); return; }
    if (!permission?.granted) {
      const next = await requestPermission();
      if (!next.granted) { Alert.alert("需要相机权限", "请在系统设置中允许相机权限。"); return; }
    }
    setScanning(true);
  };
  const revoke = (profileId: string) => Alert.alert("清除连接？",
    "将移除这个 profile 及其独立本地缓存。", [
      { text: "取消", style: "cancel" },
      { text: "清除", style: "destructive", onPress: () => void (async () => {
        const connection = connections.find((item) => item.profileId === profileId);
        if (connection?.kind === "control") {
          await new ControlApi(connection).deleteDevice().catch(() => undefined);
        }
        await removeConnection(profileId); await reload();
        const remaining = useAppStore.getState().connections[0];
        if (remaining) await switchConnection(remaining.profileId);
      })() },
    ]);

  const inspectKey = async (privateKey = ssh.privateKey) => {
    const result = await sshTransport.inspectPrivateKey(privateKey, ssh.passphrase || null);
    setSSH((current) => ({ ...current, privateKey, publicKey: result.publicKey }));
    return result;
  };
  const importKey = async () => {
    const selected = await DocumentPicker.getDocumentAsync({ copyToCacheDirectory: true });
    if (selected.canceled) return;
    try { await inspectKey(await new File(selected.assets[0]!.uri).text()); }
    catch (error) { Alert.alert("无法读取私钥", error instanceof Error ? error.message : "请检查口令"); }
  };
  const generateKey = async () => {
    try {
      const generated = await sshTransport.generateEd25519Key();
      setSSH((current) => ({ ...current, privateKey: generated.privateKey,
        publicKey: generated.publicKey, passphrase: "" }));
    } catch (error) {
      Alert.alert("无法生成密钥", error instanceof Error ? error.message : "原生 SSH 模块不可用");
    }
  };
  const loadSSHDirectory = async (path: string, fingerprint: string) => {
    const port = Number(ssh.port);
    setBrowsingSSH(true);
    try {
      const entries = await listSSHDirectoryAddress({ profileId: "directory-browser",
        host: ssh.host.trim(), port, user: ssh.user.trim(), privateKey: ssh.privateKey,
        passphrase: ssh.passphrase || null, expectedHostFingerprint: fingerprint }, path);
      setDirectoryBrowser({ path, entries });
    } catch (error) {
      Alert.alert("SFTP 浏览失败", error instanceof Error ? error.message : "无法读取远端目录");
    } finally {
      setBrowsingSSH(false);
    }
  };
  const openSSHBrowser = async () => {
    const port = Number(ssh.port);
    if (!ssh.host.trim() || !ssh.user.trim() || !ssh.privateKey.trim() ||
      !Number.isInteger(port) || port < 1 || port > 65535) {
      Alert.alert("SSH 配置不完整", "浏览前请填写 Host、Port、User 和私钥。");
      return;
    }
    setBrowsingSSH(true);
    try {
      await inspectKey();
      const fingerprint = await probeSSHHostAddress(ssh.host.trim(), port, ssh.user.trim());
      if (confirmedFingerprint && confirmedFingerprint !== fingerprint) {
        setConfirmedFingerprint(null);
        setDirectoryBrowser(null);
        throw new Error(`SSH 主机指纹已变化：期望 ${confirmedFingerprint}，实际 ${fingerprint}`);
      }
      const startPath = ssh.root.trim().startsWith("/") ? ssh.root.trim() : "/";
      if (confirmedFingerprint === fingerprint) {
        await loadSSHDirectory(startPath, fingerprint);
        return;
      }
      setBrowsingSSH(false);
      Alert.alert("确认 SSH 主机指纹", fingerprint, [
        { text: "取消", style: "cancel" },
        { text: "确认并浏览", onPress: () => {
          setConfirmedFingerprint(fingerprint);
          void loadSSHDirectory(startPath, fingerprint);
        } },
      ]);
    } catch (error) {
      setBrowsingSSH(false);
      Alert.alert("SSH 校验失败", error instanceof Error ? error.message : "无法连接主机");
    }
  };
  const createSSH = async () => {
    const port = Number(ssh.port);
    if (!ssh.host.trim() || !ssh.user.trim() || !ssh.root.trim() || !ssh.privateKey.trim() ||
      !Number.isInteger(port) || port < 1 || port > 65535) {
      Alert.alert("SSH 配置不完整", "请填写 Host、Port、User、项目根目录和私钥。");
      return;
    }
    setSavingSSH(true);
    try {
      await inspectKey();
      const fingerprint = await probeSSHHostAddress(ssh.host.trim(), port, ssh.user.trim());
      if (confirmedFingerprint && confirmedFingerprint !== fingerprint) {
        throw new Error(`SSH 主机指纹已变化：期望 ${confirmedFingerprint}，实际 ${fingerprint}`);
      }
      Alert.alert("确认 SSH 主机指纹", fingerprint, [
        { text: "取消", style: "cancel", onPress: () => setSavingSSH(false) },
        { text: "确认并保存", onPress: () => void (async () => {
          try {
            const profileId = Crypto.randomUUID();
            await saveSSHConnection({ kind: "ssh", profileId,
              name: ssh.name.trim() || `${ssh.user.trim()}@${ssh.host.trim()}`,
              host: ssh.host.trim(), port, user: ssh.user.trim(), keyRef: Crypto.randomUUID(),
              hostFingerprint: fingerprint, remoteProjectRoot: ssh.root.trim(),
              privateKey: ssh.privateKey, ...(ssh.passphrase ? { passphrase: ssh.passphrase } : {}) });
            setSSHVisible(false); await reload(); await switchConnection(profileId);
          } catch (error) {
            Alert.alert("保存失败", error instanceof Error ? error.message : "请重试");
          } finally { setSavingSSH(false); }
        })() },
      ]);
    } catch (error) {
      setSavingSSH(false);
      Alert.alert("SSH 校验失败", error instanceof Error ? error.message : "无法连接主机");
    }
  };
  const replaceFingerprint = async (profileId: string) => {
    const connection = connections.find((item) => item.profileId === profileId);
    if (connection?.kind !== "ssh") return;
    try {
      const fingerprint = await probeSSHHost(connection);
      Alert.alert("替换主机指纹？", fingerprint, [
        { text: "取消", style: "cancel" },
        { text: "替换", style: "destructive", onPress: () => void (async () => {
          await updateSSHHostFingerprint(profileId, fingerprint); await reload();
        })() },
      ]);
    } catch (error) { Alert.alert("无法读取主机指纹", error instanceof Error ? error.message : "请重试"); }
  };

  return <Screen><ScrollView contentContainerStyle={styles.screen}>
    <View style={styles.header}><View style={styles.headerCopy}><Title>设备连接</Title>
      <Muted>Control 与 SSH profile 互不合并。</Muted></View>
      <View style={styles.headerActions}>
        <Button testID="connection:add-ssh" title="SSH" variant="secondary"
          onPress={() => setSSHVisible(true)} />
        <Button testID="connection:add" title="扫码" onPress={() => void openScanner()} />
      </View></View>
    <View style={styles.theme}><Muted>外观</Muted><SegmentedControl testIDPrefix="theme" value={mode}
      options={[{ value: "system", label: "跟随系统" }, { value: "light", label: "浅色" },
        { value: "dark", label: "深色" }] as const}
      onChange={(value: ThemeMode) => setMode(value)} /></View>
    <ConnectionErrorBanner />
    <View style={styles.list}>{connections.length === 0
      ? <EmptyState title="还没有连接" detail="扫描 Control 二维码，或直接添加 SSH 主机。" />
      : connections.map((connection) => <Pressable key={connection.profileId}
        testID={`connection:${encodeURIComponent(connection.profileId)}`}
        onPress={() => void switchConnection(connection.profileId)}><Card style={styles.connection}>
        <View style={styles.row}><StatusDot status={active?.profileId === connection.profileId
          ? connectionError ? "danger" : "success" : "muted"} />
          <View testID={active?.profileId === connection.profileId ? "connection:active" : "connection:inactive"}
            style={styles.connectionCopy}><Title>{connection.name}</Title>
            <Muted numberOfLines={1}>{connection.kind === "control" ? connection.baseUrl
              : `${connection.user}@${connection.host}:${connection.port}`}</Muted></View>
          {connection.kind === "ssh" && <Pressable onPress={() => void replaceFingerprint(connection.profileId)}>
            <Text style={{ color: theme.colors.textMuted, padding: 8 }}>指纹</Text></Pressable>}
          <Pressable testID={`connection:${encodeURIComponent(connection.profileId)}:rename`}
            onPress={() => { setRenameId(connection.profileId); setName(connection.name); }}>
            <Text style={{ color: theme.colors.textMuted, padding: 8 }}>改名</Text></Pressable>
          <Pressable testID={`connection:${encodeURIComponent(connection.profileId)}:remove`}
            onPress={() => revoke(connection.profileId)}>
            <Text style={{ color: theme.colors.danger, padding: 8 }}>清除</Text></Pressable>
        </View></Card></Pressable>)}</View>
  </ScrollView>
    <Modal testID="connection:scanner" visible={scanning} animationType="slide"
      onRequestClose={() => setScanning(false)}><View style={styles.scanner}>
      {isPreviewMode ? <View style={[StyleSheet.absoluteFill, styles.previewCamera,
        { backgroundColor: theme.colors.surfaceAlt }]}><View style={[styles.previewQr,
          { borderColor: theme.colors.textMuted }]} /><Muted>预览模式相机画面</Muted></View>
        : <CameraView style={StyleSheet.absoluteFill}
          barcodeScannerSettings={{ barcodeTypes: ["qr"] }}
          onBarcodeScanned={({ data }) => void scanned(data)} />}
      <View style={styles.scannerOverlay}><Title>扫描设备二维码</Title>
        <Muted>请扫描管理后台生成的二维码。</Muted>
        <Button title={claiming ? "等待确认…" : "取消"} disabled={claiming}
          onPress={() => setScanning(false)} /></View>
    </View></Modal>
    <Modal visible={sshVisible} animationType="slide" presentationStyle="pageSheet"
      onRequestClose={() => !savingSSH && setSSHVisible(false)}>
      <ScrollView style={{ backgroundColor: theme.colors.app }} contentContainerStyle={styles.sshForm}>
        <Title>添加 SSH 连接</Title>
        <Field testID="connection:ssh:name" label="名称（可选）" value={ssh.name}
          onChange={(value) => setSSH((current) => ({ ...current, name: value }))} />
        <Field testID="connection:ssh:host" label="Host" value={ssh.host} autoCapitalize="none"
          onChange={(value) => setSSH((current) => ({ ...current, host: value }))} />
        <View style={styles.row}><View style={{ flex: 1 }}><Field testID="connection:ssh:port"
          label="Port" value={ssh.port}
          keyboardType="number-pad" onChange={(value) => setSSH((current) => ({ ...current, port: value }))} /></View>
          <View style={{ flex: 2 }}><Field testID="connection:ssh:user" label="User"
            value={ssh.user} autoCapitalize="none"
            onChange={(value) => setSSH((current) => ({ ...current, user: value }))} /></View></View>
        <Field testID="connection:ssh:root" label="远端项目根目录" value={ssh.root} autoCapitalize="none"
          onChange={(value) => setSSH((current) => ({ ...current, root: value }))} />
        <View style={styles.headerActions}><Button title="浏览 SFTP" variant="secondary"
          loading={browsingSSH} onPress={() => void openSSHBrowser()} />
          {directoryBrowser ? <Button title="使用当前目录"
            onPress={() => {
              setSSH((current) => ({ ...current, root: directoryBrowser.path }));
              setDirectoryBrowser(null);
            }} /> : null}</View>
        {directoryBrowser ? <Card style={styles.directoryBrowser}>
          <Muted numberOfLines={1}>{directoryBrowser.path}</Muted>
          {directoryBrowser.path !== "/" ? <Pressable style={styles.directoryRow}
            onPress={() => {
              if (confirmedFingerprint) void loadSSHDirectory(
                parentRemotePath(directoryBrowser.path), confirmedFingerprint);
            }}><Text style={{ color: theme.colors.accent }}>↰ 上一级</Text></Pressable> : null}
          {directoryBrowser.entries.filter((entry) => entry.directory).map((entry) =>
            <Pressable key={entry.path} style={styles.directoryRow}
              onPress={() => {
                if (confirmedFingerprint) void loadSSHDirectory(entry.path, confirmedFingerprint);
              }}><Text numberOfLines={1} style={{ color: theme.colors.text }}>📁 {entry.name}</Text>
            </Pressable>)}
          {directoryBrowser.entries.every((entry) => !entry.directory)
            ? <Muted>当前目录没有子目录。</Muted> : null}
        </Card> : null}
        <Field testID="connection:ssh:passphrase" label="私钥口令（可选）"
          value={ssh.passphrase} secureTextEntry
          onChange={(value) => setSSH((current) => ({ ...current, passphrase: value }))} />
        <View style={styles.headerActions}><Button testID="connection:ssh:generate-key"
          title="生成 Ed25519" variant="secondary"
          onPress={() => void generateKey()} /><Button title="导入私钥" variant="secondary"
          onPress={() => void importKey()} /></View>
        <TextInput testID="connection:ssh:private-key" multiline value={ssh.privateKey}
          onChangeText={(value) => setSSH((current) => ({ ...current, privateKey: value,
            publicKey: "" }))} placeholder="OpenSSH 私钥" placeholderTextColor={theme.colors.textMuted}
          style={[styles.keyInput, { color: theme.colors.text, borderColor: theme.colors.border }]} />
        {ssh.publicKey ? <View style={[styles.publicKey, { backgroundColor: theme.colors.surfaceAlt }]}>
          <Muted>将这把设备公钥加入远端 authorized_keys</Muted>
          <Text testID="connection:ssh:public-key" selectable
            style={{ color: theme.colors.text, fontFamily: "monospace", fontSize: 12 }}>
            {ssh.publicKey}
          </Text></View> : null}
        <View style={styles.formActions}><Button title="取消" variant="secondary" disabled={savingSSH}
          onPress={() => setSSHVisible(false)} />
          <Button testID="connection:ssh:save" title="校验并保存" loading={savingSSH}
            onPress={() => void createSSH()} /></View>
      </ScrollView>
    </Modal>
    <Modal visible={renameId !== null} transparent animationType="fade"
      onRequestClose={() => setRenameId(null)}><View style={[styles.modalBackdrop,
        { backgroundColor: theme.colors.overlay }]}><Card style={styles.renameCard}>
      <Title>连接名称</Title><TextInput testID="connection:rename:input" value={name}
        onChangeText={setName} autoFocus style={[styles.input,
          { color: theme.colors.text, borderColor: theme.colors.border }]} />
      <View style={styles.row}><Button title="取消" variant="secondary"
        onPress={() => setRenameId(null)} /><Button testID="connection:rename:save" title="保存"
        onPress={() => void (async () => { if (renameId && name.trim()) {
          await renameConnection(renameId, name); await reload(); setRenameId(null);
        } })()} /></View>
    </Card></View></Modal>
  </Screen>;
}

function Field({ testID, label, value, onChange, ...input }: {
  testID?: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  autoCapitalize?: "none";
  keyboardType?: "number-pad";
  secureTextEntry?: boolean;
}) {
  const theme = useTheme();
  return <View style={styles.field}><Muted>{label}</Muted><TextInput testID={testID} value={value}
    onChangeText={onChange} {...input} style={[styles.input,
      { color: theme.colors.text, borderColor: theme.colors.border }]} /></View>;
}

const styles = StyleSheet.create({
  screen: { paddingBottom: 24 },
  header: { padding: 16, flexDirection: "row", alignItems: "center",
    justifyContent: "space-between", gap: 12 },
  headerCopy: { flex: 1, minWidth: 0 },
  headerActions: { flexDirection: "row", gap: 8, flexWrap: "wrap" },
  theme: { paddingHorizontal: 16, gap: 6 },
  list: { padding: 16, gap: 8 },
  connection: { padding: 13 },
  connectionCopy: { flex: 1, minWidth: 0 },
  row: { flexDirection: "row", alignItems: "center", gap: 10 },
  scanner: { flex: 1, justifyContent: "flex-end" },
  previewCamera: { alignItems: "center", justifyContent: "center", gap: 14 },
  previewQr: { width: 160, height: 160, borderWidth: 8, borderRadius: 8 },
  scannerOverlay: { margin: 20, padding: 18, borderRadius: 8,
    backgroundColor: "rgba(255,255,255,0.92)", gap: 8 },
  sshForm: { padding: 20, gap: 13 },
  field: { gap: 5 },
  keyInput: { minHeight: 140, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8,
    padding: 11, fontFamily: "monospace", fontSize: 12, textAlignVertical: "top" },
  publicKey: { borderRadius: 8, padding: 11, gap: 5 },
  directoryBrowser: { gap: 2, maxHeight: 300 },
  directoryRow: { minHeight: 40, justifyContent: "center", paddingHorizontal: 4 },
  formActions: { flexDirection: "row", justifyContent: "flex-end", gap: 8, marginTop: 6 },
  modalBackdrop: { flex: 1, justifyContent: "center", padding: 24 },
  renameCard: { gap: 12 },
  input: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 8, minHeight: 44,
    paddingHorizontal: 12, fontFamily: "Inter_400Regular" },
});

function parentRemotePath(value: string): string {
  const parts = value.split("/").filter(Boolean);
  parts.pop();
  return `/${parts.join("/")}` || "/";
}
