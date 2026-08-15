import { getControlDeviceToken, type ControlMachineLink } from "@/db/connections";

export type ScheduledTaskProject = {
  id: string;
  name: string;
  relativePath: string;
  projectKind: string;
  availabilityStatus: string;
};

export type ScheduledTaskSession = {
  id: string;
  title: string;
  externalThreadId?: string;
};

export type ScheduledTask = {
  id: string;
  workspaceId: string;
  kind: "standalone" | "heartbeat";
  name: string;
  prompt: string;
  status: "active" | "paused" | "completed" | "deleted";
  schedule: string;
  timezone: string;
  scheduleKind: "interval" | "wall_clock";
  intervalSeconds?: number;
  nextRunAt?: string;
  blockedUntil?: string;
  lastRunAt?: string;
  scheduleRevision: number;
  lastErrorCode?: string;
  lastErrorMessage?: string;
  project: ScheduledTaskProject;
  targetSession?: ScheduledTaskSession;
  standaloneSettings?: {
    agentProfileId: string;
    model?: string;
    reasoningEffort?: string;
    serviceTier?: string;
  };
  createdAt: string;
  updatedAt: string;
};

export type ScheduledTaskRun = {
  id: string;
  scheduleRevision: number;
  trigger: "scheduled" | "run_now";
  triggerKey: string;
  scheduledFor: string;
  coalescedThrough?: string;
  status: "queued" | "running" | "waiting_for_user" | "succeeded" | "failed" | "canceled";
  errorCode?: string;
  errorMessage?: string;
  startedAt?: string;
  finishedAt?: string;
  session?: ScheduledTaskSession;
  createdAt: string;
  updatedAt: string;
};

export type Page<T> = { items: T[]; nextCursor?: string };

async function controlRequest<T>(link: ControlMachineLink, path: string,
  init?: RequestInit): Promise<T> {
  const token = await getControlDeviceToken(link.serverId);
  if (!token) throw new Error("定时任务授权凭证不存在，请重新扫码");
  let response: Response;
  try {
    response = await fetch(`${link.baseUrl.replace(/\/$/, "")}/api/v1/client${path}`, {
      ...init,
      headers: { Accept: "application/json", Authorization: `Bearer ${token}`, ...init?.headers },
    });
  } catch {
    throw new Error("无法连接 Control，定时任务数据暂不可用");
  }
  if (!response.ok) {
    const problem = await response.json().catch(() => null) as { title?: string; detail?: string } | null;
    throw new Error(problem?.detail || problem?.title || `Control 请求失败（${response.status}）`);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export function listScheduledTasks(link: ControlMachineLink,
  status?: ScheduledTask["status"]): Promise<Page<ScheduledTask>> {
  const query = status ? `?limit=100&status=${encodeURIComponent(status)}` : "?limit=100";
  return controlRequest(link, `/machines/${link.workerId}/scheduled-tasks${query}`);
}

export function getScheduledTask(link: ControlMachineLink, taskId: string): Promise<ScheduledTask> {
  return controlRequest(link, `/machines/${link.workerId}/scheduled-tasks/${taskId}`);
}

export function listScheduledTaskRuns(link: ControlMachineLink, taskId: string,
  cursor?: string): Promise<Page<ScheduledTaskRun>> {
  const query = cursor ? `?limit=30&cursor=${encodeURIComponent(cursor)}` : "?limit=30";
  return controlRequest(link, `/machines/${link.workerId}/scheduled-tasks/${taskId}/runs${query}`);
}

export function revokeScheduledTaskMachine(link: ControlMachineLink): Promise<void> {
  return controlRequest(link, `/machines/${link.workerId}`, { method: "DELETE" });
}
