import { useWindowDimensions } from "react-native";

export function useTablet(): boolean {
  const { width, height } = useWindowDimensions();
  return Math.min(width, height) >= 600;
}
