import { z } from "zod";

export const controlProjectSchema = z.object({
  id: z.string().uuid(),
  workspaceId: z.string().uuid(),
  name: z.string(),
  relativePath: z.string(),
  absolutePath: z.string(),
  kind: z.string(),
  availabilityStatus: z.string(),
  branch: z.string().nullable(),
  dirty: z.boolean(),
});
export type ControlProject = z.infer<typeof controlProjectSchema>;

export const controlBootstrapSchema = z.object({
  serverId: z.string().uuid(),
  protocolVersion: z.literal(4),
  user: z.object({ id: z.string().uuid(), username: z.string() }),
  capabilities: z.object({
    attachments: z.boolean(),
    pushNotifications: z.boolean(),
    appServerTunnel: z.literal(true),
  }),
  workspaces: z.array(z.object({ id: z.string().uuid(), name: z.string() })),
  projects: z.array(controlProjectSchema),
});
export type ControlBootstrap = z.infer<typeof controlBootstrapSchema>;

export const appServerTunnelSchema = z.object({
  tunnelId: z.string().uuid(),
  expiresAt: z.string(),
  websocketPath: z.string().startsWith("/"),
});
export type AppServerTunnel = z.infer<typeof appServerTunnelSchema>;

export const materializedAttachmentSchema = z.object({
  id: z.string().uuid(),
  sha256: z.string(),
  filename: z.string(),
  mediaType: z.string(),
  sizeBytes: z.number().int().nonnegative(),
  remotePath: z.string(),
  inputType: z.enum(["localImage", "mention"]),
});
export type MaterializedAttachment = z.infer<typeof materializedAttachmentSchema>;
