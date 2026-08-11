import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";

type UserMessage = Extract<ThreadItem, { type: "userMessage" }>;

export type UserAttachment = {
  key: string;
  name: string;
  kind: "image" | "file";
  uri: string | null;
  remotePath: string | null;
};

export type UserMessagePresentation = { text: string; attachments: UserAttachment[] };

const legacyHeader = "# Files mentioned by the user:\n\n";
const legacyRequestMarker = "\n\n## My request:\n";
const imagePattern = /\.(?:avif|gif|heic|heif|jpe?g|png|webp)$/i;

export function projectUserMessage(item: UserMessage): UserMessagePresentation {
  const textParts: string[] = [];
  const attachments: UserAttachment[] = [];
  const legacyAttachments: UserAttachment[] = [];
  let hasStructuredImage = false;
  for (const [index, input] of item.content.entries()) {
    if (input.type === "text") {
      const legacy = parseLegacyAttachmentMessage(input.text);
      if (legacy) {
        if (legacy.text) textParts.push(legacy.text);
        legacyAttachments.push(...legacy.attachments.map((attachment, attachmentIndex) => ({
          ...attachment, key: `${item.id}:legacy:${index}:${attachmentIndex}`,
        })));
      } else if (input.text) {
        textParts.push(input.text);
      }
      continue;
    }
    if (input.type === "image") {
      hasStructuredImage = true;
      attachments.push(attachment(`${item.id}:image:${index}`, filename(input.url), input.url,
        true));
    } else if (input.type === "localImage") {
      hasStructuredImage = true;
      attachments.push(attachment(`${item.id}:localImage:${index}`, filename(input.path),
        input.path, true));
    } else if (input.type === "mention") {
      attachments.push({ key: `${item.id}:mention:${index}`, name: input.name, kind: "file",
        uri: null, remotePath: null });
    } else if (input.type === "audio" || input.type === "localAudio" || input.type === "skill") {
      const value = "url" in input ? input.url : input.path;
      attachments.push({ key: `${item.id}:${input.type}:${index}`, name: filename(value),
        kind: "file", uri: null, remotePath: null });
    }
  }
  attachments.push(...legacyAttachments.filter((value) =>
    value.kind !== "image" || !hasStructuredImage));
  return { text: textParts.join("\n"), attachments };
}

export function parseLegacyAttachmentMessage(text: string): UserMessagePresentation | null {
  const normalized = text.startsWith(`\n${legacyHeader}`) ? text.slice(1) : text;
  if (!normalized.startsWith(legacyHeader)) return null;
  const markerIndex = normalized.indexOf(legacyRequestMarker, legacyHeader.length);
  if (markerIndex < 0) return null;
  const fileBlock = normalized.slice(legacyHeader.length, markerIndex);
  const sections = fileBlock.split("\n\n");
  if (sections.length === 0) return null;
  const attachments: UserAttachment[] = [];
  for (const [index, section] of sections.entries()) {
    const match = section.match(/^## ([^\r\n]+): (\/[^\r\n]+)$/);
    if (!match?.[1] || !match[2] || !match[2].includes("/.codex/attachments/")) return null;
    attachments.push({ ...attachment(`legacy:${index}`, match[1], match[2]) });
  }
  const request = normalized.slice(markerIndex + legacyRequestMarker.length).replace(/\n$/, "");
  return { text: request, attachments };
}

function attachment(key: string, name: string, source: string,
  forceImage = false): UserAttachment {
  const remotePath = source.startsWith("/") ? source : null;
  return { key, name, kind: forceImage || imagePattern.test(name) || imagePattern.test(source)
    ? "image" : "file",
    uri: remotePath ? null : source, remotePath };
}

function filename(value: string): string {
  if (value.startsWith("data:image/")) return "图片";
  const clean = value.split(/[?#]/, 1)[0] ?? value;
  const name = clean.split("/").at(-1) || "附件";
  try {
    return decodeURIComponent(name);
  } catch {
    return name;
  }
}
