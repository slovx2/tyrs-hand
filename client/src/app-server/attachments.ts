import type { UserInput } from "@codex-app-server/v2/UserInput";

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
    const response = await uploadSSHAttachment(connection, attachment);
    result.push(attachment.mimeType?.startsWith("image/")
      ? { type: "localImage", path: response.remotePath }
      : { type: "mention", name: attachment.name, path: response.remotePath });
  }
  return result;
}
