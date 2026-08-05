import { z } from "zod";

export const reasoningEffortSchema = z.string().min(1).max(64);

export const sessionSettingsSchema = z.object({
  agentProfileId: z.string().uuid(),
  model: z.string().nullable(),
  reasoningEffort: reasoningEffortSchema.nullable(),
  serviceTier: z.enum(["standard", "fast"]),
  collaborationMode: z.enum(["default", "plan"]),
  settingsVersion: z.number().int().nonnegative(),
});
export type SessionSettings = z.infer<typeof sessionSettingsSchema>;

export const projectSchema = z.object({
  id: z.string().uuid(),
  workspaceId: z.string().uuid(),
  name: z.string(),
  relativePath: z.string(),
  kind: z.string(),
  availabilityStatus: z.string(),
  branch: z.string().nullable(),
  dirty: z.boolean(),
});
export type Project = z.infer<typeof projectSchema>;

export const sessionSchema = z.object({
  id: z.string().uuid(),
  workspaceId: z.string().uuid(),
  projectId: z.string().uuid(),
  agentProfileId: z.string().uuid(),
  title: z.string(),
  lifecycleState: z.enum(["active", "archive_pending", "archived", "unarchive_pending"]),
  historyCompleteness: z.enum(["complete", "partial"]),
  model: z.string().nullable(),
  reasoningEffort: z.string().nullable(),
  serviceTier: z.enum(["standard", "fast"]),
  collaborationMode: z.enum(["default", "plan"]),
  settingsVersion: z.number().int(),
  lastMessageSeq: z.number().int(),
  isRunning: z.boolean(),
  hasRunIssue: z.boolean(),
  lastAgentMessageSeq: z.number().int().nonnegative(),
  pendingInteractiveId: z.string().uuid().nullable(),
  lastActivityAt: z.string(),
  createdAt: z.string(),
  updatedAt: z.string(),
});
export type Session = z.infer<typeof sessionSchema>;

export const attachmentSchema = z.object({
  id: z.string().uuid(),
  sessionId: z.string().uuid().nullable(),
  kind: z.enum(["image", "file"]),
  filename: z.string(),
  mediaType: z.string(),
  sizeBytes: z.number().int(),
  sha256: z.string(),
  status: z.enum(["uploaded", "attached", "deleted"]),
  createdAt: z.string(),
});
export type Attachment = z.infer<typeof attachmentSchema>;

export const messageContentSchema = z.discriminatedUnion("type", [
  z.object({ type: z.literal("text"), text: z.string() }),
  z.object({ type: z.literal("image"), attachment: attachmentSchema }),
  z.object({ type: z.literal("file"), attachment: attachmentSchema }),
  z.object({ type: z.literal("event"), event: z.string(), detail: z.string().optional() }),
  z.object({ type: z.literal("plan"), markdown: z.string(), runId: z.string().uuid() }),
]);
export type MessageContentV1 = z.infer<typeof messageContentSchema>;

export const messageSchema = z.object({
  id: z.string().uuid(),
  sessionId: z.string().uuid(),
  seq: z.number().int().positive(),
  localId: z.string(),
  participantId: z.string().uuid().nullable(),
  conversationTurnId: z.string().uuid().nullable().optional(),
  role: z.enum(["user", "agent", "event"]),
  content: z.unknown(),
  attachments: z.array(attachmentSchema),
  createdAt: z.string(),
  updatedAt: z.string(),
});
export type Message = z.infer<typeof messageSchema>;

export const interactiveQuestionSchema = z.object({
  id: z.string(), header: z.string(), question: z.string(),
  options: z.array(z.object({ label: z.string(), description: z.string() })).optional(),
  isSecret: z.boolean().optional(),
});

export const runSnapshotSchema = z.object({
  id: z.string().uuid(),
  status: z.enum(["starting", "running", "waiting_for_user", "reconciling", "completed", "failed", "canceled"]),
  actualSettings: z.object({ model: z.string().nullable(), reasoningEffort: z.string().nullable(),
    serviceTier: z.string().nullable(), collaborationMode: z.enum(["default", "plan"]),
    settingsVersion: z.number().int() }),
  startedAt: z.string(), finishedAt: z.string().nullable(), errorCode: z.string().nullable(),
  errorMessage: z.string().nullable(),
  timeline: z.array(z.object({ sequence: z.number().int(), type: z.string(), payload: z.unknown(), occurredAt: z.string() })),
  pendingInteractives: z.array(z.object({ id: z.string().uuid(), status: z.string(),
    questions: z.array(interactiveQuestionSchema), secret: z.boolean(), deadlineAt: z.string().nullable() })),
});
export type RunSnapshot = z.infer<typeof runSnapshotSchema>;

export const runActivitySchema = z.object({
  id: z.string().uuid(), itemId: z.string(), kind: z.enum(["commentary", "operation"]),
  firstEventSequence: z.number().int().positive(), lastEventSequence: z.number().int().positive(),
  status: z.enum(["running", "completed", "failed"]), payload: z.unknown(), occurredAt: z.string(),
});
export type RunActivity = z.infer<typeof runActivitySchema>;

export const runSegmentSchema = z.object({
  id: z.string().uuid(), sequence: z.number().int().nonnegative(),
  triggerType: z.enum(["initial", "steer", "interactive"]),
  triggerMessageId: z.string().uuid().nullable(), interactiveRequestId: z.string().uuid().nullable(),
  startEventSequence: z.number().int().nonnegative(), endEventSequence: z.number().int().nullable(),
  activityCount: z.number().int().nonnegative(),
});
export type RunSegment = z.infer<typeof runSegmentSchema>;

const interactiveSnapshotSchema = z.object({ id: z.string().uuid(), status: z.string(),
  questions: z.array(interactiveQuestionSchema), secret: z.boolean(), deadlineAt: z.string().nullable() });

export const turnRunSchema = z.object({
  id: z.string().uuid(), attempt: z.number().int().positive(),
  status: z.enum(["starting", "running", "waiting_for_user", "reconciling", "completed", "failed", "canceled"]),
  actualSettings: z.object({ model: z.string().nullable(), reasoningEffort: z.string().nullable(),
    serviceTier: z.string().nullable(), collaborationMode: z.enum(["default", "plan"]),
    settingsVersion: z.number().int() }),
  startedAt: z.string(), finishedAt: z.string().nullable(), errorCode: z.string().nullable(),
  errorMessage: z.string().nullable(), segments: z.array(runSegmentSchema),
  pendingInteractives: z.array(interactiveSnapshotSchema),
});
export type TurnRun = z.infer<typeof turnRunSchema>;

export const conversationTurnSchema = z.object({
  kind: z.enum(["turn", "message"]), id: z.string(), anchorSeq: z.number().int().positive(),
  messages: z.array(messageSchema), runs: z.array(turnRunSchema),
});
export type ConversationTurn = z.infer<typeof conversationTurnSchema>;

export const modelSchema = z.object({
  id: z.string(),
  model: z.string().optional(),
  displayName: z.string(),
  description: z.string(),
  inputModalities: z.array(z.string()).optional().default([]),
  supportedReasoningEfforts: z.array(z.object({ reasoningEffort: reasoningEffortSchema,
    description: z.string() })),
  defaultReasoningEffort: reasoningEffortSchema,
  serviceTiers: z.array(z.object({ id: z.string(), name: z.string(), description: z.string() })).
    optional().default([]),
  additionalSpeedTiers: z.array(z.string()).optional().default([]),
  defaultServiceTier: z.string().nullable().optional().default(null),
  isDefault: z.boolean(),
  hidden: z.boolean().optional().default(false),
});

export const modelCatalogSchema = z.object({
  data: z.array(modelSchema),
  nextCursor: z.string().nullable().optional().default(null),
});

export const bootstrapSchema = z.object({
  serverId: z.string().uuid(),
  protocolVersion: z.literal(3),
  currentCursor: z.number().int().nonnegative(),
  user: z.object({ id: z.string().uuid(), username: z.string() }),
  capabilities: z.record(z.string(), z.boolean()),
  projects: z.array(projectSchema),
  agentProfiles: z.array(z.object({ id: z.string().uuid(), name: z.string() })),
  modelCatalogs: z.record(z.string().uuid(), modelCatalogSchema),
  lastStartedSettings: sessionSettingsSchema.nullable(),
});
export type Bootstrap = z.infer<typeof bootstrapSchema>;
