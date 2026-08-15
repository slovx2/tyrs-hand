import { describe, expect, it } from "vitest";

import { lookupMarkdownPlaceholder, parseResponseDirective, prepareMarkdown,
  renderableFinalAnswer } from "./responseDirectives";

describe("parseResponseDirective", () => {
  it("把 Codex 行协议解析为名称和属性", () => {
    expect(parseResponseDirective('::git-push{cwd="/workspace/project" branch="main"}')).toEqual({
      name: "git-push",
      attributes: { cwd: "/workspace/project", branch: "main" },
    });
  });
});

describe("renderableFinalAnswer", () => {
  it("移除最终回答中独占一行的 Git 协议指令", () => {
    expect(renderableFinalAnswer([
      "部署完成。",
      "",
      '::git-commit{cwd="/workspace/project"}',
      '::git-push{cwd="/workspace/project" branch="main"}',
    ].join("\n"))).toBe("部署完成。");
  });

  it("保留正文中的 Git 文字和非独占行内容", () => {
    const value = "可手动运行 git commit。\n示例：::git-push{branch=\"main\"}";
    expect(renderableFinalAnswer(value)).toBe(value);
  });

  it("保留 Markdown 代码围栏里的协议指令示例", () => {
    const value = [
      "```text",
      '::git-commit{cwd="/workspace/project"}',
      "```",
    ].join("\n");
    expect(renderableFinalAnswer(value)).toBe(value);
  });

  it("保留缩进代码块里的协议指令示例", () => {
    const value = '    ::git-commit{cwd="/workspace/project"}';
    expect(renderableFinalAnswer(value)).toBe(value);
  });

  it("保留不完整的协议指令", () => {
    const value = '::git-push{cwd="/workspace/project"';
    expect(renderableFinalAnswer(value)).toBe(value);
  });
});

describe("prepareMarkdown", () => {
  it("把正文文件引用转换为可渲染占位符，并保护代码围栏", () => {
    const prepared = prepareMarkdown([
      "查看【src/app.ts†L10-L12】。",
      "",
      "```text",
      "【src/app.ts†L10】",
      "```",
    ].join("\n"));
    expect(prepared.source).not.toContain("查看【src/app.ts†L10-L12】");
    expect(prepared.source).toContain("【src/app.ts†L10】");
    const token = prepared.source.match(/\uE000codex-[^\uE001]+\uE001/)?.[0];
    expect(token ? lookupMarkdownPlaceholder(token) : null).toEqual({
      kind: "file-citation", path: "src/app.ts", lineStart: 10, lineEnd: 12,
    });
  });

  it("支持官方 codex-file-citation 内联协议", () => {
    const prepared = prepareMarkdown(':codex-file-citation{path="src/app.ts" line_range_start="8" line_range_end="9"}');
    const token = prepared.source.match(/\uE000codex-[^\uE001]+\uE001/)?.[0];
    expect(token ? lookupMarkdownPlaceholder(token) : null).toEqual({
      kind: "file-citation", path: "src/app.ts", lineStart: 8, lineEnd: 9,
    });
  });

  it("把正文卡片协议转成块占位符并移除系统协议", () => {
    const prepared = prepareMarkdown([
      '::task-stub{title="检查测试" prompt="运行测试"}',
      '::git-commit{cwd="/workspace"}',
      ":::writing{variant=\"email\" id=\"12345\"}",
      "主题：测试",
      ":::",
    ].join("\n"));
    expect(prepared.source).not.toContain("git-commit");
    expect(prepared.source).not.toContain("task-stub");
    expect(prepared.source).not.toContain(":::writing");
    const tokens = prepared.source.match(/\uE000codex-[^\uE001]+\uE001/g) ?? [];
    expect(tokens.length).toBe(2);
    expect(tokens.map((token) => lookupMarkdownPlaceholder(token))).toContainEqual({
      kind: "writing", variant: "email", content: "主题：测试",
    });
  });

  it("对齐受控基础 HTML 和 GFM task list 的安全文本表现", () => {
    const prepared = prepareMarkdown("<strong>重点</strong> <u>下划线</u>\n- [x] 已完成");
    expect(prepared.source).toContain("**重点**");
    expect(prepared.source).toContain("☑ 已完成");
    const token = prepared.source.match(/\uE000codex-[^\uE001]+\uE001/)?.[0];
    expect(token ? lookupMarkdownPlaceholder(token) : null).toEqual({
      kind: "html-inline", variant: "u", content: "下划线",
    });
  });
});
