import { useState } from "react";
import { Image, Pressable, StyleSheet, Text, View } from "react-native";

import { useTheme } from "@/theme/ThemeProvider";
import { ZoomableImageViewer } from "./ZoomableImageViewer";

export function CachedMessageImage({ uri, filename, testID }: {
  uri: string;
  filename: string;
  testID?: string;
}) {
  const theme = useTheme();
  const [failed, setFailed] = useState(false);
  const [visible, setVisible] = useState(false);
  if (failed || (!uri.startsWith("https://") && !uri.startsWith("http://") &&
    !uri.startsWith("file://") && !uri.startsWith("content://") && !uri.startsWith("data:"))) {
    return <View testID={testID} style={[styles.fallback, { borderColor: theme.colors.border }]}>
      <Text numberOfLines={1} style={{ color: theme.colors.textMuted }}>{filename}</Text>
    </View>;
  }
  return <>
    <Pressable accessibilityRole="imagebutton" accessibilityLabel={`查看图片 ${filename}`}
      onPress={() => setVisible(true)} style={styles.pressable}>
      <Image testID={testID} source={{ uri }} resizeMode="contain"
        onError={() => setFailed(true)}
        style={[styles.image, { backgroundColor: theme.colors.surfaceAlt }]} />
    </Pressable>
    <ZoomableImageViewer key={uri} uri={uri} visible={visible}
      onRequestClose={() => setVisible(false)} />
  </>;
}

const styles = StyleSheet.create({
  pressable: { width: "100%" },
  image: { width: "100%", height: 220, borderRadius: 8, marginVertical: 6 },
  fallback: { width: "100%", minHeight: 44, borderWidth: StyleSheet.hairlineWidth,
    borderRadius: 8, marginVertical: 6, paddingHorizontal: 10, justifyContent: "center" },
});
