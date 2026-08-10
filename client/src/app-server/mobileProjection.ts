import type { Thread } from "@codex-app-server/v2/Thread";
import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import type { Turn } from "@codex-app-server/v2/Turn";

/**
 * 移动端只保留复原会话顺序与描述操作所需的数据。
 * 原始工具输出不会进入 Store 或 SQLite，避免流式输出反复触发大对象投影。
 */
export function projectThreadForMobile(thread: Thread): Thread {
  if (thread.turns.length === 0) return thread;
  return { ...thread, turns: thread.turns.map(projectTurnForMobile) };
}

export function projectTurnForMobile(turn: Turn): Turn {
  return { ...turn, items: turn.items.map(projectItemForMobile) };
}

export function projectItemForMobile(item: ThreadItem): ThreadItem {
  switch (item.type) {
  case "commandExecution":
    return { ...item, commandActions: [], aggregatedOutput: null };
  case "fileChange":
    return { ...item, changes: item.changes.map((change) => ({ ...change, diff: "" })) };
  case "mcpToolCall":
    return { ...item, arguments: null, result: null, error: null };
  case "dynamicToolCall":
    return { ...item, arguments: null, contentItems: null };
  case "collabAgentToolCall":
    return { ...item, prompt: null, agentsStates: {} };
  case "webSearch":
    return { ...item, results: null };
  case "imageGeneration": {
    const { savedPath: _savedPath, ...rest } = item;
    return { ...rest, revisedPrompt: null, result: "" };
  }
  default:
    return item;
  }
}
