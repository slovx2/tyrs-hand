import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { Model } from "@codex-app-server/v2/Model";
import type { Thread } from "@codex-app-server/v2/Thread";

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
  thread: Thread;
  archived: boolean;
  workspaceId: string | null;
  projectId: string | null;
};

export type TargetCatalog = {
  workspaceId: string | null;
  models: Model[];
};

export type PendingRequestRecord = {
  profileId: string;
  request: ServerRequest;
};

export function threadTitle(thread: Thread): string {
  return thread.name?.trim() || thread.preview.trim() || "新的开发任务";
}

export function projectForThread(projects: MobileProject[], thread: Thread): MobileProject | null {
  const cwd = cleanPath(thread.cwd);
  return projects
    .filter((project) => cwd === cleanPath(project.cwd) || cwd.startsWith(`${cleanPath(project.cwd)}/`))
    .sort((left, right) => right.cwd.length - left.cwd.length)[0] ?? null;
}

export function targetKey(profileId: string, workspaceId: string | null): string {
  return `${profileId}:${workspaceId ?? "ssh"}`;
}

function cleanPath(value: string): string {
  const normalized = value.replace(/\\/g, "/").replace(/\/+$/, "");
  return normalized || "/";
}
