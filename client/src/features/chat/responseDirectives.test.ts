import { describe, expect, it } from "vitest";

import { parseResponseDirective, renderableFinalAnswer } from "./responseDirectives";

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
