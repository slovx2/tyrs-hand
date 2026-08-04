import { router, Stack, useLocalSearchParams } from "expo-router";
import { Pressable, Text } from "react-native";

import { Screen } from "@/components/ui";
import { ConversationPane } from "@/features/chat/ConversationPane";
import { SessionActionsMenu } from "@/features/chat/SessionActionsMenu";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

export default function SessionDetailScreen() {
  const theme = useTheme();
  const { id } = useLocalSearchParams<{ id: string }>();
  const title = useAppStore((state) => state.sessions.find((item) => item.id === id)?.title ?? "会话");
  return <Screen>
    <Stack.Screen options={{ title, headerBackTitle: "会话", gestureEnabled: true,
      fullScreenGestureEnabled: true, headerLeft: () => <Pressable testID="session:back"
        accessibilityRole="button" accessibilityLabel="返回" hitSlop={8} onPress={() => router.back()}
        style={{ minHeight: 44, paddingRight: 12, justifyContent: "center" }}>
        <Text style={{ color: theme.colors.accent, fontFamily: "Inter_500Medium", fontSize: 16 }}>‹ 返回</Text>
      </Pressable>, headerRight: () => <SessionActionsMenu sessionId={id} /> }} />
    <ConversationPane sessionId={id} />
  </Screen>;
}
