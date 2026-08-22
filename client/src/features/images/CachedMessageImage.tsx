import { Image } from "expo-image";
import { memo, useEffect, useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";

import { useTheme } from "@/theme/ThemeProvider";
import { ZoomableImageViewer } from "./ZoomableImageViewer";

export const CachedMessageImage = memo(function CachedMessageImage({ uri, filename, cacheKey, testID }: {
  uri: string;
  filename: string;
  cacheKey: string;
  testID?: string;
}) {
  const theme = useTheme();
  const [failed, setFailed] = useState(false);
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    setFailed(false);
    setVisible(false);
  }, [cacheKey, uri]);
  const networkSource = uri.startsWith("https://") || uri.startsWith("http://");
  if (failed || (!uri.startsWith("https://") && !uri.startsWith("http://") &&
    !uri.startsWith("file://") && !uri.startsWith("content://") && !uri.startsWith("data:"))) {
    return <View testID={testID} style={[styles.fallback, { borderColor: theme.colors.border }]}>
      <Text numberOfLines={1} style={{ color: theme.colors.textMuted }}>{filename}</Text>
    </View>;
  }
  return <>
    <Pressable accessibilityRole="imagebutton" accessibilityLabel={`查看图片 ${filename}`}
      onPress={() => setVisible(true)} style={styles.pressable}>
      <Image testID={testID} source={uri} contentFit="contain"
        cachePolicy={networkSource ? "memory-disk" : "memory"}
        priority="low" recyclingKey={`${cacheKey}\0${uri}`}
        onError={() => setFailed(true)}
        style={[styles.image, { backgroundColor: theme.colors.surfaceAlt }]} />
    </Pressable>
    {visible ? <ZoomableImageViewer key={uri} uri={uri} visible
      onRequestClose={() => setVisible(false)} /> : null}
  </>;
});

const styles = StyleSheet.create({
  pressable: { width: "100%" },
  image: { width: "100%", height: 220, borderRadius: 8, marginVertical: 6 },
  fallback: { width: "100%", height: 220, borderWidth: StyleSheet.hairlineWidth,
    borderRadius: 8, marginVertical: 6, paddingHorizontal: 10, justifyContent: "center" },
});
