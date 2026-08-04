import { router, Stack, useLocalSearchParams } from "expo-router";

import { EmptyState, Screen } from "@/components/ui";
import { NewTaskPane } from "@/features/projects/NewTaskPane";
import { useAppStore } from "@/store/appStore";

export default function NewProjectTaskScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const project = useAppStore((state) => state.bootstrap?.projects.find((item) => item.id === id));

  if (!project) {
    return <Screen><Stack.Screen options={{ title: "新任务" }} />
      <EmptyState title="项目不可用" detail="它可能已被移除，或不在当前连接中。" /></Screen>;
  }

  return <Screen><Stack.Screen options={{ title: project.name }} />
    <NewTaskPane project={project} expanded onSubmitted={(sessionId) => router.replace({
      pathname: "/session/[id]", params: { id: sessionId },
    })} />
  </Screen>;
}
