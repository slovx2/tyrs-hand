import { StatusBar } from "expo-status-bar";
import { useState } from "react";
import { Image, Modal, Pressable, StyleSheet, Text, useWindowDimensions,
  type ImageLoadEvent } from "react-native";
import { GestureHandlerRootView } from "react-native-gesture-handler";
import { ResumableZoom, fitContainer } from "react-native-zoom-toolkit";
import { useSafeAreaInsets } from "react-native-safe-area-context";

type ImageSize = { width: number; height: number };

export function ZoomableImageViewer({ uri, visible, onRequestClose }: {
  uri: string;
  visible: boolean;
  onRequestClose(): void;
}) {
  const insets = useSafeAreaInsets();
  const window = useWindowDimensions();
  const [resolution, setResolution] = useState<ImageSize | null>(null);
  if (!visible) return null;

  const imageSize = resolution
    ? fitContainer(resolution.width / resolution.height, window)
    : window;
  const handleLoad = (event: ImageLoadEvent) => {
    const { width, height } = event.nativeEvent.source;
    if (width > 0 && height > 0) setResolution({ width, height });
  };

  return <Modal animationType="fade" hardwareAccelerated navigationBarTranslucent
    onRequestClose={onRequestClose} presentationStyle="fullScreen" statusBarTranslucent visible>
    <GestureHandlerRootView style={styles.container}>
      <StatusBar backgroundColor="transparent" style="light" translucent />
      <ResumableZoom extendGestures maxScale={6} style={styles.zoom}>
        <Image accessibilityLabel="图片预览" accessibilityRole="image" onLoad={handleLoad}
          resizeMode="contain" source={{ uri }} style={imageSize} />
      </ResumableZoom>
      <Pressable accessibilityLabel="关闭图片预览" accessibilityRole="button"
        onPress={onRequestClose} style={[styles.close, { top: insets.top + 8 }]}>
        <Text style={styles.closeText}>✕</Text>
      </Pressable>
    </GestureHandlerRootView>
  </Modal>;
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: "#000" },
  zoom: { flex: 1 },
  close: { position: "absolute", right: 16, width: 48, height: 48, borderRadius: 24,
    alignItems: "center", justifyContent: "center", backgroundColor: "rgba(0, 0, 0, 0.58)" },
  closeText: { color: "#fff", fontSize: 28, lineHeight: 34 },
});
