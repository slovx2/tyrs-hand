import { useEffect, useRef } from "react";
import { AccessibilityInfo, Animated, StyleSheet, View } from "react-native";

export function LiveMark({ active, color }: { active: boolean; color: string }) {
  const pulse = useRef(new Animated.Value(0)).current;
  useEffect(() => {
    let cancelled = false;
    let loop: Animated.CompositeAnimation | undefined;
    const play = (reduceMotion: boolean) => {
      loop?.stop();
      pulse.setValue(0);
      if (!active || reduceMotion) return;
      loop = Animated.loop(Animated.timing(pulse, {
        toValue: 1, duration: 2200, useNativeDriver: true,
      }));
      loop.start();
    };
    void AccessibilityInfo.isReduceMotionEnabled().then((enabled) => {
      if (!cancelled) play(enabled);
    });
    const sub = AccessibilityInfo.addEventListener("reduceMotionChanged", play);
    return () => {
      cancelled = true;
      loop?.stop();
      sub.remove();
    };
  }, [active, pulse]);
  const scale = pulse.interpolate({ inputRange: [0, 1], outputRange: [0.86, 1.12] });
  const opacity = pulse.interpolate({ inputRange: [0, 1], outputRange: [0.4, 0] });
  return <View style={styles.mark}>
    <Animated.View style={[styles.ring, styles.outer, { borderColor: color, opacity, transform: [{ scale }] }]} />
    <View style={[styles.ring, styles.inner, { borderColor: color }]} />
    <View style={styles.wave}>
      <View style={[styles.bar, { backgroundColor: color, height: 10 }]} />
      <View style={[styles.bar, { backgroundColor: color, height: 18 }]} />
      <View style={[styles.bar, { backgroundColor: color, height: 12 }]} />
    </View>
  </View>;
}

const styles = StyleSheet.create({
  mark: { width: 96, height: 96, alignItems: "center", justifyContent: "center" },
  ring: { position: "absolute", borderWidth: 1.5, borderRadius: 999 },
  outer: { width: 96, height: 96 },
  inner: { width: 56, height: 56, opacity: 0.9 },
  wave: { flexDirection: "row", alignItems: "center", gap: 4 },
  bar: { width: 4, borderRadius: 2 },
});
