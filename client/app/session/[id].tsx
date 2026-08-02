import { router, Stack, useLocalSearchParams } from "expo-router";
import { Pressable, Text } from "react-native";

import { Screen } from "@/components/ui";
import { ConversationPane } from "@/features/chat/ConversationPane";
import { useAppStore } from "@/store/appStore";

export default function SessionDetailScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const title = useAppStore((state) => state.sessions.find((item) => item.id === id)?.title ?? "会话");
  return <Screen>
    <Stack.Screen options={{ title, headerBackTitle: "会话", gestureEnabled: true,
      fullScreenGestureEnabled: true, headerLeft: () => <Pressable testID="session:back"
        accessibilityRole="button" onPress={() => router.back()} style={{ paddingVertical: 8, paddingRight: 12 }}>
        <Text>‹ 返回</Text>
      </Pressable> }} />
    <ConversationPane sessionId={id} />
  </Screen>;
}
