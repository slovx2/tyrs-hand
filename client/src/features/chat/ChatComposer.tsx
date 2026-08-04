import * as DocumentPicker from "expo-document-picker";
import * as ImagePicker from "expo-image-picker";
import { useState } from "react";
import { Alert, Keyboard, Pressable, StyleSheet, Text, TextInput, View } from "react-native";

import type { LocalAttachment } from "@/sync/outbox";
import { useTheme } from "@/theme/ThemeProvider";

export function ChatComposer({ value, onChange, attachments, onAttachmentsChange, onParameters,
  onSend, sending, parameterLabel }: {
  value: string;
  onChange: (value: string) => void;
  attachments: LocalAttachment[];
  onAttachmentsChange: (attachments: LocalAttachment[]) => void;
  onParameters: () => void;
  onSend: () => void;
  sending: boolean;
  parameterLabel: string;
}) {
  const theme = useTheme();
  const [showAttachmentMenu, setShowAttachmentMenu] = useState(false);
  const add = (items: LocalAttachment[]) => {
    if (attachments.length + items.length > 10) {
      Alert.alert("附件过多", "每条消息最多 10 个附件");
      return;
    }
    const oversized = items.find((item) => item.size !== null && item.size > 25 * 1024 * 1024);
    if (oversized) { Alert.alert("附件过大", `${oversized.name} 超过 25 MiB`); return; }
    onAttachmentsChange([...attachments, ...items]);
  };
  const image = async (camera: boolean) => {
    setShowAttachmentMenu(false);
    const result = camera ? await ImagePicker.launchCameraAsync({ quality: 0.9 }) :
      await ImagePicker.launchImageLibraryAsync({ allowsMultipleSelection: true, quality: 0.9 });
    if (!result.canceled) add(result.assets.map((asset) => ({ uri: asset.uri,
      name: asset.fileName ?? `image-${Date.now()}.jpg`, mimeType: asset.mimeType ?? "image/jpeg",
      size: asset.fileSize ?? null })));
  };
  const document = async () => {
    setShowAttachmentMenu(false);
    const result = await DocumentPicker.getDocumentAsync({ multiple: true, copyToCacheDirectory: true });
    if (!result.canceled) add(result.assets.map((asset) => ({ uri: asset.uri, name: asset.name,
      mimeType: asset.mimeType ?? null, size: asset.size ?? null })));
  };
  return <View>
    {attachments.length > 0 && <View testID="composer:attachment-list" style={styles.attachments}>{attachments.map((item, index) =>
      <Pressable key={`${item.uri}-${index}`} testID={`composer:attachment:${index}`}
        onPress={() => onAttachmentsChange(attachments.filter((_, i) => i !== index))}
        style={[styles.attachment, { backgroundColor: theme.colors.surfaceAlt }]}>
        <Text numberOfLines={1} style={{ color: theme.colors.text, maxWidth: 160 }}>{item.name} ×</Text>
      </Pressable>)}</View>}
    {showAttachmentMenu && <View testID="composer:attachment-menu" style={[styles.menu, { backgroundColor: theme.colors.surface,
      borderColor: theme.colors.border }]}>
      <Pressable testID="composer:attachment:camera" onPress={() => void image(true)}><Text style={[styles.menuText, { color: theme.colors.text }]}>拍照</Text></Pressable>
      <Pressable testID="composer:attachment:library" onPress={() => void image(false)}><Text style={[styles.menuText, { color: theme.colors.text }]}>从相册选择</Text></Pressable>
      <Pressable testID="composer:attachment:document" onPress={() => void document()}><Text style={[styles.menuText, { color: theme.colors.text }]}>选择文件</Text></Pressable>
    </View>}
    <View style={[styles.container, { backgroundColor: theme.colors.surface, borderColor: theme.colors.border }]}>
      <TextInput testID="composer:input" value={value} onChangeText={onChange} multiline placeholder="描述你想完成的任务…"
        placeholderTextColor={theme.colors.textMuted} style={[styles.input, { color: theme.colors.text }]} />
      <View style={styles.actions}>
        <Pressable testID="composer:attachments" accessibilityLabel="添加附件" onPress={() => setShowAttachmentMenu((open) => !open)}
          style={styles.iconButton}><Text style={{ color: theme.colors.text, fontSize: 21 }}>＋</Text></Pressable>
        <Pressable testID="composer:parameters" onPress={onParameters} style={[styles.parameter, { backgroundColor: theme.colors.surfaceAlt }]}> 
          <Text numberOfLines={1} style={{ color: theme.colors.textMuted, fontSize: 12 }}>{parameterLabel}</Text>
        </Pressable>
        <Pressable testID="composer:send" accessibilityLabel="发送" disabled={sending || value.trim() === ""}
          onPress={() => { Keyboard.dismiss(); onSend(); }}
          style={[styles.send, { backgroundColor: theme.colors.accent,
            opacity: sending || value.trim() === "" ? 0.4 : 1 }]}>
          <Text style={{ color: theme.colors.accentForeground, fontSize: 18 }}>↑</Text>
        </Pressable>
      </View>
    </View>
  </View>;
}

const styles = StyleSheet.create({
  container: { margin: 12, borderWidth: StyleSheet.hairlineWidth, borderRadius: 16, padding: 10,
    position: "relative" },
  input: { minHeight: 94, maxHeight: 200, fontFamily: "Inter_400Regular", fontSize: 16,
    paddingHorizontal: 4, paddingTop: 4, paddingBottom: 48, textAlignVertical: "top" },
  actions: { position: "absolute", left: 10, right: 10, bottom: 10,
    flexDirection: "row", alignItems: "center", gap: 8 },
  iconButton: { width: 36, height: 36, alignItems: "center", justifyContent: "center" },
  parameter: { flex: 1, height: 34, borderRadius: 8, paddingHorizontal: 10, justifyContent: "center" },
  send: { width: 36, height: 36, borderRadius: 999, alignItems: "center", justifyContent: "center" },
  attachments: { flexDirection: "row", flexWrap: "wrap", gap: 6, paddingHorizontal: 12 },
  attachment: { borderRadius: 6, paddingHorizontal: 9, paddingVertical: 6 },
  menu: { marginHorizontal: 12, marginBottom: 4, borderWidth: StyleSheet.hairlineWidth,
    borderRadius: 12, padding: 12, gap: 14 },
  menuText: { fontFamily: "Inter_500Medium", fontSize: 15 },
});
