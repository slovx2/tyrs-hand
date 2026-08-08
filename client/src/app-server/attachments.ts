import type { UserInput } from "@codex-app-server/v2/UserInput";

import { ControlApi } from "@/api/control";
import type { Connection } from "@/db/connections";
import { uploadSSHAttachment } from "@/native/sshTransport";
import type { MobileProject } from "./types";

export type LocalAttachment = {
  uri: string;
  name: string;
  mimeType: string | null;
  size: number | null;
};

export async function materializeUserInput(connection: Connection, project: MobileProject,
  clientMessageId: string, text: string, attachments: LocalAttachment[]): Promise<UserInput[]> {
  const result: UserInput[] = text.trim()
    ? [{ type: "text", text: text.trim(), text_elements: [] }]
    : [];
  for (let index = 0; index < attachments.length; index++) {
    const attachment = attachments[index]!;
    if (connection.kind === "control") {
      if (!project.workspaceId) throw new Error("Control 项目缺少 Workspace ID");
      const response = await new ControlApi(connection).materializeAttachment(project.workspaceId,
        `${clientMessageId}:${index}`, attachment);
      result.push(response.attachment.inputType === "localImage"
        ? { type: "localImage", path: response.attachment.remotePath }
        : { type: "mention", name: response.attachment.filename,
          path: response.attachment.remotePath });
    } else {
      const response = await uploadSSHAttachment(connection, attachment);
      result.push(attachment.mimeType?.startsWith("image/")
        ? { type: "localImage", path: response.remotePath }
        : { type: "mention", name: attachment.name, path: response.remotePath });
    }
  }
  return result;
}
