import { router, Stack, useLocalSearchParams } from "expo-router";
import { useState } from "react";

import { EmptyState, Screen } from "@/components/ui";
import { ConversationPane } from "@/features/chat/ConversationPane";
import { SessionActionsMenu } from "@/features/chat/SessionActionsMenu";
import { NewTaskPane } from "@/features/projects/NewTaskPane";
import { useAppStore } from "@/store/appStore";

export default function NewProjectTaskScreen() {
  const { id, sessionId: initialSessionId } = useLocalSearchParams<{
    id: string;
    sessionId?: string;
  }>();
  const [sessionId, setSessionId] = useState<string | null>(initialSessionId ?? null);
  const project = useAppStore((state) => state.projects.find((item) => item.id === id));
  const title = useAppStore((state) => {
    if (!sessionId) return "新会话";
    const record = state.threads.find((item) => item.thread.id === sessionId);
    return record?.thread.name?.trim() || "新会话";
  });

  if (!project) {
    return <Screen><Stack.Screen options={{ title: "新会话" }} />
      <EmptyState title="项目不可用" detail="它可能已被移除，或不在当前连接中。" /></Screen>;
  }

  return <Screen><Stack.Screen options={{ title, headerBackTitle: "会话",
    ...(sessionId ? { headerRight: () => <SessionActionsMenu sessionId={sessionId}
      onArchiveAccepted={() => router.back()} /> } : {}) }} />
    {sessionId ? <ConversationPane sessionId={sessionId} />
      : <NewTaskPane project={project} expanded showHeading={false} onSubmitted={(value) => {
        setSessionId(value);
        router.setParams({ sessionId: value });
      }} />}
  </Screen>;
}
