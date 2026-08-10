import MaskedView from "@react-native-masked-view/masked-view";
import { LinearGradient } from "expo-linear-gradient";
import { memo, useEffect, useMemo, useRef, useState } from "react";
import { Animated, Easing, type LayoutChangeEvent,
  StyleSheet, Text, type TextStyle, View } from "react-native";

import { useReducedMotion } from "@/hooks/useReducedMotion";

const SHIMMER_DELAY_MS = 600;
const SHIMMER_DURATION_MS = 1000;
const SHIMMER_INTERVAL_MS = 4000;

type ThinkingShimmerProps = {
  active: boolean;
  children: string;
  color: string;
  highlightColor: string;
  style: TextStyle;
  testID?: string | undefined;
};

export const ThinkingShimmer = memo(function ThinkingShimmer({ active, children, color,
  highlightColor, style, testID }: ThinkingShimmerProps) {
  const progress = useRef(new Animated.Value(0)).current;
  const animation = useRef<Animated.CompositeAnimation | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [width, setWidth] = useState(0);
  const reduceMotion = useReducedMotion();

  useEffect(() => {
    if (!active || reduceMotion || width <= 0) return;
    let canceled = false;
    const clearTimer = () => {
      if (!timer.current) return;
      clearTimeout(timer.current);
      timer.current = null;
    };
    const sweep = () => {
      if (canceled) return;
      progress.setValue(0);
      animation.current = Animated.timing(progress, { toValue: 1,
        duration: SHIMMER_DURATION_MS, easing: Easing.linear, useNativeDriver: true });
      animation.current.start(({ finished }) => {
        animation.current = null;
        if (!finished || canceled) return;
        timer.current = setTimeout(sweep, SHIMMER_INTERVAL_MS - SHIMMER_DURATION_MS);
      });
    };
    timer.current = setTimeout(sweep, SHIMMER_DELAY_MS);
    return () => {
      canceled = true;
      clearTimer();
      animation.current?.stop();
      animation.current = null;
      progress.setValue(0);
    };
  }, [active, progress, reduceMotion, width]);

  const sweepWidth = Math.max(72, width * 0.55);
  const translateX = useMemo(() => progress.interpolate({
    inputRange: [0, 1], outputRange: [-sweepWidth, width + sweepWidth],
  }), [progress, sweepWidth, width]);
  const measure = (event: LayoutChangeEvent) => setWidth(event.nativeEvent.layout.width);

  return <View testID={testID} style={styles.container} onLayout={measure}>
    <Text numberOfLines={1} style={[style, { color }]}>{children}</Text>
    {active && !reduceMotion && width > 0 ? <MaskedView pointerEvents="none"
      style={StyleSheet.absoluteFill} maskElement={
        <Text numberOfLines={1} style={[style, styles.maskText]}>{children}</Text>
      }>
      <Animated.View style={[styles.sweep, { width: sweepWidth, transform: [{ translateX }] }]}>
        <LinearGradient colors={["transparent", highlightColor, "transparent"]}
          locations={[0, 0.5, 1]} start={{ x: 0, y: 0.5 }} end={{ x: 1, y: 0.5 }}
          style={StyleSheet.absoluteFill} />
      </Animated.View>
    </MaskedView> : null}
  </View>;
});

const styles = StyleSheet.create({
  container: { alignSelf: "flex-start", maxWidth: "100%", position: "relative" },
  maskText: { color: "black" },
  sweep: { bottom: 0, position: "absolute", top: 0 },
});
