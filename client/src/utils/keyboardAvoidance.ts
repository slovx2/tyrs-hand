export type KeyboardAvoidance = {
  behavior?: "height" | "position" | "padding";
  keyboardVerticalOffset?: number;
  enabled?: boolean;
};

export function keyboardAvoidance(platform: string, topInset: number,
  keyboardVisible = false): KeyboardAvoidance {
  if (platform === "android") return {
    behavior: "height",
    keyboardVerticalOffset: topInset + 56,
    enabled: keyboardVisible,
  };
  if (platform !== "ios") return {};
  return { behavior: "padding", keyboardVerticalOffset: topInset + 44 };
}
