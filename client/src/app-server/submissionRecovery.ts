import type { UserInput } from "@codex-app-server/v2/UserInput";

import type { OfficialAppServerClient, SubmitInput, TurnPreferences } from "./officialClient";
import type { PendingSubmission } from "./submissions";

type RecoveryProject = { id: string; workspaceId: string | null };
type RecoveryClient = Pick<OfficialAppServerClient, "connect" | "recoverSubmission">;

export type SubmissionRecoveryResult = {
  recovered: string[];
  errors: string[];
};

const profileRecoveries = new Map<string, Promise<SubmissionRecoveryResult>>();

export function recoverPendingProfileSubmissions(input: {
  profileId: string;
  projects: RecoveryProject[];
  clientFor: (workspaceId: string | null) => RecoveryClient;
  loadPending?: (profileId: string) => Promise<PendingSubmission[]>;
}): Promise<SubmissionRecoveryResult> {
  const active = profileRecoveries.get(input.profileId);
  if (active) return active;
  const recovery = recoverPending(input).finally(() => {
    if (profileRecoveries.get(input.profileId) === recovery) {
      profileRecoveries.delete(input.profileId);
    }
  });
  profileRecoveries.set(input.profileId, recovery);
  return recovery;
}

async function recoverPending(input: {
  profileId: string;
  projects: RecoveryProject[];
  clientFor: (workspaceId: string | null) => RecoveryClient;
  loadPending?: (profileId: string) => Promise<PendingSubmission[]>;
}): Promise<SubmissionRecoveryResult> {
  const result: SubmissionRecoveryResult = { recovered: [], errors: [] };
  const pending = await (input.loadPending ?? loadPersistentPending)(input.profileId);
  for (const record of pending) {
    try {
      const project = requireRecoveryProject(record, input.projects);
      const client = input.clientFor(project.workspaceId);
      await client.connect();
      await client.recoverSubmission(pendingSubmitInput(record));
      result.recovered.push(record.clientMessageId);
    } catch (error) {
      const message = error instanceof Error ? error.message : "未知错误";
      result.errors.push(`${record.clientMessageId}: ${message}`);
    }
  }
  return result;
}

async function loadPersistentPending(profileId: string): Promise<PendingSubmission[]> {
  const { listPendingSubmissions } = await import("./submissions");
  return listPendingSubmissions(profileId);
}

function requireRecoveryProject(record: PendingSubmission,
  projects: RecoveryProject[]): RecoveryProject {
  if (!record.projectId) throw new Error("持久提交缺少项目 ID");
  const project = projects.find((item) => item.id === record.projectId);
  if (!project) throw new Error("持久提交对应的项目已不存在");
  return project;
}

function pendingSubmitInput(record: PendingSubmission): SubmitInput {
  if (!record.threadId) throw new Error("持久提交缺少官方 Thread ID");
  if (!isObject(record.payload) || !Array.isArray(record.payload.input) ||
    !isPreferences(record.payload.preferences)) {
    throw new Error("持久提交 payload 无效");
  }
  return {
    threadId: record.threadId,
    clientMessageId: record.clientMessageId,
    projectId: record.projectId,
    input: record.payload.input as UserInput[],
    preferences: record.payload.preferences,
  };
}

function isPreferences(value: unknown): value is TurnPreferences {
  if (!isObject(value)) return false;
  return typeof value.model === "string" &&
    (value.effort === null || typeof value.effort === "string") &&
    (value.serviceTier === null || typeof value.serviceTier === "string") &&
    typeof value.collaborationMode === "string";
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
