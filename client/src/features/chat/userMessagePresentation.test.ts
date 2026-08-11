import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import { describe, expect, it } from "vitest";

import { parseLegacyAttachmentMessage, projectUserMessage } from "./userMessagePresentation";

describe("用户消息图片投影", () => {
  it("解析桌面 legacy 附件包装并只保留真实请求正文", () => {
    const result = parseLegacyAttachmentMessage(`
# Files mentioned by the user:

## codex-clipboard.png: /home/user/.codex/attachments/id/codex-clipboard.png

## notes.txt: /home/user/.codex/attachments/id/notes.txt

## My request:
怎么评价这个案子
`);

    expect(result).toMatchObject({ text: "怎么评价这个案子", attachments: [
      { name: "codex-clipboard.png", kind: "image", uri: null,
        remotePath: "/home/user/.codex/attachments/id/codex-clipboard.png" },
      { name: "notes.txt", kind: "file", uri: null,
        remotePath: "/home/user/.codex/attachments/id/notes.txt" },
    ] });
  });

  it("不把普通 Markdown 或非官方附件路径误判为包装消息", () => {
    expect(parseLegacyAttachmentMessage("# Files mentioned by the user:\n普通内容")).toBeNull();
    expect(parseLegacyAttachmentMessage(`# Files mentioned by the user:

## image.png: /tmp/image.png

## My request:
正文`)).toBeNull();
  });

  it("投影结构化远端、本地和网络图片且不泄漏 mention 路径", () => {
    const item = { type: "userMessage", id: "user", clientId: "client", content: [
      { type: "text", text: "看图", text_elements: [] },
      { type: "localImage", path: "/remote/cache/a.png" },
      { type: "image", url: "https://example.com/b.webp" },
      { type: "image", url: "data:image/png;base64,iVBORw0KGgo=" },
      { type: "mention", name: "report.pdf", path: "/secret/report.pdf" },
    ] } as ThreadItem;
    const result = projectUserMessage(item as Extract<ThreadItem, { type: "userMessage" }>);

    expect(result.text).toBe("看图");
    expect(result.attachments).toMatchObject([
      { name: "a.png", kind: "image", remotePath: "/remote/cache/a.png", uri: null },
      { name: "b.webp", kind: "image", remotePath: null, uri: "https://example.com/b.webp" },
      { name: "图片", kind: "image", remotePath: null,
        uri: "data:image/png;base64,iVBORw0KGgo=" },
      { name: "report.pdf", kind: "file", remotePath: null, uri: null },
    ]);
  });

  it("桌面同时返回包装路径和结构化 image 时只展示一份图片", () => {
    const item = { type: "userMessage", id: "user", clientId: null, content: [
      { type: "text", text: `\n# Files mentioned by the user:

## screenshot.png: /home/user/.codex/attachments/id/screenshot.png

## My request:
看图\n`, text_elements: [] },
      { type: "image", url: "data:image/png;base64,iVBORw0KGgo=" },
    ] } as ThreadItem;
    const result = projectUserMessage(item as Extract<ThreadItem, { type: "userMessage" }>);

    expect(result.text).toBe("看图");
    expect(result.attachments).toEqual([expect.objectContaining({ name: "图片", kind: "image",
      uri: "data:image/png;base64,iVBORw0KGgo=" })]);
  });
});
