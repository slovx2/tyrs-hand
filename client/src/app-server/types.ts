import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { ModeKind } from "@codex-app-server/ModeKind";
import type { ReasoningEffort } from "@codex-app-server/ReasoningEffort";
import type { Model } from "@codex-app-server/v2/Model";
import type { Thread } from "@codex-app-server/v2/Thread";
import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import type { Turn } from "@codex-app-server/v2/Turn";

export type UserInputResponseItem = {
  type: "userInputResponse";
  id: string;
  requestId: string;
  turnId: string;
  questions: {
    id: string;
    header: string;
    question: string;
    options: { label: string; description: string }[];
  }[];
  answers: Record<string, string[]>;
  completed: true;
};

export type MobileThreadItem = ThreadItem | UserInputResponseItem;
export type MobileTurn = Omit<Turn, "items"> & { items: MobileThreadItem[] };
export type MobileThread = Omit<Thread, "turns"> & { turns: MobileTurn[] };

export type MobileProject = {
  id: string;
  workspaceId: string | null;
  name: string;
  relativePath: string;
  cwd: string;
  kind: string;
  availabilityStatus: string;
  branch: string | null;
  dirty: boolean;
};

export type ThreadRecord = {
  thread: MobileThread;
  archived: boolean;
  workspaceId: string | null;
  projectId: string | null;
  preferences?: ThreadPreferences | null | undefined;
  history: ThreadHistoryState;
};

export type ThreadPreferences = {
  model: string;
  effort: ReasoningEffort | null;
  serviceTier: string | null;
  collaborationMode: ModeKind;
};

export type ThreadHistoryState =
  | { kind: "summary" }
  | {
    kind: "loaded";
    olderCursor: string | null;
    tailOlderCursor: string | null;
    hasLoadedOldest: boolean;
  };

export type TargetCatalog = {
  workspaceId: string | null;
  models: Model[];
};

export type PendingRequestRecord = {
  profileId: string;
  request: ServerRequest;
};

export function threadTitle(thread: MobileThread): string {
  return thread.name?.trim() || thread.preview.trim() || "新的开发任务";
}

export function projectForThread(projects: MobileProject[], thread: MobileThread): MobileProject | null {
  const cwd = cleanPath(thread.cwd);
  return projects
    .filter((project) => isWithinProject(cwd, cleanPath(project.cwd)))
    .sort((left, right) => right.cwd.length - left.cwd.length)[0] ?? null;
}

export function targetKey(profileId: string, workspaceId: string | null): string {
  return `${profileId}:${workspaceId ?? "ssh"}`;
}

function cleanPath(value: string): string {
  const normalized = value.replace(/\\/g, "/").replace(/\/+$/, "");
  return normalized || "/";
}

function isWithinProject(cwd: string, root: string): boolean {
  return root === "/" ? cwd.startsWith("/") : cwd === root || cwd.startsWith(`${root}/`);
}
