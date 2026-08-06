import { memo, useEffect, useMemo, useState } from "react";
import { ActivityIndicator, Image, Modal, Pressable, StyleSheet, Text, useWindowDimensions,
  View } from "react-native";

import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import type { Attachment } from "@/types/protocol";
import { resolveImageURI, type ImageReference } from "./imageCache";

type Props = {
  attachment?: Attachment;
  uri?: string;
  filename: string;
  testID: string;
  thumbnail?: boolean;
};

export const CachedMessageImage = memo(function CachedMessageImage({ attachment, uri, filename,
  testID, thumbnail = false }: Props) {
  const connection = useAppStore((state) => state.activeConnection);
  const theme = useTheme();
  const window = useWindowDimensions();
  const [resolvedURI, setResolvedURI] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [ratio, setRatio] = useState(thumbnail ? 1 : 4 / 3);
  const [viewer, setViewer] = useState(false);
  const identity = attachment ? `attachment:${attachment.id}:${attachment.sha256}` : `uri:${uri ?? ""}`;
  const reference = useMemo<ImageReference | null>(() => attachment ?
    { type: "attachment", attachment } : uri ? { type: "uri", uri } : null,
  // identity 精确表达了需要重新解析图片的字段。
  // eslint-disable-next-line react-hooks/exhaustive-deps
  [identity]);

  useEffect(() => {
    let active = true;
    setResolvedURI(null);
    setError(null);
    if (!connection || !reference) {
      setError("图片地址不可用");
      return () => { active = false; };
    }
    void resolveImageURI(connection, reference).then((value) => {
      if (active) setResolvedURI(value);
    }, (reason: unknown) => {
      if (active) setError(reason instanceof Error ? reason.message : "图片加载失败");
    });
    return () => { active = false; };
  }, [attempt, connection, reference]);

  const maxWidth = Math.min(window.width - (thumbnail ? 52 : 56), 640);
  const imageHeight = thumbnail ? undefined : Math.min(maxWidth / Math.max(ratio, 0.1), 360);
  const retry = () => setAttempt((value) => value + 1);
  const content = error ? <Pressable accessibilityRole="button" onPress={retry}
    testID={`${testID}:retry`} style={[styles.placeholder, thumbnail && styles.thumbnail,
      { borderColor: theme.colors.border, backgroundColor: theme.colors.surfaceAlt }]}>
    <Text style={[styles.failureTitle, { color: theme.colors.text }]}>图片加载失败</Text>
    <Text numberOfLines={2} style={[styles.failureDetail, { color: theme.colors.textMuted }]}>点击重试</Text>
  </Pressable> : resolvedURI ? <Pressable accessibilityRole="imagebutton" accessibilityLabel={`查看图片 ${filename}`}
    testID={testID} onPress={() => setViewer(true)} style={thumbnail && styles.thumbnail}>
    <Image source={{ uri: resolvedURI }} resizeMode={thumbnail ? "cover" : "contain"}
      onError={() => {
        setResolvedURI(null);
        if (attempt === 0) setAttempt(1);
        else setError("图片文件无法读取");
      }}
      onLoad={(event) => {
        const source = event.nativeEvent.source;
        if (source.width > 0 && source.height > 0) setRatio(source.width / source.height);
      }} style={[styles.image, thumbnail ? styles.thumbnail : { height: imageHeight,
        backgroundColor: theme.colors.surfaceAlt }]} />
  </Pressable> : <View testID={`${testID}:loading`} style={[styles.placeholder,
    thumbnail && styles.thumbnail, { backgroundColor: theme.colors.surfaceAlt }]}>
    <ActivityIndicator color={theme.colors.textMuted} />
  </View>;

  return <>
    <View style={thumbnail ? styles.thumbnailFrame : styles.singleFrame}>{content}</View>
    <Modal visible={viewer} animationType="fade" statusBarTranslucent
      onRequestClose={() => setViewer(false)}>
      <View testID="image:viewer" style={styles.viewer}>
        <View style={styles.viewerHeader}>
          <Text numberOfLines={1} style={styles.viewerFilename}>{filename}</Text>
          <Pressable accessibilityRole="button" accessibilityLabel="关闭图片"
            testID="image:viewer:close" onPress={() => setViewer(false)} style={styles.closeButton}>
            <Text style={styles.closeText}>×</Text>
          </Pressable>
        </View>
        {resolvedURI ? <Image source={{ uri: resolvedURI }} resizeMode="contain" style={styles.viewerImage} /> : null}
      </View>
    </Modal>
  </>;
});

const styles = StyleSheet.create({
  singleFrame: { width: "100%", marginTop: 7, overflow: "hidden", borderRadius: 10 },
  thumbnailFrame: { width: "49%", aspectRatio: 1, overflow: "hidden", borderRadius: 9 },
  image: { width: "100%", borderRadius: 10 },
  thumbnail: { width: "100%", height: "100%", borderRadius: 9 },
  placeholder: { width: "100%", height: 180, borderRadius: 10, alignItems: "center",
    justifyContent: "center", borderWidth: StyleSheet.hairlineWidth, padding: 12 },
  failureTitle: { fontFamily: "Inter_600SemiBold", fontSize: 14 },
  failureDetail: { marginTop: 3, fontFamily: "Inter_400Regular", fontSize: 12 },
  viewer: { flex: 1, backgroundColor: "#050505" },
  viewerHeader: { position: "absolute", zIndex: 2, top: 0, left: 0, right: 0, minHeight: 84,
    paddingTop: 28, paddingLeft: 18, paddingRight: 10, paddingBottom: 8,
    flexDirection: "row", alignItems: "center",
    backgroundColor: "rgba(0,0,0,0.58)" },
  viewerFilename: { flex: 1, color: "#fff", fontFamily: "Inter_500Medium", fontSize: 15 },
  closeButton: { width: 44, height: 44, alignItems: "center", justifyContent: "center", borderRadius: 22,
    backgroundColor: "rgba(255,255,255,0.14)" },
  closeText: { color: "#fff", fontSize: 30, lineHeight: 34 },
  viewerImage: { flex: 1, width: "100%", height: "100%" },
});
