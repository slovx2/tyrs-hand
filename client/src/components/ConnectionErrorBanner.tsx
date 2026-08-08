import { StyleSheet, View } from "react-native";

import { useAppStore } from "@/store/appStore";
import { Button, Card, Muted, Title } from "./ui";

export function ConnectionErrorBanner() {
  const error = useAppStore((state) => state.error);
  const refreshing = useAppStore((state) => state.refreshing);
  const refresh = useAppStore((state) => state.refresh);
  if (!error) return null;
  return <View testID="connection:error" style={styles.wrapper}>
    <Card style={styles.card}>
      <View style={styles.copy}>
        <Title>连接失败</Title>
        <Muted selectable>{error}</Muted>
      </View>
      <Button testID="connection:error:retry" title="重试" variant="secondary"
        loading={refreshing} onPress={() => void refresh()} />
    </Card>
  </View>;
}

const styles = StyleSheet.create({
  wrapper: { paddingHorizontal: 12, paddingTop: 12 },
  card: { flexDirection: "row", alignItems: "center", gap: 12, padding: 12 },
  copy: { flex: 1, minWidth: 0, gap: 2 },
});
