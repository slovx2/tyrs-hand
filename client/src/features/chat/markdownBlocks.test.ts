import type { ASTNode } from "react-native-markdown-display";
import { describe, expect, it } from "vitest";
import { createRequire } from "node:module";
import { headingSourceChunks, markdownSourceChunks, splitMarkdownAst } from "./markdownBlocks";

// 使用渲染库实际安装的解析器，避免手写 AST 隐藏运行时结构差异。
const require = createRequire(import.meta.url);
const MarkdownIt = createRequire(require.resolve("react-native-markdown-display/package.json"))("markdown-it");

function node(type: string, content = "", children: ASTNode[] = []): ASTNode {
  return { type, content, children, key: type, sourceType: type, markup: "", tokenIndex: 0,
    index: 0, attributes: {} };
}
function content(value: ASTNode): string {
  return value.content + value.children.map(content).join("");
}

describe("Markdown AST 虚拟化切块", () => {
  it("章节快速分块不切开围栏和嵌套容器；含引用定义时交回完整块解析", () => {
    const md = new MarkdownIt({ breaks: true, typographer: true });
    const section = "# Heading\n\n" + "text **bold**\n\n".repeat(30) +
      "1. list\n   # nested heading\n\n```md\n# not a heading\n" + "# code\n".repeat(150) + "```\n\n";
    const source = section.repeat(40);
    const chunks = headingSourceChunks(source)!;
    expect(chunks.join("")).toBe(source);
    expect(chunks.length).toBeGreaterThan(10);
    expect(chunks.map((chunk) => md.render(chunk)).join("")).toBe(md.render(source));
    expect(headingSourceChunks(`${source}\n[ref]: /path`)).toBeNull();
  });
  it("块级分页保持整篇语义，跨页引用、嵌套列表、围栏和表格不会断开", () => {
    const md = new MarkdownIt({ breaks: true, typographer: true });
    const lexer = new MarkdownIt({ breaks: true, typographer: true })
      .disable(["inline", "linkify", "replacements", "smartquotes"]);
    const section = "# Title\n\n[跨页引用][ref]\n\n1. first\n   - nested\n2. second\n\n" +
      "```ts\nconst a = `**value**`;\n\n```\n\n| a | b |\n| --- | --- |\n| c | d |\n\n";
    const source = section.repeat(700) + "[ref]: https://example.com\n";
    const env = {};
    const tokens = lexer.parse(source, env);
    const chunks = markdownSourceChunks(source, tokens);
    expect(chunks.join("")).toBe(source);
    expect(chunks.length).toBeGreaterThan(20);
    expect(chunks.length).toBeLessThan(200);
    expect(chunks.map((chunk) => md.render(chunk, { ...env })).join("")).toBe(md.render(source));
  });
  it("支持解析器生成的无 content 文本分组", () => {
    const group = node("textgroup", "", [node("text", "answer")]);
    delete (group as Partial<ASTNode>).content;
    expect(splitMarkdownAst([node("paragraph", "", [group])])).toHaveLength(1);
  });
  it("10 万字符段落保留全部内容及链接格式，不切断代理对", () => {
    const text = "文本😀".repeat(25000);
    const link = { ...node("link", "", [node("text", text)]), attributes: { href: "/file" } };
    const blocks = splitMarkdownAst([node("paragraph", "", [node("textgroup", "", [link])])]);
    expect(blocks.length).toBeGreaterThan(40);
    expect(blocks.map((block) => block.ast.map(content).join("")).join("")).toBe(text);
    for (const block of blocks) {
      const value = content(block.ast[0]!);
      expect(value.length).toBeLessThanOrEqual(2400);
      expect(value.isWellFormed()).toBe(true);
      expect(block.ast[0]?.children[0]?.children[0]?.attributes.href).toBe("/file");
    }
  });

  it("代码按行分段，保留空行和原始顺序", () => {
    const source = "first\n\n" + "const x = 1;\n".repeat(500);
    const blocks = splitMarkdownAst([node("fence", source)]);
    expect(blocks.length).toBeGreaterThan(10);
    expect(blocks.map((block) => content(block.ast[0]!)).join("")).toBe(source);
  });

  it("有序列表保留项目索引；表格按行拆分且共享列数", () => {
    const list = node("ordered_list", "", Array.from({ length: 100 }, (_, index) =>
      ({ ...node("list_item", `item-${index}`), index })));
    const items = splitMarkdownAst([list]);
    expect(items).toHaveLength(100);
    expect(items[50]?.ast[0]?.children[0]?.index).toBe(50);
    const table = node("table", "", [node("tbody", "", Array.from({ length: 100 }, () =>
      node("tr", "", [node("td", "a"), node("td", "b"), node("td", "c")])))]);
    const rows = splitMarkdownAst([table]);
    expect(rows).toHaveLength(100);
    expect(rows.every((row) => row.ast[0]?.attributes.virtualColumnCount === 3)).toBe(true);
  });
});
